package tunnel

import (
	"context"
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
const staleThreshold = 15 * time.Second

// maxTunnelAge: force-reconnect tunnels older than this, even if they
// appear healthy. Prevents long-lived connection degradation in DNS tunnels.
const maxTunnelAge = 3 * time.Minute

// keepaliveInterval: how often to send keepalive frames to detect dead tunnels.
const keepaliveInterval = 10 * time.Second

// TunnelConn wraps a persistent TCP connection to a single instance.
type TunnelConn struct {
	inst      *engine.Instance
	mu        sync.Mutex
	conn      net.Conn
	writeMu   sync.Mutex
	closed    bool
	createdAt time.Time

	lastRead  atomic.Int64 // unix millis of last successful read
	lastWrite atomic.Int64 // unix millis of last successful write
}

// TunnelPool manages ONE persistent connection per healthy instance.
type TunnelPool struct {
	mgr       *engine.Manager
	mu        sync.RWMutex
	tunnels   map[int]*TunnelConn
	handlers  sync.Map // ConnID (uint32) → chan *Frame
	stopCh    chan struct{}
	wg        sync.WaitGroup
	ready     chan struct{} // closed when at least one tunnel is connected
	readyOnce sync.Once
}

func NewTunnelPool(mgr *engine.Manager) *TunnelPool {
	return &TunnelPool{
		mgr:     mgr,
		tunnels: make(map[int]*TunnelConn),
		stopCh:  make(chan struct{}),
		ready:   make(chan struct{}),
	}
}

// WaitReady blocks until at least one tunnel is connected, or ctx is cancelled.
func (p *TunnelPool) WaitReady(ctx context.Context) bool {
	select {
	case <-p.ready:
		return true
	case <-ctx.Done():
		return false
	}
}

// HasTunnels returns true if at least one tunnel is currently connected.
func (p *TunnelPool) HasTunnels() bool {
	p.mu.RLock()
	n := len(p.tunnels)
	p.mu.RUnlock()
	return n > 0
}

// HasActiveTunnel returns true if the given instance has a non-closed tunnel.
func (p *TunnelPool) HasActiveTunnel(instID int) bool {
	p.mu.RLock()
	tc, ok := p.tunnels[instID]
	p.mu.RUnlock()
	return ok && !tc.closed
}

func (p *TunnelPool) Start() {
	p.refreshConnections()

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		refreshTicker := time.NewTicker(5 * time.Second)
		keepaliveTicker := time.NewTicker(keepaliveInterval)
		defer refreshTicker.Stop()
		defer keepaliveTicker.Stop()
		for {
			select {
			case <-p.stopCh:
				return
			case <-refreshTicker.C:
				p.refreshConnections()
			case <-keepaliveTicker.C:
				p.sendKeepalives()
			}
		}
	}()

	log.Printf("[tunnel-pool] started (stale_threshold=%s, max_age=%s, keepalive=%s)",
		staleThreshold, maxTunnelAge, keepaliveInterval)
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

// sendKeepalives writes a small keepalive frame through each tunnel.
// This detects dead tunnels faster than waiting for stale detection,
// and keeps DNS tunnel sessions alive.
func (p *TunnelPool) sendKeepalives() {
	p.mu.RLock()
	tunnels := make([]*TunnelConn, 0, len(p.tunnels))
	for _, tc := range p.tunnels {
		tunnels = append(tunnels, tc)
	}
	p.mu.RUnlock()

	keepalive := &Frame{
		ConnID:  0, // reserved — CentralServer ignores ConnID 0
		SeqNum:  0,
		Flags:   FlagData,
		Payload: nil,
	}

	for _, tc := range tunnels {
		if err := tc.writeFrame(keepalive); err != nil {
			log.Printf("[tunnel-pool] instance %d: keepalive failed: %v", tc.inst.ID(), err)
			tc.close()
		}
	}
}

// refreshConnections reconnects dead/stale/old tunnels and adds new ones.
func (p *TunnelPool) refreshConnections() {
	healthy := p.mgr.HealthyInstances()
	now := time.Now()
	nowMs := now.UnixMilli()

	p.mu.Lock()
	defer p.mu.Unlock()

	activeIDs := make(map[int]bool)
	for _, inst := range healthy {
		activeIDs[inst.ID()] = true
	}

	for id, tc := range p.tunnels {
		shouldRemove := false
		reason := ""

		if !activeIDs[id] {
			shouldRemove = true
			reason = "instance unhealthy"
		} else if tc.closed {
			shouldRemove = true
			reason = "connection closed"
		} else if now.Sub(tc.createdAt) > maxTunnelAge {
			// Force-reconnect old connections to prevent DNS tunnel degradation
			shouldRemove = true
			reason = fmt.Sprintf("max age exceeded (%s)", now.Sub(tc.createdAt).Round(time.Second))
		} else {
			// Detect half-dead connections
			lastW := tc.lastWrite.Load()
			lastR := tc.lastRead.Load()
			if lastW > 0 && (nowMs-lastR) > staleThreshold.Milliseconds() {
				shouldRemove = true
				reason = fmt.Sprintf("stale (last_read=%dms ago, last_write=%dms ago)", nowMs-lastR, nowMs-lastW)
			}
		}

		if shouldRemove {
			log.Printf("[tunnel-pool] instance %d: removing (%s)", id, reason)
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

	// Signal readiness once we have at least one tunnel
	if len(p.tunnels) > 0 {
		p.readyOnce.Do(func() {
			close(p.ready)
			log.Printf("[tunnel-pool] ready (%d tunnels connected)", len(p.tunnels))
		})
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

	now := time.Now()
	tunnel := &TunnelConn{
		inst:      inst,
		conn:      conn,
		createdAt: now,
	}
	tunnel.lastRead.Store(now.UnixMilli())
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

		frame, err := ReadFrame(tc.conn)
		if err != nil {
			if err != io.EOF && !isClosedErr(err) {
				log.Printf("[tunnel-pool] instance %d read error: %v", tc.inst.ID(), err)
			}
			tc.close()
			return
		}

		tc.lastRead.Store(time.Now().UnixMilli())

		// Skip keepalive responses (ConnID 0)
		if frame.ConnID == 0 {
			continue
		}

		if v, ok := p.handlers.Load(frame.ConnID); ok {
			ch := v.(chan *Frame)
			select {
			case ch <- frame:
			default:
				log.Printf("[tunnel-pool] instance %d: dropping frame conn=%d (buffer full)",
					tc.inst.ID(), frame.ConnID)
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
