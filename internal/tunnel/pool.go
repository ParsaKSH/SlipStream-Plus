package tunnel

import (
	"fmt"
	"io"
	"log"
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
// in this long, the connection is considered half-dead and will be
// force-closed by refreshConnections (which triggers reconnect).
const staleThreshold = 20 * time.Second

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

// TunnelPool manages ONE persistent connection per healthy instance.
type TunnelPool struct {
	mgr      *engine.Manager
	mu       sync.RWMutex
	tunnels  map[int]*TunnelConn
	handlers sync.Map // ConnID (uint32) → chan *Frame
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

func NewTunnelPool(mgr *engine.Manager) *TunnelPool {
	return &TunnelPool{
		mgr:     mgr,
		tunnels: make(map[int]*TunnelConn),
		stopCh:  make(chan struct{}),
	}
}

func (p *TunnelPool) Start() {
	p.refreshConnections()

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-p.stopCh:
				return
			case <-ticker.C:
				p.refreshConnections()
			}
		}
	}()

	log.Printf("[tunnel-pool] started (stale_threshold=%s)", staleThreshold)
}

func (p *TunnelPool) Stop() {
	close(p.stopCh)
	p.mu.Lock()
	for _, tc := range p.tunnels {
		tc.close()
	}
	p.tunnels = make(map[int]*TunnelConn)
	p.mu.Unlock()
	p.wg.Wait()
	log.Printf("[tunnel-pool] stopped")
}

func (p *TunnelPool) RegisterConn(connID uint32) chan *Frame {
	ch := make(chan *Frame, 256)
	p.handlers.Store(connID, ch)
	return ch
}

func (p *TunnelPool) UnregisterConn(connID uint32) {
	if v, ok := p.handlers.LoadAndDelete(connID); ok {
		ch := v.(chan *Frame)
		close(ch)
	}
}

func (p *TunnelPool) SendFrame(instID int, f *Frame) error {
	p.mu.RLock()
	tc, ok := p.tunnels[instID]
	p.mu.RUnlock()

	if !ok {
		return fmt.Errorf("no tunnel for instance %d", instID)
	}

	return tc.writeFrame(f)
}

// refreshConnections reconnects dead/stale tunnels and adds new ones.
func (p *TunnelPool) refreshConnections() {
	healthy := p.mgr.HealthyInstances()
	nowMs := time.Now().UnixMilli()

	p.mu.Lock()
	defer p.mu.Unlock()

	activeIDs := make(map[int]bool)
	for _, inst := range healthy {
		activeIDs[inst.ID()] = true
	}

	for id, tc := range p.tunnels {
		shouldRemove := false

		if !activeIDs[id] || tc.closed {
			shouldRemove = true
		} else {
			// Detect half-dead connections:
			// If we wrote recently but haven't read in staleThreshold,
			// the QUIC tunnel is likely dead but local TCP is still open.
			// Force-close it so readLoop exits and we can reconnect.
			lastW := tc.lastWrite.Load()
			lastR := tc.lastRead.Load()
			if lastW > 0 && (nowMs-lastR) > staleThreshold.Milliseconds() {
				log.Printf("[tunnel-pool] instance %d: stale (last_read=%dms ago, last_write=%dms ago), force-closing",
					id, nowMs-lastR, nowMs-lastW)
				shouldRemove = true
			}
		}

		if shouldRemove {
			tc.close()
			delete(p.tunnels, id)
		}
	}

	for _, inst := range healthy {
		if inst.Config.Mode == "ssh" {
			continue
		}
		if _, exists := p.tunnels[inst.ID()]; exists {
			continue
		}
		tc, err := p.connectInstance(inst)
		if err != nil {
			continue
		}
		p.tunnels[inst.ID()] = tc
		log.Printf("[tunnel-pool] connected to instance %d (%s:%d)",
			inst.ID(), inst.Config.Domain, inst.Config.Port)
	}
}

func (p *TunnelPool) connectInstance(inst *engine.Instance) (*TunnelConn, error) {
	conn, err := inst.Dial()
	if err != nil {
		return nil, err
	}

	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
		tc.SetNoDelay(true)
	}

	now := time.Now().UnixMilli()
	tunnel := &TunnelConn{
		inst: inst,
		conn: conn,
	}
	tunnel.lastRead.Store(now)
	tunnel.lastWrite.Store(0)

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.readLoop(tunnel)
	}()

	return tunnel, nil
}

// readLoop reads frames WITHOUT any read deadline.
// CRITICAL: ReadFrame must NEVER be interrupted mid-read, because partial
// header reads corrupt the entire frame stream (all subsequent reads get
// misaligned). The only way to stop readLoop is to close the connection
// (via tc.close()), which makes ReadFrame return an error.
func (p *TunnelPool) readLoop(tc *TunnelConn) {
	for {
		select {
		case <-p.stopCh:
			return
		default:
		}

		// NO read deadline here! ReadFrame blocks until a complete frame
		// arrives or the connection closes. read deadline would cause
		// partial header reads → stream corruption.
		frame, err := ReadFrame(tc.conn)
		if err != nil {
			if err != io.EOF && !isClosedErr(err) {
				log.Printf("[tunnel-pool] instance %d read error: %v", tc.inst.ID(), err)
			}
			tc.close()
			return
		}

		tc.lastRead.Store(time.Now().UnixMilli())

		if v, ok := p.handlers.Load(frame.ConnID); ok {
			ch := v.(chan *Frame)
			select {
			case ch <- frame:
			default:
				// Buffer full — drop silently
			}
		}
	}
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
