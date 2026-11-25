package player

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

func TestSimulationBackendUsesMonotonicElapsedPosition(t *testing.T) {
	clock := newFakePlaybackClock()
	backend := newSimulationBackend(clock)
	track := protocol.Track{ID: "track", DurationMs: 10000}
	if err := backend.Prepare(context.Background(), track); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	backend.Play(track, 125)
	clock.Advance(2*time.Second + 300*time.Millisecond)
	if got := backend.PositionMs(); got != 2425 {
		t.Fatalf("position = %d ms, want 2425", got)
	}
	if got := backend.Pause(); got != 2425 {
		t.Fatalf("paused position = %d ms, want 2425", got)
	}
	clock.Advance(7 * time.Second)
	if got := backend.PositionMs(); got != 2425 {
		t.Fatalf("paused position advanced to %d ms", got)
	}
	backend.Resume()
	clock.Advance(575 * time.Millisecond)
	if got := backend.PositionMs(); got != 3000 {
		t.Fatalf("resumed position = %d ms, want 3000", got)
	}
	backend.Stop()
	if got := backend.PositionMs(); got != 0 {
		t.Fatalf("stopped position = %d ms, want 0", got)
	}
}

func TestSimulationBackendSignalsNaturalCompletion(t *testing.T) {
	clock := newFakePlaybackClock()
	backend := newSimulationBackend(clock)
	completed := 0
	backend.SetOnCompletion(func() { completed++ })
	backend.Play(protocol.Track{ID: "track", DurationMs: 1000}, 0)
	clock.Advance(999 * time.Millisecond)
	if completed != 0 {
		t.Fatalf("completion count before duration = %d, want 0", completed)
	}
	clock.Advance(time.Millisecond)
	if completed != 1 || backend.PositionMs() != 1000 {
		t.Fatalf("completion count=%d position=%d, want 1 and 1000ms", completed, backend.PositionMs())
	}
}

func TestSimulationBackendCompletesZeroDurationTrack(t *testing.T) {
	clock := newFakePlaybackClock()
	backend := newSimulationBackend(clock)
	completed := 0
	backend.SetOnCompletion(func() { completed++ })
	backend.Play(protocol.Track{ID: "instant", DurationMs: 0}, 0)
	clock.Advance(0)
	if completed != 1 {
		t.Fatalf("zero-duration completion count = %d, want 1", completed)
	}
}

type fakePlaybackClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakePlaybackTimer
}

type fakePlaybackTimer struct {
	clock    *fakePlaybackClock
	at       time.Time
	callback func()
	stopped  bool
}

func newFakePlaybackClock() *fakePlaybackClock {
	return &fakePlaybackClock{now: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakePlaybackClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakePlaybackClock) AfterFunc(delay time.Duration, callback func()) playbackTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakePlaybackTimer{clock: c, at: c.now.Add(delay), callback: callback}
	c.timers = append(c.timers, timer)
	return timer
}

func (timer *fakePlaybackTimer) Stop() bool {
	timer.clock.mu.Lock()
	defer timer.clock.mu.Unlock()
	if timer.stopped {
		return false
	}
	timer.stopped = true
	return true
}

func (c *fakePlaybackClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *fakePlaybackTimer
		for _, timer := range c.timers {
			if timer.stopped || timer.at.After(c.now) {
				continue
			}
			if next == nil || timer.at.Before(next.at) {
				next = timer
			}
		}
		if next == nil {
			c.mu.Unlock()
			return
		}
		next.stopped = true
		callback := next.callback
		c.mu.Unlock()
		callback()
	}
}

var _ playbackClock = (*fakePlaybackClock)(nil)