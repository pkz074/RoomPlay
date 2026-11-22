package library

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

type Server struct {
	catalog   *Catalog
	audioRoot string
}

func NewServer(catalog *Catalog, audioRoot string) *Server {
	return &Server{catalog: catalog, audioRoot: audioRoot}
}

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
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is supported.")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(protocol.TracksResponse{Tracks: s.catalog.Tracks()})
}

func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
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

	path := filepath.Join(s.audioRoot, entry.AudioFile)
	file, err := os.Open(path)
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
