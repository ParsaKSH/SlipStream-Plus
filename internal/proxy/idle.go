package proxy

import (
	"net"
	"time"
)

// idleConn wraps a net.Conn and resets the deadline on every Read/Write.
// If no data flows for the given timeout duration, the connection is closed
// automatically by the OS returning a deadline-exceeded error, which causes
// io.CopyBuffer to return and free the associated buffer + goroutine.
type idleConn struct {
	net.Conn
	timeout time.Duration
}

func newIdleConn(c net.Conn, timeout time.Duration) *idleConn {
	return &idleConn{Conn: c, timeout: timeout}
}

func (c *idleConn) Read(b []byte) (int, error) {
	if err := c.Conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Read(b)
}

func (c *idleConn) Write(b []byte) (int, error) {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, err
	}
	return c.Conn.Write(b)
}
