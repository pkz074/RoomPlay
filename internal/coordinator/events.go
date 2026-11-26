package coordinator

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/pkz074/RoomPlay/internal/protocol"
)

type streamEvent struct {
	name string
	data []byte
}

// EventHub disconnects slow clients instead of blocking independent rooms.
type EventHub struct {
	mu      sync.Mutex
	clients map[chan streamEvent]struct{}
	closed  bool
}

func NewEventHub() *EventHub { return &EventHub{clients: make(map[chan streamEvent]struct{})} }
func (h *EventHub) Publish(name string, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- streamEvent{name, data}:
		default:
			delete(h.clients, ch)
			close(ch)
		}
	}
}
func (h *EventHub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for ch := range h.clients {
		delete(h.clients, ch)
		close(ch)
	}
}
func (h *EventHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, ok := w.(http.Flusher); !ok {
		writeAPIError(w, readID(), apiError(protocol.ErrServiceUnavailable, "Streaming unavailable."))
		return
	}
	ch := make(chan streamEvent, 64)
	h.mu.Lock()
	if h.closed || len(h.clients) >= 256 {
		h.mu.Unlock()
		writeAPIError(w, readID(), apiError(protocol.ErrServiceUnavailable, "Streaming unavailable."))
		return
	}
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.clients, ch); h.mu.Unlock() }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	write := func(data string) bool {
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := fmt.Fprint(w, data); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	if !write(": connected\n\n") {
		return
	}
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-ch:
			if !ok || !write("event: "+event.name+"\ndata: "+string(event.data)+"\n\n") {
				return
			}
		case <-tick.C:
			if !write(": keepalive\n\n") {
				return
			}
		}
	}
}
