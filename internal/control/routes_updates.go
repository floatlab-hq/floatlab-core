package control

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/release"
	"github.com/go-chi/chi/v5"
)

var updateOperationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func registerUpdateRoutes(r chi.Router, s *Server) {
	r.Group(func(r chi.Router) {
		r.Use(s.requireAdminJWT)
		r.Get("/nodes/{id}/updates/{component}/releases", s.handleUpdate)
		r.Get("/nodes/{id}/updates/{component}/status", s.handleUpdate)
		r.Post("/nodes/{id}/updates/{component}/{action}", s.handleUpdate)
	})
}

// Update idempotency belongs to hostd so recovery never needs rqlite.
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	component := chi.URLParam(r, "component")
	if component != "management" && component != "host" {
		writeError(w, http.StatusNotFound, "unknown update component")
		return
	}
	action := chi.URLParam(r, "action")
	if r.Method == http.MethodGet {
		action = "status"
		if strings.HasSuffix(r.URL.Path, "/releases") {
			action = "releases"
		}
	}
	allowed := action == "status" || action == "releases" || action == "rollback" || action == "cleanup" || component == "management" && action == "start" || component == "host" && (action == "prepare" || action == "activate" || action == "commit")
	if !allowed || r.Method == http.MethodPost && (action == "status" || action == "releases") {
		writeError(w, http.StatusNotFound, "unknown update action")
		return
	}
	var body struct {
		Release     string `json:"release,omitempty"`
		OperationID string `json:"operation_id,omitempty"`
	}
	if r.Method == http.MethodPost {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "one JSON request object is required")
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			writeError(w, http.StatusBadRequest, "one JSON request object is required")
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if key == "" || len(key) > 255 {
			writeError(w, http.StatusBadRequest, "Idempotency-Key is required and must not exceed 255 characters")
			return
		}
		if action == "start" || action == "prepare" {
			prefix := "manage-v"
			if component == "host" {
				prefix = "host-v"
			}
			if !strings.HasPrefix(body.Release, prefix) {
				writeError(w, http.StatusBadRequest, "release must be a matching immutable release tag")
				return
			}
			if _, err := release.Parse(strings.TrimPrefix(body.Release, prefix)); err != nil {
				writeError(w, http.StatusBadRequest, "invalid release version")
				return
			}
			if body.OperationID == "" {
				body.OperationID = key
				if !updateOperationID.MatchString(key) {
					body.OperationID = fmt.Sprintf("%x", sha256.Sum256([]byte(key)))
				}
			}
		}
		if !updateOperationID.MatchString(body.OperationID) {
			writeError(w, http.StatusBadRequest, "a valid update operation_id is required")
			return
		}
		actor, _ := r.Context().Value(keyActor).(string)
		payload := map[string]string{"operation_id": body.OperationID, "release": body.Release, "actor": actor, "idempotency_key": key}
		s.forwardUpdate(w, r, component+".update."+action, payload, http.StatusAccepted)
		return
	}
	s.forwardUpdate(w, r, component+".update."+action, map[string]string{"operation_id": r.URL.Query().Get("operation_id")}, http.StatusOK)
}

func (s *Server) forwardUpdate(w http.ResponseWriter, r *http.Request, command string, payload any, status int) {
	result, err := s.hosts.Execute(r.Context(), chi.URLParam(r, "id"), command, payload)
	if err != nil {
		var remote *ipc.RPCError
		code := "update.unavailable"
		if errors.As(err, &remote) {
			code = remote.Code
			switch {
			case strings.HasSuffix(code, ".invalid"):
				status = http.StatusBadRequest
			case strings.HasSuffix(code, ".conflict"):
				status = http.StatusConflict
			case strings.HasSuffix(code, ".not_found"):
				status = http.StatusNotFound
			default:
				status = http.StatusServiceUnavailable
			}
		} else {
			status = http.StatusServiceUnavailable
		}
		writeErrorCode(w, status, code, err.Error())
		return
	}
	writeJSON(w, status, json.RawMessage(result))
}
