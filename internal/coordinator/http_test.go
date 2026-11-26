package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/pkz074/RoomPlay/internal/protocol"
)

type testRequester struct {
	online bool
	call   func(context.Context, string, []byte) (*nats.Msg, error)
}

func (f *testRequester) IsConnected() bool { return f.online }
func (f *testRequester) RequestWithContext(ctx context.Context, subject string, data []byte) (*nats.Msg, error) {
	return f.call(ctx, subject, data)
}

func testHTTPService(t *testing.T) (*Service, *fakeClock, Room) {
	t.Helper()
	reg, clock, _ := newTest()
	reg.Register(reqFor("bedroom", "i1"))
	room, _ := reg.Get("bedroom")
	s := NewService(nil, reg, log.New(io.Discard, "", 0))
	t.Cleanup(func() { s.cancel(); s.events.Close() })
	s.requester = &testRequester{online: true, call: func(ctx context.Context, subject string, data []byte) (*nats.Msg, error) {
		var request protocol.CommandRequest
		json.Unmarshal(data, &request)
		state := protocol.NewPlaybackState()
		response := protocol.CommandResponse{RequestID: request.RequestID, InstanceID: room.InstanceID, RegistrationToken: room.Token, Success: true, State: &state}
		out, _ := json.Marshal(response)
		return &nats.Msg{Data: out}, nil
	}}
	return s, clock, room
}
func testHTTPHandler(t *testing.T, s *Service) http.Handler {
	t.Helper()
	h, e := NewHTTPHandler(s, HTTPOptions{LibraryURL: "http://library"})
	if e != nil {
		t.Fatal(e)
	}
	return h
}
func serveHTTP(h http.Handler, method, path, body, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if id != "" {
		r.Header.Set("X-Request-ID", id)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHTTPRoutesPreserveIDsAndTranslateQueues(t *testing.T) {
	for _, tt := range []struct {
		method, path, body string
		kind               protocol.CommandType
		payload            string
	}{
		{"POST", "/api/v1/rooms/bedroom/commands", `{"request_id":"r1","type":"playback.pause","payload":{}}`, protocol.PlaybackPause, `{}`},
		{"POST", "/api/v1/rooms/bedroom/queue", `{"track_id":"a"}`, protocol.QueueAdd, `{"track_id":"a"}`},
		{"DELETE", "/api/v1/rooms/bedroom/queue/item-1", "", protocol.QueueRemove, `{"item_id":"item-1"}`},
		{"DELETE", "/api/v1/rooms/bedroom/queue", "", protocol.QueueClear, `{}`},
	} {
		t.Run(tt.method+tt.path, func(t *testing.T) {
			s, _, room := testHTTPService(t)
			called := false
			original := s.requester.(*testRequester).call
			s.requester.(*testRequester).call = func(ctx context.Context, subject string, data []byte) (*nats.Msg, error) {
				called = true
				var req protocol.CommandRequest
				if json.Unmarshal(data, &req) != nil || subject != protocol.CommandSubject("bedroom") || req.RequestID != "r1" || req.Type != tt.kind || string(req.Payload) != tt.payload || req.TargetInstanceID != room.InstanceID || req.RegistrationToken != room.Token {
					t.Errorf("forwarded request: %s subject=%s", data, subject)
				}
				return original(ctx, subject, data)
			}
			w := serveHTTP(testHTTPHandler(t, s), tt.method, tt.path, tt.body, "r1")
			if w.Code != 200 || !called {
				t.Fatalf("status=%d body=%s called=%t", w.Code, w.Body, called)
			}
			var result protocol.CommandResult
			if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.RequestID != "r1" || result.InstanceID != "i1" {
				t.Fatalf("result: %s", w.Body)
			}
			if strings.Contains(w.Body.String(), room.Token) {
				t.Fatal("lease leaked through HTTP")
			}
		})
	}
}

func TestHTTPReadsAndValidation(t *testing.T) {
	s, _, _ := testHTTPService(t)
	h := testHTTPHandler(t, s)
	for _, path := range []string{"/api/v1/rooms", "/api/v1/rooms/bedroom", "/api/v1/rooms/bedroom/queue"} {
		w := serveHTTP(h, "GET", path, "", "")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	var calls atomic.Int32
	s.requester.(*testRequester).call = func(context.Context, string, []byte) (*nats.Msg, error) {
		calls.Add(1)
		return nil, errors.New("must not route")
	}
	for _, tt := range []struct {
		method, path, body, id string
		status                 int
	}{
		{"POST", "/api/v1/rooms/bedroom/commands", `{"request_id":"body","type":"playback.stop","payload":{}}`, "header", 400},
		{"POST", "/api/v1/rooms/bedroom/commands", `{"type":"playback.stop","payload":{}}`, "", 400},
		{"POST", "/api/v1/rooms/bedroom/commands", `{"type":"queue.clear","payload":{}}`, "r1", 400},
		{"POST", "/api/v1/rooms/bedroom/commands", `{"type":"playback.shuffle","payload":{}}`, "r1", 400},
		{"POST", "/api/v1/rooms/bedroom/commands", `{"type":"playback.stop","payload":null}`, "r1", 400},
		{"POST", "/api/v1/rooms/bedroom/commands", `{"type":"playback.stop","payload":{},"extra":1}`, "r1", 400},
		{"POST", "/api/v1/rooms/bedroom/queue", `{"track_id":"a"}]`, "r1", 400},
		{"POST", "/api/v1/rooms/bedroom/queue", `{}`, "r1", 400},
		{"POST", "/api/v1/rooms/bedroom/queue", strings.Repeat(" ", 65<<10), "r1", 400},
		{"DELETE", "/api/v1/rooms/bedroom/queue", `{}`, "r1", 400},
		{"POST", "/api/v1/rooms/bad.room/queue", `{"track_id":"a"}`, "r1", 400},
		{"PUT", "/api/v1/rooms/bedroom", "", "", 405},
		{"GET", "/api/v1/absent", "", "", 404},
	} {
		w := serveHTTP(h, tt.method, tt.path, tt.body, tt.id)
		if w.Code != tt.status {
			t.Errorf("%s %s: %d %s", tt.method, tt.path, w.Code, w.Body)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid requests reached NATS")
	}
	// Body-only IDs remain supported by the unified playback endpoint.
	s, _, _ = testHTTPService(t)
	w := serveHTTP(testHTTPHandler(t, s), "POST", "/api/v1/rooms/bedroom/commands", `{"request_id":"r1","type":"playback.stop","payload":{}}`, "")
	if w.Code != 200 {
		t.Fatalf("body-only request ID: %s", w.Body)
	}
}

func TestHTTPErrorMappingAndReplyFencing(t *testing.T) {
	for _, tt := range []struct {
		name   string
		err    error
		code   protocol.ErrorCode
		status int
	}{
		{"no responders", nats.ErrNoResponders, protocol.ErrNoResponders, 503},
		{"timeout", context.DeadlineExceeded, protocol.ErrCommandTimeout, 504},
		{"broker", nats.ErrDisconnected, protocol.ErrBrokerUnavailable, 503},
		{"canceled", context.Canceled, protocol.ErrServiceUnavailable, 503},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, _, _ := testHTTPService(t)
			var calls int
			s.requester.(*testRequester).call = func(context.Context, string, []byte) (*nats.Msg, error) { calls++; return nil, tt.err }
			w := serveHTTP(testHTTPHandler(t, s), "DELETE", "/api/v1/rooms/bedroom/queue", "", "r1")
			var result protocol.HTTPErrorResponse
			json.Unmarshal(w.Body.Bytes(), &result)
			if w.Code != tt.status || result.Error.Code != tt.code || result.RequestID != "r1" || calls != 1 {
				t.Fatalf("error mapping: %d %s calls=%d", w.Code, w.Body, calls)
			}
			if tt.code == protocol.ErrCommandTimeout && !strings.Contains(result.Error.Message, "unknown") {
				t.Fatal("timeout did not describe unknown outcome")
			}
		})
	}
	for _, fault := range []string{"instance", "token", "request", "state", "application", "replacement", "expected-instance", "missing", "offline", "disconnected"} {
		t.Run(fault, func(t *testing.T) {
			s, clock, room := testHTTPService(t)
			original := s.requester.(*testRequester).call
			s.requester.(*testRequester).call = func(ctx context.Context, subject string, data []byte) (*nats.Msg, error) {
				msg, _ := original(ctx, subject, data)
				var response protocol.CommandResponse
				json.Unmarshal(msg.Data, &response)
				switch fault {
				case "instance":
					response.InstanceID = "old"
				case "token":
					response.RegistrationToken = "old"
				case "request":
					response.RequestID = "other"
				case "state":
					response.State.Status = protocol.StatusPlaying
				case "application":
					response.Success = false
					response.State = nil
					response.Error = apiError(protocol.ErrInvalidState, "private dependency path")
				case "replacement":
					clock.Advance(HeartbeatTimeout)
					s.reg.Register(reqFor(room.ID, "i2"))
				}
				msg.Data, _ = json.Marshal(response)
				return msg, nil
			}
			path := "/api/v1/rooms/bedroom/queue"
			want := 503
			if fault == "application" {
				want = 409
			}
			if fault == "missing" {
				path = "/api/v1/rooms/missing/queue"
				want = 404
			}
			if fault == "offline" {
				clock.Advance(HeartbeatTimeout)
			}
			if fault == "disconnected" {
				s.requester.(*testRequester).online = false
			}
			r := httptest.NewRequest("DELETE", path, nil)
			r.Header.Set("X-Request-ID", "r1")
			if fault == "expected-instance" {
				r.Header.Set("X-Room-Instance-ID", "old")
			}
			w := httptest.NewRecorder()
			testHTTPHandler(t, s).ServeHTTP(w, r)
			if w.Code != want || strings.Contains(w.Body.String(), "private dependency path") {
				t.Fatalf("%s: %d %s", fault, w.Code, w.Body)
			}
		})
	}
}

func TestLibraryProxyValidatesDependency(t *testing.T) {
	for _, body := range []string{`{"tracks":[]}`, `{"tracks":null}`, `{"tracks":[]} {}`, `{"tracks":[{"id":"a","title":"A","artist":"B","duration_ms":-1,"url":"http://library/a"}]}`} {
		t.Run(body, func(t *testing.T) {
			lib := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/tracks" {
					t.Error(r.URL.Path)
				}
				io.WriteString(w, body)
			}))
			defer lib.Close()
			s, _, _ := testHTTPService(t)
			h, e := NewHTTPHandler(s, HTTPOptions{LibraryURL: lib.URL})
			if e != nil {
				t.Fatal(e)
			}
			w := serveHTTP(h, "GET", "/api/v1/tracks", "", "")
			want := 503
			if body == `{"tracks":[]}` {
				want = 200
			}
			if w.Code != want {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
		})
	}
}

func TestCacheRejectsStaleEventsAndIsolatesSnapshots(t *testing.T) {
	s, clock, room := testHTTPService(t)
	state := protocol.NewPlaybackState()
	state.Version = 2
	state.Queue = append(state.Queue, protocol.QueueItem{ID: "item", TrackID: "a"})
	event := protocol.StateEvent{RoomID: room.ID, InstanceID: room.InstanceID, RegistrationToken: room.Token, State: state}
	pub := func() {
		data, _ := json.Marshal(event)
		s.handleState(&nats.Msg{Subject: protocol.StateSubject(event.RoomID), Data: data})
	}
	pub()
	event.State.Version = 1
	pub()
	event.State.Version = 99
	event.RegistrationToken = "wrong"
	pub()
	got, _ := s.reg.Get(room.ID)
	if got.State == nil || got.State.Version != 2 {
		t.Fatalf("cache: %+v", got)
	}
	got.State.Queue[0].TrackID = "changed"
	got, _ = s.reg.Get(room.ID)
	if got.State.Queue[0].TrackID != "a" {
		t.Fatal("snapshot aliases cache")
	}
	clock.Advance(HeartbeatTimeout)
	s.reg.Register(reqFor(room.ID, "i2"))
	event.RegistrationToken = room.Token
	pub()
	got, _ = s.reg.Get(room.ID)
	if got.State != nil {
		t.Fatal("old event populated replacement cache")
	}
}

func TestSlowSSEClientIsDisconnected(t *testing.T) {
	h := NewEventHub()
	ch := make(chan streamEvent, 1)
	h.clients[ch] = struct{}{}
	h.Publish("room.state", map[string]int{"version": 1})
	h.Publish("room.state", map[string]int{"version": 2})
	if len(h.clients) != 0 {
		t.Fatal("slow client retained")
	}
	<-ch
	if _, ok := <-ch; ok {
		t.Fatal("slow channel remained open")
	}
	h.Close()
}

func TestRequestDeadlineIsBounded(t *testing.T) {
	s, _, _ := testHTTPService(t)
	s.RequestTimeout = 5 * time.Millisecond
	s.requester.(*testRequester).call = func(ctx context.Context, _ string, _ []byte) (*nats.Msg, error) { <-ctx.Done(); return nil, ctx.Err() }
	w := serveHTTP(testHTTPHandler(t, s), "DELETE", "/api/v1/rooms/bedroom/queue", "", "r1")
	if w.Code != 504 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestCacheStaysStaleUntilRecoverySnapshot(t *testing.T) {
	s, clock, room := testHTTPService(t)
	state := protocol.NewPlaybackState()
	if !s.reg.StoreState(room.ID, room.InstanceID, room.Token, &state, true) {
		t.Fatal("initial snapshot rejected")
	}
	clock.Advance(HeartbeatTimeout)
	s.reg.Sweep()
	s.reg.Register(reqFor(room.ID, room.InstanceID))
	if !s.Rooms().Rooms[0].StateStale {
		t.Fatal("renewal advertised old cache as fresh")
	}
	if !s.reg.StoreState(room.ID, room.InstanceID, room.Token, &state, true) || s.Rooms().Rooms[0].StateStale {
		t.Fatal("recovery query did not refresh cache")
	}
	s.reg.MarkStatesStale()
	if !s.Rooms().Rooms[0].StateStale {
		t.Fatal("broker outage did not stale cache")
	}
}

func TestDifferentRoomRequestsProgressIndependently(t *testing.T) {
	s, _, room := testHTTPService(t)
	s.reg.Register(reqFor("slow", "i2"))
	slow, _ := s.reg.Get("slow")
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 1)
	s.requester.(*testRequester).call = func(ctx context.Context, subject string, data []byte) (*nats.Msg, error) {
		target := room
		if subject == protocol.CommandSubject("slow") {
			target = slow
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		var req protocol.CommandRequest
		json.Unmarshal(data, &req)
		state := protocol.NewPlaybackState()
		out, _ := json.Marshal(protocol.CommandResponse{RequestID: req.RequestID, InstanceID: target.InstanceID, RegistrationToken: target.Token, Success: true, State: &state})
		return &nats.Msg{Data: out}, nil
	}
	h := testHTTPHandler(t, s)
	go func() { done <- serveHTTP(h, "DELETE", "/api/v1/rooms/slow/queue", "", "slow") }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("slow request never started")
	}
	fast := serveHTTP(h, "DELETE", "/api/v1/rooms/bedroom/queue", "", "fast")
	select {
	case <-done:
		t.Error("slow request completed before it was released")
	default:
	}
	close(release)
	if fast.Code != 200 {
		t.Fatal(fast.Body)
	}
	select {
	case response := <-done:
		if response.Code != 200 {
			t.Fatal(response.Body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow request stuck")
	}
}

func TestEveryPlayerErrorHasItsHTTPStatus(t *testing.T) {
	for _, tt := range []struct {
		code   protocol.ErrorCode
		status int
	}{
		{protocol.ErrInvalidCommand, 400}, {protocol.ErrInvalidPayload, 400}, {protocol.ErrTrackNotFound, 404}, {protocol.ErrQueueItemNotFound, 404}, {protocol.ErrInvalidState, 409}, {protocol.ErrDuplicateRequestConflict, 409}, {protocol.ErrAudio, 500}, {protocol.ErrInternal, 500},
	} {
		t.Run(string(tt.code), func(t *testing.T) {
			s, _, room := testHTTPService(t)
			s.requester.(*testRequester).call = func(_ context.Context, _ string, data []byte) (*nats.Msg, error) {
				var req protocol.CommandRequest
				json.Unmarshal(data, &req)
				out, _ := json.Marshal(protocol.CommandResponse{RequestID: req.RequestID, InstanceID: room.InstanceID, RegistrationToken: room.Token, Error: apiError(tt.code, "private details")})
				return &nats.Msg{Data: out}, nil
			}
			w := serveHTTP(testHTTPHandler(t, s), "DELETE", "/api/v1/rooms/bedroom/queue", "", "r1")
			if w.Code != tt.status || strings.Contains(w.Body.String(), "private details") {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
		})
	}
}

func TestInvalidPlaybackStatesCannotEnterCache(t *testing.T) {
	for _, mutate := range []func(*protocol.PlaybackState){
		func(s *protocol.PlaybackState) { s.Status = "unknown" }, func(s *protocol.PlaybackState) { s.Status = protocol.StatusPlaying }, func(s *protocol.PlaybackState) { s.PositionMs = -1 }, func(s *protocol.PlaybackState) { s.PositionMs = 1 }, func(s *protocol.PlaybackState) { s.History = nil }, func(s *protocol.PlaybackState) {
			s.Queue = []protocol.QueueItem{{ID: "same", TrackID: "a"}, {ID: "same", TrackID: "a"}}
		},
	} {
		s, _, room := testHTTPService(t)
		state := protocol.NewPlaybackState()
		mutate(&state)
		if s.reg.StoreState(room.ID, room.InstanceID, room.Token, &state, true) {
			t.Fatal("invalid state accepted")
		}
	}
}

func TestSSEClientCancellationCleansUp(t *testing.T) {
	hub := NewEventHub()
	finished := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hub.ServeHTTP(w, r); close(finished) }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	response.Body.Close()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE handler did not exit")
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(hub.clients) != 0 {
		t.Fatal("disconnected SSE client retained")
	}
}

func TestRecoveryQueryPublishesFreshnessAtSameVersion(t *testing.T) {
	s, _, room := testHTTPService(t)
	ch := make(chan streamEvent, 4)
	s.events.clients[ch] = struct{}{}
	state := protocol.NewPlaybackState()
	s.storeAndPublish(room.ID, room.InstanceID, room.Token, &state, true)
	<-ch
	s.reg.MarkStatesStale()
	s.storeAndPublish(room.ID, room.InstanceID, room.Token, &state, true)
	select {
	case event := <-ch:
		if event.name != "room.state" {
			t.Fatal(event.name)
		}
	default:
		t.Fatal("recovery snapshot did not notify browser")
	}
	if s.Rooms().Rooms[0].StateStale {
		t.Fatal("recovered cache remained stale")
	}
}

func TestClosedSSEHubReturnsStructuredError(t *testing.T) {
	hub := NewEventHub()
	hub.Close()
	w := httptest.NewRecorder()
	hub.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/events", nil))
	var response protocol.HTTPErrorResponse
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || w.Code != 503 || response.Error.Code != protocol.ErrServiceUnavailable {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
