package player

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/pkz074/RoomPlay/internal/protocol"
)

func TestNATSPlayerFlushesBeforeRegisteringAndPublishesFencedState(t *testing.T) {
	registration, _ := json.Marshal(protocol.RegistrationResponse{
		Success:           true,
		RoomID:            "bedroom",
		RegistrationToken: "lease-1",
	})
	connection := &fakePlayerNATS{registrationReply: registration}
	processor := NewCommandProcessor(testTrackIndex(), newSimulationBackend(newFakePlaybackClock()))
	client, err := NewNATSPlayer(connection, processor, NATSPlayerConfig{
		RoomID:     "bedroom",
		Name:       "Bedroom",
		InstanceID: "instance-1",
		Logf:       func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("NewNATSPlayer: %v", err)
	}
	client.register(context.Background())
	if token, registered, _ := client.registration(); !registered || token != "lease-1" {
		t.Fatalf("registration token=%q registered=%t", token, registered)
	}
	if len(connection.calls) != 2 || connection.calls[0] != "flush" || connection.calls[1] != "request:"+protocol.SubjectRegister {
		t.Fatalf("registration call order = %v", connection.calls)
	}
	var request protocol.RegistrationRequest
	if err := json.Unmarshal(connection.requestData, &request); err != nil {
		t.Fatalf("decode registration request: %v", err)
	}
	if request.RoomID != "bedroom" || request.Name != "Bedroom" || request.InstanceID != "instance-1" {
		t.Fatalf("registration request = %+v", request)
	}

	client.publishState(protocol.NewPlaybackState())
	if len(connection.published) != 2 || connection.published[1].subject != protocol.StateSubject("bedroom") {
		t.Fatalf("published messages = %+v", connection.published)
	}
	var event protocol.StateEvent
	if err := json.Unmarshal(connection.published[1].data, &event); err != nil {
		t.Fatalf("decode state event: %v", err)
	}
	if event.RoomID != "bedroom" || event.InstanceID != "instance-1" || event.RegistrationToken != "lease-1" {
		t.Fatalf("state event fencing metadata = %+v", event)
	}
}

func TestNATSPlayerRejectsDuplicateRoomWithoutApplyingCommands(t *testing.T) {
	registration, _ := json.Marshal(protocol.RegistrationResponse{
		Success: false,
		RoomID:  "bedroom",
		Error:   &protocol.APIError{Code: protocol.ErrDuplicateRoomID, Message: "already active"},
	})
	connection := &fakePlayerNATS{registrationReply: registration}
	processor := NewCommandProcessor(testTrackIndex(), newSimulationBackend(newFakePlaybackClock()))
	client, err := NewNATSPlayer(connection, processor, NATSPlayerConfig{
		RoomID: "bedroom", Name: "Bedroom", InstanceID: "instance-2", Logf: func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("NewNATSPlayer: %v", err)
	}
	client.register(context.Background())
	if _, registered, code := client.registration(); registered || code != protocol.ErrDuplicateRoomID {
		t.Fatalf("duplicate registration state: registered=%t code=%q", registered, code)
	}
	client.handleCommand(&nats.Msg{Data: []byte(`{"request_id":"play","type":"playback.play","payload":{"track_id":"a"}}`)})
	if state := processor.Snapshot(); state.Version != 0 || state.Current != nil {
		t.Fatalf("unregistered player applied command: %+v", state)
	}
}

func TestNATSPlayerReconnectEpochFencesOldRegistration(t *testing.T) {
	registration, _ := json.Marshal(protocol.RegistrationResponse{Success: true, RoomID: "bedroom", RegistrationToken: "lease-1"})
	epoch := &atomic.Uint64{}
	connection := &fakePlayerNATS{registrationReply: registration}
	client, err := NewNATSPlayer(connection, NewCommandProcessor(testTrackIndex(), nil), NATSPlayerConfig{
		RoomID: "bedroom", Name: "Bedroom", InstanceID: "instance-1", ReconnectEpoch: epoch, Logf: func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("NewNATSPlayer: %v", err)
	}
	client.register(context.Background())
	if _, registered, _ := client.registration(); !registered {
		t.Fatal("registration was not active before reconnect")
	}
	epoch.Add(1)
	if _, registered, _ := client.registration(); registered {
		t.Fatal("old registration remained active after broker reconnect")
	}
}

func TestLocalPlaybackContinuesWhenCoordinatorRegistrationIsLost(t *testing.T) {
	clock := newFakePlaybackClock()
	processor := NewCommandProcessor(testTrackIndex(), newSimulationBackend(clock))
	registration, _ := json.Marshal(protocol.RegistrationResponse{
		Success: true, RoomID: "bedroom", RegistrationToken: "lease-1",
	})
	client, err := NewNATSPlayer(&fakePlayerNATS{registrationReply: registration}, processor, NATSPlayerConfig{
		RoomID: "bedroom", Name: "Bedroom", InstanceID: "instance-1", Logf: func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("NewNATSPlayer: %v", err)
	}
	client.register(context.Background())
	processor.SetOnStateChange(client.publishState)
	for _, request := range []protocol.CommandRequest{
		commandRequest("play-a", protocol.PlaybackPlay, `{"track_id":"a"}`),
		commandRequest("queue-b", protocol.QueueAdd, `{"track_id":"b"}`),
	} {
		if response := processor.Process(context.Background(), request); !response.Success {
			t.Fatalf("%s response = %+v", request.Type, response)
		}
	}
	client.registrationFailed(protocol.ErrServiceUnavailable, "heartbeat", errors.New("coordinator unavailable"))
	clock.Advance(time.Second)
	state := processor.Snapshot()
	if state.Status != protocol.StatusPlaying || state.Current == nil || state.Current.TrackID != "b" ||
		len(state.History) != 1 || state.History[0].TrackID != "a" || state.Version != 3 {
		t.Fatalf("local playback did not continue after registration loss: %+v", state)
	}
}

func TestNATSPlayerSubscribesAndFlushesBeforeRegistration(t *testing.T) {
	registration, _ := json.Marshal(protocol.RegistrationResponse{Success: true, RoomID: "bedroom", RegistrationToken: "lease-1"})
	ctx, cancel := context.WithCancel(context.Background())
	connection := &fakePlayerNATS{registrationReply: registration, cancel: cancel}
	client, err := NewNATSPlayer(connection, NewCommandProcessor(testTrackIndex(), nil), NATSPlayerConfig{
		RoomID: "bedroom", Name: "Bedroom", InstanceID: "instance-1", Logf: func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("NewNATSPlayer: %v", err)
	}
	if err := client.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []string{
		"subscribe:" + protocol.CommandSubject("bedroom"),
		"subscribe:" + protocol.GetStateSubject("bedroom"),
		"flush",
		"flush",
		"request:" + protocol.SubjectRegister,
		"flush",
	}
	if len(connection.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", connection.calls, want)
	}
	for i := range want {
		if connection.calls[i] != want[i] {
			t.Fatalf("calls = %v, want %v", connection.calls, want)
		}
	}
}

type fakePlayerNATS struct {
	mu                sync.Mutex
	registrationReply []byte
	calls             []string
	requestData       []byte
	published         []publishedPlayerMessage
	cancel            context.CancelFunc
}

type publishedPlayerMessage struct {
	subject string
	data    []byte
}

func (f *fakePlayerNATS) Subscribe(subject string, _ nats.MsgHandler) (*nats.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "subscribe:"+subject)
	return nil, nil
}

func (f *fakePlayerNATS) FlushTimeout(time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "flush")
	return nil
}

func (f *fakePlayerNATS) RequestWithContext(_ context.Context, subject string, data []byte) (*nats.Msg, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "request:"+subject)
	f.requestData = append([]byte(nil), data...)
	message := &nats.Msg{Data: append([]byte(nil), f.registrationReply...)}
	cancel := f.cancel
	f.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return message, nil
}

func (f *fakePlayerNATS) Publish(subject string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, publishedPlayerMessage{subject: subject, data: append([]byte(nil), data...)})
	return nil
}

var _ NATSConnection = (*fakePlayerNATS)(nil)
