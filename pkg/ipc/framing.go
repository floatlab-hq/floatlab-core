package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

const maxFrameBytes = 8 * 1024 * 1024 // 8 MB

// Conn wraps a net.Conn with newline-delimited JSON framing.
type Conn struct {
	conn    net.Conn
	r       *bufio.Reader
	writing chan struct{}
}

func NewConn(c net.Conn) *Conn {
	return &Conn{conn: c, r: bufio.NewReaderSize(c, 64*1024), writing: make(chan struct{}, 1)}
}

func (c *Conn) Close() error { return c.conn.Close() }

func (c *Conn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

func (c *Conn) Send(msg Message) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return c.SendContext(ctx, msg)
}

func (c *Conn) SendContext(ctx context.Context, msg Message) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("ipc: marshal: %w", err)
	}
	b = append(b, '\n')
	select {
	case c.writing <- struct{}{}:
		defer func() { <-c.writing }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = c.conn.SetWriteDeadline(time.Now())
		close(interrupted)
	})
	_, err = c.conn.Write(b)
	if !stop() {
		<-interrupted
	}
	_ = c.conn.SetWriteDeadline(time.Time{})
	if err != nil {
		// A partial JSON frame makes the shared stream unusable.
		_ = c.Close()
	}
	return err
}

func (c *Conn) Recv() (Message, error) {
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		if err == io.EOF {
			return Message{}, fmt.Errorf("ipc: connection closed")
		}
		return Message{}, fmt.Errorf("ipc: read: %w", err)
	}
	if len(line) > maxFrameBytes {
		return Message{}, fmt.Errorf("ipc: frame too large (%d bytes)", len(line))
	}
	var msg Message
	if err := json.Unmarshal(line, &msg); err != nil {
		return Message{}, fmt.Errorf("ipc: unmarshal: %w", err)
	}
	return msg, nil
}
