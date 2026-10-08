package control

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/floatlab/floatlab-core/internal/hostnetwork"
	"github.com/floatlab/floatlab-core/internal/ipam"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/go-chi/chi/v5"
)

func registerHostNetworkRoutes(r chi.Router, s *Server) {
	r.Group(func(r chi.Router) {
		r.Use(s.requireAdminJWT)
		r.Get("/nodes/{id}/settings/network", s.handleHostNetwork)
		r.Put("/nodes/{id}/settings/network", s.handleApplyHostNetwork)
		r.Get("/nodes/{id}/settings/network/changes/{change}", s.handleNetworkChange)
		r.Post("/nodes/{id}/settings/network/changes/{change}/confirm", s.handleNetworkChange)
		r.Post("/nodes/{id}/settings/network/changes/{change}/rollback", s.handleNetworkChange)
	})
}
func networkError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	var rpc *ipc.RPCError
	if errors.As(err, &rpc) {
		switch rpc.Code {
		case "network.invalid":
			status = http.StatusBadRequest
		case "network.conflict":
			status = http.StatusConflict
		case "network.not_found":
			status = http.StatusNotFound
		}
	}
	if rpc != nil {
		writeErrorCode(w, status, rpc.Code, rpc.Message)
	} else {
		writeErrorCode(w, status, "network.unavailable", err.Error())
	}
}
func (s *Server) handleHostNetwork(w http.ResponseWriter, r *http.Request) {
	result, err := s.hosts.Execute(r.Context(), chi.URLParam(r, "id"), "net.settings.get", nil)
	if err != nil {
		networkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(result))
}
func (s *Server) handleApplyHostNetwork(w http.ResponseWriter, r *http.Request) {
	var update hostnetwork.Update
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&update); err != nil {
		writeError(w, http.StatusBadRequest, "invalid network settings: "+err.Error())
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(w, http.StatusBadRequest, "request must contain one JSON object")
		return
	}
	if update.Revision == "" || update.Config.ExcludedMACs == nil || update.Config.Bonds == nil || update.Config.DNSServers == nil {
		writeError(w, http.StatusBadRequest, "revision, excluded_macs, bonds, and dns_servers are required")
		return
	}
	if err := update.Config.Validate(); err != nil {
		networkError(w, err)
		return
	}
	nodeID := chi.URLParam(r, "id")
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 255 {
		writeError(w, http.StatusBadRequest, "Idempotency-Key is required and must not exceed 255 characters")
		return
	}
	actor, _ := r.Context().Value(keyActor).(string)
	payload := hostnetwork.ApplyPayload{Revision: update.Revision, Config: update.Config, Key: key, Actor: actor}
	retry, err := s.hosts.Execute(r.Context(), nodeID, "net.settings.retry", payload)
	if err != nil {
		networkError(w, err)
		return
	}
	var accepted *hostnetwork.Change
	if err = json.Unmarshal(retry, &accepted); err != nil {
		networkError(w, err)
		return
	}
	if accepted != nil {
		writeJSON(w, http.StatusAccepted, accepted)
		return
	}
	// Only pool references require database access. Recovery endpoints never do.
	if update.Config.DefaultPoolID != "" {
		pools, err := ipam.ListPools(r.Context(), s.db)
		if err != nil {
			networkError(w, err)
			return
		}
		found := false
		for _, pool := range pools {
			if pool.ID == update.Config.DefaultPoolID {
				found = true
				if !ipam.Member(pool, nodeID) {
					writeError(w, http.StatusConflict, "default pool is not eligible for this host")
					return
				}
				if update.Config.IPv4.Mode == "static" {
					prefix, _ := update.Config.IPv4.Prefix()
					if err := ipam.ValidateHostRange(pool, []string{prefix.String()}, []string{update.Config.IPv4.Gateway}); err != nil {
						writeError(w, http.StatusConflict, err.Error())
						return
					}
					if !hostnetwork.Compatible(pool.CIDR, []string{prefix.String()}) {
						writeError(w, http.StatusConflict, "default pool does not match static subnet")
						return
					}
				} else {
					status, err := ipam.HostStatus(r.Context(), s.hosts, nodeID)
					if err != nil {
						networkError(w, err)
						return
					}
					if err := ipam.ValidateHostRange(pool, status.Live.PrimaryAddresses, status.Live.Gateways); err != nil {
						writeError(w, http.StatusConflict, err.Error())
						return
					}
					if !hostnetwork.Compatible(pool.CIDR, status.Live.PrimaryAddresses) {
						writeError(w, http.StatusConflict, "default pool does not match effective DHCP subnet")
						return
					}
				}
			}
		}
		if !found {
			writeError(w, http.StatusConflict, "default pool not found")
			return
		}
	}
	result, err := s.hosts.Execute(r.Context(), nodeID, "net.settings.apply", payload)
	if err != nil {
		networkError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, json.RawMessage(result))
}
func (s *Server) handleNetworkChange(w http.ResponseWriter, r *http.Request) {
	command := "net.settings.change"
	if r.Method == http.MethodPost {
		if strings.HasSuffix(r.URL.Path, "/confirm") {
			command = "net.settings.confirm"
		} else {
			command = "net.settings.rollback"
		}
	}
	result, err := s.hosts.Execute(r.Context(), chi.URLParam(r, "id"), command, map[string]string{"id": chi.URLParam(r, "change")})
	if err != nil {
		networkError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(result))
}
