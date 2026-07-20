package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Client connects to a hostd UNIX socket and sends commands.
type Client struct {
	socketPath string
	conn       *Conn
	mu         sync.Mutex
	pending    map[string]chan Response
	events     chan Event
	subs       map[int]chan Event
	nextSub    int
}

func NewClient(socketPath string) *Client {
	return &Client{
		socketPath: socketPath,
		pending:    make(map[string]chan Response),
		events:     make(chan Event, 256),
		subs:       make(map[int]chan Event),
	}
}

func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	nc, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return fmt.Errorf("ipc client: dial %s: %w", c.socketPath, err)
	}
	c.conn = NewConn(nc)
	go c.readLoop(ctx)
	return nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// Events returns the channel on which unsolicited events are delivered.
func (c *Client) Events() <-chan Event { return c.events }

func (c *Client) SubscribeEvents() (<-chan Event, func()) {
	c.mu.Lock()
	id := c.nextSub
	c.nextSub++
	ch := make(chan Event, 64)
	c.subs[id] = ch
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		delete(c.subs, id)
		c.mu.Unlock()
	}
}

func (c *Client) Execute(ctx context.Context, name string, payload any) (json.RawMessage, error) {
	id := uuid.New().String()
	payloadBytes, _ := json.Marshal(payload)
	cmd := Command{Name: name, Payload: json.RawMessage(payloadBytes)}
	cmdBytes, _ := json.Marshal(cmd)

	msg := Message{ID: id, Type: TypeCommand, Payload: json.RawMessage(cmdBytes)}

	ch := make(chan Response, 1)
	c.mu.Lock()
	c.pending[id] = ch
	err := c.conn.Send(msg)
	if err != nil {
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("ipc client: send: %w", err)
	}
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case resp := <-ch:
		if !resp.OK {
			if resp.Error != nil {
				return nil, resp.Error
			}
			return nil, fmt.Errorf("ipc: command failed")
		}
		return resp.Payload, nil
	case <-time.After(30 * time.Second):
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("ipc client: timeout waiting for response to %s", name)
	}
}

func (c *Client) readLoop(ctx context.Context) {
	for {
		msg, err := c.conn.Recv()
		if err != nil {
			return
		}
		switch msg.Type {
		case TypeResponse:
			var resp Response
			if err := json.Unmarshal(msg.Payload, &resp); err != nil {
				continue
			}
			c.mu.Lock()
			ch, ok := c.pending[msg.ID]
			if ok {
				delete(c.pending, msg.ID)
			}
			c.mu.Unlock()
			if ok {
				select {
				case ch <- resp:
				default:
				}
			}
		case TypeEvent:
			var ev Event
			if err := json.Unmarshal(msg.Payload, &ev); err != nil {
				continue
			}
			select {
			case c.events <- ev:
			default:
			}
			c.mu.Lock()
			for _, subscriber := range c.subs {
				select {
				case subscriber <- ev:
				default:
				}
			}
			c.mu.Unlock()
		}
	}
}
