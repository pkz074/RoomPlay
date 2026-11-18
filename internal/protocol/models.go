// Package protocol contains the shared wire models for RoomPlay.
// The wire contracts are specified in docs/API.md; changing them needs review.
package protocol

import "encoding/json"

// Track describes a catalog entry. URL should be reachable by room players.
type Track struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	DurationMs int64  `json:"duration_ms"`
	URL        string `json:"url"`
}

// QueueItem identifies an occurrence of a song, not the song itself.
type QueueItem struct {
	ID      string `json:"id"`
	TrackID string `json:"track_id"`
}

type PlaybackStatus string

const (
	StatusStopped PlaybackStatus = "stopped"
	StatusPlaying PlaybackStatus = "playing"
	StatusPaused  PlaybackStatus = "paused"
)

// PlaybackState is owned and mutated by one room player only.
type PlaybackState struct {
	Status     PlaybackStatus `json:"status"`
	Current    *QueueItem     `json:"current"`
	PositionMs int64          `json:"position_ms"`
	History    []QueueItem    `json:"history"`
	Queue      []QueueItem    `json:"queue"`
	Version    uint64         `json:"version"`
}

// NewPlaybackState initializes wire arrays to [] rather than null.
func NewPlaybackState() PlaybackState {
	return PlaybackState{
		Status:  StatusStopped,
		History: make([]QueueItem, 0),
		Queue:   make([]QueueItem, 0),
	}
}

// CommandRequest is the shared NATS mutation envelope.
// Payload is decoded according to Type by the player.
type CommandRequest struct {
	RequestID string          `json:"request_id"`
	Type      CommandType     `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type CommandType string

const (
	PlaybackPlay     CommandType = "playback.play"
	PlaybackPause    CommandType = "playback.pause"
	PlaybackResume   CommandType = "playback.resume"
	PlaybackStop     CommandType = "playback.stop"
	PlaybackNext     CommandType = "playback.next"
	PlaybackPrevious CommandType = "playback.previous"
	QueueAdd         CommandType = "queue.add"
	QueueRemove      CommandType = "queue.remove"
	QueueClear       CommandType = "queue.clear"
)

type TrackPayload struct {
	TrackID string `json:"track_id"`
}

type RemoveItemPayload struct {
	ItemID string `json:"item_id"`
}

// APIError is reused within NATS command errors and HTTP error responses.
type APIError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

type CommandResponse struct {
	RequestID string         `json:"request_id"`
	Success   bool           `json:"success"`
	State     *PlaybackState `json:"state"`
	Error     *APIError      `json:"error"`
}

type HTTPErrorResponse struct {
	RequestID string   `json:"request_id"`
	Error     APIError `json:"error"`
}

// CommandResult is the public HTTP mutation success shape.
type CommandResult struct {
	RequestID string        `json:"request_id"`
	State     PlaybackState `json:"state"`
}

type RegistrationRequest struct {
	RoomID     string `json:"room_id"`
	Name       string `json:"name"`
	InstanceID string `json:"instance_id"`
}

type RegistrationResponse struct {
	Success             bool      `json:"success"`
	RoomID              string    `json:"room_id"`
	RegistrationToken   string    `json:"registration_token,omitempty"`
	HeartbeatIntervalMs int64     `json:"heartbeat_interval_ms,omitempty"`
	Error               *APIError `json:"error"`
}

type Heartbeat struct {
	RoomID            string `json:"room_id"`
	InstanceID        string `json:"instance_id"`
	RegistrationToken string `json:"registration_token"`
}

type StateQuery struct {
	RequestID string `json:"request_id"`
}

type StateEvent struct {
	RoomID            string        `json:"room_id"`
	InstanceID        string        `json:"instance_id"`
	RegistrationToken string        `json:"registration_token"`
	State             PlaybackState `json:"state"`
}

type Availability string

const (
	RoomOnline  Availability = "online"
	RoomOffline Availability = "offline"
)

type RoomSummary struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	InstanceID  string         `json:"instance_id"`
	Availability Availability  `json:"availability"`
	LastSeen    string         `json:"last_seen"`
	State       *PlaybackState `json:"state"`
	StateStale  bool           `json:"state_stale"`
}

type RoomsResponse struct {
	Rooms []RoomSummary `json:"rooms"`
}

type TracksResponse struct {
	Tracks []Track `json:"tracks"`
}

type QueueResponse struct {
	RoomID string      `json:"room_id"`
	Queue  []QueueItem `json:"queue"`
}

type AvailabilityEvent struct {
	RoomID       string       `json:"room_id"`
	Availability Availability `json:"availability"`
}
