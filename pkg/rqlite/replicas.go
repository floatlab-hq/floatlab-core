package rqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type ReplicaPeer struct {
	NodeID         string `json:"node_id"`
	Address        string `json:"address"`
	SSHUser        string `json:"ssh_user"`
	IdentityFile   string `json:"identity_file"`
	KnownHostsFile string `json:"known_hosts_file"`
}

type ReplicaStatus struct {
	LocalIdentity
	AppliedAt time.Time `json:"applied_at"`
	Error     string    `json:"error,omitempty"`
}

// Replicator publishes only in response to commits, peer changes or retry timers.
// Its acknowledgements live outside the published database to avoid feedback.
type Replicator struct {
	DB       *Client
	StateDir string
	Peers    func(context.Context) ([]ReplicaPeer, error)
	Transfer func(context.Context, ReplicaPeer, string) (LocalIdentity, error)
	mu       sync.Mutex
	wake     chan struct{}
}

func (r *Replicator) wakeChannel() chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.wake == nil {
		r.wake = make(chan struct{}, 1)
	}
	return r.wake
}

// Wake schedules catch-up when enrollment, removal or peer reconnection changes.
func (r *Replicator) Wake() {
	select {
	case r.wakeChannel() <- struct{}{}:
	default:
	}
}

type peerReplicaWorker struct {
	peer   ReplicaPeer
	wake   chan struct{}
	cancel context.CancelFunc
}

func (r *Replicator) Run(ctx context.Context) error {
	if r.DB == nil || r.DB.LocalPath() == "" || r.Peers == nil || r.Transfer == nil {
		return errors.New("incomplete workload replicator configuration")
	}
	if err := os.MkdirAll(r.StateDir, 0700); err != nil {
		return err
	}
	workers := map[string]*peerReplicaWorker{}
	var wg sync.WaitGroup
	defer func() {
		for _, w := range workers {
			w.cancel()
		}
		wg.Wait()
	}()
	wake := r.wakeChannel()
	retry := time.NewTimer(0)
	defer retry.Stop()
	retryDelay := time.Second
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.DB.Changes():
		case <-wake:
		case <-retry.C:
		}
		peers, err := r.Peers(ctx)
		if err != nil {
			retry.Reset(retryDelay)
			retryDelay = min(retryDelay*2, time.Minute)
		} else {
			retryDelay = time.Second
			keep := map[string]bool{}
			for _, p := range peers {
				if !validNodeID(p.NodeID) {
					return errors.New("invalid replica destination identity")
				}
				if keep[p.NodeID] {
					continue
				}
				keep[p.NodeID] = true
				if old := workers[p.NodeID]; old != nil && old.peer != p {
					old.cancel()
					delete(workers, p.NodeID)
				}
				if workers[p.NodeID] == nil {
					peerCtx, cancel := context.WithCancel(ctx)
					w := &peerReplicaWorker{peer: p, wake: make(chan struct{}, 1), cancel: cancel}
					workers[p.NodeID] = w
					wg.Go(func() { r.runPeer(peerCtx, w) })
				}
			}
			for id, w := range workers {
				if !keep[id] {
					w.cancel()
					delete(workers, id)
				}
			}
		}
		for _, w := range workers {
			select {
			case w.wake <- struct{}{}:
			default:
			}
		}
	}
}

func (r *Replicator) runPeer(ctx context.Context, w *peerReplicaWorker) {
	retry := time.NewTimer(0)
	defer retry.Stop()
	delay := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-retry.C:
		case <-w.wake:
			quiet := time.NewTimer(250 * time.Millisecond)
			limit := time.NewTimer(time.Second)
		debounce:
			for {
				select {
				case <-ctx.Done():
					quiet.Stop()
					limit.Stop()
					return
				case <-w.wake:
					if !quiet.Stop() {
						select {
						case <-quiet.C:
						default:
						}
					}
					quiet.Reset(250 * time.Millisecond)
				case <-quiet.C:
					break debounce
				case <-limit.C:
					break debounce
				}
			}
			quiet.Stop()
			limit.Stop()
		}
		if err := r.synchronizePeer(ctx, w.peer); err != nil {
			retry.Reset(delay)
			delay = min(delay*2, time.Minute)
		} else {
			delay = time.Second
			if !retry.Stop() {
				select {
				case <-retry.C:
				default:
				}
			}
		}
	}
}

func (r *Replicator) synchronizePeer(ctx context.Context, peer ReplicaPeer) error {
	id, err := r.DB.LocalIdentity(ctx)
	if err != nil {
		return err
	}
	if peer.NodeID == id.NodeID {
		return nil
	}
	path := filepath.Join(r.StateDir, peer.NodeID+".json")
	var status ReplicaStatus
	if b, e := os.ReadFile(path); e == nil {
		_ = json.Unmarshal(b, &status)
	}
	if status.Generation == id.Generation && status.Revision >= id.Revision && status.Error == "" {
		return nil
	}
	received, transferErr := r.Transfer(ctx, peer, r.DB.LocalPath())
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if transferErr == nil && (received.NodeID != id.NodeID || received.Generation != id.Generation || received.Revision < id.Revision) {
		transferErr = errors.New("peer acknowledged wrong source or stale revision")
	}
	if transferErr != nil {
		status.Error = transferErr.Error()
	} else {
		status = ReplicaStatus{LocalIdentity: received, AppliedAt: time.Now().UTC()}
	}
	if err = writePrivateJSON(path, status); err != nil {
		return err
	}
	if transferErr != nil {
		return transferErr
	}
	latest, err := r.DB.LocalIdentity(ctx)
	if err != nil {
		return err
	}
	if latest.Generation != received.Generation || latest.Revision > received.Revision {
		r.Wake()
	}
	return nil
}

func validNodeID(s string) bool {
	if s == "" || len(s) > 128 || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func writePrivateJSON(path string, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".replica-state-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open durability directory: %w", err)
	}
	defer f.Close()
	return f.Sync()
}
