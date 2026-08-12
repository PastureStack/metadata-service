package main

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func compressedMetadata(t *testing.T, objects ...map[string]interface{}) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer, err := flate.NewWriter(&compressed, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		if err := json.NewEncoder(writer).Encode(object); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func TestDownloadAndReloadUsesOneTrustedOrigin(t *testing.T) {
	metadata := compressedMetadata(t,
		map[string]interface{}{"metadata_kind": "defaultData", "version": "42"},
		map[string]interface{}{"metadata_kind": "environment", "uuid": "env-1", "name": "primary"},
	)
	var downloads atomic.Int32
	var applies atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		username, password, ok := req.BasicAuth()
		if !ok || username != "access" || password != "secret" {
			t.Errorf("unexpected authentication")
		}
		if req.URL.Path != "/v1/configcontent/metadata-answers" {
			t.Errorf("path = %q", req.URL.Path)
		}
		switch req.Method {
		case http.MethodGet:
			downloads.Add(1)
			w.Write(metadata)
		case http.MethodPut:
			if req.URL.Query().Get("version") != "42" {
				t.Errorf("applied version = %q", req.URL.Query().Get("version"))
			}
			applies.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	var reloadedVersion string
	generator := NewGenerator(true, filepath.Join(t.TempDir(), "answers.json"))
	subscriber, err := NewSubscriber(server.URL+"/v1", "access", "secret", generator, 1,
		func(_ Versions, _ []Credential, version string) error { reloadedVersion = version; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := subscriber.downloadAndReload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 1 || applies.Load() != 1 || reloadedVersion != "42" {
		t.Fatalf("downloads=%d applies=%d version=%q", downloads.Load(), applies.Load(), reloadedVersion)
	}
}

func TestDownloadRejectsCrossOriginRedirectWithoutContact(t *testing.T) {
	var attackerRequests atomic.Int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		attackerRequests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer attacker.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, attacker.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	generator := NewGenerator(true, filepath.Join(t.TempDir(), "answers.json"))
	subscriber, err := NewSubscriber(origin.URL, "access", "secret", generator, 1, func(Versions, []Credential, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := subscriber.downloadAndReload(context.Background()); err == nil {
		t.Fatal("cross-origin metadata redirect unexpectedly succeeded")
	}
	if attackerRequests.Load() != 0 {
		t.Fatalf("attacker received %d requests", attackerRequests.Load())
	}
}

func TestRejectedSnapshotIsNotReportedAsApplied(t *testing.T) {
	metadata := compressedMetadata(t,
		map[string]interface{}{"metadata_kind": "defaultData", "version": "42"},
	)
	var applyRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPut {
			applyRequests.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Write(metadata)
	}))
	defer server.Close()
	generator := NewGenerator(true, filepath.Join(t.TempDir(), "answers.json"))
	subscriber, err := NewSubscriber(server.URL, "access", "secret", generator, 1,
		func(Versions, []Credential, string) error { return context.Canceled })
	if err != nil {
		t.Fatal(err)
	}
	if err := subscriber.downloadAndReload(context.Background()); err == nil {
		t.Fatal("rejected snapshot unexpectedly succeeded")
	}
	if applyRequests.Load() != 0 {
		t.Fatalf("rejected snapshot was reported applied %d times", applyRequests.Load())
	}
	if generator.delta.Version != "0" || len(generator.delta.Data) != 0 {
		t.Fatalf("rejected snapshot was retained: version=%q bytes=%d", generator.delta.Version, len(generator.delta.Data))
	}
}

func TestWaitForReloadWindowCanBeCancelled(t *testing.T) {
	generator := NewGenerator(true, filepath.Join(t.TempDir(), "answers.json"))
	server := httptest.NewServer(nil)
	defer server.Close()
	subscriber, err := NewSubscriber(server.URL, "access", "secret", generator, 60_000, func(Versions, []Credential, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := subscriber.waitForReloadWindow(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := subscriber.waitForReloadWindow(ctx); err == nil {
		t.Fatal("cancelled reload wait unexpectedly succeeded")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancelled reload wait took %s", elapsed)
	}
}

func TestGeneratorDeltaStateIsIsolated(t *testing.T) {
	first := NewGenerator(true, filepath.Join(t.TempDir(), "first.json"))
	second := NewGenerator(false, filepath.Join(t.TempDir(), "second.json"))

	if _, version, err := first.GenerateDelta(bytes.NewReader(compressedMetadata(t,
		map[string]interface{}{"metadata_kind": "defaultData", "version": "42"},
	))); err != nil || version != "42" {
		t.Fatalf("first delta version = %q, err = %v", version, err)
	}
	if _, version, err := second.GenerateDelta(bytes.NewReader(compressedMetadata(t,
		map[string]interface{}{"metadata_kind": "defaultData", "version": "99"},
	))); err != nil || version != "99" {
		t.Fatalf("second delta version = %q, err = %v", version, err)
	}
	if first.delta.Version != "42" || second.delta.Version != "99" {
		t.Fatalf("generator state leaked: first=%q second=%q", first.delta.Version, second.delta.Version)
	}
}

func TestConfigurationContentURLUsesPlatformPath(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()
	generator := NewGenerator(true, filepath.Join(t.TempDir(), "answers.json"))
	subscriber, err := NewSubscriber(server.URL+"/v1", "access", "secret", generator, 1, func(Versions, []Credential, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}

	downloadURL, err := subscriber.platformClient.ConfigurationContentURL("7", "")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(downloadURL.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/v1/configcontent/metadata-answers" {
		t.Fatalf("path = %q", parsed.Path)
	}
	if parsed.Query().Get("client") != "v2" || parsed.Query().Get("requestedVersion") != "7" {
		t.Fatalf("query = %v", parsed.Query())
	}
}

func TestGenerateScopedAnswersPreservesMultiSubscriberMetadata(t *testing.T) {
	data := []map[string]interface{}{
		{"metadata_kind": "defaultData", "version": "12"},
		{"metadata_kind": "environment", "name": "remote", "uuid": "env-1"},
		{"metadata_kind": "credential", "url": "https://region.example/v1", "public_value": "access", "secret_value": "secret"},
	}
	versions, credentials, err := GenerateScopedAnswers(data, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 1 || credentials[0].URL != "https://region.example/v1" {
		t.Fatalf("credentials = %#v", credentials)
	}
	if _, ok := versions[version1]; ok {
		t.Fatal("external source unexpectedly generated legacy metadata versions")
	}
	defaults := versions[version3][DEFAULT_KEY].(map[string]interface{})
	if defaults["name"] != "remote" || defaults["uuid"] != "env-1" {
		t.Fatalf("environment defaults = %#v", defaults)
	}
}

func TestGeneratorPersistsOnlyAcceptedSnapshot(t *testing.T) {
	answersFile := filepath.Join(t.TempDir(), "answers.json")
	generator := NewGenerator(true, answersFile)
	accepted := compressedMetadata(t, map[string]interface{}{"metadata_kind": "defaultData", "version": "42"})
	data, version, content, err := decodeDelta(bytes.NewReader(accepted))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := generator.GenerateAnswers(data); err != nil {
		t.Fatal(err)
	}
	generator.commitDelta(version, content)
	if err := generator.SaveToFile(time.Now()); err != nil {
		t.Fatal(err)
	}

	restarted := NewGenerator(true, answersFile)
	versions, _, loadedVersion, err := restarted.LoadVersionsFromFile(false)
	if err != nil {
		t.Fatal(err)
	}
	if loadedVersion != "42" || versionFromAnswers(versions) != "42" {
		t.Fatalf("restarted version=%q answers version=%q", loadedVersion, versionFromAnswers(versions))
	}
}
