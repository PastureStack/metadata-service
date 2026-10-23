package main

import (
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func tokenAnswers(token string) Versions {
	versions := testVersions("unchanged-payload")
	for _, date := range versionList {
		versions[date][DEFAULT_KEY].(map[string]interface{})["version"] = token
		versions[date]["client"] = map[string]interface{}{"version": token, "payload": "preserved"}
	}
	return versions
}

func TestMergePreservesLocalChangeTokenWithoutOverride(t *testing.T) {
	for _, token := range []string{"7", "403531-0123456789abcdef0123456789abcdef", "9223372036854775808", "opaque-token", ""} {
		t.Run(token, func(t *testing.T) {
			local := tokenAnswers(token)
			before := cloneVersions(local)
			merged := MergeVersions(local, nil, "")
			for _, date := range append(append([]string{}, versionList...), "latest") {
				for _, client := range []string{DEFAULT_KEY, "client"} {
					got, ok := merged.Matching(date, client, []string{"version"})
					if !ok || got != token {
						t.Fatalf("%s/%s token = %#v, want %q", date, client, got, token)
					}
				}
			}
			if !reflect.DeepEqual(local, before) {
				t.Fatal("merge mutated the local source")
			}
		})
	}
}

func TestMergePreservesEachDatasetTokenInsteadOfSelectingNumericMaximum(t *testing.T) {
	local := tokenAnswers("baseline")
	local[version1][DEFAULT_KEY].(map[string]interface{})["version"] = "legacy-1"
	local[version2][DEFAULT_KEY].(map[string]interface{})["version"] = "100"
	local[version3][DEFAULT_KEY].(map[string]interface{})["version"] = "latest-3"
	merged := MergeVersions(local, nil, "")
	for date, expected := range map[string]string{version1: "legacy-1", version2: "100", version3: "latest-3", "latest": "latest-3"} {
		got, ok := merged.Matching(date, DEFAULT_KEY, []string{"version"})
		if !ok || got != expected {
			t.Fatalf("%s token = %#v, want %q", date, got, expected)
		}
	}
}

func TestMergeExplicitTokenAndExternalViewsRemainCompatible(t *testing.T) {
	for _, external := range [][]Versions{nil, {testVersions("region-a"), testVersions("region-b")}} {
		local := tokenAnswers("local-opaque")
		before := cloneVersions(local)
		merged := MergeVersions(local, external, "explicit-override")
		for _, date := range versionList {
			got, ok := merged.Matching(date, DEFAULT_KEY, []string{"version"})
			if !ok || got != "explicit-override" {
				t.Fatalf("override for %s = %#v", date, got)
			}
		}
		environments := merged[version3][DEFAULT_KEY].(map[string]interface{})["environments"].([]interface{})
		if len(environments) != len(external) {
			t.Fatalf("external environment count = %d, want %d", len(environments), len(external))
		}
		for i, environment := range environments {
			if !reflect.DeepEqual(environment, external[i][version3][DEFAULT_KEY]) {
				t.Fatal("external environment contents or order changed")
			}
		}
		if !reflect.DeepEqual(local, before) {
			t.Fatal("explicit merge mutated its local source")
		}
	}
	for _, local := range []Versions{nil, {}} {
		if got := MergeVersions(local, nil, ""); !reflect.DeepEqual(got, local) {
			t.Fatal("nil/empty source changed")
		}
	}
}

// Cond.Wait takes its notification ticket before calling Locker.Unlock.
// This barrier proves the waiter is registered, without sleep or scheduler luck.
type enteredWaitLocker struct {
	sync.Locker
	entered chan struct{}
}

func (locker enteredWaitLocker) Unlock() {
	locker.Locker.Unlock()
	locker.entered <- struct{}{}
}

func TestLocalOpaqueTokenChangesWakeWaiterTwiceAndUnchangedDoesNot(t *testing.T) {
	controller := NewMetadataController(false, filepath.Join(t.TempDir(), "answers.json"), 1, "")
	entered := make(chan struct{}, 1)
	controller.cond.L = enteredWaitLocker{Locker: &controller.mu, entered: entered}
	controller.local = newMetadataSource("", "", "", true, controller.answersFile)
	tokens := []string{"1-0123456789abcdef", "2-0123456789abcdef", "3-0123456789abcdef"}
	controller.local.setData(tokenAnswers(tokens[0]), nil, tokens[0])
	if err := controller.reconcile(); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(tokens); i++ {
		done := make(chan interface{}, 1)
		previous := tokens[i-1]
		go func() {
			value, _ := controller.LookupAnswer(true, previous, version3, DEFAULT_KEY, []string{"version"}, 30*time.Second)
			done <- value
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("lookup did not register a waiting notification ticket")
		}
		controller.local.setData(tokenAnswers(tokens[i]), nil, tokens[i])
		if err := controller.reconcile(); err != nil {
			t.Fatal(err)
		}
		select {
		case value := <-done:
			if value != tokens[i] {
				t.Fatalf("watcher returned %#v, want %q", value, tokens[i])
			}
		case <-time.After(time.Second):
			controller.cond.Broadcast()
			t.Fatal("watcher did not observe the opaque-token update")
		}
	}
	if err := controller.reconcile(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	value, ok := controller.LookupAnswer(true, tokens[2], version3, DEFAULT_KEY, []string{"version"}, 20*time.Millisecond)
	if !ok || value != tokens[2] || time.Since(start) < 15*time.Millisecond {
		t.Fatalf("unchanged token caused a false notification: %#v, %v", value, ok)
	}
}
