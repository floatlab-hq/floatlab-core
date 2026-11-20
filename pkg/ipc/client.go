package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/google/uuid"
)

// ErrOutcomeUnknown means transport loss prevented confirmation that a command exited.
var ErrOutcomeUnknown = errors.New("command outcome unknown")

// Client connects to a hostd UNIX socket and sends commands.
type Client struct {
	socketPath string
	conn       *Conn
	mu         sync.Mutex
	pending    map[string]pendingCommand
	events     chan Event
	subs       map[int]chan Event
	nextSub    int
	done       chan struct{}
}

type commandResult struct {
	response Response
	err      error
}

type pendingCommand struct {
	conn   *Conn
	result chan commandResult
}

func NewClient(socketPath string) *Client {
	return &Client{
		socketPath: socketPath,
		pending:    make(map[string]pendingCommand),
		events:     make(chan Event, 256),
		subs:       make(map[int]chan Event),
		done:       make(chan struct{}),
	}
}

func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return fmt.Errorf("ipc client: already connected")
	}
	nc, err := (&net.Dialer{}).DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		return fmt.Errorf("ipc client: dial %s: %w", c.socketPath, err)
	}
	c.conn = NewConn(nc)
	c.done = make(chan struct{})
	go c.readLoop(c.conn, c.done)
	return nil
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		conn := c.conn
		c.conn = nil
		return conn.Close()
	}
	return nil
}

// Events returns the channel on which unsolicited events are delivered.
func (c *Client) Events() <-chan Event { return c.events }

// Done closes when the current connection's reader exits.
func (c *Client) Done() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.done
}

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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := uuid.New().String()
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("ipc client: marshal payload: %w", err)
	}
	cmd := Command{Name: name, Payload: json.RawMessage(payloadBytes)}
	if deadline, ok := ctx.Deadline(); ok {
		cmd.Deadline = &deadline
	}
	cmdBytes, _ := json.Marshal(cmd)

	msg := Message{ID: id, Type: TypeCommand, Payload: json.RawMessage(cmdBytes)}

	ch := make(chan commandResult, 1)
	c.mu.Lock()
	conn := c.conn
	if conn == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("ipc client: not connected")
	}
	c.pending[id] = pendingCommand{conn: conn, result: ch}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := conn.SendContext(ctx, msg); err != nil {
		return nil, fmt.Errorf("ipc client: send failed; %w: %w", ErrOutcomeUnknown, err)
	}

	var result commandResult
	select {
	case <-ctx.Done():
		if err := conn.Send(Message{ID: id, Type: TypeCancel}); err != nil {
			return nil, fmt.Errorf("ipc client: cancel failed; %w: %w", ErrOutcomeUnknown, err)
		}
		// Wait for the handler to exit before its caller can retry or recover.
		// Handlers must honor cancellation; disconnects have an unknown outcome.
		result = <-ch
	case result = <-ch:
	}
	if result.err != nil {
		return nil, result.err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !result.response.OK {
		if result.response.Error != nil {
			return nil, result.response.Error
		}
		return nil, fmt.Errorf("ipc: command failed")
	}
	return result.response.Payload, nil
}

func (c *Client) readLoop(conn *Conn, done chan struct{}) {
	defer close(done)
	defer conn.Close()
	for {
		msg, err := conn.Recv()
		if err != nil {
			c.mu.Lock()
			// Fail waiters promptly, including cancellation acknowledgments.
			for id, pending := range c.pending {
				if pending.conn == conn {
					pending.result <- commandResult{err: fmt.Errorf("ipc client: connection lost; %w: %w", ErrOutcomeUnknown, err)}
					delete(c.pending, id)
				}
			}
			if c.conn == conn {
				c.conn = nil
			}
			c.mu.Unlock()
			return
		}
		switch msg.Type {
		case TypeResponse:
			var resp Response
			if err := json.Unmarshal(msg.Payload, &resp); err != nil {
				continue
			}
			c.mu.Lock()
			pending, ok := c.pending[msg.ID]
			if ok {
				delete(c.pending, msg.ID)
			}
			c.mu.Unlock()
			if ok {
				select {
				case pending.result <- commandResult{response: resp}:
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
