package player

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"sync"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

const defaultCommandCacheSize = 1000

// CommandProcessor serializes validation, deduplication, state mutation, and backend coordination.
type CommandProcessor struct {
	mu            sync.Mutex
	state         *StateMachine
	tracks        TrackIndex
	backend       PlaybackBackend
	cache         map[string]cachedCommand
	cacheOrder    []string
	cacheCursor   int
	onStateChange func(protocol.PlaybackState)
	closed         bool
}

type cachedCommand struct {
	fingerprint [32]byte
	response    protocol.CommandResponse
}

func NewCommandProcessor(tracks TrackIndex, backend PlaybackBackend) *CommandProcessor {
	if tracks == nil {
		tracks = make(TrackIndex)
	}
	if backend == nil {
		backend = NewSimulationBackend()
	}
	trackCopy := make(TrackIndex, len(tracks))
	for id, track := range tracks {
		trackCopy[id] = track
	}
	processor := &CommandProcessor{
		state: NewStateMachine(func(trackID string) bool {
			_, exists := trackCopy[trackID]
			return exists
		}),
		tracks:      trackCopy,
		backend:     backend,
		cache:       make(map[string]cachedCommand, defaultCommandCacheSize),
		cacheOrder:  make([]string, 0, defaultCommandCacheSize),
	}
	if completionSource, ok := backend.(interface{ SetOnCompletion(func()) }); ok {
		completionSource.SetOnCompletion(processor.completeNaturally)
	}
	return processor
}

func (p *CommandProcessor) SetOnStateChange(callback func(protocol.PlaybackState)) {
	p.mu.Lock()
	p.onStateChange = callback
	p.mu.Unlock()
}

// Process applies one command and returns the original cached response for retained duplicate IDs.
func (p *CommandProcessor) Process(ctx context.Context, request protocol.CommandRequest) protocol.CommandResponse {
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errorResponse(request.RequestID, &protocol.APIError{Code: protocol.ErrServiceUnavailable, Message: "Player is shutting down."})
	}

	fingerprint := commandFingerprint(request)
	if protocol.ValidIdentifier(request.RequestID) {
		if cached, exists := p.cache[request.RequestID]; exists {
			if cached.fingerprint != fingerprint {
				return errorResponse(request.RequestID, &protocol.APIError{
					Code:    protocol.ErrDuplicateRequestConflict,
					Message: "request_id was already used for a different command.",
				})
			}
			return cloneResponse(cached.response)
		}
	}

	if apiErr := validateCommand(request); apiErr != nil {
		response := errorResponse(request.RequestID, apiErr)
		p.cacheResponse(request, fingerprint, response)
		return cloneResponse(response)
	}

	before := p.snapshotLocked()
	if request.Type == protocol.PlaybackPause || request.Type == protocol.PlaybackPrevious {
		if before.Status == protocol.StatusPlaying {
			p.state.setPositionMs(before.PositionMs)
		}
	}

	if targetID := p.preparedTargetID(request, before); targetID != "" {
		track, exists := p.tracks[targetID]
		if !exists {
			response := errorResponse(request.RequestID, &protocol.APIError{
				Code:    protocol.ErrTrackNotFound,
				Message: "Unknown track.",
			})
			p.cacheResponse(request, fingerprint, response)
			return cloneResponse(response)
		}
		if err := p.backend.Prepare(ctx, track); err != nil {
			response := errorResponse(request.RequestID, &protocol.APIError{
				Code:    protocol.ErrAudio,
				Message: "Unable to prepare audio for playback.",
			})
			p.cacheResponse(request, fingerprint, response)
			return cloneResponse(response)
		}
	}

	state, apiErr := p.execute(request)
	if apiErr != nil {
		response := errorResponse(request.RequestID, apiErr)
		p.cacheResponse(request, fingerprint, response)
		return cloneResponse(response)
	}
	after := p.state.Snapshot()
	p.syncBackend(request.Type, before, after)
	state = p.snapshotLocked()
	response := protocol.CommandResponse{
		RequestID: request.RequestID,
		Success:   true,
		State:     &state,
	}
	if state.Version != before.Version && p.onStateChange != nil {
		p.onStateChange(cloneState(state))
	}
	p.cacheResponse(request, fingerprint, response)
	return cloneResponse(response)
}

func (p *CommandProcessor) Snapshot() protocol.PlaybackState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshotLocked()
}

func (p *CommandProcessor) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	return p.backend.Close()
}

func (p *CommandProcessor) execute(request protocol.CommandRequest) (protocol.PlaybackState, *protocol.APIError) {
	switch request.Type {
	case protocol.PlaybackPlay:
		var payload protocol.TrackPayload
		_ = json.Unmarshal(request.Payload, &payload)
		return p.state.Play(payload.TrackID)
	case protocol.PlaybackPause:
		return p.state.Pause()
	case protocol.PlaybackResume:
		return p.state.Resume()
	case protocol.PlaybackStop:
		return p.state.Stop()
	case protocol.PlaybackNext:
		return p.state.Next()
	case protocol.PlaybackPrevious:
		return p.state.Previous()
	case protocol.QueueAdd:
		var payload protocol.TrackPayload
		_ = json.Unmarshal(request.Payload, &payload)
		return p.state.QueueAdd(payload.TrackID)
	case protocol.QueueRemove:
		var payload protocol.RemoveItemPayload
		_ = json.Unmarshal(request.Payload, &payload)
		return p.state.QueueRemove(payload.ItemID)
	case protocol.QueueClear:
		return p.state.QueueClear()
	default:
		return protocol.PlaybackState{}, &protocol.APIError{Code: protocol.ErrInvalidCommand, Message: "Unknown command type."}
	}
}

func (p *CommandProcessor) preparedTargetID(request protocol.CommandRequest, before protocol.PlaybackState) string {
	switch request.Type {
	case protocol.PlaybackPlay:
		var payload protocol.TrackPayload
		_ = json.Unmarshal(request.Payload, &payload)
		return payload.TrackID
	case protocol.PlaybackNext:
		if len(before.Queue) > 0 {
			return before.Queue[0].TrackID
		}
	case protocol.PlaybackPrevious:
		if len(before.History) > 0 {
			return before.History[len(before.History)-1].TrackID
		}
		if before.Current != nil {
			return before.Current.TrackID
		}
	}
	return ""
}

func (p *CommandProcessor) syncBackend(command protocol.CommandType, before, after protocol.PlaybackState) {
	changed := before.Version != after.Version
	if !changed {
		return
	}
	switch command {
	case protocol.PlaybackPlay, protocol.PlaybackNext, protocol.PlaybackPrevious:
		if after.Status == protocol.StatusPlaying && after.Current != nil {
			if track, exists := p.tracks[after.Current.TrackID]; exists {
				p.backend.Play(track, after.PositionMs)
			}
		} else {
			p.backend.Stop()
		}
	case protocol.PlaybackPause:
		if after.Status == protocol.StatusPaused {
			p.state.setPositionMs(p.backend.Pause())
		}
	case protocol.PlaybackResume:
		if after.Status == protocol.StatusPlaying {
			p.backend.Resume()
		}
	case protocol.PlaybackStop:
		p.backend.Stop()
	}
}

func (p *CommandProcessor) snapshotLocked() protocol.PlaybackState {
	state := p.state.Snapshot()
	if state.Status == protocol.StatusPlaying || state.Status == protocol.StatusPaused {
		state.PositionMs = p.backend.PositionMs()
	} else {
		state.PositionMs = 0
	}
	return state
}

func (p *CommandProcessor) completeNaturally() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	before := p.snapshotLocked()
	if before.Status != protocol.StatusPlaying || before.Current == nil {
		return
	}
	state, apiErr := p.state.Next()
	if apiErr != nil || state.Version == before.Version {
		return
	}
	p.syncBackend(protocol.PlaybackNext, before, state)
	state = p.snapshotLocked()
	if p.onStateChange != nil {
		p.onStateChange(cloneState(state))
	}
}

func (p *CommandProcessor) cacheResponse(request protocol.CommandRequest, fingerprint [32]byte, response protocol.CommandResponse) {
	if !protocol.ValidIdentifier(request.RequestID) {
		return
	}
	p.cache[request.RequestID] = cachedCommand{fingerprint: fingerprint, response: cloneResponse(response)}
	if len(p.cacheOrder) < defaultCommandCacheSize {
		p.cacheOrder = append(p.cacheOrder, request.RequestID)
		return
	}
	oldest := p.cacheOrder[p.cacheCursor]
	delete(p.cache, oldest)
	p.cacheOrder[p.cacheCursor] = request.RequestID
	p.cacheCursor = (p.cacheCursor + 1) % defaultCommandCacheSize
}

func validateCommand(request protocol.CommandRequest) *protocol.APIError {
	if !protocol.ValidIdentifier(request.RequestID) {
		return &protocol.APIError{Code: protocol.ErrInvalidPayload, Message: "Missing or invalid request_id."}
	}
	switch request.Type {
	case protocol.PlaybackPlay, protocol.PlaybackPause, protocol.PlaybackResume, protocol.PlaybackStop,
		protocol.PlaybackNext, protocol.PlaybackPrevious, protocol.QueueAdd, protocol.QueueRemove, protocol.QueueClear:
	default:
		return &protocol.APIError{Code: protocol.ErrInvalidCommand, Message: "Unknown command type."}
	}
	if err := protocol.ValidateCommand(request); err != nil {
		return &protocol.APIError{Code: protocol.ErrInvalidPayload, Message: "Malformed or invalid command payload."}
	}
	return nil
}

func commandFingerprint(request protocol.CommandRequest) [32]byte {
	var canonical []byte
	decoder := json.NewDecoder(bytes.NewReader(request.Payload))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err == nil {
		var trailing any
		if decoder.Decode(&trailing) == io.EOF {
			canonical, _ = json.Marshal(payload)
		}
	}
	if canonical == nil {
		var compact bytes.Buffer
		if err := json.Compact(&compact, request.Payload); err == nil {
			canonical = compact.Bytes()
		} else {
			canonical = bytes.TrimSpace(request.Payload)
		}
	}
	data := make([]byte, 0, len(request.Type)+1+len(canonical))
	data = append(data, string(request.Type)...)
	data = append(data, 0)
	data = append(data, canonical...)
	return sha256.Sum256(data)
}

func errorResponse(requestID string, apiErr *protocol.APIError) protocol.CommandResponse {
	return protocol.CommandResponse{RequestID: requestID, Success: false, Error: apiErr}
}

func cloneResponse(response protocol.CommandResponse) protocol.CommandResponse {
	cloned := response
	if response.State != nil {
		state := cloneState(*response.State)
		cloned.State = &state
	}
	if response.Error != nil {
		apiErr := *response.Error
		cloned.Error = &apiErr
	}
	return cloned
}