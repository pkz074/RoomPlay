//go:build broker

package integration_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/pkz074/RoomPlay/internal/coordinator"
	"github.com/pkz074/RoomPlay/internal/protocol"
)

type httpFixture struct {
	broker    *server.Server
	base      string
	s         *coordinator.Service
	reg       *coordinator.Registry
	clock     *brokerClock
	nc        *nats.Conn
	brokerURL string
}

func startCoordinatorHTTP(t *testing.T) *httpFixture {
	t.Helper()
	broker := startTestBroker(t, -1)
	nc := brokerConnect(t, broker.ClientURL())
	clock := &brokerClock{now: time.Now()}
	reg := coordinator.NewRegistry(clock, nil)
	s := coordinator.NewService(nc, reg, log.New(io.Discard, "", 0))
	s.RequestTimeout = 500 * time.Millisecond
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop() })
	lib := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tracks" {
			t.Error(r.URL.Path)
		}
		io.WriteString(w, `{"tracks":[{"id":"a","title":"A","artist":"Test","duration_ms":60000,"url":"http://library/audio/a.mp3"}]}`)
	}))
	t.Cleanup(lib.Close)
	h, err := coordinator.NewHTTPHandler(s, coordinator.HTTPOptions{LibraryURL: lib.URL, WebFS: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("RoomPlay test controller")}}})
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(h)
	t.Cleanup(web.Close)
	return &httpFixture{broker, web.URL, s, reg, clock, brokerConnect(t, broker.ClientURL()), broker.ClientURL()}
}
func httpJSON(t *testing.T, f *httpFixture, method, path, body, id string, status int, dst any) {
	t.Helper()
	r, err := http.NewRequest(method, f.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		r.Header.Set("X-Request-ID", id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("%s %s: %d %s", method, path, response.StatusCode, data)
	}
	if dst != nil && json.Unmarshal(data, dst) != nil {
		t.Fatalf("invalid JSON: %s", data)
	}
}

type sseMessage struct{ name, data string }

func openTestSSE(t *testing.T, base string) (<-chan sseMessage, <-chan struct{}, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/v1/events", nil)
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" {
		response.Body.Close()
		cancel()
		t.Fatal("SSE not established")
	}
	out := make(chan sseMessage, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(response.Body)
		var event sseMessage
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				event.name = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				event.data = strings.TrimPrefix(line, "data: ")
			}
			if line == "" && event.name != "" {
				select {
				case out <- event:
				case <-ctx.Done():
					return
				}
				event = sseMessage{}
			}
		}
	}()
	closeStream := func() { cancel(); response.Body.Close() }
	t.Cleanup(closeStream)
	return out, done, closeStream
}

func TestBrokerHTTPAndSSEWorkflow(t *testing.T) {
	f := startCoordinatorHTTP(t)
	room := startBrokerRoom(t, f.brokerURL, "bedroom", "i1")
	readyBrokerRoom(t, f.nc, room, "bedroom")
	var catalog protocol.TracksResponse
	httpJSON(t, f, "GET", "/api/v1/tracks", "", "", 200, &catalog)
	if len(catalog.Tracks) != 1 || catalog.Tracks[0].ID != "a" {
		t.Fatalf("catalog: %+v", catalog)
	}
	events, done, closeStream := openTestSSE(t, f.base)
	var added protocol.CommandResult
	httpJSON(t, f, "POST", "/api/v1/rooms/bedroom/queue", `{"track_id":"a"}`, "add", 200, &added)
	if added.RequestID != "add" || added.InstanceID != "i1" || len(added.State.Queue) != 1 {
		t.Fatalf("queue add: %+v", added)
	}
	for {
		event := awaitBroker(t, events)
		if event.name == "room.state" {
			var state struct {
				RoomID, InstanceID string
				State              protocol.PlaybackState
			}
			var payload map[string]json.RawMessage
			json.Unmarshal([]byte(event.data), &payload)
			json.Unmarshal(payload["state"], &state.State)
			if _, leak := payload["registration_token"]; leak {
				t.Fatal("lease leaked to browser")
			}
			if state.State.Version == added.State.Version {
				break
			}
		}
	}
	var queue protocol.QueueResponse
	httpJSON(t, f, "GET", "/api/v1/rooms/bedroom/queue", "", "", 200, &queue)
	if len(queue.Queue) != 1 {
		t.Fatal(queue)
	}
	var played protocol.CommandResult
	httpJSON(t, f, "POST", "/api/v1/rooms/bedroom/commands", `{"request_id":"play","type":"playback.play","payload":{"track_id":"a"}}`, "play", 200, &played)
	if played.State.Status != protocol.StatusPlaying {
		t.Fatal(played)
	}
	var removed protocol.CommandResult
	httpJSON(t, f, "DELETE", "/api/v1/rooms/bedroom/queue/"+added.State.Queue[0].ID, "", "remove", 200, &removed)
	httpJSON(t, f, "DELETE", "/api/v1/rooms/bedroom/queue", "", "clear", 200, &removed)
	var detail protocol.RoomSummary
	httpJSON(t, f, "GET", "/api/v1/rooms/bedroom", "", "", 200, &detail)
	if detail.StateStale || detail.InstanceID != "i1" || detail.State.Current == nil {
		t.Fatalf("detail: %+v", detail)
	}
	// A high-version old-token event must not poison the HTTP snapshot cache.
	stale := protocol.StateEvent{RoomID: "bedroom", InstanceID: "old", RegistrationToken: "wrong", State: played.State}
	stale.State.Version = 999
	data, _ := json.Marshal(stale)
	f.nc.Publish(protocol.StateSubject("bedroom"), data)
	f.nc.FlushTimeout(time.Second)
	httpJSON(t, f, "GET", "/api/v1/rooms/bedroom", "", "", 200, &detail)
	if detail.State.Version == 999 {
		t.Fatal("stale event accepted")
	}
	f.clock.Advance(coordinator.HeartbeatTimeout)
	f.reg.Sweep()
	for {
		event := awaitBroker(t, events)
		if event.name == "room.availability" {
			var availability protocol.AvailabilityEvent
			if err := json.Unmarshal([]byte(event.data), &availability); err != nil {
				t.Fatal(err)
			}
			if availability.RoomID == "bedroom" && availability.Availability == protocol.RoomOffline {
				break
			}
		}
	}
	closeStream()
	awaitBroker(t, done)
	_, _, closeAgain := openTestSSE(t, f.base)
	defer closeAgain()
	httpJSON(t, f, "GET", "/api/v1/rooms/bedroom", "", "", 200, &detail)
	if detail.Availability != protocol.RoomOffline || !detail.StateStale {
		t.Fatalf("offline snapshot: %+v", detail)
	}
}

func TestBrokerTargetedRoutingIgnoresStaleSubscriber(t *testing.T) {
	f := startCoordinatorHTTP(t)
	old := startBrokerRoom(t, f.brokerURL, "bedroom", "old")
	readyBrokerRoom(t, f.nc, old, "bedroom")
	f.clock.Advance(coordinator.HeartbeatTimeout)
	current := startBrokerRoom(t, f.brokerURL, "bedroom", "new")
	readyBrokerRoom(t, f.nc, current, "bedroom")
	var result protocol.CommandResult
	httpJSON(t, f, "POST", "/api/v1/rooms/bedroom/queue", `{"track_id":"a"}`, "targeted", 200, &result)
	if result.InstanceID != "new" || result.State.Version != 1 || old.processor.Snapshot().Version != 0 || current.processor.Snapshot().Version != 1 {
		t.Fatalf("routing reached stale subscriber: %+v", result)
	}
}

func TestBrokerHTTPTimeoutAndNoResponders(t *testing.T) {
	f := startCoordinatorHTTP(t)
	f.reg.Register(protocol.RegistrationRequest{RoomID: "ghost", Name: "Ghost", InstanceID: "g1"})
	var failure protocol.HTTPErrorResponse
	httpJSON(t, f, "DELETE", "/api/v1/rooms/ghost/queue", "", "absent", 503, &failure)
	if failure.Error.Code != protocol.ErrNoResponders {
		t.Fatalf("no responders: %+v", failure)
	}
	var calls atomic.Int32
	sub, err := f.nc.Subscribe(protocol.CommandSubject("ghost"), func(message *nats.Msg) { calls.Add(1) /* executed, deliberately drop the reply */ })
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Unsubscribe()
	f.nc.FlushTimeout(time.Second)
	httpJSON(t, f, "DELETE", "/api/v1/rooms/ghost/queue", "", "uncertain", 504, &failure)
	if failure.Error.Code != protocol.ErrCommandTimeout || failure.RequestID != "uncertain" || !strings.Contains(failure.Error.Message, "unknown") || calls.Load() != 1 {
		t.Fatalf("timeout/retry behavior: %+v calls=%d", failure, calls.Load())
	}
}

func TestBrokerHTTPUnavailableDoesNotQueueCommands(t *testing.T) {
	f := startCoordinatorHTTP(t)
	room := startBrokerRoom(t, f.brokerURL, "bedroom", "i1")
	readyBrokerRoom(t, f.nc, room, "bedroom")
	port := f.broker.Addr().(*net.TCPAddr).Port
	f.broker.Shutdown()
	f.broker.WaitForShutdown()
	awaitBroker(t, room.disconnected)
	eventuallyBroker(t, func() bool { current, _ := f.reg.Get("bedroom"); return current.StateStale && f.nc.IsReconnecting() })
	var failure protocol.HTTPErrorResponse
	httpJSON(t, f, "POST", "/api/v1/rooms/bedroom/queue", `{"track_id":"a"}`, "during-outage", 503, &failure)
	if failure.Error.Code != protocol.ErrBrokerUnavailable {
		t.Fatalf("outage response: %+v", failure)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := f.nc.RequestWithContext(ctx, protocol.CommandSubject("bedroom"), []byte(`{"request_id":"not-buffered","type":"queue.add","payload":{"track_id":"a"}}`))
	if !errors.Is(err, nats.ErrReconnectBufExceeded) {
		t.Fatalf("reconnect publication was not rejected immediately: %v", err)
	}
	startTestBroker(t, port)
	awaitBroker(t, room.reconnected)
	eventuallyBroker(t, f.nc.IsConnected)
	readyBrokerRoom(t, f.nc, room, "bedroom")
	var queue protocol.QueueResponse
	httpJSON(t, f, "GET", "/api/v1/rooms/bedroom/queue", "", "", 200, &queue)
	if len(queue.Queue) != 0 {
		t.Fatal("outage command replayed after reconnect")
	}
}
