package control

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func registerConfigRoutes(r chi.Router, s *Server) {
	r.Group(func(r chi.Router) {
		r.Use(s.requireAdminJWT)
		r.Use(s.idempotency)
		r.Get("/networks", s.handleNetworks)
		r.Post("/networks", s.handleNetworks)
		r.Get("/networks/{id}", s.handleNetworks)
		r.Delete("/networks/{id}", s.handleNetworks)
		r.Get("/alert-rules", s.handleAlertRules)
		r.Post("/alert-rules", s.handleAlertRules)
		r.Get("/alert-rules/{id}", s.handleAlertRules)
		r.Put("/alert-rules/{id}", s.handleAlertRules)
		r.Delete("/alert-rules/{id}", s.handleAlertRules)
		r.Get("/alerts", s.handleAlerts)
		r.Get("/alerts/{id}", s.handleAlerts)
	})
}

// queryObjects keeps SQL projections responsible for the public field names.
func (s *Server) queryObjects(r *http.Request, query string, args ...interface{}) ([]map[string]interface{}, error) {
	result, err := s.db.Query(r.Context(), rqlite.Statement{SQL: query, Params: args})
	if err != nil {
		return nil, err
	}
	rows := make([]map[string]interface{}, 0, len(result.Values))
	for _, values := range result.Values {
		row := map[string]interface{}{}
		for i, column := range result.Columns {
			if i < len(values) {
				row[column] = values[i]
				if text, ok := values[i].(string); ok && (strings.HasSuffix(column, "_at") || column == "ts") {
					if timestamp, err := time.Parse("2006-01-02 15:04:05", text); err == nil {
						row[column] = timestamp.UTC().Format(time.RFC3339Nano)
					}
				}
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (s *Server) writeObjects(w http.ResponseWriter, r *http.Request, query string, args ...interface{}) {
	rows, err := s.queryObjects(r, query, args...)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if chi.URLParam(r, "id") != "" {
		if len(rows) == 0 {
			writeError(w, 404, "resource not found")
			return
		}
		writeJSON(w, 200, rows[0])
		return
	}
	writeJSON(w, 200, rows)
}

func decodeBody(w http.ResponseWriter, r *http.Request, value interface{}) bool {
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(value); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return false
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeError(w, 400, "request must contain one JSON value")
		return false
	}
	return true
}

func validSeverity(value string) bool {
	return value == "info" || value == "warning" || value == "critical"
}

func (s *Server) handleNetworks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	const projection = `SELECT id,name,prefix AS cidr,stack_id,created_at FROM networks`
	switch r.Method {
	case http.MethodGet:
		if id != "" {
			s.writeObjects(w, r, projection+" WHERE id=?", id)
		} else {
			s.writeObjects(w, r, projection+" ORDER BY name")
		}
	case http.MethodPost:
		var body struct {
			Name    string `json:"name"`
			CIDR    string `json:"cidr"`
			StackID string `json:"stack_id"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		prefix, err := netip.ParsePrefix(body.CIDR)
		if err != nil || !prefix.Addr().Is6() || strings.TrimSpace(body.Name) == "" {
			writeError(w, 400, "name and valid IPv6 cidr are required")
			return
		}
		existing, err := s.queryObjects(r, projection)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		for _, row := range existing {
			other, _ := netip.ParsePrefix(fmt.Sprint(row["cidr"]))
			if row["name"] == body.Name || (other.IsValid() && prefix.Overlaps(other)) {
				writeError(w, 409, "network name or CIDR conflicts")
				return
			}
		}
		id = uuid.NewString()
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if err := s.db.Execute(r.Context(), []rqlite.Statement{{SQL: `INSERT INTO networks(id,name,prefix,stack_id,created_at) VALUES(?,?,?,?,?)`, Params: []interface{}{id, body.Name, prefix.Masked().String(), body.StackID, now}}}); err != nil {
			writeError(w, 409, err.Error())
			return
		}
		writeJSON(w, 201, map[string]interface{}{"id": id, "name": body.Name, "cidr": prefix.Masked().String(), "stack_id": body.StackID, "created_at": now})
	case http.MethodDelete:
		rows, err := s.queryObjects(r, projection+" WHERE id=?", id)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		if len(rows) == 0 {
			writeError(w, 404, "network not found")
			return
		}
		reservations, err := s.queryObjects(r, `SELECT id FROM ip_reservations WHERE prefix_pool=? OR prefix_pool=?`, id, rows[0]["cidr"])
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		if len(reservations) > 0 {
			writeError(w, 409, "network has active reservations")
			return
		}
		if err := s.store.DeleteNetwork(r.Context(), id); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		w.WriteHeader(204)
	}
}

const ruleProjection = `SELECT id,name,condition,severity,stack_id,node_id,for_duration,annotations,enabled,created_at,updated_at FROM alert_rules`

func normalizeRule(row map[string]interface{}) {
	row["enabled"] = number(row["enabled"]) != 0
	annotations := map[string]string{}
	if value, ok := row["annotations"].(string); ok {
		_ = json.Unmarshal([]byte(value), &annotations)
	}
	row["annotations"] = annotations
}

func (s *Server) handleAlertRules(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	switch r.Method {
	case http.MethodGet:
		query := ruleProjection + " WHERE 1=1"
		args := []interface{}{}
		if id != "" {
			query += " AND id=?"
			args = append(args, id)
		}
		for _, field := range []string{"stack_id", "node_id", "enabled"} {
			if value := r.URL.Query().Get(field); value != "" {
				var filter interface{} = value
				if field == "enabled" {
					b, err := strconv.ParseBool(value)
					if err != nil {
						writeError(w, 400, "invalid enabled filter")
						return
					}
					filter = b
				}
				query += " AND " + field + "=?"
				args = append(args, filter)
			}
		}
		rows, err := s.queryObjects(r, query+" ORDER BY created_at DESC", args...)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		for _, row := range rows {
			normalizeRule(row)
		}
		if id != "" {
			if len(rows) == 0 {
				writeError(w, 404, "rule not found")
				return
			}
			writeJSON(w, 200, rows[0])
		} else {
			writeJSON(w, 200, rows)
		}
	case http.MethodPost, http.MethodPut:
		var body struct {
			Name        string            `json:"name"`
			Condition   string            `json:"condition"`
			Severity    string            `json:"severity"`
			StackID     string            `json:"stack_id"`
			NodeID      string            `json:"node_id"`
			Duration    string            `json:"for_duration"`
			Annotations map[string]string `json:"annotations"`
			Enabled     *bool             `json:"enabled"`
		}
		var input map[string]json.RawMessage
		if !decodeBody(w, r, &input) {
			return
		}
		raw, _ := json.Marshal(input)
		if err := json.Unmarshal(raw, &body); err != nil {
			writeError(w, 400, "invalid rule JSON: "+err.Error())
			return
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		created := now
		enabled := true
		if r.Method == http.MethodPut {
			rows, err := s.queryObjects(r, ruleProjection+" WHERE id=?", id)
			if err != nil {
				writeError(w, 500, err.Error())
				return
			}
			if len(rows) == 0 {
				writeError(w, 404, "rule not found")
				return
			}
			row := rows[0]
			created = fmt.Sprint(row["created_at"])
			enabled = number(row["enabled"]) != 0
			if _, present := input["name"]; !present {
				body.Name = fmt.Sprint(row["name"])
			}
			if _, present := input["condition"]; !present {
				body.Condition = fmt.Sprint(row["condition"])
			}
			if _, present := input["severity"]; !present {
				body.Severity = fmt.Sprint(row["severity"])
			}
			body.StackID, _ = row["stack_id"].(string)
			body.NodeID, _ = row["node_id"].(string)
			if _, present := input["for_duration"]; !present {
				body.Duration, _ = row["for_duration"].(string)
			}
			if _, present := input["annotations"]; !present {
				value, _ := row["annotations"].(string)
				_ = json.Unmarshal([]byte(value), &body.Annotations)
			}
		} else {
			id = uuid.NewString()
		}
		if strings.TrimSpace(body.Name) == "" || strings.TrimSpace(body.Condition) == "" || !validSeverity(body.Severity) {
			writeError(w, 400, "name, condition and valid severity are required")
			return
		}
		if body.Enabled != nil {
			enabled = *body.Enabled
		}
		if body.Annotations == nil {
			body.Annotations = map[string]string{}
		}
		annotations, _ := json.Marshal(body.Annotations)
		statement := rqlite.Statement{SQL: `INSERT INTO alert_rules(id,name,type,condition,severity,stack_id,node_id,for_duration,annotations,enabled,created_at,updated_at) VALUES(?,?,'metrics',?,?,?,?,?,?,?,?,?)`, Params: []interface{}{id, body.Name, body.Condition, body.Severity, body.StackID, body.NodeID, body.Duration, string(annotations), enabled, created, now}}
		if r.Method == http.MethodPut {
			statement = rqlite.Statement{SQL: `UPDATE alert_rules SET name=?,condition=?,severity=?,for_duration=?,annotations=?,enabled=?,updated_at=? WHERE id=?`, Params: []interface{}{body.Name, body.Condition, body.Severity, body.Duration, string(annotations), enabled, now, id}}
		}
		if err := s.db.Execute(r.Context(), []rqlite.Statement{statement}); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		status := 201
		if r.Method == http.MethodPut {
			status = 200
		}
		writeJSON(w, status, map[string]interface{}{"id": id, "name": body.Name, "condition": body.Condition, "severity": body.Severity, "stack_id": body.StackID, "node_id": body.NodeID, "for_duration": body.Duration, "annotations": body.Annotations, "enabled": enabled, "created_at": created, "updated_at": now})
	case http.MethodDelete:
		result, err := s.db.Request(r.Context(), rqlite.Statement{SQL: `DELETE FROM alert_rules WHERE id=?`, Params: []interface{}{id}})
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		if result.RowsAffected == 0 {
			writeError(w, 404, "rule not found")
			return
		}
		w.WriteHeader(204)
	}
}

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	query := `SELECT id,rule_id,stack_id,node_id,severity,state,message,silenced_until,resolved_at,created_at FROM alerts WHERE 1=1`
	args := []interface{}{}
	if id := chi.URLParam(r, "id"); id != "" {
		query += " AND id=?"
		args = append(args, id)
	}
	for _, field := range []string{"state", "severity", "stack_id", "node_id"} {
		if value := r.URL.Query().Get(field); value != "" {
			query += " AND " + field + "=?"
			args = append(args, value)
		}
	}
	s.writeObjects(w, r, query+" ORDER BY created_at DESC", args...)
}
