package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

type HTTPOptions struct {
	LibraryURL string
	WebFS      fs.FS
	Client     *http.Client
}

func NewHTTPHandler(s *Service, options HTTPOptions) (http.Handler, error) {
	base, err := url.Parse(options.LibraryURL)
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Hostname() == "" || base.User != nil || base.Opaque != "" || base.ForceQuery || base.RawPath != "" || base.RawQuery != "" || strings.Contains(options.LibraryURL, "#") || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return nil, errors.New("library URL must be an HTTP(S) origin")
	}
	base.Path = "/tracks"
	client := options.Client
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/rooms", func(w http.ResponseWriter, r *http.Request) {
		if httpMethod(w, r, "GET") {
			writeJSON(w, 200, s.Rooms())
		}
	})
	mux.HandleFunc("/api/v1/rooms/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !httpMethod(w, r, "GET") || !httpRoomID(w, r) {
			return
		}
		room, e := s.Detail(r.Context(), r.PathValue("id"))
		if e != nil {
			writeAPIError(w, readID(), e)
			return
		}
		writeJSON(w, 200, room)
	})
	mux.HandleFunc("/api/v1/rooms/{id}/commands", func(w http.ResponseWriter, r *http.Request) {
		if !httpMethod(w, r, "POST") || !httpRoomID(w, r) {
			return
		}
		var body struct {
			RequestID string               `json:"request_id"`
			Type      protocol.CommandType `json:"type"`
			Payload   json.RawMessage      `json:"payload"`
		}
		if !httpDecode(w, r, &body) {
			return
		}
		id, ok := httpRequestID(w, r, body.RequestID)
		if !ok {
			return
		}
		command := protocol.CommandRequest{RequestID: id, Type: body.Type, Payload: body.Payload}
		if !playbackType(body.Type) {
			writeAPIError(w, id, apiError(protocol.ErrInvalidCommand, "Unknown playback command."))
			return
		}
		if protocol.ValidateCommand(command) != nil {
			writeAPIError(w, id, apiError(protocol.ErrInvalidPayload, "Invalid command payload."))
			return
		}
		httpMutation(w, r, s, command)
	})
	mux.HandleFunc("/api/v1/rooms/{id}/queue", func(w http.ResponseWriter, r *http.Request) {
		if !httpMethod(w, r, "GET", "POST", "DELETE") || !httpRoomID(w, r) {
			return
		}
		if r.Method == "GET" {
			room, e := s.Detail(r.Context(), r.PathValue("id"))
			if e != nil {
				writeAPIError(w, readID(), e)
				return
			}
			if room.Availability != protocol.RoomOnline || room.State == nil {
				writeAPIError(w, readID(), apiError(protocol.ErrRoomOffline, "Room state is unavailable."))
				return
			}
			writeJSON(w, 200, protocol.QueueResponse{RoomID: room.ID, Queue: room.State.Queue})
			return
		}
		id, ok := httpRequestID(w, r, "")
		if !ok {
			return
		}
		command := protocol.CommandRequest{RequestID: id, Type: protocol.QueueClear, Payload: json.RawMessage(`{}`)}
		if r.Method == "POST" {
			var body protocol.TrackPayload
			if !httpDecode(w, r, &body) {
				return
			}
			command.Type = protocol.QueueAdd
			command.Payload, _ = json.Marshal(body)
		} else if !httpEmpty(w, r) {
			return
		}
		if protocol.ValidateCommand(command) != nil {
			writeAPIError(w, id, apiError(protocol.ErrInvalidPayload, "Invalid queue payload."))
			return
		}
		httpMutation(w, r, s, command)
	})
	mux.HandleFunc("/api/v1/rooms/{id}/queue/{item}", func(w http.ResponseWriter, r *http.Request) {
		if !httpMethod(w, r, "DELETE") || !httpRoomID(w, r) {
			return
		}
		id, ok := httpRequestID(w, r, "")
		if !ok || !httpEmpty(w, r) {
			return
		}
		payload, _ := json.Marshal(protocol.RemoveItemPayload{ItemID: r.PathValue("item")})
		command := protocol.CommandRequest{RequestID: id, Type: protocol.QueueRemove, Payload: payload}
		if protocol.ValidateCommand(command) != nil {
			writeAPIError(w, id, apiError(protocol.ErrInvalidPayload, "Invalid queue item."))
			return
		}
		httpMutation(w, r, s, command)
	})
	mux.HandleFunc("/api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		if httpMethod(w, r, "GET") {
			s.events.ServeHTTP(w, r)
		}
	})
	mux.HandleFunc("/api/v1/tracks", func(w http.ResponseWriter, r *http.Request) {
		if !httpMethod(w, r, "GET") {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		request, _ := http.NewRequestWithContext(ctx, "GET", base.String(), nil)
		response, e := client.Do(request)
		if e != nil {
			writeAPIError(w, readID(), apiError(protocol.ErrServiceUnavailable, "Library is unavailable."))
			return
		}
		defer response.Body.Close()
		data, e := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		var catalog protocol.TracksResponse
		if e != nil || response.StatusCode != 200 || decodeResponse(data, &catalog) != nil || catalog.Tracks == nil {
			writeAPIError(w, readID(), apiError(protocol.ErrServiceUnavailable, "Invalid library response."))
			return
		}
		seen := make(map[string]bool, len(catalog.Tracks))
		for _, track := range catalog.Tracks {
			u, e := url.Parse(track.URL)
			if seen[track.ID] || !protocol.ValidIdentifier(track.ID) || track.DurationMs < 0 || strings.TrimSpace(track.Title) == "" || strings.TrimSpace(track.Artist) == "" || e != nil || u == nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
				writeAPIError(w, readID(), apiError(protocol.ErrServiceUnavailable, "Invalid library track."))
				return
			}
			seen[track.ID] = true
		}
		writeJSON(w, 200, catalog)
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, readID(), apiError(protocol.ErrRoomNotFound, "Unknown API route."))
	})
	if options.WebFS != nil {
		mux.Handle("/", http.FileServer(http.FS(options.WebFS)))
	}
	return mux, nil
}

func playbackType(kind protocol.CommandType) bool {
	switch kind {
	case protocol.PlaybackPlay, protocol.PlaybackPause, protocol.PlaybackResume, protocol.PlaybackStop, protocol.PlaybackNext, protocol.PlaybackPrevious:
		return true
	}
	return false
}
func httpMutation(w http.ResponseWriter, r *http.Request, s *Service, command protocol.CommandRequest) {
	expected := r.Header.Get("X-Room-Instance-ID")
	if expected != "" && !protocol.ValidIdentifier(expected) {
		writeAPIError(w, command.RequestID, apiError(protocol.ErrInvalidPayload, "Invalid room instance."))
		return
	}
	result, e := s.Command(r.Context(), r.PathValue("id"), expected, command)
	if e != nil {
		writeAPIError(w, command.RequestID, e)
		return
	}
	writeJSON(w, 200, result)
}
func httpRoomID(w http.ResponseWriter, r *http.Request) bool {
	if !protocol.ValidRoomID(r.PathValue("id")) {
		writeAPIError(w, readID(), apiError(protocol.ErrInvalidPayload, "Invalid room ID."))
		return false
	}
	return true
}
func httpRequestID(w http.ResponseWriter, r *http.Request, body string) (string, bool) {
	header := r.Header.Get("X-Request-ID")
	id := header
	if id == "" {
		id = body
	}
	if len(r.Header.Values("X-Request-ID")) > 1 || !protocol.ValidIdentifier(id) || (header != "" && body != "" && header != body) {
		writeAPIError(w, id, apiError(protocol.ErrInvalidPayload, "Missing, invalid or mismatched request ID."))
		return "", false
	}
	return id, true
}
func httpDecode(w http.ResponseWriter, r *http.Request, dst any) bool {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil || decodeResponse(data, dst) != nil {
		writeAPIError(w, r.Header.Get("X-Request-ID"), apiError(protocol.ErrInvalidPayload, "Invalid JSON body."))
		return false
	}
	return true
}
func httpEmpty(w http.ResponseWriter, r *http.Request) bool {
	data, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
	if e != nil || len(data) > 0 {
		writeAPIError(w, r.Header.Get("X-Request-ID"), apiError(protocol.ErrInvalidPayload, "This request must have no body."))
		return false
	}
	return true
}
func httpMethod(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, method := range methods {
		if r.Method == method {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeJSON(w, 405, protocol.HTTPErrorResponse{RequestID: r.Header.Get("X-Request-ID"), Error: *apiError(protocol.ErrInvalidCommand, "Method is not supported.")})
	return false
}
func writeAPIError(w http.ResponseWriter, id string, e *protocol.APIError) {
	writeJSON(w, protocol.HTTPStatus(e.Code), protocol.HTTPErrorResponse{RequestID: id, Error: *e})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
