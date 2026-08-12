package kicker

import (
	"sync/atomic"
	"testing"
	"time"
)

func receiveWithin(t *testing.T, ch <-chan int) int {
	t.Helper()

	select {
	case value := <-ch:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker")
		return 0
	}
}

func waitWithin(t *testing.T, k *Kicker, generation int) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		k.Wait(generation)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for generation")
	}
}

func TestKickCoalescesPendingWorkWithoutLosingIt(t *testing.T) {
	started := make(chan int, 2)
	release := make(chan struct{}, 2)
	var runs atomic.Int32

	k := New(func() {
		started <- int(runs.Add(1))
		<-release
	})

	firstGeneration := k.Kick()
	if firstGeneration != 1 {
		t.Fatalf("first generation = %d, want 1", firstGeneration)
	}
	if got := receiveWithin(t, started); got != 1 {
		t.Fatalf("first run = %d, want 1", got)
	}

	pendingGeneration := k.Kick()
	if pendingGeneration != 2 {
		t.Fatalf("pending generation = %d, want 2", pendingGeneration)
	}

	release <- struct{}{}
	waitWithin(t, k, firstGeneration)
	if got := receiveWithin(t, started); got != 2 {
		t.Fatalf("second run = %d, want 2", got)
	}

	release <- struct{}{}
	waitWithin(t, k, pendingGeneration)
	if got := runs.Load(); got != 2 {
		t.Fatalf("runs = %d, want 2", got)
	}
}
