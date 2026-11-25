package player

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

func TestFetchTrackIndex(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tracks" || r.Method != http.MethodGet {
			t.Errorf("unexpected library request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"tracks":[{"id":"a","title":"Track A","artist":"Artist","duration_ms":1000,"url":"http://library/audio/a.mp3"}]}`)
	}))
	defer server.Close()
	index, err := FetchTrackIndex(context.Background(), server.URL, server.Client())
	if err != nil {
		t.Fatalf("FetchTrackIndex: %v", err)
	}
	track, exists := index["a"]
	if !exists || track.Title != "Track A" || track.DurationMs != 1000 {
		t.Fatalf("indexed track = %+v, exists=%t", track, exists)
	}
}

func TestFetchTrackIndexRejectsInvalidCatalog(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"tracks":[],"extra":true}`},
		{name: "missing array", body: `{}`},
		{name: "negative duration", body: `{"tracks":[{"id":"a","title":"A","artist":"Artist","duration_ms":-1,"url":"http://library/a.mp3"}]}`},
		{name: "invalid audio URL", body: `{"tracks":[{"id":"a","title":"A","artist":"Artist","duration_ms":1,"url":"file:///secret.mp3"}]}`},
		{name: "trailing value", body: `{"tracks":[]} {}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			if _, err := FetchTrackIndex(context.Background(), server.URL, server.Client()); err == nil {
				t.Fatal("FetchTrackIndex succeeded for invalid catalog")
			}
		})
	}
}

func TestNewTrackIndexRejectsDuplicatesAndInvalidMetadata(t *testing.T) {
	valid := protocol.Track{ID: "a", Title: "A", Artist: "Artist", DurationMs: 1, URL: "https://library/audio/a.mp3"}
	if _, err := NewTrackIndex([]protocol.Track{valid, valid}); err == nil {
		t.Fatal("duplicate track ID was accepted")
	}
	invalid := valid
	invalid.URL = "http://user:pass@library/a.mp3"
	if _, err := NewTrackIndex([]protocol.Track{invalid}); err == nil {
		t.Fatal("credential-bearing audio URL was accepted")
	}
}

func TestFetchTrackIndexBoundsResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"tracks":[]}`+strings.Repeat(" ", maxTrackCatalogBytes))
	}))
	defer server.Close()
	if _, err := FetchTrackIndex(context.Background(), server.URL, server.Client()); err == nil {
		t.Fatal("oversized catalog was accepted")
	}
}