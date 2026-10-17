package control

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	managementapi "github.com/floatlab/floatlab-core/api/openapi"
	floatraft "github.com/floatlab/floatlab-core/pkg/raft"
	"go.uber.org/zap"
)

func TestDevelopmentModeHealth(t *testing.T) {
	node, err := floatraft.NewNode(floatraft.Config{
		NodeID: "test", BindAddr: "127.0.0.1:0", AdvertiseAddr: "127.0.0.1:1", DataDir: t.TempDir(),
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer node.Shutdown()
	for _, dev := range []bool{false, true} {
		s := &Server{cfg: &Config{DevMode: dev}, raft: node, startedAt: time.Now()}
		for _, path := range []string{"/api/v1/health", "/api/v1/health/raft"} {
			response := httptest.NewRecorder()
			request := httptest.NewRequest("GET", path, nil)
			if path == "/api/v1/health" {
				s.handleHealth(response, request)
			} else {
				s.handleHealthRaft(response, request)
			}
			var body struct {
				DevMode *bool `json:"dev_mode"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != 200 || body.DevMode == nil || *body.DevMode != dev {
				t.Fatalf("dev=%v %s: %s", dev, path, response.Body.String())
			}
			if err := managementapi.ValidateJSONResponse("GET", path, response.Code, response.Body.Bytes()); err != nil {
				t.Fatal(err)
			}
		}
	}
}
