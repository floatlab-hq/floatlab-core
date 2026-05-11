package hostclient

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	"go.uber.org/zap"
)

// Pool manages one IPC client connection per registered node.
// The control plane uses this to issue commands to any host daemon.
type Pool struct {
	mu      sync.RWMutex
	clients map[string]*ipc.Client // key: node ID
	sockets map[string]string      // key: node ID, value: socket path
	log     *zap.Logger
}

func NewPool(log *zap.Logger) *Pool {
	return &Pool{
		clients: make(map[string]*ipc.Client),
		sockets: make(map[string]string),
		log:     log,
	}
}

// Register adds or replaces the socket path for a node.
// The connection is established lazily on first use.
func (p *Pool) Register(nodeID, socketPath string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.clients[nodeID]; ok {
		_ = old.Close()
		delete(p.clients, nodeID)
	}
	p.sockets[nodeID] = socketPath
}

// Execute sends a command to the specified node and returns the raw response payload.
func (p *Pool) Execute(ctx context.Context, nodeID, command string, payload any) (json.RawMessage, error) {
	c, err := p.getOrConnect(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	result, err := c.Execute(ctx, command, payload)
	if err != nil {
		// Drop the connection on error; next call will reconnect.
		p.mu.Lock()
		_ = c.Close()
		delete(p.clients, nodeID)
		p.mu.Unlock()
		return nil, fmt.Errorf("hostclient: execute %s on %s: %w", command, nodeID, err)
	}
	return result, nil
}

// Events returns the event channel for the given node, or nil if not connected.
func (p *Pool) Events(nodeID string) <-chan ipc.Event {
	p.mu.RLock()
	c, ok := p.clients[nodeID]
	p.mu.RUnlock()
	if !ok {
		return nil
	}
	return c.Events()
}

func (p *Pool) getOrConnect(ctx context.Context, nodeID string) (*ipc.Client, error) {
	p.mu.RLock()
	c, ok := p.clients[nodeID]
	p.mu.RUnlock()
	if ok {
		return c, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	// Re-check under write lock.
	if c, ok = p.clients[nodeID]; ok {
		return c, nil
	}
	socketPath, ok := p.sockets[nodeID]
	if !ok {
		return nil, fmt.Errorf("hostclient: node %s not registered", nodeID)
	}
	nc := ipc.NewClient(socketPath)
	if err := nc.Connect(ctx); err != nil {
		return nil, fmt.Errorf("hostclient: connect to %s at %s: %w", nodeID, socketPath, err)
	}
	p.clients[nodeID] = nc
	p.log.Info("hostclient: connected to node", zap.String("node", nodeID), zap.String("socket", socketPath))
	return nc, nil
}

func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, c := range p.clients {
		_ = c.Close()
		delete(p.clients, id)
	}
}
