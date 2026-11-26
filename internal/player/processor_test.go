package player

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

func TestStopThenPlayRestartsBackendAndFencesOldCompletion(t *testing.T) {
	clock := newFakePlaybackClock()
	backend := newSimulationBackend(clock)
	p := NewCommandProcessor(testTrackIndex(), backend)
	t.Cleanup(func() { p.Close() })
	process := func(id string, kind protocol.CommandType, payload string) protocol.PlaybackState {
		t.Helper()
		result := p.Process(context.Background(), commandRequest(id, kind, payload))
		if !result.Success || result.State == nil {
			t.Fatalf("command failed: %+v", result)
		}
		return *result.State
	}
	process("play", protocol.PlaybackPlay, `{"track_id":"a"}`)
	process("queue", protocol.QueueAdd, `{"track_id":"b"}`)
	oldCompletion := backend.onComplete
	clock.Advance(100 * time.Millisecond)
	stopped := process("stop", protocol.PlaybackStop, `{}`)
	restarted := process("restart", protocol.PlaybackPlay, `{"track_id":"a"}`)
	want := cloneState(stopped)
	want.Status = protocol.StatusPlaying
	want.Version++
	if !reflect.DeepEqual(restarted, want) {
		t.Fatalf("restart changed retained state: %+v", restarted)
	}
	oldCompletion()
	if !reflect.DeepEqual(p.Snapshot(), restarted) {
		t.Fatal("old completion advanced the retained occurrence")
	}
	clock.Advance(100 * time.Millisecond)
	if got := p.Snapshot(); got.PositionMs != 100 || got.Version != restarted.Version {
		t.Fatalf("restarted backend did not advance correctly: %+v", got)
	}
}

func TestCommandProcessorDeduplicatesAndReturnsOriginalResponse(t *testing.T) {
	processor := newProcessorWithClock(t, newFakePlaybackClock())
	request := commandRequest("request-1", protocol.PlaybackPlay, `{"track_id":"a"}`)
	first := processor.Process(context.Background(), request)
	if !first.Success || first.State == nil || first.State.Version != 1 {
		t.Fatalf("first response = %+v, want successful version 1", first)
	}
	paused := processor.Process(context.Background(), commandRequest("request-2", protocol.PlaybackPause, `{}`))
	if !paused.Success || paused.State.Version != 2 {
		t.Fatalf("pause response = %+v, want successful version 2", paused)
	}

	request.Payload = json.RawMessage(`{ "track_id" : "a" }`)
	duplicate := processor.Process(context.Background(), request)
	if !duplicate.Success || duplicate.State == nil || duplicate.State.Version != 1 {
		t.Fatalf("duplicate response = %+v, want original version 1", duplicate)
	}
	if state := processor.Snapshot(); state.Version != 2 || state.Status != protocol.StatusPaused {
		t.Fatalf("duplicate re-executed command: state = %+v", state)
	}
}

func TestCommandProcessorRejectsRequestIDConflict(t *testing.T) {
	processor := newProcessorWithClock(t, newFakePlaybackClock())
	first := processor.Process(context.Background(), commandRequest("reuse", protocol.PlaybackPlay, `{"track_id":"a"}`))
	if !first.Success {
		t.Fatalf("play response = %+v", first)
	}
	conflict := processor.Process(context.Background(), commandRequest("reuse", protocol.PlaybackStop, `{}`))
	if conflict.Success || conflict.Error == nil || conflict.Error.Code != protocol.ErrDuplicateRequestConflict || conflict.State != nil {
		t.Fatalf("conflict response = %+v", conflict)
	}
	if state := processor.Snapshot(); state.Status != protocol.StatusPlaying || state.Version != 1 {
		t.Fatalf("conflicting request changed state: %+v", state)
	}
}

func TestCommandProcessorEvictsOldestDedupEntryAtCapacity(t *testing.T) {
	processor := newProcessorWithClock(t, newFakePlaybackClock())
	for i := 0; i <= defaultCommandCacheSize; i++ {
		request := commandRequest(fmt.Sprintf("request-%04d", i), protocol.QueueClear, `{}`)
		if response := processor.Process(context.Background(), request); !response.Success {
			t.Fatalf("queue clear %d response = %+v", i, response)
		}
	}
	response := processor.Process(context.Background(), commandRequest("request-0000", protocol.PlaybackPlay, `{"track_id":"a"}`))
	if !response.Success || response.State == nil || response.State.Version != 1 {
		t.Fatalf("evicted ID was not eligible for execution: %+v", response)
	}
}

func TestCommandProcessorSerializesConcurrentDuplicates(t *testing.T) {
	processor := newProcessorWithClock(t, newFakePlaybackClock())
	request := commandRequest("same-concurrent-id", protocol.PlaybackPlay, `{"track_id":"a"}`)
	const callers = 24
	responses := make(chan protocol.CommandResponse, callers)
	var wait sync.WaitGroup
	for i := 0; i < callers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			responses <- processor.Process(context.Background(), request)
		}()
	}
	wait.Wait()
	close(responses)
	for response := range responses {
		if !response.Success || response.State == nil || response.State.Version != 1 {
			t.Fatalf("concurrent duplicate response = %+v", response)
		}
	}
	if state := processor.Snapshot(); state.Version != 1 {
		t.Fatalf("version after concurrent duplicates = %d, want 1", state.Version)
	}
}

func TestCommandProcessorValidationAndPreparationFailuresAreAtomic(t *testing.T) {
	clock := newFakePlaybackClock()
	backend := &failingPrepareBackend{SimulationBackend: newSimulationBackend(clock), fail: true}
	processor := NewCommandProcessor(testTrackIndex(), backend)
	tests := []struct {
		name    string
		request protocol.CommandRequest
		want    protocol.ErrorCode
	}{
		{name: "unknown command", request: commandRequest("invalid-command", "queue.shuffle", `{}`), want: protocol.ErrInvalidCommand},
		{name: "malformed payload", request: commandRequest("bad-payload", protocol.PlaybackPlay, `{"track_id":`), want: protocol.ErrInvalidPayload},
		{name: "unknown track", request: commandRequest("missing-track", protocol.PlaybackPlay, `{"track_id":"missing"}`), want: protocol.ErrTrackNotFound},
		{name: "audio preparation", request: commandRequest("audio-failure", protocol.PlaybackPlay, `{"track_id":"a"}`), want: protocol.ErrAudio},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := processor.Process(context.Background(), tt.request)
			if response.Success || response.Error == nil || response.Error.Code != tt.want || response.State != nil {
				t.Fatalf("response = %+v, want error %s without state", response, tt.want)
			}
			if state := processor.Snapshot(); state.Version != 0 || state.Current != nil || state.Status != protocol.StatusStopped {
				t.Fatalf("rejected command partially changed state: %+v", state)
			}
		})
	}
}

func TestAudioPreparationFailurePreservesCurrentTrack(t *testing.T) {
	backend := &failingPrepareBackend{SimulationBackend: newSimulationBackend(newFakePlaybackClock())}
	processor := NewCommandProcessor(testTrackIndex(), backend)
	if response := processor.Process(context.Background(), commandRequest("play-a", protocol.PlaybackPlay, `{"track_id":"a"}`)); !response.Success {
		t.Fatalf("initial play response = %+v", response)
	}
	before := processor.Snapshot()
	backend.fail = true
	response := processor.Process(context.Background(), commandRequest("play-b", protocol.PlaybackPlay, `{"track_id":"b"}`))
	if response.Success || response.Error == nil || response.Error.Code != protocol.ErrAudio || response.State != nil {
		t.Fatalf("failed preparation response = %+v", response)
	}
	if after := processor.Snapshot(); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed preparation damaged current session: before=%+v after=%+v", before, after)
	}
}

func TestCommandProcessorSimulationPositionAndNaturalNext(t *testing.T) {
	clock := newFakePlaybackClock()
	backend := newSimulationBackend(clock)
	processor := NewCommandProcessor(testTrackIndex(), backend)
	play := processor.Process(context.Background(), commandRequest("play", protocol.PlaybackPlay, `{"track_id":"a"}`))
	if !play.Success {
		t.Fatalf("play response = %+v", play)
	}
	queued := processor.Process(context.Background(), commandRequest("add", protocol.QueueAdd, `{"track_id":"b"}`))
	if !queued.Success || len(queued.State.Queue) != 1 {
		t.Fatalf("queue response = %+v", queued)
	}
	clock.Advance(450 * time.Millisecond)
	if state := processor.Snapshot(); state.PositionMs != 450 || state.Version != 2 {
		t.Fatalf("position/version = %d/%d, want 450/2", state.PositionMs, state.Version)
	}

	paused := processor.Process(context.Background(), commandRequest("pause", protocol.PlaybackPause, `{}`))
	if !paused.Success || paused.State.PositionMs != 450 {
		t.Fatalf("pause response = %+v, want frozen position 450", paused)
	}
	clock.Advance(2 * time.Second)
	if state := processor.Snapshot(); state.PositionMs != 450 {
		t.Fatalf("paused position advanced to %d ms", state.PositionMs)
	}
	resumed := processor.Process(context.Background(), commandRequest("resume", protocol.PlaybackResume, `{}`))
	if !resumed.Success {
		t.Fatalf("resume response = %+v", resumed)
	}
	clock.Advance(550 * time.Millisecond)
	state := processor.Snapshot()
	if state.Status != protocol.StatusPlaying || state.Current == nil || state.Current.TrackID != "b" ||
		len(state.History) != 1 || state.History[0].TrackID != "a" || len(state.Queue) != 0 || state.Version != 5 {
		t.Fatalf("natural completion did not advance with Next semantics: %+v", state)
	}
}

func TestCommandProcessorNaturalCompletionStopsAndArchivesWhenQueueIsEmpty(t *testing.T) {
	clock := newFakePlaybackClock()
	processor := NewCommandProcessor(testTrackIndex(), newSimulationBackend(clock))
	if response := processor.Process(context.Background(), commandRequest("play", protocol.PlaybackPlay, `{"track_id":"a"}`)); !response.Success {
		t.Fatalf("play response = %+v", response)
	}
	clock.Advance(time.Second)
	state := processor.Snapshot()
	if state.Status != protocol.StatusStopped || state.Current != nil || len(state.History) != 1 ||
		state.History[0].TrackID != "a" || state.PositionMs != 0 || state.Version != 2 {
		t.Fatalf("natural completion with empty queue = %+v", state)
	}
}

func TestCommandProcessorPreviousThenNextRestoresDisplacedTrack(t *testing.T) {
	processor := newProcessorWithClock(t, newFakePlaybackClock())
	for _, request := range []protocol.CommandRequest{
		commandRequest("play-a", protocol.PlaybackPlay, `{"track_id":"a"}`),
		commandRequest("play-b", protocol.PlaybackPlay, `{"track_id":"b"}`),
		commandRequest("queue-c", protocol.QueueAdd, `{"track_id":"c"}`),
		commandRequest("previous", protocol.PlaybackPrevious, `{}`),
	} {
		if response := processor.Process(context.Background(), request); !response.Success {
			t.Fatalf("%s response = %+v", request.Type, response)
		}
	}
	if response := processor.Process(context.Background(), commandRequest("next", protocol.PlaybackNext, `{}`)); !response.Success {
		t.Fatalf("next response = %+v", response)
	}
	state := processor.Snapshot()
	if state.Current == nil || state.Current.TrackID != "b" || len(state.Queue) != 1 || state.Queue[0].TrackID != "c" {
		t.Fatalf("Previous then Next did not restore displaced track: %+v", state)
	}
}

func TestCommandProcessorIgnoresCompletionFromSupersededTrack(t *testing.T) {
	clock := newFakePlaybackClock()
	backend := newSimulationBackend(clock)
	processor := NewCommandProcessor(testTrackIndex(), backend)
	if response := processor.Process(context.Background(), commandRequest("play-a", protocol.PlaybackPlay, `{"track_id":"a"}`)); !response.Success {
		t.Fatalf("play a response = %+v", response)
	}
	staleCompletion := backend.onComplete
	if response := processor.Process(context.Background(), commandRequest("play-b", protocol.PlaybackPlay, `{"track_id":"b"}`)); !response.Success {
		t.Fatalf("play b response = %+v", response)
	}
	staleCompletion()
	state := processor.Snapshot()
	if state.Status != protocol.StatusPlaying || state.Current == nil || state.Current.TrackID != "b" || state.Version != 2 {
		t.Fatalf("stale completion changed the new playback: %+v", state)
	}
}

func TestCommandProcessorStopsCleanlyWhenNaturalAdvanceCannotPrepareAudio(t *testing.T) {
	clock := newFakePlaybackClock()
	backend := &failingPrepareBackend{SimulationBackend: newSimulationBackend(clock)}
	processor := NewCommandProcessor(testTrackIndex(), backend)
	for _, request := range []protocol.CommandRequest{
		commandRequest("play-a", protocol.PlaybackPlay, `{"track_id":"a"}`),
		commandRequest("queue-b", protocol.QueueAdd, `{"track_id":"b"}`),
	} {
		if response := processor.Process(context.Background(), request); !response.Success {
			t.Fatalf("%s response = %+v", request.Type, response)
		}
	}
	backend.fail = true
	clock.Advance(time.Second)
	state := processor.Snapshot()
	if state.Status != protocol.StatusStopped || state.Current == nil || state.Current.TrackID != "b" ||
		len(state.History) != 1 || state.History[0].TrackID != "a" || len(state.Queue) != 0 || state.Version != 4 {
		t.Fatalf("failed natural preparation left inconsistent playback: %+v", state)
	}
}

func TestCommandProcessorDoesNotPrepareNoOpPreviousAtTrackStart(t *testing.T) {
	clock := newFakePlaybackClock()
	backend := &failingPrepareBackend{SimulationBackend: newSimulationBackend(clock)}
	processor := NewCommandProcessor(testTrackIndex(), backend)
	if response := processor.Process(context.Background(), commandRequest("play-a", protocol.PlaybackPlay, `{"track_id":"a"}`)); !response.Success {
		t.Fatalf("play response = %+v", response)
	}
	backend.fail = true
	response := processor.Process(context.Background(), commandRequest("previous", protocol.PlaybackPrevious, `{}`))
	if !response.Success || response.State == nil || response.State.Version != 1 || response.State.Current == nil || response.State.Current.TrackID != "a" {
		t.Fatalf("no-op previous unexpectedly prepared/restarted audio: %+v", response)
	}
}

func newProcessorWithClock(t *testing.T, clock *fakePlaybackClock) *CommandProcessor {
	t.Helper()
	return NewCommandProcessor(testTrackIndex(), newSimulationBackend(clock))
}

func testTrackIndex() TrackIndex {
	return TrackIndex{
		"a": {ID: "a", DurationMs: 1000, URL: "http://library/audio/a.mp3"},
		"b": {ID: "b", DurationMs: 1000, URL: "http://library/audio/b.mp3"},
		"c": {ID: "c", DurationMs: 1000, URL: "http://library/audio/c.mp3"},
	}
}

func commandRequest(id string, command protocol.CommandType, payload string) protocol.CommandRequest {
	return protocol.CommandRequest{RequestID: id, Type: command, Payload: json.RawMessage(payload)}
}

type failingPrepareBackend struct {
	*SimulationBackend
	fail bool
}

func (b *failingPrepareBackend) Prepare(ctx context.Context, track protocol.Track) error {
	if b.fail {
		return errors.New("test audio preparation failure")
	}
	return b.SimulationBackend.Prepare(ctx, track)
}

var _ PlaybackBackend = (*failingPrepareBackend)(nil)
