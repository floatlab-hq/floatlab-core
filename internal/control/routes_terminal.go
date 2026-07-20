package control

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/run"
)

func (s *Server) handleStackTerminal(w http.ResponseWriter, r *http.Request) {
	stackID, containerID := chi.URLParam(r, "id"), chi.URLParam(r, "containerId")
	stack, err := s.store.GetStack(r.Context(), stackID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	nodeID := stack.PrimaryNodeID
	if state, ok := s.raft.FSM().State(stackID); ok && state.State == run.StateRunningBackup {
		nodeID = stack.BackupNodeID
	}
	events, unsubscribe, err := s.hosts.SubscribeEvents(r.Context(), nodeID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer unsubscribe()

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "terminal closed")
	rows, cols := uint(queryInt(r, "rows", 24)), uint(queryInt(r, "cols", 80))
	raw, err := s.hosts.Execute(r.Context(), nodeID, "docker.exec.open", ipc.TerminalOpenPayload{StackID: stackID, ContainerID: containerID, Command: r.URL.Query()["command"], Rows: rows, Cols: cols})
	if err != nil {
		_ = conn.Close(websocket.StatusPolicyViolation, err.Error())
		return
	}
	var session ipc.TerminalSessionPayload
	if json.Unmarshal(raw, &session) != nil {
		_ = conn.Close(websocket.StatusInternalError, "invalid host response")
		return
	}
	defer s.hosts.Execute(context.Background(), nodeID, "docker.exec.close", session)

	inputErr := make(chan error, 1)
	go func() {
		for {
			kind, data, err := conn.Read(r.Context())
			if err != nil {
				inputErr <- err
				return
			}
			if kind == websocket.MessageText {
				var resize struct {
					Type string `json:"type"`
					Rows uint   `json:"rows"`
					Cols uint   `json:"cols"`
				}
				if json.Unmarshal(data, &resize) == nil && resize.Type == "resize" {
					_, err = s.hosts.Execute(r.Context(), nodeID, "docker.exec.resize", ipc.TerminalResizePayload{SessionID: session.SessionID, Rows: resize.Rows, Cols: resize.Cols})
					if err != nil {
						inputErr <- err
						return
					}
					continue
				}
			}
			if _, err = s.hosts.Execute(r.Context(), nodeID, "docker.exec.write", ipc.TerminalWritePayload{SessionID: session.SessionID, Data: data}); err != nil {
				inputErr <- err
				return
			}
		}
	}()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-inputErr:
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			if event.Name != "docker.exec.output" {
				continue
			}
			var output ipc.TerminalOutputEvent
			if json.Unmarshal(event.Payload, &output) != nil || output.SessionID != session.SessionID {
				continue
			}
			if output.Error != "" {
				_ = conn.Close(websocket.StatusInternalError, output.Error)
				return
			}
			if output.Closed {
				return
			}
			if err := conn.Write(r.Context(), websocket.MessageBinary, output.Data); err != nil {
				return
			}
		}
	}
}

func queryInt(r *http.Request, name string, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || value < 1 || value > 4096 {
		return fallback
	}
	return value
}
