package hostd

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/managementupdate"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	"go.uber.org/zap"
)

func TestPreflightSocketIsDefaultDeny(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	path := filepath.Join(t.TempDir(), "readonly.sock")
	server := ipc.NewServer(path, zap.NewNop())
	production := ipc.NewServer(filepath.Join(t.TempDir(), "production.sock"), zap.NewNop())
	d := newDispatcher(production, nil, zap.NewNop())
	d.register()
	d.registerPreflightReads(server)
	production.Handle("future.destructive.operation", func(context.Context, json.RawMessage) (any, error) {
		t.Error("production mutation invoked")
		return nil, nil
	})
	go func() { _ = server.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for {
		c, e := net.Dial("unix", path)
		if e == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(e)
		}
		time.Sleep(time.Millisecond)
	}
	client := ipc.NewClient(path)
	if e := client.Connect(ctx); e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	if _, e := client.Execute(ctx, "sys.version", nil); e != nil {
		t.Fatalf("audited read denied: %v", e)
	}
	for _, name := range []string{"fs.snapshot.create", "fs.snapshot.rollback", "fs.dataset.destroy", "docker.exec.run", "docker.start", "compose.up", "net.settings.apply", "management.update.start", "host.update.activate", "future.destructive.operation"} {
		if _, e := client.Execute(ctx, name, map[string]any{}); e == nil {
			t.Fatalf("mutation allowed: %s", name)
		} else if rpc, ok := e.(*ipc.RPCError); !ok || rpc.Code != "cmd.unknown" {
			t.Fatalf("%s rejected for wrong reason: %v", name, e)
		}
	}
}
func TestPublicManagementStatusOmitsPrivateCompose(t *testing.T) {
	s := publicManagementStatus(&managementupdate.Status{OperationID: "x", PreviousCompose: []byte("secret"), CandidateCompose: []byte("secret")})
	b, e := json.Marshal(s)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(b, []byte("secret")) || bytes.Contains(b, []byte("compose")) {
		t.Fatal(string(b))
	}
}
