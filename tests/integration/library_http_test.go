package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pkz074/RoomPlay/internal/library"
	"github.com/pkz074/RoomPlay/internal/protocol"
)

// Tests actual TCP/HTTP serialization from the shared catalog to the library handler.
// This is a library service boundary test, not a full NATS/coordinator/player test.
func TestLibraryHTTPRoundTrip(t *testing.T) {
	root := t.TempDir()
	const filename = "song.mp3"
	const audio = "fake-mp3-test-data"
	if err := os.WriteFile(filepath.Join(root, filename), []byte(audio), 0600); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + listener.Addr().String()
	source := `{"tracks":[{"id":"track-001","title":"Demo","artist":"Test Artist","duration_ms":3000,"audio_file":"song.mp3","source":"https://example.com/demo","license":"CC0","license_url":"https://creativecommons.org/publicdomain/zero/1.0/","attribution":"Test Artist"}]}`
	catalog, err := library.LoadCatalog(strings.NewReader(source), base)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	service, err := library.NewServer(catalog, root)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	server := &http.Server{Handler: service.Handler()}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); wg.Wait(); _ = service.Close() })

	client := &http.Client{}
	resp, err := client.Get(base + "/tracks")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/tracks status %d", resp.StatusCode)
	}
	var result protocol.TracksResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result.Tracks) != 1 {
		t.Fatalf("tracks count %d, want 1", len(result.Tracks))
	}
	track := result.Tracks[0]
	if track.ID != "track-001" || track.Title != "Demo" || track.DurationMs != 3000 {
		t.Fatalf("unexpected track: %+v", track)
	}
	if track.URL != base+"/audio/"+filename {
		t.Fatalf("invalid player-facing URL %q", track.URL)
	}

	audioResp, err := client.Get(track.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(audioResp.Body)
	audioResp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if audioResp.StatusCode != http.StatusOK || audioResp.Header.Get("Content-Type") != "audio/mpeg" || !bytes.Equal(body, []byte(audio)) {
		t.Fatalf("audio fetch status=%d type=%q body=%q", audioResp.StatusCode, audioResp.Header.Get("Content-Type"), body)
	}

	head, err := client.Head(track.URL)
	if err != nil {
		t.Fatal(err)
	}
	head.Body.Close()
	if head.StatusCode != http.StatusOK || head.ContentLength != int64(len(audio)) {
		t.Errorf("HEAD audio status=%d length=%d", head.StatusCode, head.ContentLength)
	}

	for _, tt := range []struct{ path string; want int }{
		{"/audio/unknown.mp3", http.StatusNotFound},
		{"/audio/../secret.mp3", http.StatusBadRequest},
	} {
		req, _ := http.NewRequest(http.MethodGet, base+tt.path, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tt.want {
			t.Errorf("%s: status=%d want=%d", tt.path, resp.StatusCode, tt.want)
		}
	}

	// Concurrent HTTP reads must not share mutable state or corrupt responses.
	errs := make(chan error, 16)
	for i := 0; i < cap(errs); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, e := client.Get(track.URL)
			if e != nil { errs <- e; return }
			defer response.Body.Close()
			data, e := io.ReadAll(response.Body)
			if e != nil { errs <- e; return }
			if response.StatusCode != http.StatusOK || string(data) != audio {
				errs <- fmt.Errorf("concurrent audio: status=%d body=%q", response.StatusCode, data)
				return
			}
			errs <- nil
		}()
	}
	// Wait for requests explicitly; keep server's own Serve goroutine alive.
	for i := 0; i < cap(errs); i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}
