//go:build process

package integration_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/pkz074/RoomPlay/internal/protocol"
)

type processCheck struct {
	Name   string    `json:"name"`
	At     time.Time `json:"at"`
	Detail any       `json:"detail,omitempty"`
}
type ownedProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func (p *ownedProcess) kill() {
	select {
	case <-p.done:
		return
	default:
	}
	_ = p.cmd.Process.Kill()
	<-p.done
}

type processRig struct {
	t                                                                  *testing.T
	root, bin, evidence, libraryURL, base, natsURL, catalog, audioRoot string
	brokerPort                                                         int
	processes                                                          map[string]*ownedProcess
	runs                                                               map[string]int
	checks                                                             []processCheck
	client                                                             *http.Client
	counter                                                            atomic.Uint64
	requests                                                           *os.File
}

func newProcessRig(t *testing.T) *processRig {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	evidence := os.Getenv("ROOMPLAY_EVIDENCE_DIR")
	if evidence == "" {
		evidence = t.TempDir()
	}
	if err := os.MkdirAll(evidence, 0700); err != nil {
		t.Fatal(err)
	}
	bin := os.Getenv("ROOMPLAY_PROCESS_BIN_DIR")
	if bin == "" {
		bin = filepath.Join(t.TempDir(), "bin")
	}
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	r := &processRig{t: t, root: root, bin: bin, evidence: evidence, processes: map[string]*ownedProcess{}, runs: map[string]int{}, client: &http.Client{Timeout: 4 * time.Second}}
	r.requests, err = os.Create(filepath.Join(evidence, "http-requests.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, p := range r.processes {
			p.kill()
		}
		r.requests.Close()
		r.save()
	})
	for _, target := range []struct{ name, pkg string }{{"coordinator", "./cmd/coordinator"}, {"library", "./cmd/library"}, {"player", "./cmd/player"}, {"nats-server", "github.com/nats-io/nats-server/v2"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		args := []string{"build"}
		if os.Getenv("ROOMPLAY_PROCESS_RACE") == "1" {
			args = append(args, "-race")
		}
		args = append(args, "-o", r.executable(target.name), target.pkg)
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = root
		hideTestProcess(cmd)
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("build %s: %v\n%s", target.name, err, output)
		}
	}
	ports := map[int]bool{}
	port := func() int {
		for {
			l, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			p := l.Addr().(*net.TCPAddr).Port
			l.Close()
			if !ports[p] {
				ports[p] = true
				return p
			}
		}
	}
	r.brokerPort = port()
	r.natsURL = fmt.Sprintf("nats://127.0.0.1:%d", r.brokerPort)
	r.libraryURL = fmt.Sprintf("http://127.0.0.1:%d", port())
	r.base = fmt.Sprintf("http://127.0.0.1:%d", port())
	r.prepareCatalog()
	r.note("environment", map[string]any{"go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "coordinator": r.base, "library": r.libraryURL, "broker": r.natsURL, "audio_fixture": "two catalog IDs reference copies of the same licensed recording"})
	r.startBroker()
	r.startLibrary()
	r.wait("library ready", func() bool {
		response, e := r.client.Get(r.libraryURL + "/tracks")
		if e != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == 200
	})
	r.startCoordinator()
	for _, room := range []string{"bedroom", "kitchen", "livingroom"} {
		r.startPlayer(room, "simulation")
	}
	r.waitRooms()
	return r
}

func (r *processRig) executable(name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(r.bin, name)
}
func (r *processRig) start(name, binary string, args []string, env map[string]string) *ownedProcess {
	r.t.Helper()
	r.runs[name]++
	file, err := os.Create(filepath.Join(r.evidence, fmt.Sprintf("%s-%d.log", name, r.runs[name])))
	if err != nil {
		r.t.Fatal(err)
	}
	cmd := exec.Command(r.executable(binary), args...)
	cmd.Dir = r.root
	cmd.Stdout = file
	cmd.Stderr = file
	hideTestProcess(cmd)
	settings := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			settings[key] = value
		}
	}
	for key, value := range env {
		settings[key] = value
	}
	for key, value := range settings {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if err := cmd.Start(); err != nil {
		file.Close()
		r.t.Fatal(err)
	}
	p := &ownedProcess{cmd: cmd, done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); file.Close(); close(p.done) }()
	// Register after fixture TempDirs so processes release their handles before
	// testing removes the catalog directory (particularly important on Windows).
	r.t.Cleanup(func() {
		p.kill()
		data, err := os.ReadFile(file.Name())
		if err != nil {
			r.t.Errorf("read child log: %v", err)
		} else if strings.Contains(string(data), "WARNING: DATA RACE") {
			r.t.Errorf("race detected in child process %s; see %s", name, file.Name())
		}
	})
	r.processes[name] = p
	r.note("process started", map[string]any{"name": name, "pid": cmd.Process.Pid, "run": r.runs[name]})
	return p
}
func (r *processRig) startBroker() {
	r.start("broker", "nats-server", []string{"-a", "127.0.0.1", "-p", fmt.Sprint(r.brokerPort)}, nil)
	r.wait("broker ready", func() bool {
		conn, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", r.brokerPort), 100*time.Millisecond)
		if e != nil {
			return false
		}
		conn.Close()
		return true
	})
}
func (r *processRig) startLibrary() {
	r.start("library", "library", nil, map[string]string{"LIBRARY_ADDR": strings.TrimPrefix(r.libraryURL, "http://"), "LIBRARY_URL": r.libraryURL, "LIBRARY_CATALOG": r.catalog, "LIBRARY_AUDIO_ROOT": r.audioRoot})
}
func (r *processRig) startCoordinator() {
	r.start("coordinator", "coordinator", nil, map[string]string{"COORDINATOR_ADDR": strings.TrimPrefix(r.base, "http://"), "LIBRARY_URL": r.libraryURL, "NATS_URL": r.natsURL, "WEB_ROOT": filepath.Join(r.root, "web")})
}
func (r *processRig) startPlayer(room, mode string) *ownedProcess {
	return r.start(room, "player", nil, map[string]string{"ROOM_ID": room, "ROOM_NAME": room, "NATS_URL": r.natsURL, "LIBRARY_URL": r.libraryURL, "PLAYER_MODE": mode})
}
func (r *processRig) kill(name string) { r.processes[name].kill(); r.note("process killed", name) }
func (r *processRig) wait(name string, check func() bool) {
	r.t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if check() {
			return
		}
		select {
		case <-deadline.C:
			r.t.Fatalf("timed out: %s; logs: %s", name, r.evidence)
		case <-tick.C:
		}
	}
}
func (r *processRig) note(name string, detail any) {
	r.checks = append(r.checks, processCheck{name, time.Now().UTC(), detail})
	r.t.Logf("%s: %v", name, detail)
	r.save()
}
func (r *processRig) save() {
	data, _ := json.MarshalIndent(map[string]any{"checks": r.checks, "failed": r.t.Failed()}, "", "  ")
	_ = os.WriteFile(filepath.Join(r.evidence, "results.json"), data, 0600)
}
func (r *processRig) prepareCatalog() {
	r.t.Helper()
	data, err := os.ReadFile(filepath.Join(r.root, "assets", "music", "catalog.json"))
	if err != nil {
		r.t.Fatal(err)
	}
	var catalog struct {
		Tracks []map[string]any `json:"tracks"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil || len(catalog.Tracks) != 1 {
		r.t.Fatal("expected one licensed catalog recording")
	}
	source := catalog.Tracks[0]
	audio, err := os.ReadFile(filepath.Join(r.root, "assets", "music", source["audio_file"].(string)))
	if err != nil {
		r.t.Fatal(err)
	}
	r.audioRoot = filepath.Join(r.t.TempDir(), "audio")
	if err := os.MkdirAll(r.audioRoot, 0700); err != nil {
		r.t.Fatal(err)
	}
	tracks := []map[string]any{}
	for _, id := range []string{"track-a", "track-b"} {
		entry := map[string]any{}
		for key, value := range source {
			entry[key] = value
		}
		entry["id"] = id
		entry["title"] = source["title"].(string) + " [" + id + " routing fixture]"
		entry["audio_file"] = id + ".mp3"
		if err := os.WriteFile(filepath.Join(r.audioRoot, id+".mp3"), audio, 0600); err != nil {
			r.t.Fatal(err)
		}
		tracks = append(tracks, entry)
	}
	data, _ = json.Marshal(map[string]any{"tracks": tracks})
	r.catalog = filepath.Join(r.audioRoot, "catalog.json")
	if err := os.WriteFile(r.catalog, data, 0600); err != nil {
		r.t.Fatal(err)
	}
	hash := sha256.Sum256(audio)
	r.note("licensed audio fixture", map[string]any{"sha256": hex.EncodeToString(hash[:]), "source": source["source"], "license": source["license"], "same_recording": true})
}
func (r *processRig) request(method, path, body, id string) (status int, data []byte, err error) {
	started := time.Now()
	defer func() {
		var result any
		if json.Valid(data) {
			result = json.RawMessage(data)
		} else {
			result = string(data)
		}
		failure := ""
		if err != nil {
			failure = err.Error()
		}
		entry, marshalErr := json.Marshal(map[string]any{"at": started.UTC(), "elapsed_ms": time.Since(started).Milliseconds(), "method": method, "path": path, "request_id": id, "request_body": body, "status": status, "response": result, "transport_error": failure})
		if marshalErr != nil {
			r.t.Error(marshalErr)
			return
		}
		// os.File supports concurrent methods; keep each JSONL record in one write.
		if _, writeErr := r.requests.Write(append(entry, '\n')); writeErr != nil {
			r.t.Error(writeErr)
		}
	}()
	request, err := http.NewRequest(method, r.base+path, strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if id != "" {
		request.Header.Set("X-Request-ID", id)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, err = io.ReadAll(response.Body)
	return response.StatusCode, data, err
}
func (r *processRig) detail(room string) (protocol.RoomSummary, error) {
	status, data, err := r.request("GET", "/api/v1/rooms/"+room, "", "")
	var result protocol.RoomSummary
	if err != nil {
		return result, err
	}
	if status != 200 {
		return result, fmt.Errorf("HTTP %d: %s", status, data)
	}
	err = json.Unmarshal(data, &result)
	return result, err
}
func (r *processRig) waitRooms() {
	r.wait("three registered fresh rooms", func() bool {
		for _, id := range []string{"bedroom", "kitchen", "livingroom"} {
			room, e := r.detail(id)
			if e != nil || room.Availability != protocol.RoomOnline || room.StateStale || room.State == nil {
				return false
			}
		}
		return true
	})
	r.note("three rooms online", nil)
}
func (r *processRig) mutation(method, path, body, id string) protocol.CommandResult {
	r.t.Helper()
	if id == "" {
		id = fmt.Sprintf("p5-%d", r.counter.Add(1))
	}
	status, data, err := r.request(method, path, body, id)
	if err != nil || status != 200 {
		r.t.Fatalf("%s %s: status=%d err=%v body=%s", method, path, status, err, data)
	}
	var result protocol.CommandResult
	if json.Unmarshal(data, &result) != nil || result.RequestID != id {
		r.t.Fatalf("invalid mutation reply: %s", data)
	}
	return result
}
func (r *processRig) command(room string, kind protocol.CommandType, payload string) protocol.CommandResult {
	body := fmt.Sprintf(`{"type":%q,"payload":%s}`, kind, payload)
	return r.mutation("POST", "/api/v1/rooms/"+room+"/commands", body, "")
}

type processSSE struct {
	events chan map[string]any
	closed chan struct{}
	cancel context.CancelFunc
	body   io.ReadCloser
}

func (r *processRig) stream() *processSSE {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, "GET", r.base+"/api/v1/events", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancel()
		r.t.Fatal(err)
	}
	if response.StatusCode != 200 {
		response.Body.Close()
		cancel()
		r.t.Fatal("SSE unavailable")
	}
	s := &processSSE{make(chan map[string]any, 128), make(chan struct{}), cancel, response.Body}
	go func() {
		defer close(s.closed)
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				var event map[string]any
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) == nil {
					select {
					case s.events <- event:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	r.t.Cleanup(s.close)
	return s
}
func (s *processSSE) close() { s.cancel(); s.body.Close() }
func (r *processRig) waitEvent(s *processSSE, match func(map[string]any) bool) map[string]any {
	r.t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-s.events:
			if match(event) {
				return event
			}
		case <-deadline.C:
			r.t.Fatal("SSE event not observed")
		case <-s.closed:
			r.t.Fatal("SSE disconnected unexpectedly")
		}
	}
}

func TestProcessDistributedLifecycle(t *testing.T) {
	r := newProcessRig(t)
	stream := r.stream()
	bed := r.command("bedroom", protocol.PlaybackPlay, `{"track_id":"track-a"}`)
	kitchen := r.command("kitchen", protocol.PlaybackPlay, `{"track_id":"track-b"}`)
	living, err := r.detail("livingroom")
	if err != nil || living.State.Version != 0 || bed.State.Current.TrackID == kitchen.State.Current.TrackID {
		t.Fatal("room playback isolation failed")
	}
	r.waitEvent(stream, func(event map[string]any) bool { return event["room_id"] == "bedroom" && event["state"] != nil })
	r.note("independent room selections and live SSE", map[string]any{"bedroom": bed, "kitchen": kitchen})
	added := r.mutation("POST", "/api/v1/rooms/bedroom/queue", `{"track_id":"track-b"}`, "queue-1")
	duplicate := r.mutation("POST", "/api/v1/rooms/bedroom/queue", `{"track_id":"track-b"}`, "queue-1")
	if duplicate.State.Version != added.State.Version || len(duplicate.State.Queue) != 1 {
		t.Fatal("duplicate request executed twice")
	}
	status, data, err := r.request("DELETE", "/api/v1/rooms/bedroom/queue", "", "queue-1")
	if err != nil || status != 409 {
		t.Fatalf("request-ID conflict: %d %v %s", status, err, data)
	}
	const callers = 16
	results := make(chan error, callers)
	for range callers {
		go func() {
			status, data, err := r.request("POST", "/api/v1/rooms/livingroom/queue", `{"track_id":"track-a"}`, "parallel-same-id")
			if err == nil && status != 200 {
				err = fmt.Errorf("HTTP %d: %s", status, data)
			}
			results <- err
		}()
	}
	for range callers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	living, err = r.detail("livingroom")
	if err != nil || len(living.State.Queue) != 1 {
		t.Fatal("concurrent duplicate mutation failed")
	}
	r.note("serial and concurrent request deduplication", living)
	// Removing one occurrence of a repeated song must retain the other one.
	second := r.mutation("POST", "/api/v1/rooms/bedroom/queue", `{"track_id":"track-b"}`, "")
	removed := r.mutation("DELETE", "/api/v1/rooms/bedroom/queue/"+second.State.Queue[1].ID, "", "")
	if len(removed.State.Queue) != 1 || removed.State.Queue[0].ID != added.State.Queue[0].ID {
		t.Fatal("queue removal removed the wrong occurrence")
	}
	// Distinct IDs must all serialize without lost updates; different rooms can
	// accept commands concurrently through the same coordinator.
	for i := range callers {
		go func() {
			room := "livingroom"
			if i%2 == 0 {
				room = "kitchen"
			}
			status, data, err := r.request("POST", "/api/v1/rooms/"+room+"/queue", `{"track_id":"track-a"}`, fmt.Sprintf("parallel-unique-%d", i))
			if err == nil && status != 200 {
				err = fmt.Errorf("HTTP %d: %s", status, data)
			}
			results <- err
		}()
	}
	for range callers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	living, err = r.detail("livingroom")
	parallelKitchen, kitchenErr := r.detail("kitchen")
	if err != nil || kitchenErr != nil || len(living.State.Queue) != 1+callers/2 || len(parallelKitchen.State.Queue) != callers/2 {
		t.Fatal("parallel distinct mutations lost updates")
	}
	r.note("concurrent same-room and different-room distinct commands", map[string]any{"livingroom": living, "kitchen": parallelKitchen})
	paused := r.command("bedroom", protocol.PlaybackPause, `{}`)
	detail, _ := r.detail("bedroom")
	if paused.State.Status != protocol.StatusPaused || detail.State.PositionMs != paused.State.PositionMs {
		t.Fatal("pause did not freeze position")
	}
	r.command("bedroom", protocol.PlaybackResume, `{}`)
	next := r.command("bedroom", protocol.PlaybackNext, `{}`)
	previous := r.command("bedroom", protocol.PlaybackPrevious, `{}`)
	again := r.command("bedroom", protocol.PlaybackNext, `{}`)
	if previous.State.Current.ID != bed.State.Current.ID || again.State.Current.ID != next.State.Current.ID {
		t.Fatal("previous/next did not preserve occurrences")
	}
	stopped := r.command("bedroom", protocol.PlaybackStop, `{}`)
	cleared := r.mutation("DELETE", "/api/v1/rooms/bedroom/queue", "", "")
	if stopped.State.PositionMs != 0 || cleared.State.Current.ID != stopped.State.Current.ID || len(cleared.State.History) != len(stopped.State.History) {
		t.Fatal("stop/clear damaged current or history")
	}
	r.note("playback and queue navigation", cleared)
	stream.close()
	missed := r.mutation("POST", "/api/v1/rooms/bedroom/queue", `{"track_id":"track-b"}`, "")
	reconnectedStream := r.stream()
	snapshot, err := r.detail("bedroom")
	if err != nil || snapshot.State.Version < missed.State.Version || len(snapshot.State.Queue) != len(missed.State.Queue) {
		t.Fatal("REST snapshot did not recover the missed SSE update")
	}
	r.mutation("POST", "/api/v1/rooms/bedroom/queue", `{"track_id":"track-a"}`, "")
	r.waitEvent(reconnectedStream, func(event map[string]any) bool { return event["room_id"] == "bedroom" && event["state"] != nil })
	r.note("SSE reconnect and authoritative snapshot", nil)
	// Duplicate OS process must exit without interfering with the registered room.
	r.start("duplicate", "player", nil, map[string]string{"ROOM_ID": "kitchen", "ROOM_NAME": "Duplicate", "NATS_URL": r.natsURL, "LIBRARY_URL": r.libraryURL, "PLAYER_MODE": "simulation"})
	dup := r.processes["duplicate"]
	select {
	case <-dup.done:
		if dup.err == nil {
			t.Fatal("duplicate process unexpectedly succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("duplicate process did not exit")
	}
	r.note("duplicate player process rejected", nil)
	beforeKitchen, _ := r.detail("kitchen")
	start := time.Now()
	r.kill("kitchen")
	status, data, err = r.request("POST", "/api/v1/rooms/kitchen/queue", `{"track_id":"track-a"}`, "dead-no-responder")
	var missingResponder protocol.HTTPErrorResponse
	if err != nil || json.Unmarshal(data, &missingResponder) != nil || status != 503 || missingResponder.Error.Code != protocol.ErrNoResponders {
		t.Fatalf("dead registered player status=%d error=%v body=%s", status, err, data)
	}
	r.wait("kitchen offline", func() bool {
		room, e := r.detail("kitchen")
		return e == nil && room.Availability == protocol.RoomOffline && room.StateStale
	})
	r.note("player crash detected", map[string]any{"elapsed_ms": time.Since(start).Milliseconds()})
	r.command("bedroom", protocol.PlaybackPlay, `{"track_id":"track-a"}`)
	r.startPlayer("kitchen", "simulation")
	r.waitRooms()
	afterKitchen, _ := r.detail("kitchen")
	if afterKitchen.InstanceID == beforeKitchen.InstanceID || afterKitchen.State.Status != protocol.StatusStopped || len(afterKitchen.State.Queue) != 0 {
		t.Fatal("player restart did not establish fresh state")
	}
	r.note("player restart resets local state", afterKitchen)
	beforeBed, _ := r.detail("bedroom")
	r.kill("coordinator")
	r.startCoordinator()
	r.waitRooms()
	afterBed, _ := r.detail("bedroom")
	if afterBed.InstanceID != beforeBed.InstanceID || afterBed.State.Current.ID != beforeBed.State.Current.ID || afterBed.State.PositionMs <= beforeBed.State.PositionMs {
		t.Fatal("coordinator restart interrupted player playback")
	}
	r.note("coordinator restart preserves playback", afterBed)
	nc, err := nats.Connect(r.natsURL, nats.MaxReconnects(-1), nats.ReconnectWait(20*time.Millisecond), nats.ReconnectBufSize(-1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	// Verify stale publications cannot poison a fresh process snapshot.
	staleState := *afterBed.State
	staleState.Version += 1000
	payload, _ := json.Marshal(protocol.StateEvent{RoomID: "bedroom", InstanceID: "retired", RegistrationToken: "old", State: staleState})
	nc.Publish(protocol.StateSubject("bedroom"), payload)
	staleHeartbeat, _ := json.Marshal(protocol.Heartbeat{RoomID: "kitchen", InstanceID: beforeKitchen.InstanceID, RegistrationToken: "retired-token"})
	nc.Publish(protocol.SubjectHeartbeat, staleHeartbeat)
	nc.FlushTimeout(time.Second)
	current, _ := r.detail("bedroom")
	if current.State.Version >= staleState.Version {
		t.Fatal("stale state event accepted")
	}
	r.note("retired instance event rejected", nil)
	currentKitchen, err := r.detail("kitchen")
	if err != nil || currentKitchen.InstanceID != afterKitchen.InstanceID || currentKitchen.Availability != protocol.RoomOnline {
		t.Fatal("retired heartbeat changed the replacement room")
	}
	// A real responder receives and records the mutation but deliberately loses its reply.
	ghostRequest, _ := json.Marshal(protocol.RegistrationRequest{RoomID: "silent", Name: "Silent", InstanceID: "s1"})
	_, err = nc.Request(protocol.SubjectRegister, ghostRequest, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var silentCalls atomic.Int32
	sub, err := nc.Subscribe(protocol.CommandSubject("silent"), func(*nats.Msg) { silentCalls.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	if err := nc.FlushTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	type httpOutcome struct {
		status int
		data   []byte
		err    error
	}
	slowReply := make(chan httpOutcome, 1)
	go func() {
		status, data, err := r.request("DELETE", "/api/v1/rooms/silent/queue", "", "timeout-original-id")
		slowReply <- httpOutcome{status, data, err}
	}()
	r.wait("silent responder received the command", func() bool { return silentCalls.Load() == 1 })
	// Another room must finish while the silent room is still awaiting a reply.
	r.mutation("POST", "/api/v1/rooms/livingroom/queue", `{"track_id":"track-b"}`, "")
	select {
	case reply := <-slowReply:
		t.Fatalf("other room stalled until silent request finished: HTTP %d %s", reply.status, reply.data)
	default:
	}
	reply := <-slowReply
	status, data, err = reply.status, reply.data, reply.err
	var timeout protocol.HTTPErrorResponse
	json.Unmarshal(data, &timeout)
	if err != nil || status != 504 || timeout.Error.Code != protocol.ErrCommandTimeout || silentCalls.Load() != 1 || !strings.Contains(timeout.Error.Message, "unknown") {
		t.Fatalf("timeout handling: %d %v %s calls=%d", status, err, data, silentCalls.Load())
	}
	sub.Unsubscribe()
	r.note("timeout is unknown outcome without retry", timeout)
	beforeBed, _ = r.detail("bedroom")
	r.kill("broker")
	r.wait("broker unavailability exposed", func() bool {
		status, data, _ := r.request("POST", "/api/v1/rooms/bedroom/queue", `{"track_id":"track-b"}`, "broker-outage")
		var failure protocol.HTTPErrorResponse
		json.Unmarshal(data, &failure)
		return status == 503 && failure.Error.Code == protocol.ErrBrokerUnavailable
	})
	r.startBroker()
	r.waitRooms()
	afterBed, _ = r.detail("bedroom")
	if afterBed.InstanceID != beforeBed.InstanceID || afterBed.State.Current.ID != beforeBed.State.Current.ID || afterBed.State.Version != beforeBed.State.Version || afterBed.State.PositionMs < beforeBed.State.PositionMs {
		t.Fatal("broker restart replayed mutation or lost playback")
	}
	r.note("broker restart and no command replay", afterBed)
	var tracks protocol.TracksResponse
	status, data, err = r.request("GET", "/api/v1/tracks", "", "")
	json.Unmarshal(data, &tracks)
	if err != nil || status != 200 || len(tracks.Tracks) != 2 {
		t.Fatal("catalog proxy failed")
	}
	audio, err := r.client.Get(tracks.Tracks[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := io.ReadAll(audio.Body)
	audio.Body.Close()
	if err != nil || audio.StatusCode != 200 || audio.Header.Get("Content-Type") != "audio/mpeg" || len(bytes) == 0 {
		t.Fatal("audio delivery failed")
	}
	r.note("actual library MP3 delivery", map[string]any{"bytes": len(bytes)})
	for path, want := range map[string]int{"/audio/not-listed.mp3": 404, "/audio/..%2fsecret.mp3": 400, "/audio/%2e%2e%5csecret.mp3": 400} {
		response, err := r.client.Get(r.libraryURL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("audio allowlist %s: %d", path, response.StatusCode)
		}
	}
	// The process must refuse a catalog whose required licensing is missing.
	invalidCatalog := filepath.Join(t.TempDir(), "unlicensed.json")
	catalogBytes, err := os.ReadFile(r.catalog)
	if err != nil {
		t.Fatal(err)
	}
	var unlicensed map[string][]map[string]any
	if err := json.Unmarshal(catalogBytes, &unlicensed); err != nil {
		t.Fatal(err)
	}
	delete(unlicensed["tracks"][0], "license")
	catalogBytes, _ = json.Marshal(unlicensed)
	if err := os.WriteFile(invalidCatalog, catalogBytes, 0600); err != nil {
		t.Fatal(err)
	}
	invalid := r.start("unlicensed-library", "library", nil, map[string]string{"LIBRARY_CATALOG": invalidCatalog, "LIBRARY_AUDIO_ROOT": r.audioRoot, "LIBRARY_ADDR": "127.0.0.1:0", "LIBRARY_URL": r.libraryURL})
	select {
	case <-invalid.done:
		if invalid.err == nil {
			t.Fatal("library accepted unlicensed catalog")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unlicensed library did not reject startup")
	}
	r.note("library allowlist, traversal and license rejection", nil)
	if os.Getenv("ROOMPLAY_AUDIO_SMOKE") == "1" {
		r.audioSmoke()
	}
	if ready := os.Getenv("ROOMPLAY_BROWSER_READY_FILE"); ready != "" {
		data, _ := json.Marshal(map[string]any{"url": r.base, "library": r.libraryURL, "broker": r.natsURL, "evidence": r.evidence})
		os.WriteFile(ready, data, 0600)
		r.t.Logf("BROWSER_READY %s", r.base)
		deadline := time.NewTimer(10 * time.Minute)
		defer deadline.Stop()
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			if _, err := os.Stat(ready + ".continue"); err == nil {
				break
			}
			select {
			case <-deadline.C:
				t.Fatal("browser inspection deadline elapsed")
			case <-tick.C:
			}
		}
	}
	r.note("separate-process lifecycle suite completed", nil)
}

func (r *processRig) audioSmoke() {
	r.t.Helper()
	p := r.startPlayer("audio-check", "audio")
	r.wait("audio device initialization", func() bool {
		select {
		case <-p.done:
			r.t.Fatalf("audio process exited: %v; see audio-check log", p.err)
		default:
		}
		room, e := r.detail("audio-check")
		return e == nil && room.Availability == protocol.RoomOnline
	})
	status, data, err := r.request("POST", "/api/v1/rooms/audio-check/commands", `{"type":"playback.play","payload":{"track_id":"track-a"}}`, "audio-play")
	if err != nil || status != 200 && status != 504 {
		r.t.Fatalf("audio play: %d %v %s", status, err, data)
	}
	r.wait("audio playing", func() bool {
		room, e := r.detail("audio-check")
		return e == nil && room.State != nil && room.State.Status == protocol.StatusPlaying && room.State.PositionMs >= 3000
	})
	r.command("audio-check", protocol.PlaybackStop, `{}`)
	r.kill("audio-check")
	r.note("hardware backend initialized and played three seconds", map[string]any{"human_audibility_confirmed": false})
}
