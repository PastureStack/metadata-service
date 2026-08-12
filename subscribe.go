package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/PastureStack/metadata-service/internal/platformevents"
	"github.com/PastureStack/metadata-service/pkg/kicker"
	"github.com/sirupsen/logrus"
)

type ReloadFunc func(versions Versions, credentials []Credential, version string) error

type Subscriber struct {
	reload         ReloadFunc
	platformClient *platformevents.Client
	generator      *Generator
	kicker         *kicker.Kicker
	reloadInterval time.Duration

	requestedMu      sync.Mutex
	requestedVersion string
	limitMu          sync.Mutex
	nextReload       time.Time
	lifecycleMu      sync.Mutex
	ctx              context.Context
	cancel           context.CancelFunc
	done             chan struct{}
	stopOnce         sync.Once
}

func NewSubscriber(rawURL, accessKey, secretKey string, generator *Generator, reloadInterval int64, reload ReloadFunc) (*Subscriber, error) {
	if generator == nil {
		return nil, fmt.Errorf("metadata generator is required")
	}
	if reload == nil {
		return nil, fmt.Errorf("metadata reload callback is required")
	}
	if reloadInterval < 1 {
		return nil, fmt.Errorf("metadata reload interval must be at least 1 millisecond")
	}
	platformClient, err := platformevents.NewClient(rawURL, accessKey, secretKey)
	if err != nil {
		return nil, err
	}
	subscriber := &Subscriber{
		reload:         reload,
		platformClient: platformClient,
		generator:      generator,
		reloadInterval: time.Duration(reloadInterval) * time.Millisecond,
	}
	subscriber.kicker = kicker.New(func() {
		subscriber.lifecycleMu.Lock()
		ctx := subscriber.ctx
		subscriber.lifecycleMu.Unlock()
		if ctx == nil {
			return
		}
		if err := subscriber.downloadAndReload(ctx); err != nil && !isContextCancellation(err) {
			logrus.WithError(err).Error("Metadata download and reload failed")
		}
	})
	return subscriber, nil
}

func (s *Subscriber) Subscribe() error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.done != nil {
		return fmt.Errorf("metadata subscriber is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	s.cancel = cancel
	s.done = make(chan struct{})
	go s.run(ctx)
	return nil
}

func (s *Subscriber) run(ctx context.Context) {
	defer close(s.done)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		s.runEventStream(ctx)
	}()
	go func() {
		defer workers.Done()
		s.runDeltaSaver(ctx)
	}()
	workers.Wait()
}

func (s *Subscriber) runEventStream(ctx context.Context) {
	handlers := map[string]platformevents.Handler{
		"ping":          s.noOp,
		"config.update": s.configUpdate,
	}
	s.kicker.Kick()
	for {
		if err := s.platformClient.RunContext(ctx, handlers); err != nil && !isContextCancellation(err) {
			logrus.WithError(err).Warn("Platform event stream disconnected")
		}
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Subscriber) runDeltaSaver(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case tick := <-ticker.C:
			if err := s.generator.SaveToFile(tick); err != nil {
				logrus.WithError(err).Error("Metadata delta save failed")
			}
		}
	}
}

func (s *Subscriber) Unsubscribe() {
	s.stopOnce.Do(func() {
		s.Cancel()
		s.lifecycleMu.Lock()
		done := s.done
		s.lifecycleMu.Unlock()
		if done != nil {
			<-done
		}
		s.kicker.WaitIdle()
		s.lifecycleMu.Lock()
		s.ctx = nil
		s.lifecycleMu.Unlock()
	})
}

func (s *Subscriber) Cancel() {
	s.lifecycleMu.Lock()
	cancel := s.cancel
	s.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
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
			s.setRequestedVersion(strconv.Itoa(item.RequestedVersion))
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

func (s *Subscriber) setRequestedVersion(version string) {
	s.requestedMu.Lock()
	s.requestedVersion = version
	s.requestedMu.Unlock()
}

func (s *Subscriber) getRequestedVersion() string {
	s.requestedMu.Lock()
	defer s.requestedMu.Unlock()
	if s.requestedVersion == "0" {
		return ""
	}
	return s.requestedVersion
}

func (s *Subscriber) downloadAndReload(ctx context.Context) error {
	if err := s.waitForReloadWindow(ctx); err != nil {
		return err
	}
	start := time.Now()
	response, err := s.platformClient.DownloadConfigurationContext(ctx, s.getRequestedVersion())
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return fmt.Errorf("download metadata: unexpected status %d", response.StatusCode)
	}

	delta, version, content, err := decodeDelta(response.Body)
	if err != nil {
		return fmt.Errorf("decode metadata delta: %w", err)
	}
	versions, credentials, err := s.generator.GenerateAnswers(delta)
	if err != nil {
		return fmt.Errorf("generate metadata answers: %w", err)
	}
	if err := s.reload(versions, credentials, version); err != nil {
		return fmt.Errorf("apply metadata snapshot: %w", err)
	}
	s.generator.commitDelta(version, content)

	applyResponse, err := s.platformClient.ReportAppliedConfigurationContext(ctx, version)
	if err != nil {
		return err
	}
	if applyResponse.Body != nil {
		defer applyResponse.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(applyResponse.Body, 1<<20))
	}
	if applyResponse.StatusCode < 200 || applyResponse.StatusCode >= 300 {
		return fmt.Errorf("report applied metadata version: unexpected status %d", applyResponse.StatusCode)
	}
	logrus.WithFields(logrus.Fields{"version": version, "duration": time.Since(start)}).Info("Metadata applied")
	return nil
}

func (s *Subscriber) waitForReloadWindow(ctx context.Context) error {
	s.limitMu.Lock()
	wait := time.Until(s.nextReload)
	if wait < 0 {
		wait = 0
	}
	s.nextReload = time.Now().Add(wait + s.reloadInterval)
	s.limitMu.Unlock()
	if wait == 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type ConfigUpdateData struct {
	ConfigURL string             `json:"configUrl"`
	Items     []ConfigUpdateItem `json:"items"`
}

type ConfigUpdateItem struct {
	Name             string `json:"name"`
	RequestedVersion int    `json:"requestedVersion"`
}

func isContextCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
