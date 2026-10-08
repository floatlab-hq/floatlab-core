package openapi

import "testing"

func TestResponseContractRejectsMissingFieldsAndWrongStatuses(t *testing.T) {
	for _, test := range []struct {
		path   string
		status int
		body   string
		valid  bool
	}{
		{"/api/v1/health", 200, `{"status":"ok","version":"test","uptime_seconds":1}`, true},
		{"/api/v1/health", 200, `{"status":"ok"}`, false},
		{"/api/v1/health", 201, `{}`, false},
		{"/api/v1/nodes/{id}", 200, `{"id":"node1","name":"test","hostname":"host","addresses":[],"status":"unknown","role":"primary","zfs_pool":"floatlab","created_at":"2026-10-08T00:00:00Z","updated_at":"2026-10-08T00:00:00Z"}`, true},
		{"/api/v1/stacks/validate", 204, `{}`, false},
		{"/api/v1/stacks/validate", 204, ``, true},
	} {
		err := ValidateJSONResponse("GET", test.path, test.status, []byte(test.body))
		if test.path == "/api/v1/stacks/validate" {
			err = ValidateJSONResponse("POST", test.path, test.status, []byte(test.body))
		}
		if (err == nil) != test.valid {
			t.Errorf("%s %d valid=%v err=%v", test.path, test.status, test.valid, err)
		}
	}
}
