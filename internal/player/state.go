package player

import (
	"fmt"
	"math"
	"sync"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

// TrackExistsFunc reports whether a track ID is present in the player's catalog.
type TrackExistsFunc func(trackID string) bool

// StateMachine serializes playback and queue mutations for one room.
type StateMachine struct {
	mu          sync.Mutex
	state       protocol.PlaybackState
	trackExists TrackExistsFunc
	nextItemID  uint64
}

// NewStateMachine creates an empty player state. A nil track lookup rejects all tracks.
func NewStateMachine(trackExists TrackExistsFunc) *StateMachine {
	return &StateMachine{
		state:       protocol.NewPlaybackState(),
		trackExists: trackExists,
	}
}

// Snapshot returns an isolated copy of the current playback state.
func (m *StateMachine) Snapshot() protocol.PlaybackState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneState(m.state)
}

func (m *StateMachine) setPositionMs(positionMs int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if positionMs >= 0 && m.state.Current != nil && m.state.Status != protocol.StatusStopped {
		m.state.PositionMs = positionMs
	}
}

// Play selects a new occurrence, retaining the upcoming queue and moving the prior current item to history.
func (m *StateMachine) Play(trackID string) (protocol.PlaybackState, *protocol.APIError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if apiErr := m.validateTrackLocked(trackID); apiErr != nil {
		return protocol.PlaybackState{}, apiErr
	}
	item, apiErr := m.newItemLocked(trackID)
	if apiErr != nil {
		return protocol.PlaybackState{}, apiErr
	}

	next := cloneState(m.state)
	if next.Current != nil {
		next.History = append(next.History, *next.Current)
	}
	next.Current = &item
	next.Status = protocol.StatusPlaying
	next.PositionMs = 0
	return m.commitLocked(next, true)
}

// Pause freezes the selected track; pausing a stopped player is invalid.
func (m *StateMachine) Pause() (protocol.PlaybackState, *protocol.APIError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Status == protocol.StatusStopped {
		return protocol.PlaybackState{}, playerError(protocol.ErrInvalidState, "Cannot pause while stopped.")
	}
	if m.state.Status == protocol.StatusPaused {
		return cloneState(m.state), nil
	}

	next := cloneState(m.state)
	next.Status = protocol.StatusPaused
	return m.commitLocked(next, true)
}

// Resume resumes a paused track; resuming a stopped player is invalid.
func (m *StateMachine) Resume() (protocol.PlaybackState, *protocol.APIError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Status == protocol.StatusStopped {
		return protocol.PlaybackState{}, playerError(protocol.ErrInvalidState, "Cannot resume while stopped.")
	}
	if m.state.Status == protocol.StatusPlaying {
		return cloneState(m.state), nil
	}

	next := cloneState(m.state)
	next.Status = protocol.StatusPlaying
	return m.commitLocked(next, true)
}

// Stop retains the selected track, history, and upcoming queue, and resets its position.
func (m *StateMachine) Stop() (protocol.PlaybackState, *protocol.APIError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Status == protocol.StatusStopped {
		return cloneState(m.state), nil
	}

	next := cloneState(m.state)
	next.Status = protocol.StatusStopped
	next.PositionMs = 0
	return m.commitLocked(next, true)
}

// Next advances to the first upcoming occurrence, or stops and archives the current item if the queue is empty.
func (m *StateMachine) Next() (protocol.PlaybackState, *protocol.APIError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.state.Queue) == 0 && m.state.Current == nil {
		return cloneState(m.state), nil
	}

	next := cloneState(m.state)
	if len(next.Queue) == 0 {
		next.History = append(next.History, *next.Current)
		next.Current = nil
		next.Status = protocol.StatusStopped
		next.PositionMs = 0
	} else {
		if next.Current != nil {
			next.History = append(next.History, *next.Current)
		}
		next.Current = &next.Queue[0]
		next.Queue = append(make([]protocol.QueueItem, 0, len(next.Queue)-1), next.Queue[1:]...)
		next.Status = protocol.StatusPlaying
		next.PositionMs = 0
	}
	return m.commitLocked(next, true)
}

// Previous restores the most recent history item, or restarts the current item when history is empty.
func (m *StateMachine) Previous() (protocol.PlaybackState, *protocol.APIError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.state.History) == 0 {
		if m.state.Current == nil {
			return cloneState(m.state), nil
		}
		if m.state.Status == protocol.StatusPlaying && m.state.PositionMs == 0 {
			return cloneState(m.state), nil
		}
		next := cloneState(m.state)
		next.Status = protocol.StatusPlaying
		next.PositionMs = 0
		return m.commitLocked(next, true)
	}

	next := cloneState(m.state)
	previous := next.History[len(next.History)-1]
	next.History = next.History[:len(next.History)-1]
	if next.Current != nil {
		next.Queue = append([]protocol.QueueItem{*next.Current}, next.Queue...)
	}
	next.Current = &previous
	next.Status = protocol.StatusPlaying
	next.PositionMs = 0
	return m.commitLocked(next, true)
}

// QueueAdd appends a new occurrence of a known track to the upcoming queue.
func (m *StateMachine) QueueAdd(trackID string) (protocol.PlaybackState, *protocol.APIError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if apiErr := m.validateTrackLocked(trackID); apiErr != nil {
		return protocol.PlaybackState{}, apiErr
	}
	item, apiErr := m.newItemLocked(trackID)
	if apiErr != nil {
		return protocol.PlaybackState{}, apiErr
	}
	next := cloneState(m.state)
	next.Queue = append(next.Queue, item)
	return m.commitLocked(next, true)
}

// QueueRemove removes exactly the matching upcoming occurrence.
func (m *StateMachine) QueueRemove(itemID string) (protocol.PlaybackState, *protocol.APIError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !protocol.ValidIdentifier(itemID) {
		return protocol.PlaybackState{}, playerError(protocol.ErrInvalidPayload, "Missing or invalid item_id.")
	}

	next := cloneState(m.state)
	for i, item := range next.Queue {
		if item.ID == itemID {
			next.Queue = append(next.Queue[:i], next.Queue[i+1:]...)
			return m.commitLocked(next, true)
		}
	}
	return protocol.PlaybackState{}, playerError(protocol.ErrQueueItemNotFound, "Unknown upcoming queue item.")
}

// QueueClear removes upcoming items without changing the current item or history.
func (m *StateMachine) QueueClear() (protocol.PlaybackState, *protocol.APIError) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.state.Queue) == 0 {
		return cloneState(m.state), nil
	}

	next := cloneState(m.state)
	next.Queue = make([]protocol.QueueItem, 0)
	return m.commitLocked(next, true)
}

func (m *StateMachine) validateTrackLocked(trackID string) *protocol.APIError {
	if !protocol.ValidIdentifier(trackID) {
		return playerError(protocol.ErrInvalidPayload, "Missing or invalid track_id.")
	}
	if m.trackExists == nil || !m.trackExists(trackID) {
		return playerError(protocol.ErrTrackNotFound, "Unknown track.")
	}
	return nil
}

func (m *StateMachine) newItemLocked(trackID string) (protocol.QueueItem, *protocol.APIError) {
	if m.nextItemID == math.MaxUint64 {
		return protocol.QueueItem{}, playerError(protocol.ErrInternal, "Unable to allocate a queue item ID.")
	}
	m.nextItemID++
	return protocol.QueueItem{ID: fmt.Sprintf("item-%d", m.nextItemID), TrackID: trackID}, nil
}

func (m *StateMachine) commitLocked(next protocol.PlaybackState, changed bool) (protocol.PlaybackState, *protocol.APIError) {
	if !changed {
		return cloneState(m.state), nil
	}
	if m.state.Version == math.MaxUint64 {
		return protocol.PlaybackState{}, playerError(protocol.ErrInternal, "Playback version is exhausted.")
	}
	next.Version = m.state.Version + 1
	m.state = cloneState(next)
	return cloneState(m.state), nil
}

func playerError(code protocol.ErrorCode, message string) *protocol.APIError {
	return &protocol.APIError{Code: code, Message: message}
}

func cloneState(state protocol.PlaybackState) protocol.PlaybackState {
	cloned := state
	if state.Current != nil {
		current := *state.Current
		cloned.Current = &current
	}
	cloned.History = make([]protocol.QueueItem, len(state.History))
	copy(cloned.History, state.History)
	cloned.Queue = make([]protocol.QueueItem, len(state.Queue))
	copy(cloned.Queue, state.Queue)
	return cloned
}