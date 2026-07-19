package main

import (
	"bytes"
	"compress/flate"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestGenerateDeltaUsesBoundedStandardDecoder(t *testing.T) {
	var compressed bytes.Buffer
	writer, err := flate.NewWriter(&compressed, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	objects := []map[string]interface{}{
		{"metadata_kind": "defaultData", "version": "42"},
		{"metadata_kind": "container", "uuid": "container-1"},
	}
	for _, object := range objects {
		if err := json.NewEncoder(writer).Encode(object); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	decoded, err := GenerateDelta(bytes.NewReader(compressed.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 2 || Delta.Version != "42" {
		t.Fatalf("decoded = %d objects, version = %q", len(decoded), Delta.Version)
	}
}

func TestConfigContentURLUsesPlatformPath(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()
	subscriber, err := NewSubscriber(server.URL+"/v1", "access", "secret", "answers.json", 1, func(Versions) {})
	if err != nil {
		t.Fatal(err)
	}

	downloadURL, err := subscriber.configContentURL("7", "")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(downloadURL)
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
