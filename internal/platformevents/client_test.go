package platformevents

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestNormalizeBaseURLs(t *testing.T) {
	tests := map[string][2]string{
		"http://platform.example":          {"http://platform.example/v1", "http://platform.example/v2-beta"},
		"http://platform.example/":         {"http://platform.example/v1", "http://platform.example/v2-beta"},
		"https://platform.example/v1":      {"https://platform.example/v1", "https://platform.example/v2-beta"},
		"https://platform.example/v1/path": {"https://platform.example/v1/path", "https://platform.example/v2-beta/path"},
		"https://platform.example/v2-beta": {"https://platform.example/v1", "https://platform.example/v2-beta"},
	}
	for input, expected := range tests {
		eventBase, apiBase, err := normalizeBaseURLs(input)
		if err != nil {
			t.Fatalf("normalize %q: %v", input, err)
		}
		if eventBase.String() != expected[0] || apiBase.String() != expected[1] {
			t.Fatalf("normalize %q: got event=%q api=%q, want event=%q api=%q", input, eventBase, apiBase, expected[0], expected[1])
		}
	}
}

func TestNormalizeBaseURLsRejectsUnsafeInput(t *testing.T) {
	tests := []string{
		"http://access:secret@platform.example/v1",
		"http://platform.example/v1?redirect=http://attacker.example",
		"http://platform.example/v1#fragment",
		"http://platform.example:70000/v1",
		"http://platform_name.example/v1",
		"http://platform.example\\@attacker.example/v1",
		"file:///var/run/platform.sock",
	}
	for _, input := range tests {
		if _, _, err := normalizeBaseURLs(input); err == nil {
			t.Errorf("normalize %q unexpectedly succeeded", input)
		}
	}
}

func TestNormalizeBaseURLsSupportsDomainsIPv4IPv6AndNonDefaultPorts(t *testing.T) {
	for _, input := range []string{
		"http://127.0.0.1:8080/v1",
		"https://[2001:db8::1]:8443/v2-beta",
		"https://platform.internal:9443/v1/region-a",
	} {
		if _, _, err := normalizeBaseURLs(input); err != nil {
			t.Errorf("normalize %q: %v", input, err)
		}
	}
}

func TestConfigurationContentURLRejectsUntrustedVersionCharacters(t *testing.T) {
	client, err := NewClient("https://platform.example/v1", "access", "secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"1&redirect=http://attacker.example", "../publish", "line\nbreak"} {
		if _, err := client.ConfigurationContentURL(version, ""); err == nil {
			t.Errorf("version %q unexpectedly succeeded", version)
		}
	}
}

func TestPublishUsesNeutralEndpointAndBasicAuth(t *testing.T) {
	var received Publish
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v2-beta/publish" {
			t.Errorf("path = %q", req.URL.Path)
		}
		username, password, ok := req.BasicAuth()
		if !ok || username != "access" || password != "secret" {
			t.Errorf("unexpected basic auth")
		}
		if err := json.NewDecoder(req.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client, err := NewClient(server.URL+"/v1", "access", "secret")
	if err != nil {
		t.Fatal(err)
	}
	payload := &Publish{Name: "reply.config", PreviousIDs: []string{"event-1"}}
	if err := client.Publish(payload); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(received, *payload) {
		t.Fatalf("received %#v, want %#v", received, *payload)
	}
}

func TestPublishRejectsCrossOriginRedirectBeforeCredentialedRequest(t *testing.T) {
	var crossOriginRequests atomic.Int32
	crossOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		crossOriginRequests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer crossOrigin.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, crossOrigin.URL+"/captured", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	client, err := NewClient(origin.URL, "access", "secret")
	if err != nil {
		t.Fatal(err)
	}
	err = client.Publish(&Publish{Name: "reply.config"})
	if err == nil {
		t.Fatal("cross-origin redirect unexpectedly succeeded")
	}
	if got := crossOriginRequests.Load(); got != 0 {
		t.Fatalf("cross-origin server received %d requests, want 0", got)
	}
}

func TestPublishAllowsSameOriginRedirectAndPreservesCredentials(t *testing.T) {
	var redirected atomic.Bool
	var server *httptest.Server
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/v2-beta/publish":
			http.Redirect(w, req, server.URL+"/accepted", http.StatusTemporaryRedirect)
		case "/accepted":
			username, password, ok := req.BasicAuth()
			if !ok || username != "access" || password != "secret" {
				t.Errorf("redirected request did not retain same-origin credentials")
			}
			redirected.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, req)
		}
	})
	server = httptest.NewServer(handler)
	defer server.Close()

	client, err := NewClient(server.URL, "access", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Publish(&Publish{Name: "reply.config"}); err != nil {
		t.Fatal(err)
	}
	if !redirected.Load() {
		t.Fatal("same-origin redirect was not followed")
	}
}

func TestRunReceivesSubscribedEvent(t *testing.T) {
	received := make(chan Event, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/subscribe" {
			t.Errorf("path = %q", req.URL.Path)
		}
		if got := req.URL.Query()["eventNames"]; !reflect.DeepEqual(got, []string{"config.update", "ping"}) {
			t.Errorf("eventNames = %#v", got)
		}
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(Event{Name: "config.update", ID: "event-1"})
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "access", "secret")
	if err != nil {
		t.Fatal(err)
	}
	err = client.Run(map[string]Handler{
		"config.update": func(event *Event, _ *Client) error {
			received <- *event
			return nil
		},
		"ping": func(_ *Event, _ *Client) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-received:
		if event.ID != "event-1" {
			t.Fatalf("event ID = %q", event.ID)
		}
	default:
		t.Fatal("event handler was not called")
	}
}
