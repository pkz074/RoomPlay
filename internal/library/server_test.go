package library

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testServer(t *testing.T) (*Server, string) {
	t.Helper()
	catalog, err := LoadCatalog(strings.NewReader(`{"tracks":[{"id":"track-001","title":"Demo","artist":"Artist","duration_ms":210000,"audio_file":"track-001.mp3","source":"https://example.com/source","license":"CC0-1.0","license_url":"https://creativecommons.org/publicdomain/zero/1.0/","attribution":"Artist"}]}`), "http://192.0.2.10:8090")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "track-001.mp3"), []byte("test-mp3"), 0o600); err != nil {
		t.Fatal(err)
	}
	return NewServer(catalog, root), root
}

func TestTracks(t *testing.T) {
	server, _ := testServer(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/tracks", nil)

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	tracks, ok := response["tracks"].([]any)
	if !ok || len(tracks) != 1 {
		t.Fatalf("tracks = %#v", response["tracks"])
	}
}

func TestAudioAllowlistAndTraversal(t *testing.T) {
	server, _ := testServer(t)

	tests := []struct {
		path string
		want int
	}{
		{path: "/audio/track-001.mp3", want: http.StatusOK},
		{path: "/audio/missing.mp3", want: http.StatusNotFound},
		{path: "/audio/../server.go", want: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, tt.path, nil)
			server.Handler().ServeHTTP(recorder, request)
			if recorder.Code != tt.want {
				t.Errorf("status = %d, want %d", recorder.Code, tt.want)
			}
		})
	}
}

func TestAudioContentType(t *testing.T) {
	server, _ := testServer(t)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/audio/track-001.mp3", nil)

	server.Handler().ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Content-Type"); got != "audio/mpeg" {
		t.Errorf("Content-Type = %q, want audio/mpeg", got)
	}
	if recorder.Body.String() != "test-mp3" {
		t.Errorf("body = %q", recorder.Body.String())
	}
}
