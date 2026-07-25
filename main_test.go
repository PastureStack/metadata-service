package main

import (
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestParseOptionsUsesNeutralContract(t *testing.T) {
	options, err := parseOptions([]string{
		"--listen", "127.0.0.1:8080",
		"--listen-reload", "127.0.0.1:8112",
		"--answers", "fixture.json",
		"--subscribe",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.listen != "127.0.0.1:8080" || options.listenReload != "127.0.0.1:8112" {
		t.Fatalf("unexpected listen options: %#v", options)
	}
	if options.answersFile != "fixture.json" || !options.subscribe {
		t.Fatalf("unexpected source options: %#v", options)
	}
}

func TestPlatformEnvironmentPrefersNeutralName(t *testing.T) {
	t.Setenv("PLATFORM_URL", "https://platform.example.test")
	t.Setenv("CATTLE_URL", "https://compatibility.example.test")
	if actual := platformEnvironment("PLATFORM_URL", "CATTLE_URL"); actual != "https://platform.example.test" {
		t.Fatalf("platform URL = %q", actual)
	}
}

func TestPlatformEnvironmentFallsBackForUpgrade(t *testing.T) {
	t.Setenv("PLATFORM_URL", "")
	t.Setenv("CATTLE_URL", "https://compatibility.example.test")
	if actual := platformEnvironment("PLATFORM_URL", "CATTLE_URL"); actual != "https://compatibility.example.test" {
		t.Fatalf("platform URL = %q", actual)
	}
}

func TestContentTypeHonorsQuality(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Accept", "application/json;q=0.4, application/yaml;q=0.9")
	if actual := contentType(request); actual != ContentYAML {
		t.Fatalf("content type = %d, want YAML", actual)
	}
}

func TestRespondSuccessSetsNegotiatedContentType(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.Header.Set("Accept", "application/json")
	recorder := httptest.NewRecorder()
	respondSuccess(recorder, request, map[string]interface{}{"status": "ok"})
	if actual := recorder.Header().Get("Content-Type"); actual != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", actual)
	}
}

func TestMatchingUsesClientThenDefault(t *testing.T) {
	versions := Versions{
		"2015-12-19": Answers{
			"10.0.0.2": map[string]interface{}{"service": map[string]interface{}{"name": "client"}},
			"default":  map[string]interface{}{"service": map[string]interface{}{"name": "default"}},
		},
	}

	clientValue, ok := versions.Matching("2015-12-19", "10.0.0.2", []string{"service", "name"})
	if !ok || !reflect.DeepEqual(clientValue, "client") {
		t.Fatalf("client value = %#v, ok = %v", clientValue, ok)
	}
	defaultValue, ok := versions.Matching("2015-12-19", "10.0.0.3", []string{"service", "name"})
	if !ok || !reflect.DeepEqual(defaultValue, "default") {
		t.Fatalf("default value = %#v, ok = %v", defaultValue, ok)
	}
}

func TestParseAnswersSupportsYAML(t *testing.T) {
	versions, err := ParseAnswers("example/simple.yml")
	if err != nil {
		t.Fatal(err)
	}
	value, ok := versions.Matching("2015-12-19", "127.0.0.1", []string{"key1"})
	if !ok || value != "value1" {
		t.Fatalf("value = %#v, ok = %v", value, ok)
	}
}
