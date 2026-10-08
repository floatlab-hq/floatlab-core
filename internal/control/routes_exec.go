package control

import (
	"encoding/json"
	"net/http"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/go-chi/chi/v5"
)

func (s *Server) handleContainerExec(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Command []string `json:"command"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil || len(body.Command) == 0 || body.Command[0] == "" {
		writeError(w, http.StatusBadRequest, "command must be a nonempty argument array")
		return
	}
	stackID := chi.URLParam(r, "id")
	stack, err := s.store.GetStack(r.Context(), stackID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	raw, err := s.hosts.Execute(r.Context(), stackNode(stack, s.raft.FSM().State), "docker.exec.run", ipc.ExecPayload{StackID: stackID, ContainerID: chi.URLParam(r, "containerId"), Command: body.Command})
	if err != nil {
		writeError(w, http.StatusBadGateway, "container exec failed: "+err.Error())
		return
	}
	var result ipc.ExecResult
	if err := json.Unmarshal(raw, &result); err != nil {
		writeError(w, http.StatusBadGateway, "invalid exec response from hostd")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
