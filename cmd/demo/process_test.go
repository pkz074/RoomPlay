//go:build process

package main

import (
	"context"
	"encoding/json"
	"errors"
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
		audioClient := &http.Client{Timeout: 10 * time.Second}
		response, err := audioClient.Post("http://"+address(ports[0])+"/api/v1/rooms/bedroom/commands", "application/json", strings.NewReader(`{"request_id":"demo-audio-check","type":"playback.play","payload":{"track_id":"track-001"}}`))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 && response.StatusCode != 504 {
			t.Fatalf("audio Play HTTP %d", response.StatusCode)
		}
		played := false
		end := time.Now().Add(20 * time.Second)
		for time.Now().Before(end) {
			response, err := audioClient.Get("http://" + address(ports[0]) + "/api/v1/rooms/bedroom")
			if err != nil {
				t.Fatal(err)
			}
			var room struct {
				State struct {
					Status     string
					PositionMs int64 `json:"position_ms"`
				}
			}
			err = json.NewDecoder(response.Body).Decode(&room)
			response.Body.Close()
			if err == nil && room.State.Status == "playing" && room.State.PositionMs >= 3000 {
				played = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !played {
			t.Fatal("Bedroom audio did not advance")
		}
		t.Log("Real audio backend advanced for three seconds; human audibility is unconfirmed.")
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
