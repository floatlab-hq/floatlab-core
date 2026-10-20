package hostd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/floatlab/floatlab-core/pkg/ipc"
)

var readCommands = map[string]bool{"sys.info": true, "sys.version": true, "sys.docker.events": true, "docker.list": true, "fs.pool.list": true, "fs.pool.health": true, "fs.dataset.list": true, "fs.snapshot.list": true, "net.config.status": true}

func (d *Dispatcher) handle(name string, h ipc.Handler) {
	if readCommands[name] {
		d.srv.Handle(name, h)
		return
	}
	d.srv.Handle(name, func(ctx context.Context, b json.RawMessage) (any, error) {
		d.mutationMu.Lock()
		_, e := os.Stat("/run/floatlab/activation/blocked")
		if e == nil || !os.IsNotExist(e) {
			d.mutationMu.Unlock()
			return nil, &ipc.RPCError{Code: "update.maintenance", Message: "management update maintenance blocks host mutations"}
		}
		d.mutations++
		d.mutationMu.Unlock()
		defer func() { d.mutationMu.Lock(); d.mutations--; d.mutationMu.Unlock() }()
		return h(ctx, b)
	})
}
func (d *Dispatcher) drainMutations(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for {
		d.mutationMu.Lock()
		active := d.mutations
		d.mutationMu.Unlock()
		if active == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("host mutations did not drain: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
