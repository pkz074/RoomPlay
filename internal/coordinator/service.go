package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

const maxMessageBytes = 4 << 10 // registration/heartbeat messages are tiny

// Service connects the Registry to NATS.
type Service struct {
	nc  *nats.Conn
	reg *Registry
	log *log.Logger
}

func NewService(nc *nats.Conn, reg *Registry, logger *log.Logger) *Service {
	return &Service{nc: nc, reg: reg, log: logger}
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
	if dec.More() {
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
	if _, err := s.nc.Subscribe(protocol.SubjectRegister, s.handleRegister); err != nil {
		return fmt.Errorf("subscribe register: %w", err)
	}
	if _, err := s.nc.Subscribe(protocol.SubjectHeartbeat, s.handleHeartbeat); err != nil {
		return fmt.Errorf("subscribe heartbeat: %w", err)
	}
	if err := s.nc.Flush(); err != nil {
		return fmt.Errorf("flush subscriptions: %w", err)
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
func (s *Service) Stop() error { return s.nc.Drain() }
