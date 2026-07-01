package protocol

import (
	"sync"
	"time"
)

// Event is a one-shot, broadcast signal mirroring threading.Event: Set is
// idempotent, IsSet is a non-blocking check, Wait blocks up to d and reports
// whether the event was set.
type Event struct {
	mu  sync.Mutex
	ch  chan struct{}
	set bool
}

func NewEvent() *Event { return &Event{ch: make(chan struct{})} }

func (e *Event) Set() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.set {
		e.set = true
		close(e.ch)
	}
}

func (e *Event) IsSet() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.set
}

// Wait blocks until the event is set or d elapses; returns true iff set.
func (e *Event) Wait(d time.Duration) bool {
	if e.IsSet() {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-e.ch:
		return true
	case <-t.C:
		return e.IsSet()
	}
}
