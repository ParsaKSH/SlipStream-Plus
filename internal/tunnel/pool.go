package tunnel

import (
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/ParsaKSH/SlipStream-Plus/internal/engine"
)

// TunnelPool manages per-user-connection tunnel connections.
// Instead of maintaining persistent connections (which degrade over QUIC),
// each user connection gets its own fresh TCP connections to instances.
// This mirrors the proven connection-level approach but with multiplexing.
type TunnelPool struct {
	mgr      *engine.Manager
	handlers sync.Map // ConnID (uint32) → chan *Frame
	stopCh   chan struct{}
}

// NewTunnelPool creates a new tunnel pool.
func NewTunnelPool(mgr *engine.Manager) *TunnelPool {
	return &TunnelPool{
		mgr:    mgr,
		stopCh: make(chan struct{}),
	}
}

// Start initializes the pool.
func (p *TunnelPool) Start() {
	log.Printf("[tunnel-pool] started (per-connection mode)")
}

// Stop signals all operations to cease.
func (p *TunnelPool) Stop() {
	close(p.stopCh)
	log.Printf("[tunnel-pool] stopped")
}

// RegisterConn creates a channel for receiving frames for a given ConnID.
func (p *TunnelPool) RegisterConn(connID uint32) chan *Frame {
	ch := make(chan *Frame, 256)
	p.handlers.Store(connID, ch)
	return ch
}

// UnregisterConn removes the handler for a ConnID.
func (p *TunnelPool) UnregisterConn(connID uint32) {
	if v, ok := p.handlers.LoadAndDelete(connID); ok {
		ch := v.(chan *Frame)
		close(ch)
	}
}

// DialInstance creates a fresh TCP connection to an instance and starts
// a read loop that dispatches incoming frames to registered handlers.
// Returns the connection and a cleanup function.
// The caller is responsible for calling cleanup when done.
func (p *TunnelPool) DialInstance(inst *engine.Instance) (net.Conn, func(), error) {
	conn, err := inst.Dial()
	if err != nil {
		return nil, nil, err
	}

	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
		tc.SetNoDelay(true)
	}

	closed := make(chan struct{})
	cleanup := func() {
		select {
		case <-closed:
			return // already closed
		default:
			close(closed)
			conn.Close()
		}
	}

	// Read loop: dispatch incoming frames to handlers
	go func() {
		defer conn.Close()
		for {
			select {
			case <-p.stopCh:
				return
			case <-closed:
				return
			default:
			}

			frame, err := ReadFrame(conn)
			if err != nil {
				if err != io.EOF {
					log.Printf("[tunnel-pool] read from instance %d: %v", inst.ID(), err)
				}
				return
			}

			if v, ok := p.handlers.Load(frame.ConnID); ok {
				ch := v.(chan *Frame)
				select {
				case ch <- frame:
				default:
					// Handler buffer full, drop
				}
			}
		}
	}()

	return conn, cleanup, nil
}

// SendFrame writes a frame directly to a connection.
// This is a convenience wrapper used by PacketSplitter.
func SendFrameTo(conn net.Conn, f *Frame) error {
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := WriteFrame(conn, f)
	conn.SetWriteDeadline(time.Time{})
	return err
}
