package player

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/pkz074/RoomPlay/internal/protocol"
)

const (
	maxPlayerMessageBytes = 64 << 10
	playerRequestTimeout  = 2 * time.Second
	playerFlushTimeout    = 2 * time.Second
	playerHeartbeatEvery  = 2 * time.Second
	playerRenewEvery      = 4 * time.Second
)

// NATSConnection is the subset of the NATS connection used by a player.
type NATSConnection interface {
	Subscribe(string, nats.MsgHandler) (*nats.Subscription, error)
	FlushTimeout(time.Duration) error
	RequestWithContext(context.Context, string, []byte) (*nats.Msg, error)
	Publish(string, []byte) error
}

type NATSPlayerConfig struct {
	RoomID         string
	Name           string
	InstanceID     string
	ReconnectEpoch *atomic.Uint64
	Logf           func(string, ...any)
}

// NATSPlayer installs room handlers, registers its lease, and maintains heartbeats and renewals.
type NATSPlayer struct {
	connection NATSConnection
	processor  *CommandProcessor
	config     NATSPlayerConfig
	epoch      *atomic.Uint64

	mu                sync.RWMutex
	registered        bool
	registrationCode  protocol.ErrorCode
	registrationToken string
	registeredEpoch   uint64
}

func NewNATSPlayer(connection NATSConnection, processor *CommandProcessor, config NATSPlayerConfig) (*NATSPlayer, error) {
	if connection == nil || processor == nil {
		return nil, fmt.Errorf("NATS connection and command processor are required")
	}
	if !protocol.ValidRoomID(config.RoomID) || !protocol.ValidIdentifier(config.InstanceID) || strings.TrimSpace(config.Name) == "" {
		return nil, fmt.Errorf("room_id, name, and instance_id are required and must be valid")
	}
	if config.ReconnectEpoch == nil {
		config.ReconnectEpoch = &atomic.Uint64{}
	}
	if config.Logf == nil {
		config.Logf = log.Printf
	}
	return &NATSPlayer{
		connection: connection,
		processor:  processor,
		config:     config,
		epoch:      config.ReconnectEpoch,
	}, nil
}

// Run subscribes before registration and returns after cancellation and subscription cleanup.
func (p *NATSPlayer) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	commandSub, stateSub, err := p.subscribeUntilReady(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	p.processor.SetOnStateChange(p.publishState)

	lifecycleDone := make(chan struct{})
	go func() {
		defer close(lifecycleDone)
		p.lifecycle(ctx)
	}()
	<-ctx.Done()
	<-lifecycleDone
	unsubscribe(commandSub)
	unsubscribe(stateSub)
	_ = p.connection.FlushTimeout(playerFlushTimeout)
	return nil
}

func (p *NATSPlayer) subscribeUntilReady(ctx context.Context) (*nats.Subscription, *nats.Subscription, error) {
	for ctx.Err() == nil {
		commandSub, err := p.connection.Subscribe(protocol.CommandSubject(p.config.RoomID), p.handleCommand)
		if err != nil {
			p.config.Logf("subscribe to room commands: %v", err)
			if !waitForRetry(ctx) {
				break
			}
			continue
		}
		stateSub, err := p.connection.Subscribe(protocol.GetStateSubject(p.config.RoomID), p.handleStateQuery)
		if err != nil {
			unsubscribe(commandSub)
			p.config.Logf("subscribe to room state queries: %v", err)
			if !waitForRetry(ctx) {
				break
			}
			continue
		}
		for ctx.Err() == nil {
			if err := p.connection.FlushTimeout(playerFlushTimeout); err == nil {
				return commandSub, stateSub, nil
			} else {
				p.config.Logf("flush player subscriptions: %v", err)
			}
			if !waitForRetry(ctx) {
				break
			}
		}
		unsubscribe(commandSub)
		unsubscribe(stateSub)
	}
	return nil, nil, ctx.Err()
}

func waitForRetry(ctx context.Context) bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func unsubscribe(subscription *nats.Subscription) {
	if subscription != nil {
		_ = subscription.Unsubscribe()
	}
}

func (p *NATSPlayer) lifecycle(ctx context.Context) {
	heartbeat := time.NewTicker(playerHeartbeatEvery)
	renew := time.NewTicker(playerRenewEvery)
	defer heartbeat.Stop()
	defer renew.Stop()
	p.register(ctx)
	for {
		select {
		case <-ctx.Done():
			p.markUnregistered(protocol.ErrServiceUnavailable)
			return
		case <-renew.C:
			p.register(ctx)
		case <-heartbeat.C:
			p.sendHeartbeat()
		}
	}
}

func (p *NATSPlayer) register(parent context.Context) {
	if err := p.connection.FlushTimeout(playerFlushTimeout); err != nil {
		p.registrationFailed(protocol.ErrServiceUnavailable, "flush before registration", err)
		return
	}
	epoch := p.epoch.Load()
	requestData, err := json.Marshal(protocol.RegistrationRequest{
		RoomID:     p.config.RoomID,
		Name:       p.config.Name,
		InstanceID: p.config.InstanceID,
	})
	if err != nil {
		p.registrationFailed(protocol.ErrInternal, "encode registration", err)
		return
	}
	ctx, cancel := context.WithTimeout(parent, playerRequestTimeout)
	defer cancel()
	message, err := p.connection.RequestWithContext(ctx, protocol.SubjectRegister, requestData)
	if err != nil {
		p.registrationFailed(protocol.ErrServiceUnavailable, "register player", err)
		return
	}
	if message == nil {
		p.registrationFailed(protocol.ErrServiceUnavailable, "register player", fmt.Errorf("empty registration response"))
		return
	}
	if epoch != p.epoch.Load() {
		p.registrationFailed(protocol.ErrServiceUnavailable, "register player after broker reconnect", fmt.Errorf("connection changed during registration"))
		return
	}
	var response protocol.RegistrationResponse
	if err := decodeStrictJSON(message.Data, &response); err != nil {
		p.registrationFailed(protocol.ErrServiceUnavailable, "decode registration response", err)
		return
	}
	if !response.Success {
		code := protocol.ErrServiceUnavailable
		if response.Error != nil && response.Error.Code != "" {
			code = response.Error.Code
		}
		p.markUnregistered(code)
		if response.Error != nil {
			p.config.Logf("player registration rejected for room %s: %s", p.config.RoomID, response.Error.Message)
		}
		return
	}
	if response.RoomID != p.config.RoomID || !protocol.ValidIdentifier(response.RegistrationToken) || response.Error != nil {
		p.registrationFailed(protocol.ErrServiceUnavailable, "validate registration response", fmt.Errorf("response room or registration token is invalid"))
		return
	}
	p.mu.Lock()
	p.registrationToken = response.RegistrationToken
	p.registered = true
	p.registrationCode = ""
	p.registeredEpoch = epoch
	p.mu.Unlock()
}

func (p *NATSPlayer) sendHeartbeat() {
	token, ok, _ := p.registration()
	if !ok {
		return
	}
	data, err := json.Marshal(protocol.Heartbeat{
		RoomID:            p.config.RoomID,
		InstanceID:        p.config.InstanceID,
		RegistrationToken: token,
	})
	if err == nil {
		err = p.connection.Publish(protocol.SubjectHeartbeat, data)
	}
	if err != nil {
		p.registrationFailed(protocol.ErrServiceUnavailable, "publish heartbeat", err)
	}
}

func (p *NATSPlayer) handleCommand(message *nats.Msg) {
	var request protocol.CommandRequest
	response := protocol.CommandResponse{}
	if err := decodePlayerEnvelope(message.Data, &request); err != nil {
		response = errorResponse(request.RequestID, &protocol.APIError{Code: protocol.ErrInvalidPayload, Message: "Malformed command envelope."})
	} else if _, ok, code := p.registration(); !ok {
		response = errorResponse(request.RequestID, &protocol.APIError{Code: registrationErrorCode(code), Message: "Player is not registered with the coordinator."})
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		response = p.processor.Process(ctx, request)
		cancel()
	}
	p.respond(message, response)
}

func (p *NATSPlayer) handleStateQuery(message *nats.Msg) {
	var query protocol.StateQuery
	if err := decodePlayerEnvelope(message.Data, &query); err != nil || !protocol.ValidIdentifier(query.RequestID) {
		p.respond(message, errorResponse(query.RequestID, &protocol.APIError{Code: protocol.ErrInvalidPayload, Message: "Malformed state query."}))
		return
	}
	if _, ok, code := p.registration(); !ok {
		p.respond(message, errorResponse(query.RequestID, &protocol.APIError{Code: registrationErrorCode(code), Message: "Player is not registered with the coordinator."}))
		return
	}
	state := p.processor.Snapshot()
	p.respond(message, protocol.CommandResponse{RequestID: query.RequestID, Success: true, State: &state})
}

func (p *NATSPlayer) publishState(state protocol.PlaybackState) {
	token, ok, _ := p.registration()
	if !ok {
		return
	}
	event := protocol.StateEvent{
		RoomID:            p.config.RoomID,
		InstanceID:        p.config.InstanceID,
		RegistrationToken: token,
		State:             state,
	}
	data, err := json.Marshal(event)
	if err == nil {
		err = p.connection.Publish(protocol.StateSubject(p.config.RoomID), data)
	}
	if err != nil {
		p.config.Logf("publish room state for %s: %v", p.config.RoomID, err)
	}
}

func (p *NATSPlayer) respond(message *nats.Msg, response protocol.CommandResponse) {
	if message == nil || message.Reply == "" {
		return
	}
	data, err := json.Marshal(response)
	if err != nil {
		p.config.Logf("encode player response: %v", err)
		return
	}
	if err := message.Respond(data); err != nil {
		p.config.Logf("reply to player request: %v", err)
	}
}

func (p *NATSPlayer) registration() (string, bool, protocol.ErrorCode) {
	epoch := p.epoch.Load()
	p.mu.RLock()
	defer p.mu.RUnlock()
	if !p.registered || p.registeredEpoch != epoch {
		return "", false, p.registrationCode
	}
	return p.registrationToken, true, ""
}

func (p *NATSPlayer) markUnregistered(code protocol.ErrorCode) {
	p.mu.Lock()
	p.registered = false
	p.registrationToken = ""
	p.registrationCode = code
	p.mu.Unlock()
}

func (p *NATSPlayer) registrationFailed(code protocol.ErrorCode, operation string, err error) {
	p.markUnregistered(code)
	p.config.Logf("%s for room %s: %v", operation, p.config.RoomID, err)
}

func registrationErrorCode(code protocol.ErrorCode) protocol.ErrorCode {
	if code == protocol.ErrDuplicateRoomID {
		return code
	}
	return protocol.ErrServiceUnavailable
}

func decodePlayerEnvelope(data []byte, target any) error {
	return decodeStrictJSON(data, target)
}

func decodeStrictJSON(data []byte, target any) error {
	if len(data) == 0 || len(data) > maxPlayerMessageBytes {
		return fmt.Errorf("message size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("message must contain exactly one JSON value")
	}
	return nil
}