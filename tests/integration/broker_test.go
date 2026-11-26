//go:build broker

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/pkz074/RoomPlay/internal/coordinator"
	"github.com/pkz074/RoomPlay/internal/player"
	"github.com/pkz074/RoomPlay/internal/protocol"
)

const brokerWait = 10 * time.Second

type brokerClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *brokerClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *brokerClock) Advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

type brokerLog chan string

func (l brokerLog) Write(data []byte) (int, error) {
	select {
	case l <- string(data):
	default:
	}
	return len(data), nil
}

// This is the real NATS server and client stack on TCP, with no fake transport.
func startTestBroker(t *testing.T, port int) *server.Server {
	t.Helper()
	s, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: port, NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	s.Start()
	if !s.ReadyForConnections(5 * time.Second) {
		s.Shutdown()
		t.Fatal("broker did not become ready")
	}
	t.Cleanup(func() { s.Shutdown(); s.WaitForShutdown() })
	return s
}

func brokerConnect(t *testing.T, url string, options ...nats.Option) *nats.Conn {
	t.Helper()
	options = append([]nats.Option{nats.Timeout(time.Second), nats.MaxReconnects(-1), nats.ReconnectWait(20 * time.Millisecond), nats.DrainTimeout(time.Second)}, options...)
	nc, err := nats.Connect(url, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func brokerCoordinator(t *testing.T, url string, options ...nats.Option) (*coordinator.Registry, *nats.Conn) {
	t.Helper()
	nc := brokerConnect(t, url, options...)
	reg := coordinator.NewRegistry(nil, nil)
	svc := coordinator.NewService(nc, reg, log.New(io.Discard, "", 0))
	if err := svc.Start(); err != nil {
		t.Fatal(err)
	}
	return reg, nc
}

type registrationObservation struct{ response protocol.RegistrationResponse }

// Observe real traffic and retain the actual subscriptions for cleanup assertions.
type observedBrokerConnection struct {
	*nats.Conn
	registrations     chan registrationObservation
	mu                sync.Mutex
	roomSubscriptions []*nats.Subscription
}

func (c *observedBrokerConnection) Subscribe(subject string, handler nats.MsgHandler) (*nats.Subscription, error) {
	sub, err := c.Conn.Subscribe(subject, handler)
	if err == nil {
		c.mu.Lock()
		c.roomSubscriptions = append(c.roomSubscriptions, sub)
		c.mu.Unlock()
	}
	return sub, err
}

func (c *observedBrokerConnection) RequestWithContext(ctx context.Context, subject string, data []byte) (*nats.Msg, error) {
	message, err := c.Conn.RequestWithContext(ctx, subject, data)
	if err == nil && subject == protocol.SubjectRegister {
		var response protocol.RegistrationResponse
		if json.Unmarshal(message.Data, &response) == nil {
			select {
			case c.registrations <- registrationObservation{response}:
			default:
			}
		}
	}
	return message, err
}

type brokerRoom struct {
	processor                 *player.CommandProcessor
	connection                *observedBrokerConnection
	cancel                    context.CancelFunc
	exited                    chan struct{}
	err                       error // read only after exited closes
	disconnected, reconnected chan struct{}
}

func startBrokerRoom(t *testing.T, url, room, instance string) *brokerRoom {
	t.Helper()
	r := &brokerRoom{exited: make(chan struct{}), disconnected: make(chan struct{}, 8), reconnected: make(chan struct{}, 8)}
	epoch := &atomic.Uint64{}
	nc := brokerConnect(t, url,
		nats.DisconnectErrHandler(func(_ *nats.Conn, _ error) { epoch.Add(1); signalBrokerEvent(r.disconnected) }),
		nats.ReconnectHandler(func(_ *nats.Conn) { epoch.Add(1); signalBrokerEvent(r.reconnected) }),
	)
	r.connection = &observedBrokerConnection{Conn: nc, registrations: make(chan registrationObservation, 16)}
	r.processor = player.NewCommandProcessor(player.TrackIndex{"a": {ID: "a", Title: "A", Artist: "Test", DurationMs: 60_000, URL: "http://library/audio/a.mp3"}}, player.NewSimulationBackend())
	client, err := player.NewNATSPlayer(r.connection, r.processor, player.NATSPlayerConfig{RoomID: room, Name: room, InstanceID: instance, ReconnectEpoch: epoch, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func() { r.err = client.Run(ctx); close(r.exited) }()
	t.Cleanup(func() { cancel(); awaitBroker(t, r.exited); nc.Close(); r.processor.Close() })
	return r
}

func signalBrokerEvent(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func awaitBroker[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(brokerWait):
		t.Fatal("broker operation did not complete")
		var zero T
		return zero
	}
}

func eventuallyBroker(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.NewTimer(brokerWait)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if check() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("broker condition did not become true")
		case <-tick.C:
		}
	}
}

func brokerJSON(t *testing.T, nc *nats.Conn, subject string, request, response any) {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	message, err := nc.Request(subject, data, time.Second)
	if err != nil {
		t.Fatalf("%s: %v", subject, err)
	}
	if err := json.Unmarshal(message.Data, response); err != nil {
		t.Fatalf("%s: %v; data=%s", subject, err, message.Data)
	}
}

func readyBrokerRoom(t *testing.T, nc *nats.Conn, r *brokerRoom, room string) protocol.RegistrationResponse {
	t.Helper()
	registration := awaitBroker(t, r.connection.registrations).response
	if !registration.Success {
		t.Fatalf("registration failed: %+v", registration)
	}
	// The observer sees the response just before the player's register method
	// installs it. Query readiness instead of assuming callback scheduling order.
	eventuallyBroker(t, func() bool {
		message, err := nc.Request(protocol.GetStateSubject(room), []byte(`{"request_id":"ready"}`), 200*time.Millisecond)
		if err != nil {
			return false
		}
		var result protocol.CommandResponse
		return json.Unmarshal(message.Data, &result) == nil && result.Success
	})
	return registration
}

func brokerCommand(t *testing.T, nc *nats.Conn, room, id string, kind protocol.CommandType, payload string) protocol.CommandResponse {
	t.Helper()
	var response protocol.CommandResponse
	brokerJSON(t, nc, protocol.CommandSubject(room), protocol.CommandRequest{RequestID: id, Type: kind, Payload: json.RawMessage(payload)}, &response)
	return response
}

func TestBrokerCommandsQueriesStateAndDeduplication(t *testing.T) {
	s := startTestBroker(t, -1)
	reg, _ := brokerCoordinator(t, s.ClientURL())
	nc := brokerConnect(t, s.ClientURL())
	events, err := nc.SubscribeSync(protocol.StateSubject("bedroom"))
	if err != nil {
		t.Fatal(err)
	}
	if err := nc.FlushTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	r := startBrokerRoom(t, s.ClientURL(), "bedroom", "i1")
	registration := readyBrokerRoom(t, nc, r, "bedroom")
	kitchen := startBrokerRoom(t, s.ClientURL(), "kitchen", "k1")
	readyBrokerRoom(t, nc, kitchen, "kitchen")
	first := brokerCommand(t, nc, "bedroom", "add", protocol.QueueAdd, `{"track_id":"a"}`)
	if !first.Success || first.RequestID != "add" || first.State == nil || len(first.State.Queue) != 1 {
		t.Fatalf("command response: %+v", first)
	}
	message, err := events.NextMsg(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var event protocol.StateEvent
	if err := json.Unmarshal(message.Data, &event); err != nil {
		t.Fatal(err)
	}
	if event.InstanceID != "i1" || event.RegistrationToken != registration.RegistrationToken || event.State.Version != first.State.Version {
		t.Fatalf("state event: %+v", event)
	}
	duplicate := brokerCommand(t, nc, "bedroom", "add", protocol.QueueAdd, `{ "track_id": "a" }`)
	if !duplicate.Success || duplicate.State == nil || duplicate.State.Version != first.State.Version || len(r.processor.Snapshot().Queue) != 1 {
		t.Fatalf("duplicate executed twice: %+v", duplicate)
	}
	conflict := brokerCommand(t, nc, "bedroom", "add", protocol.QueueClear, `{}`)
	if conflict.Success || conflict.Error == nil || conflict.Error.Code != protocol.ErrDuplicateRequestConflict || conflict.State != nil {
		t.Fatalf("conflict response: %+v", conflict)
	}
	var state protocol.CommandResponse
	brokerJSON(t, nc, protocol.GetStateSubject("bedroom"), protocol.StateQuery{RequestID: "read"}, &state)
	if !state.Success || state.State == nil || state.RequestID != "read" || state.State.Version != 1 || len(state.State.Queue) != 1 {
		t.Fatalf("query response: %+v", state)
	}
	room, _ := reg.Get("bedroom")
	if !room.Online || room.InstanceID != "i1" {
		t.Fatalf("registry: %+v", room)
	}
	bad, err := nc.Request(protocol.CommandSubject("bedroom"), []byte(`{"request_id":"bad","type":"queue.add","payload":{}}`), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var rejected protocol.CommandResponse
	if err := json.Unmarshal(bad.Data, &rejected); err != nil {
		t.Fatal(err)
	}
	if rejected.Success || rejected.Error == nil || rejected.Error.Code != protocol.ErrInvalidPayload || rejected.State != nil {
		t.Fatalf("invalid command: %s", bad.Data)
	}
	const callers = 16
	type result struct {
		message *nats.Msg
		err     error
	}
	responses := make(chan result, callers)
	for range callers {
		go func() {
			message, err := nc.Request(protocol.CommandSubject("bedroom"), []byte(`{"request_id":"concurrent","type":"queue.add","payload":{"track_id":"a"}}`), time.Second)
			responses <- result{message, err}
		}()
	}
	for range callers {
		result := awaitBroker(t, responses)
		if result.err != nil {
			t.Fatal(result.err)
		}
		var response protocol.CommandResponse
		if err := json.Unmarshal(result.message.Data, &response); err != nil {
			t.Fatal(err)
		}
		if !response.Success || response.State == nil || response.State.Version != 2 || len(response.State.Queue) != 2 {
			t.Fatalf("concurrent duplicate: %+v", response)
		}
	}
	if kitchen.processor.Snapshot().Version != 0 {
		t.Fatal("bedroom command affected kitchen")
	}
}

func TestBrokerDuplicateRoomExitsAndUnsubscribes(t *testing.T) {
	s := startTestBroker(t, -1)
	brokerCoordinator(t, s.ClientURL())
	nc := brokerConnect(t, s.ClientURL())
	live := startBrokerRoom(t, s.ClientURL(), "bedroom", "i1")
	readyBrokerRoom(t, nc, live, "bedroom")
	duplicate := startBrokerRoom(t, s.ClientURL(), "bedroom", "i2")
	response := awaitBroker(t, duplicate.connection.registrations).response
	if response.Success || response.Error == nil || response.Error.Code != protocol.ErrDuplicateRoomID {
		t.Fatalf("duplicate registration: %+v", response)
	}
	awaitBroker(t, duplicate.exited)
	if duplicate.err == nil || !strings.Contains(duplicate.err.Error(), string(protocol.ErrDuplicateRoomID)) {
		t.Fatalf("duplicate Run error: %v", duplicate.err)
	}
	duplicate.connection.mu.Lock()
	for _, sub := range duplicate.connection.roomSubscriptions {
		if sub.IsValid() {
			t.Errorf("rejected player retained %s", sub.Subject)
		}
	}
	duplicate.connection.mu.Unlock()
	result := brokerCommand(t, nc, "bedroom", "add", protocol.QueueAdd, `{"track_id":"a"}`)
	if !result.Success || len(live.processor.Snapshot().Queue) != 1 || duplicate.processor.Snapshot().Version != 0 {
		t.Fatalf("duplicate affected live room: %+v", result)
	}
	live.cancel()
	awaitBroker(t, live.exited)
	_, err := nc.Request(protocol.CommandSubject("bedroom"), []byte(`{}`), time.Second)
	if !errors.Is(err, nats.ErrNoResponders) {
		t.Fatalf("after shutdown: %v, want no responders", err)
	}
}

func TestBrokerRegistrationHeartbeatFencingAndExpiry(t *testing.T) {
	s := startTestBroker(t, -1)
	coordinatorConn := brokerConnect(t, s.ClientURL())
	nc := brokerConnect(t, s.ClientURL())
	clock := &brokerClock{now: time.Now()}
	reg := coordinator.NewRegistry(clock, nil)
	logs := make(brokerLog, 8)
	svc := coordinator.NewService(coordinatorConn, reg, log.New(logs, "", 0))
	if err := svc.Start(); err != nil {
		t.Fatal(err)
	}
	var first protocol.RegistrationResponse
	brokerJSON(t, nc, protocol.SubjectRegister, protocol.RegistrationRequest{RoomID: "bedroom", Name: "Bedroom", InstanceID: "i1"}, &first)
	if !first.Success {
		t.Fatalf("registration: %+v", first)
	}
	clock.Advance(2 * time.Second)
	hb := protocol.Heartbeat{RoomID: "bedroom", InstanceID: "i1", RegistrationToken: first.RegistrationToken}
	data, _ := json.Marshal(hb)
	if err := nc.Publish(protocol.SubjectHeartbeat, data); err != nil {
		t.Fatal(err)
	}
	eventuallyBroker(t, func() bool { room, _ := reg.Get("bedroom"); return room.LastHeartbeat.Equal(clock.Now()) })
	lastValid := clock.Now()
	clock.Advance(2 * time.Second)
	hb.RegistrationToken = "wrong-token"
	data, _ = json.Marshal(hb)
	if err := nc.Publish(protocol.SubjectHeartbeat, data); err != nil {
		t.Fatal(err)
	}
	if line := awaitBroker(t, logs); !strings.Contains(line, "ignored") {
		t.Fatalf("invalid heartbeat log: %s", line)
	}
	if room, _ := reg.Get("bedroom"); !room.LastHeartbeat.Equal(lastValid) {
		t.Fatal("invalid heartbeat refreshed lease")
	}
	clock.Advance(4 * time.Second)
	reg.Sweep()
	if room, _ := reg.Get("bedroom"); room.Online {
		t.Fatal("room stayed online after six seconds without valid heartbeat")
	}
	var replacement protocol.RegistrationResponse
	brokerJSON(t, nc, protocol.SubjectRegister, protocol.RegistrationRequest{RoomID: "bedroom", Name: "Bedroom", InstanceID: "i2"}, &replacement)
	if !replacement.Success || replacement.RegistrationToken == first.RegistrationToken {
		t.Fatalf("replacement: %+v", replacement)
	}
	hb.RegistrationToken = first.RegistrationToken
	data, _ = json.Marshal(hb)
	if err := nc.Publish(protocol.SubjectHeartbeat, data); err != nil {
		t.Fatal(err)
	}
	awaitBroker(t, logs)
	if room, _ := reg.Get("bedroom"); room.InstanceID != "i2" || room.Token != replacement.RegistrationToken {
		t.Fatalf("old heartbeat corrupted replacement: %+v", room)
	}
}

func TestBrokerMalformedRegistrationIsRejected(t *testing.T) {
	s := startTestBroker(t, -1)
	reg, _ := brokerCoordinator(t, s.ClientURL())
	nc := brokerConnect(t, s.ClientURL())
	valid := `{"room_id":"bedroom","name":"Bedroom","instance_id":"i1"}`
	for _, data := range []string{"not json", valid + "]", valid + "}", valid + " {}", `{"room_id":"bad.*","name":"Bad","instance_id":"i1"}`} {
		message, err := nc.Request(protocol.SubjectRegister, []byte(data), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		var response protocol.RegistrationResponse
		if err := json.Unmarshal(message.Data, &response); err != nil {
			t.Fatal(err)
		}
		if response.Success || response.Error == nil || response.Error.Code != protocol.ErrInvalidPayload {
			t.Errorf("accepted invalid registration %q: %s", data, message.Data)
		}
	}
	if len(reg.List()) != 0 {
		t.Fatal("malformed registration created a room")
	}
}

func TestBrokerOrdinarySubscribersBothReceiveRequests(t *testing.T) {
	s := startTestBroker(t, -1)
	nc := brokerConnect(t, s.ClientURL())
	for _, id := range []string{"one", "two"} {
		other := brokerConnect(t, s.ClientURL())
		if _, err := other.Subscribe(protocol.CommandSubject("fanout"), func(message *nats.Msg) { message.Respond([]byte(id)) }); err != nil {
			t.Fatal(err)
		}
		if err := other.FlushTimeout(time.Second); err != nil {
			t.Fatal(err)
		}
	}
	inbox := nats.NewInbox()
	replies, err := nc.SubscribeSync(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if err := nc.FlushTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	if err := nc.PublishRequest(protocol.CommandSubject("fanout"), inbox, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for range 2 {
		message, err := replies.NextMsg(time.Second)
		if err != nil {
			t.Fatal(err)
		}
		seen[string(message.Data)] = true
	}
	if !seen["one"] || !seen["two"] {
		t.Fatalf("request fanout: %v", seen)
	}
}

func TestBrokerDuplicateRejectionOnRenewalExits(t *testing.T) {
	s := startTestBroker(t, -1)
	_, oldCoordinator := brokerCoordinator(t, s.ClientURL())
	nc := brokerConnect(t, s.ClientURL())
	r := startBrokerRoom(t, s.ClientURL(), "bedroom", "i1")
	readyBrokerRoom(t, nc, r, "bedroom")
	oldCoordinator.Close()
	// A replacement coordinator has already accepted another instance before
	// the old player renews. The old subscriber must stop on that rejection.
	reg := coordinator.NewRegistry(nil, nil)
	reg.Register(protocol.RegistrationRequest{RoomID: "bedroom", Name: "Bedroom", InstanceID: "i2"})
	svc := coordinator.NewService(brokerConnect(t, s.ClientURL()), reg, log.New(io.Discard, "", 0))
	if err := svc.Start(); err != nil {
		t.Fatal(err)
	}
	rejected := awaitBroker(t, r.connection.registrations).response
	if rejected.Success || rejected.Error == nil || rejected.Error.Code != protocol.ErrDuplicateRoomID {
		t.Fatalf("renewal rejection: %+v", rejected)
	}
	awaitBroker(t, r.exited)
	if r.err == nil || !strings.Contains(r.err.Error(), string(protocol.ErrDuplicateRoomID)) {
		t.Fatalf("Run after rejected renewal: %v", r.err)
	}
	r.connection.mu.Lock()
	for _, sub := range r.connection.roomSubscriptions {
		if sub.IsValid() {
			t.Errorf("rejected renewal retained %s", sub.Subject)
		}
	}
	r.connection.mu.Unlock()
}

func TestBrokerCoordinatorRestartRenewsRegistration(t *testing.T) {
	s := startTestBroker(t, -1)
	_, oldCoordinator := brokerCoordinator(t, s.ClientURL())
	nc := brokerConnect(t, s.ClientURL())
	r := startBrokerRoom(t, s.ClientURL(), "bedroom", "i1")
	old := readyBrokerRoom(t, nc, r, "bedroom")
	play := brokerCommand(t, nc, "bedroom", "play", protocol.PlaybackPlay, `{"track_id":"a"}`)
	if !play.Success || play.State == nil || play.State.Current == nil {
		t.Fatalf("play: %+v", play)
	}
	oldCoordinator.Close()
	reg, _ := brokerCoordinator(t, s.ClientURL())
	newRegistration := readyBrokerRoom(t, nc, r, "bedroom")
	if newRegistration.RegistrationToken == old.RegistrationToken {
		t.Fatal("coordinator restart reused the old lease")
	}
	room, _ := reg.Get("bedroom")
	if !room.Online || room.Token != newRegistration.RegistrationToken || room.InstanceID != "i1" {
		t.Fatalf("rebuilt registry: %+v", room)
	}
	state := r.processor.Snapshot()
	if state.Current == nil || state.Version != play.State.Version || state.Current.ID != play.State.Current.ID || state.PositionMs <= play.State.PositionMs {
		t.Fatalf("playback did not survive coordinator restart: %+v", state)
	}
	heartbeats, err := nc.SubscribeSync(protocol.SubjectHeartbeat)
	if err != nil {
		t.Fatal(err)
	}
	if err := nc.FlushTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	message, err := heartbeats.NextMsg(4 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var hb protocol.Heartbeat
	if err := json.Unmarshal(message.Data, &hb); err != nil {
		t.Fatal(err)
	}
	if hb.RegistrationToken != room.Token || hb.InstanceID != "i1" {
		t.Fatalf("renewed heartbeat: %+v", hb)
	}
}

func TestBrokerDisconnectReconnectRestoresControl(t *testing.T) {
	s := startTestBroker(t, -1)
	port := s.Addr().(*net.TCPAddr).Port
	coordinatorReconnected := make(chan struct{}, 1)
	brokerCoordinator(t, s.ClientURL(), nats.ReconnectHandler(func(_ *nats.Conn) { signalBrokerEvent(coordinatorReconnected) }))
	nc := brokerConnect(t, s.ClientURL())
	r := startBrokerRoom(t, s.ClientURL(), "bedroom", "i1")
	readyBrokerRoom(t, nc, r, "bedroom")
	play := brokerCommand(t, nc, "bedroom", "play", protocol.PlaybackPlay, `{"track_id":"a"}`)
	if !play.Success || play.State == nil || play.State.Current == nil {
		t.Fatalf("play: %+v", play)
	}
	s.Shutdown()
	s.WaitForShutdown()
	awaitBroker(t, r.disconnected)
	eventuallyBroker(t, func() bool { return r.processor.Snapshot().PositionMs > play.State.PositionMs })
	startTestBroker(t, port)
	awaitBroker(t, r.reconnected)
	awaitBroker(t, coordinatorReconnected)
	eventuallyBroker(t, nc.IsConnected)
	readyBrokerRoom(t, nc, r, "bedroom")
	result := brokerCommand(t, nc, "bedroom", "after-reconnect", protocol.QueueAdd, `{"track_id":"a"}`)
	if !result.Success || result.State == nil || result.State.Current == nil || result.State.Current.ID != play.State.Current.ID || len(result.State.Queue) != 1 || result.State.Version != play.State.Version+1 {
		t.Fatalf("control after reconnect: %+v", result)
	}
}

func TestBrokerTimeoutCanFollowExecution(t *testing.T) {
	s := startTestBroker(t, -1)
	brokerCoordinator(t, s.ClientURL())
	nc := brokerConnect(t, s.ClientURL())
	r := startBrokerRoom(t, s.ClientURL(), "bedroom", "i1")
	readyBrokerRoom(t, nc, r, "bedroom")
	_, err := nc.Request("test.absent", nil, time.Second)
	if !errors.Is(err, nats.ErrNoResponders) {
		t.Fatalf("absent responder: %v", err)
	}
	applied, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	proxy := brokerConnect(t, s.ClientURL())
	_, err = proxy.Subscribe("test.delayed", func(message *nats.Msg) {
		reply, err := proxy.Request(protocol.CommandSubject("bedroom"), message.Data, time.Second)
		if err == nil {
			close(applied)
			<-release
			message.Respond(reply.Data)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := proxy.FlushTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request := []byte(`{"request_id":"uncertain","type":"queue.add","payload":{"track_id":"a"}}`)
	errCh := make(chan error, 1)
	go func() { _, err := nc.RequestWithContext(ctx, "test.delayed", request); errCh <- err }()
	awaitBroker(t, applied)
	// Execution is confirmed before waiting for the deadline. The reply remains
	// held, proving that a request timeout is not proof of non-execution.
	if err := awaitBroker(t, errCh); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost reply: %v", err)
	}
	unblock()
	retry := brokerCommand(t, nc, "bedroom", "uncertain", protocol.QueueAdd, `{"track_id":"a"}`)
	if !retry.Success || retry.State == nil || retry.State.Version != 1 || len(r.processor.Snapshot().Queue) != 1 {
		t.Fatalf("retry after unknown outcome: %+v", retry)
	}
}
