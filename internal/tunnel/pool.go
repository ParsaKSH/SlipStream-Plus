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
// in this long, the connection is considered half-dead.
const staleThreshold = 15 * time.Second

// ConnsPerInstance is the number of persistent connections to maintain
// per healthy instance. When one dies, it's replaced on the next refresh.
// Multiple connections provide redundancy — if one degrades, others serve traffic.
const ConnsPerInstance = 3

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

// TunnelPool manages multiple persistent connections per healthy instance.
// All connections serve the same handler map, providing redundancy.
type TunnelPool struct {
	mgr        *engine.Manager
	mu         sync.RWMutex
	conns      map[int][]*TunnelConn // instance ID → pool of connections
	handlers   sync.Map              // ConnID (uint32) → chan *Frame
	stopCh     chan struct{}
	wg         sync.WaitGroup
	ready      chan struct{}
	readyOnce  sync.Once
	roundRobin atomic.Uint64
}

func NewTunnelPool(mgr *engine.Manager) *TunnelPool {
	return &TunnelPool{
		mgr:    mgr,
		conns:  make(map[int][]*TunnelConn),
		stopCh: make(chan struct{}),
		ready:  make(chan struct{}),
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
	defer p.mu.RUnlock()
	for _, conns := range p.conns {
		for _, tc := range conns {
			if !tc.closed {
				return true
			}
		}
	}
	return false
}

// HasActiveTunnel returns true if the given instance has at least one non-closed tunnel.
func (p *TunnelPool) HasActiveTunnel(instID int) bool {
	p.mu.RLock()
	conns := p.conns[instID]
	p.mu.RUnlock()
	for _, tc := range conns {
		if !tc.closed {
			return true
		}
	}
	return false
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

	log.Printf("[tunnel-pool] started (conns_per_instance=%d, stale_threshold=%s)",
		ConnsPerInstance, staleThreshold)
}

func (p *TunnelPool) Stop() {
	close(p.stopCh)
	p.mu.Lock()
	for _, conns := range p.conns {
		for _, tc := range conns {
			tc.close()
		}
	}
	p.conns = make(map[int][]*TunnelConn)
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

// SendFrame sends a frame through one of the instance's pooled connections.
// Uses round-robin with automatic failover to the next connection on error.
func (p *TunnelPool) SendFrame(instID int, f *Frame) error {
	p.mu.RLock()
	conns := p.conns[instID]
	n := len(conns)
	p.mu.RUnlock()

	if n == 0 {
		return fmt.Errorf("no tunnel for instance %d", instID)
	}

	// Round-robin with fallback: try each connection once
	start := int(p.roundRobin.Add(1)) % n
	for i := 0; i < n; i++ {
		tc := conns[(start+i)%n]
		if tc.closed {
			continue
		}
		if err := tc.writeFrame(f); err != nil {
			// Mark dead — will be cleaned up and replaced by refreshConnections
			tc.close()
			continue
		}
		return nil
	}
	return fmt.Errorf("all %d tunnels for instance %d failed", n, instID)
}

// refreshConnections removes dead/stale connections and replenishes pools.
func (p *TunnelPool) refreshConnections() {
	healthy := p.mgr.HealthyInstances()
	nowMs := time.Now().UnixMilli()

	p.mu.Lock()
	defer p.mu.Unlock()

	activeIDs := make(map[int]bool)
	for _, inst := range healthy {
		activeIDs[inst.ID()] = true
	}

	// Phase 1: Remove dead connections, clean up unhealthy instances
	for id, conns := range p.conns {
		if !activeIDs[id] {
			for _, tc := range conns {
				tc.close()
			}
			delete(p.conns, id)
			continue
		}

		// Filter out closed/stale connections (new slice to avoid sharing backing array)
		var alive []*TunnelConn
		for _, tc := range conns {
			shouldRemove := false
			if tc.closed {
				shouldRemove = true
			} else {
				lastW := tc.lastWrite.Load()
				lastR := tc.lastRead.Load()
				if lastW > 0 && (nowMs-lastR) > staleThreshold.Milliseconds() {
					log.Printf("[tunnel-pool] instance %d: stale conn (last_read=%dms ago), replacing",
						id, nowMs-lastR)
					shouldRemove = true
				}
			}

			if shouldRemove {
				tc.close()
			} else {
				alive = append(alive, tc)
			}
		}

		if len(alive) == 0 {
			delete(p.conns, id)
		} else {
			p.conns[id] = alive
		}
	}

	// Phase 2: Replenish — ensure each healthy instance has ConnsPerInstance connections
	for _, inst := range healthy {
		if inst.Config.Mode == "ssh" {
			continue
		}
		id := inst.ID()
		current := len(p.conns[id])
		need := ConnsPerInstance - current

		if need > 0 {
			added := 0
			for i := 0; i < need; i++ {
				tc, err := p.connectInstance(inst)
				if err != nil {
					break // dial failed, stop trying this instance
				}
				p.conns[id] = append(p.conns[id], tc)
				added++
			}
			if added > 0 {
				log.Printf("[tunnel-pool] instance %d: +%d connections (now %d/%d)",
					id, added, len(p.conns[id]), ConnsPerInstance)
			}
		}
	}

	// Signal readiness
	total := 0
	for _, conns := range p.conns {
		total += len(conns)
	}
	if total > 0 {
		p.readyOnce.Do(func() {
			close(p.ready)
			log.Printf("[tunnel-pool] ready (%d total connections across %d instances)",
				total, len(p.conns))
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
	tc := &TunnelConn{
		inst: inst,
		conn: conn,
	}
	tc.lastRead.Store(now.UnixMilli())
	tc.lastWrite.Store(0)

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.readLoop(tc)
	}()

	return tc, nil
}

// readLoop reads frames WITHOUT any read deadline.
// CRITICAL: ReadFrame must NEVER be interrupted mid-read, because partial
// header reads corrupt the entire frame stream (all subsequent reads get
// misaligned). The only way to stop readLoop is to close the connection
// (via tc.close()), which makes ReadFrame return an error.
//
// Each connection in the pool has its own readLoop. All readLoops dispatch
// to the same handlers map, so frames from any connection reach the right handler.
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
				log.Printf("[tunnel-pool] instance %d: read error: %v", tc.inst.ID(), err)
			}
			tc.close()
			return
		}

		tc.lastRead.Store(time.Now().UnixMilli())

		// ConnID 0 is reserved (keepalive) — skip
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
