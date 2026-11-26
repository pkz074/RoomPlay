package player

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

const maxPlaybackTimerMilliseconds = int64((1<<63 - 1) / int64(time.Millisecond))

// PlaybackBackend separates logical commands from the playback implementation.
type PlaybackBackend interface {
	Prepare(context.Context, protocol.Track) error
	Play(protocol.Track, int64)
	Pause() int64
	Resume()
	Stop()
	PositionMs() int64
	Close() error
}

type playbackTimer interface {
	Stop() bool
}

type playbackClock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) playbackTimer
}

type systemPlaybackClock struct{}

func (systemPlaybackClock) Now() time.Time { return time.Now() }

func (systemPlaybackClock) AfterFunc(delay time.Duration, callback func()) playbackTimer {
	return time.AfterFunc(delay, callback)
}

// SimulationBackend advances position from a monotonic clock and schedules only track-end timers.
type SimulationBackend struct {
	mu         sync.Mutex
	clock      playbackClock
	track      protocol.Track
	baseMs     int64
	startedAt  time.Time
	playing    bool
	closed     bool
	timer      playbackTimer
	generation uint64
	onComplete func()
}

func NewSimulationBackend() *SimulationBackend {
	return newSimulationBackend(systemPlaybackClock{})
}

func newSimulationBackend(clock playbackClock) *SimulationBackend {
	if clock == nil {
		clock = systemPlaybackClock{}
	}
	return &SimulationBackend{clock: clock}
}

func (b *SimulationBackend) SetOnCompletion(callback func()) {
	b.mu.Lock()
	b.onComplete = callback
	b.mu.Unlock()
}

func (b *SimulationBackend) Prepare(ctx context.Context, track protocol.Track) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !protocol.ValidIdentifier(track.ID) || track.DurationMs < 0 {
		return fmt.Errorf("invalid track metadata")
	}
	return nil
}

func (b *SimulationBackend) Play(track protocol.Track, positionMs int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.cancelTimerLocked()
	b.track = track
	b.baseMs = positionMs
	if b.baseMs < 0 {
		b.baseMs = 0
	}
	if track.DurationMs > 0 && b.baseMs > track.DurationMs {
		b.baseMs = track.DurationMs
	}
	b.playing = true
	b.startedAt = b.clock.Now()
	b.scheduleCompletionLocked(b.onComplete)
}

func (b *SimulationBackend) Pause() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.playing {
		b.baseMs = b.positionLocked()
		b.playing = false
		b.cancelTimerLocked()
	}
	return b.baseMs
}

func (b *SimulationBackend) Resume() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.playing || b.track.ID == "" {
		return
	}
	b.playing = true
	b.startedAt = b.clock.Now()
	b.scheduleCompletionLocked(b.onComplete)
}

func (b *SimulationBackend) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cancelTimerLocked()
	b.playing = false
	b.baseMs = 0
}

func (b *SimulationBackend) PositionMs() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.positionLocked()
}

func (b *SimulationBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cancelTimerLocked()
	b.playing = false
	b.closed = true
	return nil
}

func (b *SimulationBackend) positionLocked() int64 {
	position := b.baseMs
	if b.playing {
		elapsed := b.clock.Now().Sub(b.startedAt).Milliseconds()
		if elapsed > 0 {
			const maxInt64 = int64(1<<63 - 1)
			if position > maxInt64-elapsed {
				position = maxInt64
			} else {
				position += elapsed
			}
		}
	}
	if position < 0 {
		return 0
	}
	if b.track.ID != "" && b.track.DurationMs >= 0 && position > b.track.DurationMs {
		return b.track.DurationMs
	}
	return position
}

func (b *SimulationBackend) cancelTimerLocked() {
	b.generation++
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
}

func (b *SimulationBackend) scheduleCompletionLocked(callback func()) {
	if !b.playing || b.track.DurationMs < 0 {
		return
	}
	b.generation++
	generation := b.generation
	remainingMs := b.track.DurationMs - b.positionLocked()
	if remainingMs < 0 {
		remainingMs = 0
	}
	if remainingMs > maxPlaybackTimerMilliseconds {
		remainingMs = maxPlaybackTimerMilliseconds
	}
	remaining := time.Duration(remainingMs) * time.Millisecond
	// Bind the notification to this playback, before another command can install
	// its callback while the old timer is still in flight.
	b.timer = b.clock.AfterFunc(remaining, func() { b.complete(generation, callback) })
}

func (b *SimulationBackend) complete(generation uint64, callback func()) {
	b.mu.Lock()
	if b.closed || !b.playing || generation != b.generation {
		b.mu.Unlock()
		return
	}
	if b.positionLocked() < b.track.DurationMs {
		b.scheduleCompletionLocked(callback)
		b.mu.Unlock()
		return
	}
	b.baseMs = b.track.DurationMs
	b.playing = false
	b.timer = nil
	b.mu.Unlock()
	if callback != nil {
		callback()
	}
}
