package kicker

import "sync"

type Kicker struct {
	mu         sync.Mutex
	cond       *sync.Cond
	f          func()
	running    bool
	pending    bool
	generation int
}

func New(f func()) *Kicker {
	kicker := &Kicker{f: f}
	kicker.cond = sync.NewCond(&kicker.mu)
	return kicker
}

func (k *Kicker) Kick() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.running {
		k.pending = true
		return k.generation + 2
	}
	k.running = true
	go k.run()
	return k.generation + 1
}

func (k *Kicker) Wait(generation int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for k.generation < generation {
		k.cond.Wait()
	}
}

func (k *Kicker) WaitIdle() {
	k.mu.Lock()
	defer k.mu.Unlock()
	for k.running {
		k.cond.Wait()
	}
}

func (k *Kicker) run() {
	for {
		k.f()
		k.mu.Lock()
		k.generation++
		k.cond.Broadcast()
		if !k.pending {
			k.running = false
			k.cond.Broadcast()
			k.mu.Unlock()
			return
		}
		k.pending = false
		k.mu.Unlock()
	}
}
