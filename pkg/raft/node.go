package raft

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/floatlab/floatlab-core/pkg/run"
	hraft "github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
	bbolt "go.etcd.io/bbolt"
	"go.uber.org/zap"
)

const (
	SnapshotThreshold = 1024
	SnapshotInterval  = 2 * time.Minute
	// BoltDB mmap cap: keep the Raft log database under 64MB.
	boltMaxMapSize = 64 * 1024 * 1024
)

type Node struct {
	raft                  *hraft.Raft
	fsm                   *FSM
	log                   *zap.Logger
	transport             *hraft.NetworkTransport
	logStore, stableStore *raftboltdb.BoltStore
	shutdownOnce          sync.Once
	shutdownErr           error
}

type Config struct {
	NodeID        string // unique identifier for this peer
	BindAddr      string // TCP address for Raft transport, e.g. "0.0.0.0:7000"
	AdvertiseAddr string // externally reachable Raft address
	DataDir       string // directory for BoltDB log + stable store + snapshots
	Bootstrap     bool   // true only for the very first node in a new cluster
}

func NewNode(cfg Config, log *zap.Logger) (*Node, error) {
	if err := os.MkdirAll(cfg.DataDir, 0750); err != nil {
		return nil, fmt.Errorf("raft: mkdir datadir: %w", err)
	}

	raftCfg := hraft.DefaultConfig()
	raftCfg.LocalID = hraft.ServerID(cfg.NodeID)
	raftCfg.SnapshotThreshold = SnapshotThreshold
	raftCfg.SnapshotInterval = SnapshotInterval

	boltOpts := &bbolt.Options{MmapFlags: 0, InitialMmapSize: int(boltMaxMapSize)}
	logStore, err := raftboltdb.New(raftboltdb.Options{
		Path:        filepath.Join(cfg.DataDir, "raft-log.bolt"),
		BoltOptions: boltOpts,
	})
	if err != nil {
		return nil, fmt.Errorf("raft: log store: %w", err)
	}

	owned := true
	defer func() {
		if owned {
			_ = logStore.Close()
		}
	}()
	stableStore, err := raftboltdb.New(raftboltdb.Options{
		Path:        filepath.Join(cfg.DataDir, "raft-stable.bolt"),
		BoltOptions: boltOpts,
	})
	if err != nil {
		return nil, fmt.Errorf("raft: stable store: %w", err)
	}

	defer func() {
		if owned {
			_ = stableStore.Close()
		}
	}()
	snapshotStore, err := hraft.NewFileSnapshotStore(cfg.DataDir, 2, nil)
	if err != nil {
		return nil, fmt.Errorf("raft: snapshot store: %w", err)
	}

	advertiseAddr, err := net.ResolveTCPAddr("tcp", cfg.AdvertiseAddr)
	if err != nil {
		return nil, fmt.Errorf("raft: resolve advertise addr: %w", err)
	}
	transport, err := hraft.NewTCPTransport(cfg.BindAddr, advertiseAddr, 3, 10*time.Second, nil)
	if err != nil {
		return nil, fmt.Errorf("raft: transport: %w", err)
	}

	defer func() {
		if owned {
			_ = transport.Close()
		}
	}()
	fsm := NewFSM()
	r, err := hraft.NewRaft(raftCfg, fsm, logStore, stableStore, snapshotStore, transport)
	if err != nil {
		return nil, fmt.Errorf("raft: new raft: %w", err)
	}

	if cfg.Bootstrap {
		configuration := hraft.Configuration{
			Servers: []hraft.Server{
				{
					ID:      hraft.ServerID(cfg.NodeID),
					Address: hraft.ServerAddress(cfg.AdvertiseAddr),
				},
			},
		}
		r.BootstrapCluster(configuration)
	}

	owned = false
	return &Node{raft: r, fsm: fsm, log: log, transport: transport, logStore: logStore, stableStore: stableStore}, nil
}

// Apply commits a StackStateChanged entry to the Raft log.
// Must be called on the leader.
func (n *Node) Apply(entry run.StackStateChanged, timeout time.Duration) error {
	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("raft: marshal: %w", err)
	}
	f := n.raft.Apply(b, timeout)
	if err := f.Error(); err != nil {
		return fmt.Errorf("raft: apply: %w", err)
	}
	if resp := f.Response(); resp != nil {
		if err, ok := resp.(error); ok {
			return fmt.Errorf("raft: fsm: %w", err)
		}
	}
	return nil
}

func (n *Node) IsLeader() bool { return n.raft.State() == hraft.Leader }

func (n *Node) Leader() string { return string(n.raft.Leader()) }

func (n *Node) State() hraft.RaftState { return n.raft.State() }

func (n *Node) Stats() map[string]string { return n.raft.Stats() }

func (n *Node) FSM() *FSM { return n.fsm }

func (n *Node) Shutdown() error {
	n.shutdownOnce.Do(func() {
		n.shutdownErr = errors.Join(n.raft.Shutdown().Error(), n.transport.Close(), n.logStore.Close(), n.stableStore.Close())
	})
	return n.shutdownErr
}

// Peers returns the configured Raft server addresses.
func (n *Node) Peers() []string {
	future := n.raft.GetConfiguration()
	if future.Error() != nil {
		return []string{}
	}
	peers := make([]string, 0, len(future.Configuration().Servers))
	for _, server := range future.Configuration().Servers {
		peers = append(peers, string(server.Address))
	}
	return peers
}

// Barrier waits until all preceding committed log entries have been applied.
func (n *Node) Barrier(timeout time.Duration) error { return n.raft.Barrier(timeout).Error() }

func (n *Node) LeadershipChanges() <-chan bool { return n.raft.LeaderCh() }
