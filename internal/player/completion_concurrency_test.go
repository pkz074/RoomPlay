package player

import (
	"context"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

// Hold Play just after the processor installs the new completion callback,
// while the backend still has the old track and its live completion timer.
type blockedPlayBackend struct {
	*SimulationBackend
	entered, release, completionStarted chan struct{}
	enterOnce                           sync.Once
}

func (b *blockedPlayBackend) Play(track protocol.Track, position int64) {
	if track.ID == "b" {
		b.enterOnce.Do(func() { close(b.entered) })
		<-b.release
	}
	b.SimulationBackend.Play(track, position)
}

func (b *blockedPlayBackend) SetOnCompletion(callback func()) {
	if callback == nil {
		b.SimulationBackend.SetOnCompletion(nil)
		return
	}
	b.SimulationBackend.SetOnCompletion(func() {
		select {
		case b.completionStarted <- struct{}{}:
		default:
		}
		callback()
	})
}

func TestCompletionDuringCommandDoesNotAdvanceNewTrack(t *testing.T) {
	for _, command := range []protocol.CommandType{protocol.PlaybackPlay, protocol.PlaybackNext} {
		t.Run(string(command), func(t *testing.T) {
			clock := newFakePlaybackClock()
			backend := &blockedPlayBackend{SimulationBackend: newSimulationBackend(clock), entered: make(chan struct{}), release: make(chan struct{}), completionStarted: make(chan struct{}, 1)}
			var once sync.Once
			unblock := func() { once.Do(func() { close(backend.release) }) }
			t.Cleanup(unblock)
			processor := NewCommandProcessor(testTrackIndex(), backend)
			t.Cleanup(func() { unblock(); processor.Close() })
			for _, request := range []protocol.CommandRequest{
				commandRequest("play-a", protocol.PlaybackPlay, `{"track_id":"a"}`),
				commandRequest("add-b", protocol.QueueAdd, `{"track_id":"b"}`),
				commandRequest("add-c", protocol.QueueAdd, `{"track_id":"c"}`),
			} {
				if response := processor.Process(context.Background(), request); !response.Success {
					t.Fatalf("setup: %+v", response)
				}
			}
			payload := `{}`
			if command == protocol.PlaybackPlay {
				payload = `{"track_id":"b"}`
			}
			responseCh := make(chan protocol.CommandResponse, 1)
			go func() {
				responseCh <- processor.Process(context.Background(), commandRequest("navigate", command, payload))
			}()
			awaitPlayer(t, backend.entered)
			completionDone := make(chan struct{})
			go func() { clock.Advance(time.Second); close(completionDone) }()
			awaitPlayer(t, backend.completionStarted)
			unblock()
			response := awaitPlayer(t, responseCh)
			awaitPlayer(t, completionDone)
			if !response.Success || response.State == nil {
				t.Fatalf("command: %+v", response)
			}
			if got := processor.Snapshot(); !reflect.DeepEqual(got, *response.State) {
				t.Fatalf("old completion changed new track: command=%+v final=%+v", *response.State, got)
			}
		})
	}
}

func TestSimulationCompletionUsesScheduledCallback(t *testing.T) {
	clock := newFakePlaybackClock()
	backend := newSimulationBackend(clock)
	defer backend.Close()
	old, replacement := 0, 0
	backend.SetOnCompletion(func() { old++ })
	backend.Play(protocol.Track{ID: "a", DurationMs: 1000}, 0)
	backend.SetOnCompletion(func() { replacement++ })
	// An early timer firing must retain its original callback when rescheduled.
	clock.timers[0].callback()
	clock.Advance(time.Second)
	if old != 1 || replacement != 0 {
		t.Fatalf("completion callbacks: old=%d replacement=%d", old, replacement)
	}
}

func TestDelayedCompletionAfterCommandIsIgnored(t *testing.T) {
	for _, action := range []string{"play", "next", "stop", "previous", "pause-resume", "close"} {
		t.Run(action, func(t *testing.T) {
			clock := newFakePlaybackClock()
			backend := newSimulationBackend(clock)
			processor := NewCommandProcessor(testTrackIndex(), backend)
			t.Cleanup(func() { processor.Close() })
			for _, request := range []protocol.CommandRequest{
				commandRequest("play-a", protocol.PlaybackPlay, `{"track_id":"a"}`),
				commandRequest("queue-b", protocol.QueueAdd, `{"track_id":"b"}`),
			} {
				if response := processor.Process(context.Background(), request); !response.Success {
					t.Fatalf("setup: %+v", response)
				}
			}
			clock.Advance(200 * time.Millisecond)
			backend.mu.Lock()
			completion := backend.onComplete
			backend.mu.Unlock()
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			go func() { close(entered); <-release; completion(); close(done) }()
			awaitPlayer(t, entered)
			commands := []protocol.CommandRequest{}
			switch action {
			case "play":
				commands = append(commands, commandRequest("replace", protocol.PlaybackPlay, `{"track_id":"a"}`))
			case "next":
				commands = append(commands, commandRequest("next", protocol.PlaybackNext, `{}`))
			case "stop":
				commands = append(commands, commandRequest("stop", protocol.PlaybackStop, `{}`))
			case "previous":
				commands = append(commands, commandRequest("previous", protocol.PlaybackPrevious, `{}`))
			case "pause-resume":
				commands = append(commands, commandRequest("pause", protocol.PlaybackPause, `{}`), commandRequest("resume", protocol.PlaybackResume, `{}`))
			case "close":
				processor.Close()
			}
			for _, request := range commands {
				if response := processor.Process(context.Background(), request); !response.Success {
					t.Fatalf("command: %+v", response)
				}
			}
			want := processor.Snapshot()
			unblock()
			awaitPlayer(t, done)
			if got := processor.Snapshot(); !reflect.DeepEqual(got, want) {
				t.Fatalf("delayed completion changed state: before=%+v after=%+v", want, got)
			}
		})
	}
}

func TestAudioCompletionUsesScheduledCallback(t *testing.T) {
	clock := newFakePlaybackClock()
	output := &fakeAudioOutput{}
	backend := newAudioBackend(output, nil, clock)
	defer backend.Close()
	pcm := newSeekablePCM(make([]byte, audioOutputSampleRate*audioBytesPerFrame))
	backend.prepared = &preparedAudio{trackID: "a", pcm: pcm, player: output.NewPlayer(pcm), durationMs: 1000}
	old, replacement := 0, 0
	backend.SetOnCompletion(func() { old++ })
	backend.Play(protocol.Track{ID: "a"}, 0)
	backend.SetOnCompletion(func() { replacement++ })
	clock.timers[0].callback()
	if _, err := io.Copy(io.Discard, pcm); err != nil {
		t.Fatal(err)
	}
	// Force a drain poll as well: callback identity must survive rescheduling.
	clock.Advance(time.Second)
	output.players[0].setPlaying(false)
	clock.Advance(audioCompletionPoll)
	if old != 1 || replacement != 0 {
		t.Fatalf("completion callbacks: old=%d replacement=%d", old, replacement)
	}
}

func awaitPlayer[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("player operation deadlocked")
		var zero T
		return zero
	}
}
