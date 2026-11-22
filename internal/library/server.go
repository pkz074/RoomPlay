package library

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

type Server struct {
	catalog   *Catalog
	audioRoot *os.Root
}

func NewServer(catalog *Catalog, audioRoot string) (*Server, error) {
	if catalog == nil {
		return nil, fmt.Errorf("catalog is required")
	}
	root, err := os.OpenRoot(audioRoot)
	if err != nil {
		return nil, fmt.Errorf("open audio root: %w", err)
	}
	return &Server{catalog: catalog, audioRoot: root}, nil
}

// Close releases the directory handle after all HTTP requests have finished.
func (s *Server) Close() error { return s.audioRoot.Close() }

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/tracks":
			s.handleTracks(w, r)
		case strings.HasPrefix(r.URL.Path, "/audio/"):
			s.handleAudio(w, r)
		default:
			writeError(w, http.StatusNotFound, "not_found", "Route not found.")
		}
	})
}

func (s *Server) handleTracks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is supported.")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(protocol.TracksResponse{Tracks: s.catalog.Tracks()})
}

func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET and HEAD are supported.")
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/audio/")
	if !safeAudioFilename(name) {
		writeError(w, http.StatusBadRequest, "invalid_audio_path", "The audio path is invalid.")
		return
	}

	entry, exists := s.catalog.EntryByFilename(name)
	if !exists {
		writeError(w, http.StatusNotFound, "track_not_found", "Unknown audio file.")
		return
	}

	// Root.Open confines symlink resolution to the opened directory, including
	// when a link is replaced concurrently. Do not replace this with check-then-open.
	file, err := s.audioRoot.Open(entry.AudioFile)
	if err != nil {
		writeError(w, http.StatusNotFound, "track_not_found", "Audio file is unavailable.")
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusNotFound, "track_not_found", "Audio file is unavailable.")
		return
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, entry.AudioFile, time.Time{}, file)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}
