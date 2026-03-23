package tunnel

import (
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ParsaKSH/SlipStream-Plus/internal/engine"
)

// tunnelMaxAge is how long a tunnel connection lives before being recycled.
// Prevents QUIC stream degradation from long-lived connections.
const tunnelMaxAge = 2 * time.Minute

// writeTimeout is the maximum time a single frame write can take.
// Prevents blocking all connections when QUIC flow control stalls.
const writeTimeout = 10 * time.Second

// TunnelConn wraps a persistent TCP connection to a single instance.
// It multiplexes many logical connections (ConnIDs) over one TCP stream.
type TunnelConn struct {
	inst      *engine.Instance
	mu        sync.Mutex
	conn      net.Conn
	writeMu   sync.Mutex
	closed    bool
	incoming  chan *Frame
	createdAt time.Time
	lastRead  atomic.Int64 // unix timestamp of last successful read
	writeErrs atomic.Int32 // consecutive write errors
}

// TunnelPool manages persistent connections to all healthy instances.
// Frames are written through the pool, and incoming frames are demuxed
// back to per-ConnID handlers.
type TunnelPool struct {
	mgr      *engine.Manager
	mu       sync.RWMutex
	tunnels  map[int]*TunnelConn // instance ID → tunnel connection
	handlers sync.Map            // ConnID (uint32) → chan *Frame
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

// NewTunnelPool creates a pool with connections to all instances.
func NewTunnelPool(mgr *engine.Manager) *TunnelPool {
	return &TunnelPool{
		mgr:     mgr,
		tunnels: make(map[int]*TunnelConn),
		stopCh:  make(chan struct{}),
	}
}

// Start connects to all instances and begins reading frames.
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

	log.Printf("[tunnel-pool] started (max_age=%s, write_timeout=%s)", tunnelMaxAge, writeTimeout)
}

// Stop closes all tunnel connections.
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

// SendFrame writes a frame through a specific instance's tunnel.
func (p *TunnelPool) SendFrame(instID int, f *Frame) error {
	p.mu.RLock()
	tc, ok := p.tunnels[instID]
	p.mu.RUnlock()

	if !ok {
		return fmt.Errorf("no tunnel for instance %d", instID)
	}

	return tc.writeFrame(f)
}

// HealthyTunnelIDs returns instance IDs that have active tunnel connections.
func (p *TunnelPool) HealthyTunnelIDs() []int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	ids := make([]int, 0, len(p.tunnels))
	for id, tc := range p.tunnels {
		if !tc.closed {
			ids = append(ids, id)
		}
	}
	return ids
}

// refreshConnections ensures we have a tunnel to every healthy instance.
// Also recycles connections that have exceeded their max age.
func (p *TunnelPool) refreshConnections() {
	healthy := p.mgr.HealthyInstances()
	now := time.Now()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Remove tunnels that are unhealthy, closed, too old, or have too many write errors
	activeIDs := make(map[int]bool)
	for _, inst := range healthy {
		activeIDs[inst.ID()] = true
	}
	for id, tc := range p.tunnels {
		shouldRemove := false

		if !activeIDs[id] || tc.closed {
			shouldRemove = true
		} else if now.Sub(tc.createdAt) > tunnelMaxAge {
			// Connection too old → recycle to get a fresh QUIC stream
			log.Printf("[tunnel-pool] recycling instance %d connection (age=%s)",
				id, now.Sub(tc.createdAt).Round(time.Second))
			shouldRemove = true
		} else if tc.writeErrs.Load() > 3 {
			// Too many consecutive write errors
			log.Printf("[tunnel-pool] recycling instance %d connection (write_errors=%d)",
				id, tc.writeErrs.Load())
			shouldRemove = true
		}

		if shouldRemove {
			tc.close()
			delete(p.tunnels, id)
		}
	}

	// Connect to healthy instances that don't have a tunnel
	for _, inst := range healthy {
		if inst.Config.Mode == "ssh" {
			continue
		}
		if _, exists := p.tunnels[inst.ID()]; exists {
			continue
		}
		tc, err := p.connectInstance(inst)
		if err != nil {
			log.Printf("[tunnel-pool] connect to instance %d failed: %v", inst.ID(), err)
			continue
		}
		p.tunnels[inst.ID()] = tc
		log.Printf("[tunnel-pool] connected to instance %d (%s:%d)",
			inst.ID(), inst.Config.Domain, inst.Config.Port)
	}
}

// connectInstance establishes a persistent TCP connection to an instance
// and starts reading frames from it.
func (p *TunnelPool) connectInstance(inst *engine.Instance) (*TunnelConn, error) {
	conn, err := inst.Dial()
	if err != nil {
		return nil, fmt.Errorf("dial instance %d: %w", inst.ID(), err)
	}

	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
		tc.SetNoDelay(true)
	}

	tunnel := &TunnelConn{
		inst:      inst,
		conn:      conn,
		incoming:  make(chan *Frame, 256),
		createdAt: time.Now(),
	}
	tunnel.lastRead.Store(time.Now().Unix())

	// Start reader goroutine for this tunnel
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.readLoop(tunnel)
	}()

	return tunnel, nil
}

// readLoop continuously reads frames from a tunnel and dispatches them
// to the appropriate ConnID handler.
func (p *TunnelPool) readLoop(tc *TunnelConn) {
	for {
		select {
		case <-p.stopCh:
			return
		default:
		}

		frame, err := ReadFrame(tc.conn)
		if err != nil {
			if err != io.EOF {
				log.Printf("[tunnel-pool] read from instance %d: %v", tc.inst.ID(), err)
			}
			tc.close()
			return
		}

		tc.lastRead.Store(time.Now().Unix())

		// Route frame to the registered handler for this ConnID
		if v, ok := p.handlers.Load(frame.ConnID); ok {
			ch := v.(chan *Frame)
			select {
			case ch <- frame:
			default:
				log.Printf("[tunnel-pool] handler buffer full for conn %d, dropping frame seq=%d",
					frame.ConnID, frame.SeqNum)
			}
		}
	}
}

// writeFrame thread-safely writes a frame to the tunnel connection.
// Includes a write deadline to prevent indefinite blocking.
func (tc *TunnelConn) writeFrame(f *Frame) error {
	tc.writeMu.Lock()
	defer tc.writeMu.Unlock()

	if tc.closed {
		return fmt.Errorf("tunnel closed")
	}

	// Set write deadline to prevent blocking forever on stalled QUIC streams
	tc.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	err := WriteFrame(tc.conn, f)
	tc.conn.SetWriteDeadline(time.Time{}) // clear deadline

	if err != nil {
		tc.writeErrs.Add(1)
		return err
	}
	tc.writeErrs.Store(0) // reset on success
	return nil
}

// close marks the tunnel as closed and closes the underlying connection.
func (tc *TunnelConn) close() {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if !tc.closed {
		tc.closed = true
		tc.conn.Close()
	}
}
