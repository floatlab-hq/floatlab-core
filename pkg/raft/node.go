package raft

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	hraft "github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
	"github.com/floatlab/floatlab-core/pkg/run"
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
	raft *hraft.Raft
	fsm  *FSM
	log  *zap.Logger
}

type Config struct {
	NodeID    string // unique identifier for this peer
	BindAddr  string // TCP address for Raft transport, e.g. "0.0.0.0:7000"
	AdvertiseAddr string // externally reachable Raft address
	DataDir   string // directory for BoltDB log + stable store + snapshots
	Bootstrap bool   // true only for the very first node in a new cluster
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

	stableStore, err := raftboltdb.New(raftboltdb.Options{
		Path:        filepath.Join(cfg.DataDir, "raft-stable.bolt"),
		BoltOptions: boltOpts,
	})
	if err != nil {
		return nil, fmt.Errorf("raft: stable store: %w", err)
	}

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

	return &Node{raft: r, fsm: fsm, log: log}, nil
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

func (n *Node) Shutdown() error { return n.raft.Shutdown().Error() }

