package player

import (
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

func TestPlaybackTransitions(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*testing.T, *StateMachine)
		action func(*StateMachine) (protocol.PlaybackState, *protocol.APIError)
		want   stateExpectation
	}{
		{
			name:   "play from stopped",
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Play("a") },
			want:   stateExpectation{status: protocol.StatusPlaying, current: "a", version: 1},
		},
		{
			name: "play archives current and preserves upcoming",
			setup: func(t *testing.T, m *StateMachine) {
				queueAddOK(t, m, "b")
				playOK(t, m, "a")
			},
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Play("c") },
			want:   stateExpectation{status: protocol.StatusPlaying, current: "c", history: []string{"a"}, queue: []string{"b"}, version: 3},
		},
		{
			name: "pause",
			setup: func(t *testing.T, m *StateMachine) {
				playOK(t, m, "a")
			},
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Pause() },
			want:   stateExpectation{status: protocol.StatusPaused, current: "a", version: 2},
		},
		{
			name: "resume",
			setup: func(t *testing.T, m *StateMachine) {
				playOK(t, m, "a")
				pauseOK(t, m)
			},
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Resume() },
			want:   stateExpectation{status: protocol.StatusPlaying, current: "a", version: 3},
		},
		{
			name: "stop retains current and queue",
			setup: func(t *testing.T, m *StateMachine) {
				playOK(t, m, "a")
				queueAddOK(t, m, "b")
			},
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Stop() },
			want:   stateExpectation{status: protocol.StatusStopped, current: "a", queue: []string{"b"}, version: 3},
		},
		{
			name: "next consumes upcoming and archives current",
			setup: func(t *testing.T, m *StateMachine) {
				playOK(t, m, "a")
				queueAddOK(t, m, "b")
			},
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Next() },
			want:   stateExpectation{status: protocol.StatusPlaying, current: "b", history: []string{"a"}, version: 3},
		},
		{
			name: "next with empty queue archives stopped current",
			setup: func(t *testing.T, m *StateMachine) {
				playOK(t, m, "a")
				stopOK(t, m)
			},
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Next() },
			want:   stateExpectation{status: protocol.StatusStopped, history: []string{"a"}, version: 3},
		},
		{
			name: "previous restores history and queues displaced current first",
			setup: func(t *testing.T, m *StateMachine) {
				playOK(t, m, "a")
				playOK(t, m, "b")
				queueAddOK(t, m, "c")
			},
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Previous() },
			want:   stateExpectation{status: protocol.StatusPlaying, current: "a", queue: []string{"b", "c"}, version: 4},
		},
		{
			name: "previous restarts stopped current without history",
			setup: func(t *testing.T, m *StateMachine) {
				playOK(t, m, "a")
				stopOK(t, m)
				m.mu.Lock()
				m.state.PositionMs = 1234 // Simulate playback progress before the stop/restart transition.
				m.mu.Unlock()
			},
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Previous() },
			want:   stateExpectation{status: protocol.StatusPlaying, current: "a", version: 3},
		},
		{
			name:   "previous with no current or history is a no-op",
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Previous() },
			want:   stateExpectation{status: protocol.StatusStopped, version: 0},
		},
		{
			name: "next while paused starts next track",
			setup: func(t *testing.T, m *StateMachine) {
				playOK(t, m, "a")
				queueAddOK(t, m, "b")
				pauseOK(t, m)
			},
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Next() },
			want:   stateExpectation{status: protocol.StatusPlaying, current: "b", history: []string{"a"}, version: 4},
		},
		{
			name: "previous while paused starts previous track",
			setup: func(t *testing.T, m *StateMachine) {
				playOK(t, m, "a")
				playOK(t, m, "b")
				pauseOK(t, m)
			},
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Previous() },
			want:   stateExpectation{status: protocol.StatusPlaying, current: "a", queue: []string{"b"}, version: 4},
		},
		{
			name: "previous then next returns to displaced track",
			setup: func(t *testing.T, m *StateMachine) {
				playOK(t, m, "a")
				playOK(t, m, "b")
				queueAddOK(t, m, "c")
				nextOK(t, m)
				previousOK(t, m)
			},
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Next() },
			want:   stateExpectation{status: protocol.StatusPlaying, current: "c", history: []string{"a", "b"}, version: 6},
		},
		{
			name: "duplicate track queue occurrences remain distinct",
			setup: func(t *testing.T, m *StateMachine) {
				queueAddOK(t, m, "a")
			},
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.QueueAdd("a") },
			want:   stateExpectation{status: protocol.StatusStopped, queue: []string{"a", "a"}, version: 2},
		},
		{
			name: "queue remove removes only the requested occurrence",
			setup: func(t *testing.T, m *StateMachine) {
				state := queueAddOK(t, m, "a")
				queueAddOK(t, m, "a")
				queueRemoveOK(t, m, state.Queue[0].ID)
			},
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.QueueAdd("b") },
			want:   stateExpectation{status: protocol.StatusStopped, queue: []string{"a", "b"}, version: 4},
		},
		{
			name: "queue clear leaves current and history untouched",
			setup: func(t *testing.T, m *StateMachine) {
				playOK(t, m, "a")
				playOK(t, m, "b")
				queueAddOK(t, m, "c")
			},
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.QueueClear() },
			want:   stateExpectation{status: protocol.StatusPlaying, current: "b", history: []string{"a"}, version: 4},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testMachine()
			if tt.setup != nil {
				tt.setup(t, m)
			}
			applyActionOK(t, tt.action, m)
			assertState(t, m.Snapshot(), tt.want)
		})
	}
}

func TestNoOpCommandsDoNotIncrementVersion(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*testing.T, *StateMachine)
		action func(*StateMachine) (protocol.PlaybackState, *protocol.APIError)
	}{
		{
			name: "pause while paused",
			setup: func(t *testing.T, m *StateMachine) {
				playOK(t, m, "a")
				pauseOK(t, m)
			},
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Pause() },
		},
		{
			name:   "resume while playing",
			setup:  func(t *testing.T, m *StateMachine) { playOK(t, m, "a") },
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Resume() },
		},
		{
			name:   "stop while stopped",
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Stop() },
		},
		{
			name:   "next with no current or queue",
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Next() },
		},
		{
			name:   "previous with no current or history",
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Previous() },
		},
		{
			name:   "previous at position zero while playing",
			setup:  func(t *testing.T, m *StateMachine) { playOK(t, m, "a") },
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Previous() },
		},
		{
			name:   "clear empty queue",
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.QueueClear() },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testMachine()
			if tt.setup != nil {
				tt.setup(t, m)
			}
			before := m.Snapshot()
			got := applyActionOK(t, tt.action, m)
			if got.Version != before.Version {
				t.Fatalf("no-op version = %d, want %d", got.Version, before.Version)
			}
			if after := m.Snapshot(); !reflect.DeepEqual(after, before) {
				t.Fatalf("no-op changed state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestRejectedMutationsDoNotChangeState(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*testing.T, *StateMachine)
		action func(*StateMachine) (protocol.PlaybackState, *protocol.APIError)
		code   protocol.ErrorCode
	}{
		{
			name:   "unknown track",
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Play("missing") },
			code:   protocol.ErrTrackNotFound,
		},
		{
			name:   "invalid track identifier",
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.QueueAdd("bad\ntrack") },
			code:   protocol.ErrInvalidPayload,
		},
		{
			name:   "pause while stopped",
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Pause() },
			code:   protocol.ErrInvalidState,
		},
		{
			name:   "resume while stopped",
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.Resume() },
			code:   protocol.ErrInvalidState,
		},
		{
			name: "unknown queue item",
			setup: func(t *testing.T, m *StateMachine) {
				queueAddOK(t, m, "a")
			},
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.QueueRemove("item-999") },
			code:   protocol.ErrQueueItemNotFound,
		},
		{
			name:   "invalid queue item identifier",
			action: func(m *StateMachine) (protocol.PlaybackState, *protocol.APIError) { return m.QueueRemove("bad\nitem") },
			code:   protocol.ErrInvalidPayload,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testMachine()
			if tt.setup != nil {
				tt.setup(t, m)
			}
			before := m.Snapshot()
			state, apiErr := tt.action(m)
			if apiErr == nil || apiErr.Code != tt.code {
				t.Fatalf("error = %v, want code %s", apiErr, tt.code)
			}
			if !reflect.DeepEqual(state, protocol.PlaybackState{}) {
				t.Fatalf("error returned state %+v; errors must not return authoritative state", state)
			}
			if after := m.Snapshot(); !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected mutation changed state: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestSnapshotsAreIsolated(t *testing.T) {
	m := testMachine()
	playOK(t, m, "a")
	queueAddOK(t, m, "b")
	snapshot := m.Snapshot()
	snapshot.Current.TrackID = "changed"
	snapshot.Queue[0].TrackID = "changed"
	snapshot.Queue = append(snapshot.Queue, protocol.QueueItem{ID: "injected", TrackID: "c"})

	got := m.Snapshot()
	if got.Current.TrackID != "a" || len(got.Queue) != 1 || got.Queue[0].TrackID != "b" {
		t.Fatalf("mutating snapshot changed machine state: %+v", got)
	}
}

func TestQueueRemovePreservesOtherSameTrackOccurrence(t *testing.T) {
	m := testMachine()
	first := queueAddOK(t, m, "a")
	second := queueAddOK(t, m, "a")
	state := queueRemoveOK(t, m, first.Queue[0].ID)
	if len(state.Queue) != 1 || state.Queue[0].ID != second.Queue[1].ID {
		t.Fatalf("queue after removing first occurrence = %+v, want remaining ID %q", state.Queue, second.Queue[1].ID)
	}
}

func TestConcurrentQueueAddsAreSerialized(t *testing.T) {
	const count = 128
	m := testMachine()
	apiErrors := make(chan *protocol.APIError, count)
	var wg sync.WaitGroup
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, apiErr := m.QueueAdd("a")
			apiErrors <- apiErr
		}()
	}
	wg.Wait()
	close(apiErrors)
	for apiErr := range apiErrors {
		if apiErr != nil {
			t.Fatalf("QueueAdd() error = %+v", apiErr)
		}
	}

	state := m.Snapshot()
	if len(state.Queue) != count || state.Version != count {
		t.Fatalf("concurrent state has %d queue items and version %d; want %d each", len(state.Queue), state.Version, count)
	}
	ids := make(map[string]struct{}, count)
	for _, item := range state.Queue {
		if _, exists := ids[item.ID]; exists {
			t.Fatalf("duplicate occurrence ID %q", item.ID)
		}
		ids[item.ID] = struct{}{}
	}
}

type stateExpectation struct {
	status  protocol.PlaybackStatus
	current string
	history []string
	queue   []string
	version uint64
}

func testMachine() *StateMachine {
	known := map[string]bool{"a": true, "b": true, "c": true}
	return NewStateMachine(func(trackID string) bool { return known[trackID] })
}

func applyOK(t *testing.T, state protocol.PlaybackState, apiErr *protocol.APIError) protocol.PlaybackState {
	t.Helper()
	if apiErr != nil {
		t.Fatalf("unexpected player error: %+v", apiErr)
	}
	return state
}

func assertState(t *testing.T, got protocol.PlaybackState, want stateExpectation) {
	t.Helper()
	current := ""
	if got.Current != nil {
		current = got.Current.TrackID
	}
	if got.Status != want.status || current != want.current || got.Version != want.version || got.PositionMs != 0 {
		t.Fatalf("state status/current/version/position = %s/%q/%d/%d, want %s/%q/%d/0", got.Status, current, got.Version, got.PositionMs, want.status, want.current, want.version)
	}
	if history := itemTrackIDs(got.History); !slices.Equal(history, want.history) {
		t.Fatalf("history tracks = %v, want %v", history, want.history)
	}
	if queue := itemTrackIDs(got.Queue); !slices.Equal(queue, want.queue) {
		t.Fatalf("queue tracks = %v, want %v", queue, want.queue)
	}
	if got.History == nil || got.Queue == nil {
		t.Fatalf("state arrays must be non-nil: %+v", got)
	}
}

func itemTrackIDs(items []protocol.QueueItem) []string {
	trackIDs := make([]string, len(items))
	for i, item := range items {
		trackIDs[i] = item.TrackID
	}
	return trackIDs
}

func pauseOK(t *testing.T, machine *StateMachine) protocol.PlaybackState {
	state, apiErr := machine.Pause()
	return applyOK(t, state, apiErr)
}

func stopOK(t *testing.T, machine *StateMachine) protocol.PlaybackState {
	state, apiErr := machine.Stop()
	return applyOK(t, state, apiErr)
}

func nextOK(t *testing.T, machine *StateMachine) protocol.PlaybackState {
	state, apiErr := machine.Next()
	return applyOK(t, state, apiErr)
}

func previousOK(t *testing.T, machine *StateMachine) protocol.PlaybackState {
	state, apiErr := machine.Previous()
	return applyOK(t, state, apiErr)
}

func queueRemoveOK(t *testing.T, machine *StateMachine, itemID string) protocol.PlaybackState {
	state, apiErr := machine.QueueRemove(itemID)
	return applyOK(t, state, apiErr)
}

func applyActionOK(t *testing.T, action func(*StateMachine) (protocol.PlaybackState, *protocol.APIError), machine *StateMachine) protocol.PlaybackState {
	state, apiErr := action(machine)
	return applyOK(t, state, apiErr)
}
