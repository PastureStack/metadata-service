package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestControllerStartsFromStaticAnswersWithoutPlatformURL(t *testing.T) {
	answersFile := filepath.Join(t.TempDir(), "answers.json")
	if err := os.WriteFile(answersFile, []byte(`{"2016-07-29":{"default":{"version":"7","name":"static"}},"latest":{"default":{"version":"7","name":"static"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	controller := NewMetadataController(false, answersFile, 1, "")
	if err := controller.Start("", "", ""); err != nil {
		t.Fatal(err)
	}
	versions := controller.GetVersions()
	value, ok := versions.Matching(version3, DEFAULT_KEY, []string{"name"})
	if !ok || value != "static" {
		t.Fatalf("static answer=%#v ok=%v", value, ok)
	}
}

func TestMergeVersionsPreservesStaticVersionWhenNoExternalSourceChanges(t *testing.T) {
	merged := MergeVersions(testVersions("static"), nil, "")
	if got := versionFromAnswers(merged); got != "1" {
		t.Fatalf("merged static version = %q", got)
	}
	if environments := merged[version3][DEFAULT_KEY].(map[string]interface{})["environments"].([]interface{}); len(environments) != 0 {
		t.Fatalf("environments = %#v", environments)
	}
}

func TestControllerRequiresPlatformURLOnlyForSubscription(t *testing.T) {
	controller := NewMetadataController(true, filepath.Join(t.TempDir(), "answers.json"), 1, "")
	if err := controller.Start("", "access", "secret"); err == nil {
		t.Fatal("subscription without a platform URL unexpectedly succeeded")
	}
}

func testVersions(name string) Versions {
	return Versions{
		version1: Answers{DEFAULT_KEY: map[string]interface{}{"version": "1"}},
		version2: Answers{DEFAULT_KEY: map[string]interface{}{"version": "1"}},
		version3: Answers{DEFAULT_KEY: map[string]interface{}{"version": "1", "name": name}},
		"latest": Answers{DEFAULT_KEY: map[string]interface{}{"version": "1", "name": name}},
	}
}

func TestLookupAnswerHonorsMaximumWaitWithoutUpdates(t *testing.T) {
	controller := NewMetadataController(false, filepath.Join(t.TempDir(), "answers.json"), 1, "")
	controller.SetStaticVersions(testVersions("primary"))
	start := time.Now()
	value, ok := controller.LookupAnswer(true, "primary", version3, DEFAULT_KEY, []string{"name"}, 25*time.Millisecond)
	if !ok || value != "primary" {
		t.Fatalf("value=%#v ok=%v", value, ok)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond || elapsed > time.Second {
		t.Fatalf("lookup elapsed = %s", elapsed)
	}
}

func TestLookupAnswerWakesWhenVersionsChange(t *testing.T) {
	controller := NewMetadataController(false, filepath.Join(t.TempDir(), "answers.json"), 1, "")
	controller.SetStaticVersions(testVersions("before"))
	done := make(chan interface{}, 1)
	go func() {
		value, _ := controller.LookupAnswer(true, "before", version3, DEFAULT_KEY, []string{"name"}, time.Second)
		done <- value
	}()
	time.Sleep(20 * time.Millisecond)
	controller.SetStaticVersions(testVersions("after"))
	select {
	case value := <-done:
		if value != "after" {
			t.Fatalf("value = %#v", value)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting lookup did not wake after metadata update")
	}
}

func TestControllerRejectsUnallowlistedExternalOrigin(t *testing.T) {
	controller := NewMetadataController(false, filepath.Join(t.TempDir(), "answers.json"), 1, "")
	controller.local = newMetadataSource("https://primary.example/v1", "primary", "secret", true, controller.answersFile)
	controller.local.setData(testVersions("primary"), []Credential{{
		URL: "https://attacker.example/v1", PublicValue: "external", SecretValue: "secret",
	}}, "1")
	if err := controller.reconcile(); err == nil {
		t.Fatal("unallowlisted external origin unexpectedly registered")
	}
	if len(controller.external) != 0 {
		t.Fatalf("external sources = %d, want 0", len(controller.external))
	}
}

func TestControllerAllowsConfiguredExternalOriginAndMergesEnvironment(t *testing.T) {
	controller := NewMetadataController(false, filepath.Join(t.TempDir(), "answers.json"), 1, "https://region.example")
	controller.local = newMetadataSource("https://primary.example/v1", "primary", "secret", true, controller.answersFile)
	local := testVersions("primary")
	controller.local.setData(local, []Credential{{
		URL: "https://region.example/v1", PublicValue: "external", SecretValue: "secret",
	}}, "1")
	if err := controller.reconcile(); err != nil {
		t.Fatal(err)
	}
	external := controller.external["external"]
	external.setData(testVersions("region"), nil, "2")
	if err := controller.reconcile(); err != nil {
		t.Fatal(err)
	}
	merged := controller.GetVersions()
	defaults := merged[version3][DEFAULT_KEY].(map[string]interface{})
	environments := defaults["environments"].([]interface{})
	if len(environments) != 1 || environments[0].(map[string]interface{})["name"] != "region" {
		t.Fatalf("environments = %#v", environments)
	}
	if _, exists := local[version3][DEFAULT_KEY].(map[string]interface{})["environments"]; exists {
		t.Fatal("merge mutated the local source snapshot")
	}
}

func TestControllerRotatesChangedCredentialWithoutExposingKeyInFileName(t *testing.T) {
	controller := NewMetadataController(false, filepath.Join(t.TempDir(), "answers.json"), 1, "")
	controller.local = newMetadataSource("https://primary.example/v1", "primary", "secret", true, controller.answersFile)
	controller.local.setData(testVersions("primary"), []Credential{{
		URL: "https://primary.example/v1", PublicValue: "credential-name", SecretValue: "old",
	}}, "1")
	if err := controller.reconcile(); err != nil {
		t.Fatal(err)
	}
	first := controller.external["credential-name"]
	controller.local.setData(testVersions("primary"), []Credential{{
		URL: "https://primary.example/v1", PublicValue: "credential-name", SecretValue: "new",
	}}, "2")
	if err := controller.reconcile(); err != nil {
		t.Fatal(err)
	}
	second := controller.external["credential-name"]
	if first == second || second.secretKey != "new" {
		t.Fatal("changed external credential was not rotated")
	}
	if first.active.Load() {
		t.Fatal("rotated source remained active")
	}
	if err := controller.applySourceData(first, testVersions("stale"), nil, "3"); err == nil {
		t.Fatal("replaced source unexpectedly updated controller state")
	}
	if filepath.Base(second.generator.answersFile) == "answers.json.external.credential-name" {
		t.Fatal("external answers file exposed the credential identifier")
	}
	if filepath.Ext(second.generator.answersFile) != ".json" {
		t.Fatalf("external answers file extension = %q", filepath.Ext(second.generator.answersFile))
	}
}

func TestCanonicalOriginNormalizesDefaultPorts(t *testing.T) {
	first, err := canonicalOrigin("https://platform.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := canonicalOrigin("https://platform.example:443/v2-beta")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("origins differ: %q %q", first, second)
	}
}

func TestCanonicalOriginFormatsIPv6Unambiguously(t *testing.T) {
	origin, err := canonicalOrigin("https://[2001:db8::1]:8443/v1")
	if err != nil {
		t.Fatal(err)
	}
	if origin != "https://[2001:db8::1]:8443" {
		t.Fatalf("origin = %q", origin)
	}
}

func TestInvalidAllowedOriginFailsClosed(t *testing.T) {
	if err := validateAllowedOrigins("https://trusted.example,https://access:secret@attacker.example"); err == nil {
		t.Fatal("invalid allowlist entry unexpectedly succeeded")
	}
	if err := validateAllowedOrigins("https://trusted.example/v1"); err == nil {
		t.Fatal("allowlist entry containing a path unexpectedly succeeded")
	}
}

func TestRejectedLocalSnapshotRestoresPreviousData(t *testing.T) {
	controller := NewMetadataController(false, filepath.Join(t.TempDir(), "answers.json"), 1, "")
	controller.local = newMetadataSource("https://primary.example/v1", "primary", "secret", true, controller.answersFile)
	controller.local.setData(testVersions("before"), nil, "1")
	if err := controller.reconcile(); err != nil {
		t.Fatal(err)
	}
	err := controller.applySourceData(controller.local, testVersions("bad"), []Credential{{
		URL: "https://attacker.example/v1", PublicValue: "external", SecretValue: "secret",
	}}, "2")
	if err == nil {
		t.Fatal("unsafe snapshot unexpectedly succeeded")
	}
	versions, credentials, version := controller.local.fullSnapshot()
	if version != "1" || len(credentials) != 0 || versions[version3][DEFAULT_KEY].(map[string]interface{})["name"] != "before" {
		t.Fatalf("previous snapshot was not restored: version=%q credentials=%d", version, len(credentials))
	}
}

func TestDuplicateExternalCredentialIdentifiersFailClosed(t *testing.T) {
	controller := NewMetadataController(false, filepath.Join(t.TempDir(), "answers.json"), 1, "")
	controller.local = newMetadataSource("https://primary.example/v1", "primary", "secret", true, controller.answersFile)
	controller.local.setData(testVersions("primary"), []Credential{
		{URL: "https://primary.example/v1", PublicValue: "duplicate", SecretValue: "one"},
		{URL: "https://primary.example/v1", PublicValue: "duplicate", SecretValue: "two"},
	}, "1")
	if err := controller.reconcile(); err == nil {
		t.Fatal("duplicate external credential identifier unexpectedly succeeded")
	}
}
