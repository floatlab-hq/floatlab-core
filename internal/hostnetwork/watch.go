//go:build linux

package hostnetwork

import (
	"context"
	"time"

	"github.com/vishvananda/netlink"
	"go.uber.org/zap"
)

// Reconcile on link creation/removal. Ignore up/carrier notifications to avoid
// feedback loops from our own reconciliation and DHCP activity.
func (m *Manager) Watch(ctx context.Context, log *zap.Logger) {
	updates := make(chan netlink.LinkUpdate, 64)
	if err := netlink.LinkSubscribe(updates, ctx.Done()); err != nil {
		log.Error("network: link subscription failed", zap.Error(err))
		return
	}
	inventory := func() (string, error) {
		_, adapters, err := discover()
		if err != nil {
			return "", err
		}
		key := ""
		for _, a := range adapters {
			if a.Eligible {
				key += a.Name + ":" + a.PermanentMAC + ";"
			}
		}
		return key, nil
	}
	retry := time.NewTicker(5 * time.Second)
	defer retry.Stop()
	m.watchLinks(ctx, log, updates, inventory, retry.C)
}

func (m *Manager) watchLinks(ctx context.Context, log *zap.Logger, updates <-chan netlink.LinkUpdate, inventory func() (string, error), retryTicks <-chan time.Time) {
	last, initialErr := inventory()
	var retry <-chan time.Time
	if initialErr != nil {
		log.Error("network: initial hotplug inventory failed", zap.Error(initialErr))
		retry = retryTicks
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-updates:
			if !ok {
				return
			}
		case <-retry:
		}
		current, err := inventory()
		if err != nil {
			log.Error("network: hotplug inventory failed", zap.Error(err))
			retry = retryTicks
			continue
		}
		if initialErr == nil && current == last {
			retry = nil
			continue
		}
		applyCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		applied := false
		err = m.locked(func(s *diskState) error {
			if s.Pending != "" {
				return nil
			}
			if err := m.Backend.Apply(applyCtx, s.Confirmed, s.Confirmed, s.BridgeMAC, s.Services); err != nil {
				return err
			}
			applied = true
			return nil
		})
		cancel()
		if err != nil {
			log.Error("network: hotplug reconciliation failed", zap.Error(err))
		}
		if applied {
			last, initialErr = current, nil
			retry = nil
		} else {
			// A failure or pending configuration must not consume the inventory change.
			retry = retryTicks
		}
	}
}
