package coordinator

import "github.com/pkz074/RoomPlay/internal/protocol"

func copyPlaybackState(state *protocol.PlaybackState) *protocol.PlaybackState {
	if state == nil {
		return nil
	}
	copy := *state
	if state.Current != nil {
		item := *state.Current
		copy.Current = &item
	}
	copy.History = append(make([]protocol.QueueItem, 0, len(state.History)), state.History...)
	copy.Queue = append(make([]protocol.QueueItem, 0, len(state.Queue)), state.Queue...)
	return &copy
}

func validPlaybackState(s *protocol.PlaybackState) bool {
	if s == nil || s.History == nil || s.Queue == nil || s.PositionMs < 0 {
		return false
	}
	switch s.Status {
	case protocol.StatusStopped:
		if s.PositionMs != 0 {
			return false
		}
	case protocol.StatusPlaying, protocol.StatusPaused:
		if s.Current == nil {
			return false
		}
	default:
		return false
	}
	seen := map[string]bool{}
	check := func(item protocol.QueueItem) bool {
		if !protocol.ValidIdentifier(item.ID) || !protocol.ValidIdentifier(item.TrackID) || seen[item.ID] {
			return false
		}
		seen[item.ID] = true
		return true
	}
	if s.Current != nil && !check(*s.Current) {
		return false
	}
	for _, item := range s.History {
		if !check(item) {
			return false
		}
	}
	for _, item := range s.Queue {
		if !check(item) {
			return false
		}
	}
	return true
}

// StoreState fences both registration identity and revision under one lock.
// Queries may refresh elapsed position at the same logical version; events may not.
func (r *Registry) StoreState(id, instance, token string, state *protocol.PlaybackState, query bool) bool {
	if !validPlaybackState(state) {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	room := r.rooms[id]
	if room == nil || !room.Online || r.clock.Now().Sub(room.LastHeartbeat) >= r.timeout || room.InstanceID != instance || room.Token != token {
		return false
	}
	if room.State != nil && (state.Version < room.State.Version || (!query && state.Version == room.State.Version)) {
		return false
	}
	room.State = copyPlaybackState(state)
	room.StateStale = false
	return true
}

func (r *Registry) MarkStatesStale() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, room := range r.rooms {
		room.StateStale = true
	}
}
