//go:build linux

package hostnetwork

import (
	"context"
	"github.com/vishvananda/netlink"
	"go.uber.org/zap"
	"time"
)

// Reconcile on link creation/removal. Ignore up/carrier notifications to avoid
// feedback loops from our own reconciliation and DHCP activity.
func (m *Manager) Watch(ctx context.Context, log *zap.Logger) {
	updates := make(chan netlink.LinkUpdate, 64)
	if err := netlink.LinkSubscribe(updates, ctx.Done()); err != nil {
		log.Error("network: link subscription failed", zap.Error(err))
		return
	}
	inventory := func() string {
		_, adapters, _ := discover()
		key := ""
		for _, a := range adapters {
			if a.Eligible {
				key += a.Name + ":" + a.PermanentMAC + ";"
			}
		}
		return key
	}
	last := inventory()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-updates:
			if !ok {
				return
			}
			current := inventory()
			if current == last {
				continue
			}
			last = current
			applyCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
			err := m.locked(func(s *diskState) error {
				if s.Pending != "" {
					return nil
				}
				return m.Backend.Apply(applyCtx, s.Confirmed, s.Confirmed, s.BridgeMAC, s.Services)
			})
			cancel()
			if err != nil {
				log.Error("network: hotplug reconciliation failed", zap.Error(err))
			}
		}
	}
}
