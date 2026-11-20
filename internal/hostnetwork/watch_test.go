//go:build linux

package hostnetwork

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"go.uber.org/zap"
)

type hotplugBackend struct {
	*testBackend
	calls    chan struct{}
	outcomes chan error
}

func (b *hotplugBackend) Apply(ctx context.Context, _, _ Config, _ string, _ map[string]Service) error {
	b.calls <- struct{}{}
	select {
	case err := <-b.outcomes:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestHotplugRetriesWithoutAnotherLinkChange(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "failed apply"
		if pending {
			name = "pending confirmation"
		}
		t.Run(name, func(t *testing.T) {
			m, base := setupManager(t)
			var change Change
			if pending {
				change = submit(t, m, base)
			}
			backend := &hotplugBackend{testBackend: base, calls: make(chan struct{}, 4), outcomes: make(chan error, 4)}
			m.Backend = backend
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			updates := make(chan netlink.LinkUpdate)
			retries := make(chan time.Time)
			inventories := make(chan string, 4)
			var current atomic.Value
			current.Store("old")
			inventory := func() (string, error) {
				value := current.Load().(string)
				inventories <- value
				return value, nil
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				m.watchLinks(ctx, zap.NewNop(), updates, inventory, retries)
			}()
			wait := func(ch <-chan struct{}) {
				t.Helper()
				select {
				case <-ch:
				case <-time.After(3 * time.Second):
					t.Fatal("watcher did not make progress")
				}
			}
			readInventory := func() {
				t.Helper()
				select {
				case <-inventories:
				case <-time.After(3 * time.Second):
					t.Fatal("watcher did not read inventory")
				}
			}
			tick := func() {
				t.Helper()
				select {
				case retries <- time.Now():
				case <-time.After(3 * time.Second):
					t.Fatal("watcher did not schedule a retry")
				}
			}
			readInventory()
			current.Store("new")
			if !pending {
				backend.outcomes <- errors.New("transient apply failure")
			}
			updates <- netlink.LinkUpdate{}
			readInventory()
			if pending {
				// Consuming a retry proves the original update finished while Pending was set.
				tick()
				readInventory()
				backend.outcomes <- nil
				if _, err := m.Confirm(change.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				wait(backend.calls)
				backend.outcomes <- nil
			}
			select {
			case <-backend.calls:
				// The in-flight retry may have observed confirmation already.
			case retries <- time.Now():
				readInventory()
				wait(backend.calls)
			case <-time.After(3 * time.Second):
				t.Fatal("watcher did not retry the unchanged inventory")
			}
			// A notification caused by our own successful apply must not apply again.
			updates <- netlink.LinkUpdate{}
			readInventory()
			cancel()
			wait(done)
			if len(backend.calls) != 0 {
				t.Fatal("unchanged inventory triggered another apply")
			}
		})
	}
}
