//go:build process

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Exercise the same entry point as `go run ./cmd/demo`, including real builds,
// all six background services, readiness and cancellation cleanup.
func TestProcessDemoLifecycle(t *testing.T) {
	t.Chdir("../..")
	ports := []int{}
	for len(ports) < 3 {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		duplicate := false
		for _, existing := range ports {
			duplicate = duplicate || existing == port
		}
		if !duplicate {
			ports = append(ports, port)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	var runError error
	simulation := os.Getenv("ROOMPLAY_DEMO_AUDIO_TEST") != "1"
	go func() { runError = run(ctx, simulation, true, ports[0], ports[1], ports[2]); close(finished) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(15 * time.Second):
			t.Error("launcher cleanup did not finish")
		}
	})
	client := &http.Client{Timeout: time.Second}
	deadline := time.NewTimer(3 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-finished:
			t.Fatalf("launcher exited before readiness: %v", runError)
		case <-deadline.C:
			t.Fatal("launcher did not become ready")
		case <-ticker.C:
		}
		response, err := client.Get("http://" + address(ports[0]) + "/api/v1/rooms")
		if err != nil {
			continue
		}
		var result struct {
			Rooms []struct {
				Availability string
				StateStale   bool `json:"state_stale"`
			}
		}
		decodeError := json.NewDecoder(response.Body).Decode(&result)
		response.Body.Close()
		fresh := len(result.Rooms) == 3
		for _, room := range result.Rooms {
			fresh = fresh && room.Availability == "online" && !room.StateStale
		}
		if response.StatusCode == 200 && decodeError == nil && fresh {
			break
		}
	}
	if !simulation {
		verifyConcurrentAudio(t, "http://"+address(ports[0]))
	}
	cancel()
	select {
	case <-finished:
		if !errors.Is(runError, context.Canceled) {
			t.Fatalf("shutdown: %v", runError)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("launcher shutdown timed out")
	}
	for _, port := range ports {
		conn, err := net.DialTimeout("tcp", address(port), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			t.Fatalf("owned service still listening on %d", port)
		}
	}
}

func verifyConcurrentAudio(t *testing.T, base string) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	rooms := []string{"bedroom", "kitchen", "livingroom"}
	for _, id := range rooms {
		body := fmt.Sprintf(`{"request_id":"demo-audio-%s","type":"playback.play","payload":{"track_id":"track-001"}}`, id)
		response, err := client.Post(base+"/api/v1/rooms/"+id+"/commands", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 && response.StatusCode != 504 {
			t.Fatalf("%s audio Play HTTP %d", id, response.StatusCode)
		}
	}
	type state struct {
		Status     string
		PositionMs int64 `json:"position_ms"`
	}
	read := func(id string) state {
		t.Helper()
		response, err := client.Get(base + "/api/v1/rooms/" + id)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var room struct{ State state }
		if err := json.NewDecoder(response.Body).Decode(&room); err != nil || response.StatusCode != 200 {
			t.Fatalf("%s state: HTTP %d, %v", id, response.StatusCode, err)
		}
		return room.State
	}
	before := map[string]state{}
	wait := func(name string, check func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal(name)
	}
	wait("all three audio players did not advance concurrently", func() bool {
		playing := true
		for _, id := range rooms {
			before[id] = read(id)
			playing = playing && before[id].Status == "playing" && before[id].PositionMs >= 3000
		}
		return playing
	})
	response, err := client.Post(base+"/api/v1/rooms/kitchen/commands", "application/json", strings.NewReader(`{"request_id":"pause-kitchen","type":"playback.pause","payload":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("pause Kitchen HTTP %d", response.StatusCode)
	}
	paused := read("kitchen")
	wait("pausing Kitchen interrupted another room", func() bool {
		bed, living, kitchen := read("bedroom"), read("livingroom"), read("kitchen")
		return kitchen.Status == "paused" && kitchen.PositionMs == paused.PositionMs && bed.Status == "playing" && living.Status == "playing" && bed.PositionMs > before["bedroom"].PositionMs && living.PositionMs > before["livingroom"].PositionMs
	})
	t.Log("Three real audio players advanced concurrently; pausing Kitchen left Bedroom and Living Room playing. Human audibility is unconfirmed.")
}
