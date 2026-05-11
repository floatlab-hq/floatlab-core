package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
)

const maxFrameBytes = 8 * 1024 * 1024 // 8 MB

// Conn wraps a net.Conn with newline-delimited JSON framing.
type Conn struct {
	conn net.Conn
	r    *bufio.Reader
}

func NewConn(c net.Conn) *Conn {
	return &Conn{conn: c, r: bufio.NewReaderSize(c, 64*1024)}
}

func (c *Conn) Close() error { return c.conn.Close() }

func (c *Conn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

func (c *Conn) Send(msg Message) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("ipc: marshal: %w", err)
	}
	b = append(b, '\n')
	_, err = c.conn.Write(b)
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
