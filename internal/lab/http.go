package lab

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// Handler serves administrative routes. Mount it behind existing administrator
// authentication. Only /enroll belongs on the certificate-optional bootstrap
// listener, through EnrollmentHandler; invitations authenticate that endpoint.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		_ = s.RefreshTrust(r.Context())
		writeResponse(w, s.Snapshot(), nil)
	})
	mux.HandleFunc("GET /members", func(w http.ResponseWriter, r *http.Request) {
		_ = s.RefreshTrust(r.Context())
		writeResponse(w, s.Snapshot(), nil)
	})
	mux.HandleFunc("POST /invitations", func(w http.ResponseWriter, r *http.Request) {
		value, err := s.CreateInvitation(r.Context())
		writeResponse(w, value, err)
	})
	mux.HandleFunc("POST /join", func(w http.ResponseWriter, r *http.Request) {
		var req JoinRequest
		if !decodeRequest(w, r, &req) {
			return
		}
		value, err := s.Join(r.Context(), req)
		writeResponse(w, value, err)
	})
	mux.HandleFunc("POST /members/{id}/remove", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Confirm     string `json:"confirm_node_id"`
			OperationID string `json:"operation_id"`
		}
		if !decodeRequest(w, r, &req) {
			return
		}
		value, err := s.Remove(r.Context(), r.PathValue("id"), req.Confirm, req.OperationID)
		writeResponse(w, value, err)
	})
	mux.HandleFunc("GET /members/{id}/removal", func(w http.ResponseWriter, r *http.Request) {
		value, err := s.RemovalPreview(r.Context(), r.PathValue("id"))
		writeResponse(w, value, err)
	})
	mux.HandleFunc("POST /members/{id}/release-peer-address", func(w http.ResponseWriter, r *http.Request) {
		var req ReleasePeerAddressRequest
		if !decodeRequest(w, r, &req) {
			return
		}
		value, err := s.ReleasePeerAddress(r.Context(), r.PathValue("id"), req)
		writeResponse(w, value, err)
	})
	mux.HandleFunc("POST /identity/rotate", func(w http.ResponseWriter, r *http.Request) {
		var req RotationRequest
		if !decodeRequest(w, r, &req) {
			return
		}
		value, err := s.RotateIdentity(r.Context(), req)
		writeResponse(w, value, err)
	})
	return mux
}

func (s *Service) EnrollmentHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || strings.TrimRight(r.URL.Path, "/") != "/lab/enroll" {
			http.NotFound(w, r)
			return
		}
		var req enrollmentRequest
		if !decodeRequest(w, r, &req) {
			return
		}
		value, err := s.enroll(r.Context(), req)
		writeResponse(w, value, err)
	})
}

func decodeRequest(w http.ResponseWriter, r *http.Request, value any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		http.Error(w, "unexpected trailing JSON", http.StatusBadRequest)
		return false
	}
	return true
}
func writeResponse(w http.ResponseWriter, value any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusConflict)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}
