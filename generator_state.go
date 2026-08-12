package main

import (
	"bytes"
	"compress/flate"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxCompressedDeltaBytes int64 = 64 << 20

type Generator struct {
	local       bool
	answersFile string

	mu           sync.Mutex
	delta        MetadataDelta
	savedVersion string
}

func NewGenerator(local bool, answersFile string) *Generator {
	return &Generator{
		local:       local,
		answersFile: answersFile,
		delta:       MetadataDelta{Version: "0"},
	}
}

func (g *Generator) GenerateDelta(body io.Reader) ([]map[string]interface{}, string, error) {
	return g.generateDelta(body, true)
}

func (g *Generator) generateDelta(body io.Reader, updateState bool) ([]map[string]interface{}, string, error) {
	data, version, content, err := decodeDelta(body)
	if err != nil {
		return nil, "", err
	}
	if updateState {
		g.commitDelta(version, content)
	}
	return data, version, nil
}

func decodeDelta(body io.Reader) ([]map[string]interface{}, string, []byte, error) {
	content, err := io.ReadAll(io.LimitReader(body, maxCompressedDeltaBytes+1))
	if err != nil {
		return nil, "", nil, err
	}
	if int64(len(content)) > maxCompressedDeltaBytes {
		return nil, "", nil, fmt.Errorf("compressed metadata delta exceeds %d bytes", maxCompressedDeltaBytes)
	}

	reader := flate.NewReader(bytes.NewReader(content))
	defer reader.Close()
	decompressed := io.LimitReader(reader, maxAnswersFileBytes+1)
	decoder := json.NewDecoder(decompressed)

	var data []map[string]interface{}
	var version string
	for {
		var object map[string]interface{}
		err := decoder.Decode(&object)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", nil, err
		}
		data = append(data, object)
		if len(data) > 1_000_000 {
			return nil, "", nil, fmt.Errorf("metadata delta contains too many objects")
		}
		if object["metadata_kind"] == "defaultData" {
			if objectVersion, ok := object["version"].(string); ok {
				version = objectVersion
			}
		}
	}
	if decoder.InputOffset() > maxAnswersFileBytes {
		return nil, "", nil, fmt.Errorf("decompressed metadata delta exceeds %d bytes", maxAnswersFileBytes)
	}
	if version == "" {
		return nil, "", nil, fmt.Errorf("metadata delta has no version")
	}
	return data, version, content, nil
}

func (g *Generator) commitDelta(version string, content []byte) {
	g.mu.Lock()
	g.delta = MetadataDelta{Version: version, Data: append([]byte(nil), content...)}
	g.mu.Unlock()
}

func (g *Generator) GenerateAnswers(data []map[string]interface{}) (Versions, []Credential, error) {
	return GenerateScopedAnswers(data, g.local)
}

func (g *Generator) SaveToFile(_ time.Time) error {
	g.mu.Lock()
	if g.savedVersion == g.delta.Version || len(g.delta.Data) == 0 {
		g.mu.Unlock()
		return nil
	}
	delta := MetadataDelta{Version: g.delta.Version, Data: append([]byte(nil), g.delta.Data...)}
	g.mu.Unlock()

	if err := writeDeltaAtomically(g.answersFile, delta); err != nil {
		return err
	}
	g.mu.Lock()
	g.savedVersion = delta.Version
	g.mu.Unlock()
	return nil
}

func writeDeltaAtomically(destination string, delta MetadataDelta) error {
	if destination == "" {
		return fmt.Errorf("answers file path is empty")
	}
	directory := filepath.Dir(destination)
	temporary, err := os.CreateTemp(directory, ".metadata-delta-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	if err := json.NewEncoder(temporary).Encode(delta); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		return fmt.Errorf("replace answers file: %w", err)
	}
	removeTemporary = false
	return nil
}

func (g *Generator) LoadVersionsFromFile(ignoreIfMissing bool) (Versions, []Credential, string, error) {
	content, err := os.ReadFile(g.answersFile)
	if err != nil {
		if ignoreIfMissing && os.IsNotExist(err) {
			return nil, nil, "", nil
		}
		return nil, nil, "", err
	}
	if int64(len(content)) > maxAnswersFileBytes {
		return nil, nil, "", fmt.Errorf("answers file exceeds %d bytes", maxAnswersFileBytes)
	}

	var saved MetadataDelta
	if err := json.Unmarshal(content, &saved); err == nil && len(saved.Data) > 0 {
		data, version, err := g.generateDelta(bytes.NewReader(saved.Data), false)
		if err != nil {
			return nil, nil, "", err
		}
		versions, credentials, err := g.GenerateAnswers(data)
		if err == nil {
			g.mu.Lock()
			g.delta = MetadataDelta{Version: saved.Version, Data: append([]byte(nil), saved.Data...)}
			g.savedVersion = saved.Version
			g.mu.Unlock()
		}
		return versions, credentials, version, err
	}

	versions, err := ParseAnswers(g.answersFile)
	if err != nil {
		return nil, nil, "", err
	}
	return versions, nil, versionFromAnswers(versions), nil
}

func versionFromAnswers(versions Versions) string {
	if defaults, ok := versions["latest"][DEFAULT_KEY].(map[string]interface{}); ok {
		if version, ok := defaults["version"].(string); ok {
			return version
		}
	}
	return ""
}
