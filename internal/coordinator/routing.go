package coordinator

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/pkz074/RoomPlay/internal/protocol"
)

type brokerRequester interface {
	IsConnected() bool
	RequestWithContext(context.Context, string, []byte) (*nats.Msg, error)
}

func apiError(code protocol.ErrorCode, message string) *protocol.APIError {
	return &protocol.APIError{Code: code, Message: message}
}
func readID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "read-" + time.Now().Format("150405.000000000")
	}
	return "read-" + hex.EncodeToString(b)
}
func decodeResponse(data []byte, dst any) error {
	if len(data) == 0 || len(data) > 1<<20 {
		return errors.New("response size")
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("expected JSON object")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return errors.New("trailing response")
	}
	return nil
}
func (s *Service) connected() bool { return s.requester != nil && s.requester.IsConnected() }
func (s *Service) room(id, expected string) (Room, *protocol.APIError) {
	s.reg.Sweep()
	room, ok := s.reg.Get(id)
	if !ok {
		return room, apiError(protocol.ErrRoomNotFound, "Unknown room.")
	}
	if expected != "" && expected != room.InstanceID {
		return room, apiError(protocol.ErrServiceUnavailable, "Room instance changed. Refresh room state.")
	}
	if !room.Online {
		return room, apiError(protocol.ErrRoomOffline, "Room is offline.")
	}
	if !s.connected() {
		return room, apiError(protocol.ErrBrokerUnavailable, "Broker is unavailable.")
	}
	return room, nil
}
func (s *Service) exchange(parent context.Context, room Room, subject, id string, request any) (protocol.CommandResponse, *protocol.APIError) {
	var response protocol.CommandResponse
	data, err := json.Marshal(request)
	if err != nil {
		return response, apiError(protocol.ErrInternal, "Unable to encode request.")
	}
	ctx, cancel := context.WithTimeout(parent, s.RequestTimeout)
	defer cancel()
	message, err := s.requester.RequestWithContext(ctx, subject, data)
	if err != nil {
		switch {
		case errors.Is(err, nats.ErrNoResponders):
			return response, apiError(protocol.ErrNoResponders, "No player is responding.")
		case errors.Is(err, context.DeadlineExceeded), errors.Is(err, nats.ErrTimeout):
			return response, apiError(protocol.ErrCommandTimeout, "Request timed out; outcome unknown. Refresh room state.")
		case errors.Is(err, context.Canceled):
			return response, apiError(protocol.ErrServiceUnavailable, "Request was canceled; outcome may be unknown.")
		default:
			return response, apiError(protocol.ErrBrokerUnavailable, "Broker is unavailable.")
		}
	}
	if message == nil || decodeResponse(message.Data, &response) != nil || response.RequestID != id || response.InstanceID != room.InstanceID || response.RegistrationToken != room.Token {
		return response, apiError(protocol.ErrServiceUnavailable, "Invalid or stale player reply; refresh room state.")
	}
	s.reg.Sweep()
	current, ok := s.reg.Get(room.ID)
	if !ok || !current.Online || current.InstanceID != room.InstanceID || current.Token != room.Token {
		return response, apiError(protocol.ErrServiceUnavailable, "Room registration changed; outcome may be unknown. Refresh room state.")
	}
	if !response.Success {
		if response.State != nil || response.Error == nil {
			return response, apiError(protocol.ErrServiceUnavailable, "Invalid player error reply.")
		}
		switch response.Error.Code {
		case protocol.ErrInvalidCommand, protocol.ErrInvalidPayload, protocol.ErrTrackNotFound, protocol.ErrQueueItemNotFound, protocol.ErrInvalidState, protocol.ErrDuplicateRequestConflict, protocol.ErrAudio, protocol.ErrInternal:
			return response, apiError(response.Error.Code, "Player rejected the request.")
		default:
			return response, apiError(protocol.ErrServiceUnavailable, "Player is unavailable.")
		}
	}
	if response.Error != nil || !validPlaybackState(response.State) {
		return response, apiError(protocol.ErrServiceUnavailable, "Invalid player state.")
	}
	return response, nil
}
func (s *Service) readRoom(ctx context.Context, room Room) (Room, *protocol.APIError) {
	id := readID()
	response, err := s.exchange(ctx, room, protocol.GetStateSubject(room.ID), id, protocol.StateQuery{RequestID: id, TargetInstanceID: room.InstanceID, RegistrationToken: room.Token})
	if err != nil {
		return room, err
	}
	s.storeAndPublish(room.ID, room.InstanceID, room.Token, response.State, true)
	current, ok := s.reg.Get(room.ID)
	if !ok || current.Token != room.Token || current.InstanceID != room.InstanceID || !current.Online {
		return current, apiError(protocol.ErrServiceUnavailable, "Room registration changed. Refresh room state.")
	}
	return current, nil
}
func (s *Service) Command(ctx context.Context, id, expected string, request protocol.CommandRequest) (protocol.CommandResult, *protocol.APIError) {
	var result protocol.CommandResult
	room, err := s.room(id, expected)
	if err != nil {
		return result, err
	}
	request.TargetInstanceID = room.InstanceID
	request.RegistrationToken = room.Token
	response, err := s.exchange(ctx, room, protocol.CommandSubject(id), request.RequestID, request)
	if err != nil {
		return result, err
	}
	s.storeAndPublish(id, room.InstanceID, room.Token, response.State, false)
	return protocol.CommandResult{RequestID: request.RequestID, InstanceID: room.InstanceID, State: *response.State}, nil
}
func (s *Service) Detail(ctx context.Context, id string) (protocol.RoomSummary, *protocol.APIError) {
	room, exists := s.reg.Get(id)
	if !exists {
		return protocol.RoomSummary{}, apiError(protocol.ErrRoomNotFound, "Unknown room.")
	}
	s.reg.Sweep()
	room, _ = s.reg.Get(id)
	if !room.Online {
		return s.summary(room), nil
	}
	if !s.connected() {
		return s.summary(room), apiError(protocol.ErrBrokerUnavailable, "Broker is unavailable.")
	}
	room, err := s.readRoom(ctx, room)
	return s.summary(room), err
}
func (s *Service) summary(room Room) protocol.RoomSummary {
	availability := protocol.RoomOffline
	if room.Online {
		availability = protocol.RoomOnline
	}
	return protocol.RoomSummary{ID: room.ID, Name: room.Name, InstanceID: room.InstanceID, Availability: availability, LastSeen: room.LastHeartbeat.UTC().Format(time.RFC3339Nano), State: copyPlaybackState(room.State), StateStale: room.StateStale || !room.Online || room.State == nil || !s.connected()}
}
func (s *Service) Rooms() protocol.RoomsResponse {
	s.reg.Sweep()
	out := protocol.RoomsResponse{Rooms: make([]protocol.RoomSummary, 0)}
	for _, room := range s.reg.List() {
		out.Rooms = append(out.Rooms, s.summary(room))
	}
	return out
}
func (s *Service) handleState(message *nats.Msg) {
	var event protocol.StateEvent
	if decodeResponse(message.Data, &event) != nil || message.Subject != protocol.StateSubject(event.RoomID) {
		return
	}
	s.storeAndPublish(event.RoomID, event.InstanceID, event.RegistrationToken, &event.State, false)
}

func (s *Service) storeAndPublish(id, instance, token string, state *protocol.PlaybackState, query bool) {
	before, _ := s.reg.Get(id)
	if s.reg.StoreState(id, instance, token, state, query) && (before.StateStale || before.State == nil || before.State.Version < state.Version) {
		s.events.Publish("room.state", struct {
			RoomID     string                 `json:"room_id"`
			InstanceID string                 `json:"instance_id"`
			State      protocol.PlaybackState `json:"state"`
		}{id, instance, *state})
	}
}
