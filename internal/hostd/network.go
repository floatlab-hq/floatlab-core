package hostd

import (
	"context"
	"encoding/json"
	"github.com/floatlab/floatlab-core/internal/hostnetwork"
)

func (d *Dispatcher) registerNetwork() {
	d.srv.Handle("net.settings.retry", func(_ context.Context, raw json.RawMessage) (any, error) {
		var p hostnetwork.ApplyPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, hostnetwork.Error("invalid", "invalid retry payload")
		}
		return d.network.Retry(p)
	})
	d.srv.Handle("net.service.sync", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var services []hostnetwork.Service
		if err := json.Unmarshal(raw, &services); err != nil {
			return nil, err
		}
		err := d.network.SyncServices(ctx, services)
		return map[string]bool{"ok": err == nil}, err
	})
	d.srv.Handle("net.settings.get", func(ctx context.Context, _ json.RawMessage) (any, error) { return d.network.Status(ctx) })
	d.srv.Handle("net.settings.apply", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p hostnetwork.ApplyPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, hostnetwork.Error("invalid", "invalid apply payload")
		}
		return d.network.Submit(ctx, p)
	})
	for name, action := range map[string]string{"net.settings.change": "get", "net.settings.confirm": "confirm", "net.settings.rollback": "rollback"} {
		action := action
		d.srv.Handle(name, func(_ context.Context, raw json.RawMessage) (any, error) {
			var p struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, hostnetwork.Error("invalid", "invalid change payload")
			}
			switch action {
			case "confirm":
				return d.network.Confirm(p.ID)
			case "rollback":
				return d.network.Rollback(p.ID, false)
			default:
				return d.network.Get(p.ID)
			}
		})
	}
	for name, add := range map[string]bool{"net.service.add": true, "net.service.del": false} {
		add := add
		d.srv.Handle(name, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var p hostnetwork.Service
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, hostnetwork.Error("invalid", "invalid service payload")
			}
			err := d.network.SetService(ctx, p, add)
			return map[string]bool{"ok": err == nil}, err
		})
	}
	d.srv.Handle("net.settings.default.migrate", func(_ context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			PoolID string `json:"pool_id"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		err := d.network.MigrateDefault(p.PoolID)
		return map[string]bool{"ok": err == nil}, err
	})
}
