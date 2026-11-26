package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

const maxMessageBytes = 4 << 10 // registration/heartbeat messages are tiny

// Service connects the Registry to NATS.
type Service struct {
	nc             *nats.Conn
	reg            *Registry
	log            *log.Logger
	events         *EventHub
	ctx            context.Context
	cancel         context.CancelFunc
	refresh        chan Room
	workers        sync.WaitGroup
	requester      brokerRequester
	RequestTimeout time.Duration // configure before Start
}

func NewService(nc *nats.Conn, reg *Registry, logger *log.Logger) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{nc: nc, requester: nc, reg: reg, log: logger, events: NewEventHub(), ctx: ctx, cancel: cancel, refresh: make(chan Room, 128), RequestTimeout: 1500 * time.Millisecond}
	if nc == nil {
		s.requester = nil
	} else {
		previous := nc.DisconnectErrHandler()
		nc.SetDisconnectErrHandler(func(connection *nats.Conn, err error) {
			reg.MarkStatesStale()
			if previous != nil {
				previous(connection, err)
			}
		})
	}
	reg.ObserveAvailability(func(c Change) {
		s.events.Publish("room.availability", protocol.AvailabilityEvent{RoomID: c.RoomID, Availability: c.Availability})
	})
	return s
}

func decodeStrict(data []byte, dst any) error {
	if len(data) == 0 || len(data) > maxMessageBytes {
		return errors.New("message size out of bounds")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}

// registerReply turns raw request bytes into raw reply bytes (testable without NATS).
func (s *Service) registerReply(data []byte) []byte {
	var req protocol.RegistrationRequest
	var resp protocol.RegistrationResponse
	if err := decodeStrict(data, &req); err != nil {
		resp = regFailure("", protocol.ErrInvalidPayload, "Malformed registration request.")
	} else {
		resp = s.reg.Register(req)
	}
	out, err := json.Marshal(resp)
	if err != nil {
		out = []byte(`{"success":false,"room_id":"","error":{"code":"INTERNAL_ERROR","message":"Internal error."}}`)
	}
	return out
}

func (s *Service) handleRegister(msg *nats.Msg) {
	if msg.Reply == "" {
		s.log.Printf("register message without reply subject ignored")
		return
	}
	if err := msg.Respond(s.registerReply(msg.Data)); err != nil {
		s.log.Printf("register reply failed: %v", err)
	}
	var req protocol.RegistrationRequest
	if decodeStrict(msg.Data, &req) == nil {
		if room, ok := s.reg.Get(req.RoomID); ok && room.Online && room.InstanceID == req.InstanceID {
			select {
			case s.refresh <- room:
			default:
			}
		}
	}
}

func (s *Service) handleHeartbeat(msg *nats.Msg) {
	var hb protocol.Heartbeat
	if err := decodeStrict(msg.Data, &hb); err != nil {
		s.log.Printf("bad heartbeat ignored: %v", err)
		return
	}
	if err := s.reg.Heartbeat(hb); err != nil {
		s.log.Printf("heartbeat from room=%q instance=%q ignored: %v", hb.RoomID, hb.InstanceID, err)
	}
}

// Start installs subscriptions and flushes them to the broker.
func (s *Service) Start() error {
	if s.nc == nil {
		return errors.New("NATS connection is required")
	}
	if _, err := s.nc.Subscribe("roomplay.rooms.*.state", s.handleState); err != nil {
		return err
	}
	if _, err := s.nc.Subscribe(protocol.SubjectRegister, s.handleRegister); err != nil {
		return fmt.Errorf("subscribe register: %w", err)
	}
	if _, err := s.nc.Subscribe(protocol.SubjectHeartbeat, s.handleHeartbeat); err != nil {
		return fmt.Errorf("subscribe heartbeat: %w", err)
	}
	if err := s.nc.FlushTimeout(2 * time.Second); err != nil {
		return fmt.Errorf("flush subscriptions: %w", err)
	}
	for range 4 {
		s.workers.Add(1)
		go func() {
			defer s.workers.Done()
			for {
				select {
				case <-s.ctx.Done():
					return
				case room := <-s.refresh:
					_, _ = s.readRoom(s.ctx, room)
				}
			}
		}()
	}
	return nil
}

// RunExpiry sweeps for expired rooms until ctx is cancelled.
func (s *Service) RunExpiry(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.reg.Sweep()
		}
	}
}

// Stop drains in-flight messages and closes the connection.
func (s *Service) Stop() error {
	s.cancel()
	s.events.Close()
	s.workers.Wait()
	if s.nc == nil {
		return nil
	}
	return s.nc.Drain()
}
