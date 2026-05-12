package control

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// handleEvents streams server-sent events to the client.
// All events (FSM state changes, notifications) flow through the broker,
// which provides independent per-subscriber channels.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ch, unsub := s.broker.Subscribe()
	defer unsub()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return

		case ev, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, ev.Payload)
			flusher.Flush()

		case <-ticker.C:
			_, _ = w.Write([]byte(": keepalive\n\n"))
			flusher.Flush()
		}
	}
}

// bridgeFSMToBroker subscribes to the FSM's fan-out channel and republishes
// each state change to the broker so all SSE clients get independent copies.
// Must be run as a goroutine — exits when the subscription channel is closed.
func (s *Server) bridgeFSMToBroker() {
	fsmCh, unsub := s.raft.FSM().Subscribe()
	defer unsub()

	for sc := range fsmCh {
		payload := struct {
			StackID string `json:"stack_id"`
			From    string `json:"from"`
			To      string `json:"to"`
			Event   string `json:"event"`
		}{
			StackID: sc.StackID,
			From:    string(sc.From),
			To:      string(sc.To),
			Event:   string(sc.Event),
		}
		if b, err := json.Marshal(payload); err == nil {
			s.broker.PublishJSON("stack.state", b)
		}
	}
}
