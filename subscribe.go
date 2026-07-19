package main

import (
	"bytes"
	"compress/flate"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PastureStack/metadata-service/internal/platformevents"
	"github.com/PastureStack/metadata-service/pkg/kicker"
	"github.com/sirupsen/logrus"
)

const maxCompressedDeltaBytes int64 = 64 << 20

type ReloadFunc func(versions Versions)

var (
	Delta          *MetadataDelta
	SavedVersion   string
	deltaDecoderMu sync.Mutex
)

type Subscriber struct {
	url            string
	accessKey      string
	secretKey      string
	reload         ReloadFunc
	answerFile     string
	client         *http.Client
	platformClient *platformevents.Client
	kicker         *kicker.Kicker
	reloadInterval int64
	limitMu        sync.Mutex
	nextReload     time.Time
}

func init() {
	Delta = &MetadataDelta{Version: "0"}
}

func NewSubscriber(rawURL, accessKey, secretKey, answerFile string, reloadInterval int64, reload ReloadFunc) (*Subscriber, error) {
	platformClient, err := platformevents.NewClient(rawURL, accessKey, secretKey)
	if err != nil {
		return nil, err
	}
	s := &Subscriber{
		url:            platformClient.ConfigurationBaseURL(),
		accessKey:      accessKey,
		secretKey:      secretKey,
		reload:         reload,
		answerFile:     answerFile,
		client:         &http.Client{Timeout: 30 * time.Second},
		platformClient: platformClient,
		reloadInterval: reloadInterval,
	}
	s.kicker = kicker.New(func() {
		if err := s.downloadAndReload(); err != nil {
			logrus.Errorf("Failed to download and reload metadata: %v", err)
		}
	})
	return s, nil
}

func (s *Subscriber) Subscribe() error {
	handlers := map[string]platformevents.Handler{
		"ping":          s.noOp,
		"config.update": s.configUpdate,
	}

	go func() {
		for {
			s.kicker.Kick()
			if err := s.platformClient.Run(handlers); err != nil {
				logrus.Errorf("Platform event stream exited: %v", err)
			}
			time.Sleep(time.Second)
		}
	}()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for tick := range ticker.C {
			s.saveToFile(tick)
		}
	}()

	return nil
}

func (s *Subscriber) noOp(_ *platformevents.Event, _ *platformevents.Client) error {
	return nil
}

func (s *Subscriber) configUpdate(event *platformevents.Event, client *platformevents.Client) error {
	encoded, err := json.Marshal(event.Data)
	if err != nil {
		return err
	}
	update := ConfigUpdateData{}
	if err := json.Unmarshal(encoded, &update); err != nil {
		return err
	}

	for _, item := range update.Items {
		if item.Name == "metadata-answers" {
			logrus.Infof("Update requested for version: %d", item.RequestedVersion)
			SetRequestedVersion(strconv.Itoa(item.RequestedVersion))
			generation := s.kicker.Kick()
			s.kicker.Wait(generation)
			break
		}
	}

	if event.ReplyTo == "" {
		return nil
	}
	return client.Publish(&platformevents.Publish{
		Name:        event.ReplyTo,
		PreviousIDs: []string{event.ID},
	})
}

func (s *Subscriber) saveDeltaToFile() error {
	tempFile := s.answerFile + ".temp"
	out, err := os.OpenFile(tempFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempFile)
		}
	}()

	if err := json.NewEncoder(out).Encode(Delta); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempFile, s.answerFile); err != nil {
		return fmt.Errorf("replace answers file: %w", err)
	}
	removeTemp = false
	return nil
}

func (s *Subscriber) downloadAndReload() error {
	s.waitForReloadWindow()
	downloadURL, err := s.configContentURL(GetRequestedVersion(), "")
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(s.accessKey, s.secretKey)
	start := time.Now()
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download metadata: unexpected status %d", resp.StatusCode)
	}
	logrus.Infof("Downloaded metadata in %s", time.Since(start))

	delta, err := GenerateDelta(resp.Body)
	if err != nil {
		return fmt.Errorf("decode metadata delta: %w", err)
	}
	versions, err := GenerateAnswers(delta)
	if err != nil {
		return fmt.Errorf("generate metadata answers: %w", err)
	}
	s.reload(versions)

	defaults, ok := versions["latest"]["default"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("metadata response has no default version")
	}
	version, ok := defaults["version"].(string)
	if !ok || version == "" {
		return fmt.Errorf("metadata response has no applied version")
	}
	appliedURL, err := s.configContentURL("", version)
	if err != nil {
		return err
	}
	applyRequest, err := http.NewRequest(http.MethodPut, appliedURL, nil)
	if err != nil {
		return err
	}
	applyRequest.SetBasicAuth(s.accessKey, s.secretKey)
	applyResponse, err := s.client.Do(applyRequest)
	if err != nil {
		return err
	}
	if applyResponse.Body != nil {
		applyResponse.Body.Close()
	}
	if applyResponse.StatusCode < 200 || applyResponse.StatusCode >= 300 {
		return fmt.Errorf("report applied metadata version: unexpected status %d", applyResponse.StatusCode)
	}
	logrus.Infof("Applied metadata version %s in %v", version, time.Since(start))
	return nil
}

func (s *Subscriber) waitForReloadWindow() {
	s.limitMu.Lock()
	defer s.limitMu.Unlock()
	now := time.Now()
	if now.Before(s.nextReload) {
		time.Sleep(s.nextReload.Sub(now))
	}
	s.nextReload = time.Now().Add(time.Duration(s.reloadInterval) * time.Millisecond)
}

func (s *Subscriber) configContentURL(requestedVersion, appliedVersion string) (string, error) {
	u, err := url.Parse(s.url)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/configcontent/metadata-answers"
	query := u.Query()
	query.Set("client", "v2")
	if requestedVersion != "" {
		query.Set("requestedVersion", requestedVersion)
	}
	if appliedVersion != "" {
		query.Set("version", appliedVersion)
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

type ConfigUpdateData struct {
	ConfigURL string             `json:"configUrl"`
	Items     []ConfigUpdateItem `json:"items"`
}

type ConfigUpdateItem struct {
	Name             string `json:"name"`
	RequestedVersion int    `json:"requestedVersion"`
}

func GenerateDelta(body io.Reader) ([]map[string]interface{}, error) {
	content, err := io.ReadAll(io.LimitReader(body, maxCompressedDeltaBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maxCompressedDeltaBytes {
		return nil, fmt.Errorf("compressed metadata delta exceeds %d bytes", maxCompressedDeltaBytes)
	}

	reader := flate.NewReader(bytes.NewBuffer(content))
	defer reader.Close()
	deltaDecoderMu.Lock()
	defer deltaDecoderMu.Unlock()
	streamDecoder := json.NewDecoder(reader)

	var data []map[string]interface{}
	var version string
	for {
		var object map[string]interface{}
		err := streamDecoder.Decode(&object)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		data = append(data, object)
		if object["metadata_kind"] == "defaultData" {
			if objectVersion, ok := object["version"].(string); ok {
				version = objectVersion
			}
		}
	}
	reloadDelta(version, content)
	return data, nil
}

func reloadDelta(version string, data []byte) {
	Delta.Lock()
	defer Delta.Unlock()
	Delta.Version = version
	Delta.Data = data
}

func (s *Subscriber) saveToFile(tick time.Time) {
	Delta.Lock()
	defer Delta.Unlock()
	currentVersion := Delta.Version
	if SavedVersion == currentVersion || len(Delta.Data) == 0 {
		return
	}
	if err := s.saveDeltaToFile(); err != nil {
		logrus.Errorf("Failed to save delta to file: %v", err)
		return
	}
	SavedVersion = currentVersion
	logrus.Debugf("Saved delta to file at %v", tick)
}
