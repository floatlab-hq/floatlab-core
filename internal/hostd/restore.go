package hostd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	"go.uber.org/zap"
)

const coreServicesPath = "/etc/floatlab/core-services.json"

type coreService struct {
	StackID     string `json:"stack_id"`
	DatasetPath string `json:"dataset_path"`
	ComposeFile string `json:"compose_file"`
}

// RestoreRunner reads core-services.json on boot and starts core services.
// It does NOT touch user stacks; the control plane owns those.
type RestoreRunner struct {
	srv *ipc.Server
	log *zap.Logger
}

func newRestoreRunner(srv *ipc.Server, log *zap.Logger) *RestoreRunner {
	return &RestoreRunner{srv: srv, log: log}
}

func (r *RestoreRunner) Run(ctx context.Context) error {
	data, err := os.ReadFile(coreServicesPath)
	if err != nil {
		if os.IsNotExist(err) {
			r.log.Info("hostd: no core-services.json found; skipping restore")
			return nil
		}
		return fmt.Errorf("hostd: read core-services.json: %w", err)
	}

	var services []coreService
	if err := json.Unmarshal(data, &services); err != nil {
		return fmt.Errorf("hostd: parse core-services.json: %w", err)
	}

	for _, svc := range services {
		r.log.Info("hostd: restoring core service", zap.String("stack_id", svc.StackID))
		r.srv.Emit("compose.up", ipc.ComposeUpPayload{
			StackID:     svc.StackID,
			DatasetPath: svc.DatasetPath,
			ComposeFile: svc.ComposeFile,
		})
	}
	return nil
}

// WriteCoreServices is called by the control plane to persist the list of core
// services that hostd should auto-start on boot.
func WriteCoreServices(services []coreService) error {
	if err := os.MkdirAll("/etc/floatlab", 0750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(services, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(coreServicesPath, b, 0640)
}
