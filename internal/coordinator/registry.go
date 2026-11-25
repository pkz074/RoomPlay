// Package coordinator holds the coordinator's room registry and NATS handlers.
package coordinator

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

const (
	// HeartbeatInterval is how often players should send heartbeats.
	HeartbeatInterval = 2 * time.Second
	// HeartbeatTimeout is how long without a valid heartbeat before a room is offline.
	HeartbeatTimeout = 6 * time.Second
)

// Clock lets tests control time. Production code uses SystemClock.
type Clock interface{ Now() time.Time }

// SystemClock uses process-local time (time.Now carries a monotonic reading).
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

var (
	ErrUnknownRoom    = errors.New("unknown room")
	ErrStaleHeartbeat = errors.New("heartbeat does not match current registration")
	ErrRoomOffline    = errors.New("room is offline; re-registration required")
)

// Room is one entry in the registry.
type Room struct {
	ID            string
	Name          string
	InstanceID    string
	Token         string
	LastHeartbeat time.Time // coordinator receive time, never the sender's clock
	Online        bool
}

// Change describes an availability transition (online <-> offline).
type Change struct {
	RoomID       string
	Availability protocol.Availability
}

// Registry is a concurrency-safe in-memory list of rooms.
type Registry struct {
	mu             sync.Mutex
	clock          Clock
	timeout        time.Duration
	rooms          map[string]*Room
	onChange       func(Change)
	pendingChanges []Change
	notifying      bool
}

// NewRegistry creates a registry. onChange may be nil. It is called AFTER the
// internal lock is released, so it may safely call back into the registry.
// Notifications preserve mutation order. Mutations during a callback enqueue
// their notifications for the active caller to drain after that callback returns.
func NewRegistry(clock Clock, onChange func(Change)) *Registry {
	if clock == nil {
		clock = SystemClock{}
	}
	return &Registry{
		clock:    clock,
		timeout:  HeartbeatTimeout,
		rooms:    make(map[string]*Room),
		onChange: onChange,
	}
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "lease-" + hex.EncodeToString(b), nil
}

func regFailure(roomID string, code protocol.ErrorCode, msg string) protocol.RegistrationResponse {
	return protocol.RegistrationResponse{
		Success: false,
		RoomID:  roomID,
		Error:   &protocol.APIError{Code: code, Message: msg},
	}
}

// queueChangesLocked records transitions before releasing the mutation lock.
// Exactly one caller owns delivery; callbacks may reenter registry mutations.
func (r *Registry) queueChangesLocked(changes []Change) bool {
	if r.onChange == nil || len(changes) == 0 {
		return false
	}
	r.pendingChanges = append(r.pendingChanges, changes...)
	if r.notifying {
		return false
	}
	r.notifying = true
	return true
}

func (r *Registry) notify() {
	for {
		r.mu.Lock()
		changes := r.pendingChanges
		r.pendingChanges = nil
		if len(changes) == 0 {
			r.notifying = false
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		for _, change := range changes {
			r.onChange(change)
		}
	}
}

// expireLocked marks stale rooms offline. Caller must hold r.mu.
func (r *Registry) expireLocked() []Change {
	var changes []Change
	now := r.clock.Now()
	for _, room := range r.rooms {
		if room.Online && now.Sub(room.LastHeartbeat) >= r.timeout {
			room.Online = false
			changes = append(changes, Change{RoomID: room.ID, Availability: protocol.RoomOffline})
		}
	}
	return changes
}

// Register handles a registration request (new room, renewal, or replacement).
func (r *Registry) Register(req protocol.RegistrationRequest) protocol.RegistrationResponse {
	name := strings.TrimSpace(req.Name)
	switch {
	case !protocol.ValidRoomID(req.RoomID):
		return regFailure(req.RoomID, protocol.ErrInvalidPayload, "Invalid room_id.")
	case !protocol.ValidIdentifier(req.InstanceID):
		return regFailure(req.RoomID, protocol.ErrInvalidPayload, "Invalid instance_id.")
	case name == "" || len(name) > 128:
		return regFailure(req.RoomID, protocol.ErrInvalidPayload, "Invalid name.")
	}

	r.mu.Lock()
	changes := r.expireLocked()
	now := r.clock.Now()
	var resp protocol.RegistrationResponse

	room, exists := r.rooms[req.RoomID]
	switch {
	case !exists:
		token, err := newToken()
		if err != nil {
			resp = regFailure(req.RoomID, protocol.ErrServiceUnavailable, "Could not create registration.")
			break
		}
		room = &Room{ID: req.RoomID, Name: name, InstanceID: req.InstanceID, Token: token, LastHeartbeat: now, Online: true}
		r.rooms[req.RoomID] = room
		changes = append(changes, Change{RoomID: room.ID, Availability: protocol.RoomOnline})
		resp = okResponse(room)

	case room.InstanceID == req.InstanceID:
		// Same-instance renewal is idempotent: keep the token, refresh the lease.
		wasOnline := room.Online
		room.Name = name
		room.LastHeartbeat = now
		room.Online = true
		if !wasOnline {
			changes = append(changes, Change{RoomID: room.ID, Availability: protocol.RoomOnline})
		}
		resp = okResponse(room)

	case room.Online:
		resp = regFailure(req.RoomID, protocol.ErrDuplicateRoomID, "Room ID is already registered by a live instance.")

	default:
		// Previous instance is offline/expired: a new instance may replace it.
		token, err := newToken()
		if err != nil {
			resp = regFailure(req.RoomID, protocol.ErrServiceUnavailable, "Could not create registration.")
			break
		}
		room.Name = name
		room.InstanceID = req.InstanceID
		room.Token = token
		room.LastHeartbeat = now
		room.Online = true
		changes = append(changes, Change{RoomID: room.ID, Availability: protocol.RoomOnline})
		resp = okResponse(room)
	}
	deliver := r.queueChangesLocked(changes)
	r.mu.Unlock()
	if deliver {
		r.notify()
	}
	return resp
}

func okResponse(room *Room) protocol.RegistrationResponse {
	return protocol.RegistrationResponse{
		Success:             true,
		RoomID:              room.ID,
		RegistrationToken:   room.Token,
		HeartbeatIntervalMs: HeartbeatInterval.Milliseconds(),
	}
}

// Heartbeat records a heartbeat if instance and token match the current registration.
// A heartbeat for a room already marked offline is rejected: the player must
// re-register (which also triggers a state resync in a later PR).
func (r *Registry) Heartbeat(hb protocol.Heartbeat) error {
	r.mu.Lock()
	changes := r.expireLocked()
	room, ok := r.rooms[hb.RoomID]

	var err error
	switch {
	case !ok:
		err = ErrUnknownRoom
	case room.InstanceID != hb.InstanceID || room.Token != hb.RegistrationToken:
		err = ErrStaleHeartbeat
	case !room.Online:
		err = ErrRoomOffline
	default:
		room.LastHeartbeat = r.clock.Now()
	}
	deliver := r.queueChangesLocked(changes)
	r.mu.Unlock()
	if deliver {
		r.notify()
	}
	return err
}

// Sweep marks rooms offline whose heartbeat has expired. Call it periodically.
func (r *Registry) Sweep() {
	r.mu.Lock()
	changes := r.expireLocked()
	deliver := r.queueChangesLocked(changes)
	r.mu.Unlock()
	if deliver {
		r.notify()
	}
}

// Get returns a copy of a room.
func (r *Registry) Get(id string) (Room, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	room, ok := r.rooms[id]
	if !ok {
		return Room{}, false
	}
	return *room, true
}

// List returns copies of all rooms sorted by ID.
func (r *Registry) List() []Room {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Room, 0, len(r.rooms))
	for _, room := range r.rooms {
		out = append(out, *room)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
