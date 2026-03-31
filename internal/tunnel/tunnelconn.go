package tunnel

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ParsaKSH/SlipStream-Plus/internal/engine"
)

// writeTimeout prevents writes from blocking forever on stalled connections.
const writeTimeout = 10 * time.Second

// staleThreshold: if we've sent data but haven't received anything
// in this long, the connection is considered half-dead.
const staleThreshold = 15 * time.Second

// TunnelConn wraps a persistent TCP connection to a single instance.
type TunnelConn struct {
	inst    *engine.Instance
	mu      sync.Mutex
	conn    net.Conn
	writeMu sync.Mutex
	closed  bool

	lastRead  atomic.Int64 // unix millis of last successful read
	lastWrite atomic.Int64 // unix millis of last successful write
}

func (tc *TunnelConn) writeFrame(f *Frame) error {
	tc.writeMu.Lock()
	defer tc.writeMu.Unlock()

	if tc.closed {
		return fmt.Errorf("tunnel closed")
	}

	tc.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	err := WriteFrame(tc.conn, f)
	tc.conn.SetWriteDeadline(time.Time{})

	if err == nil {
		tc.lastWrite.Store(time.Now().UnixMilli())
	}
	return err
}

func (tc *TunnelConn) close() {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if !tc.closed {
		tc.closed = true
		tc.conn.Close()
	}
}

func isClosedErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "use of closed network connection") ||
		strings.Contains(err.Error(), "connection reset by peer")
}
