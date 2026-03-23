package tunnel

import (
	"context"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ParsaKSH/SlipStream-Plus/internal/engine"
)

// PacketSplitter distributes data from a client connection across multiple
// instances at the packet/chunk level, and reassembles reverse-direction
// frames back to the client.
//
// Each PacketSplitter creates its own fresh TCP connections to instances
// (instead of using persistent pool connections). This gives each user
// connection fresh QUIC streams, avoiding the degradation that happens
// with long-lived multiplexed streams through DNS tunnels.
type PacketSplitter struct {
	connID    uint32
	pool      *TunnelPool
	instances []*engine.Instance
	chunkSize int
	incoming  chan *Frame // frames coming back from instances for this ConnID

	txSeq atomic.Uint32 // next send sequence number

	// Per-instance connections: fresh TCP for each user connection
	connMu    sync.Mutex
	instConns map[int]net.Conn   // instance ID → TCP connection
	cleanups  []func()           // cleanup functions for all connections

	// Weighted round-robin state
	mu      sync.Mutex
	weights []int
	current int
	counter int
}

// NewPacketSplitter creates a splitter for one client connection.
// It dials fresh TCP connections to each instance.
func NewPacketSplitter(connID uint32, pool *TunnelPool, instances []*engine.Instance, chunkSize int) *PacketSplitter {
	ps := &PacketSplitter{
		connID:    connID,
		pool:      pool,
		instances: instances,
		chunkSize: chunkSize,
		incoming:  pool.RegisterConn(connID),
		instConns: make(map[int]net.Conn),
	}
	ps.recalcWeights()

	// Dial fresh connections to all instances
	for _, inst := range instances {
		conn, cleanup, err := pool.DialInstance(inst)
		if err != nil {
			log.Printf("[splitter] conn=%d: dial instance %d failed: %v",
				connID, inst.ID(), err)
			continue
		}
		ps.instConns[inst.ID()] = conn
		ps.cleanups = append(ps.cleanups, cleanup)
	}

	return ps
}

// Close sends FIN to all instances, closes all connections, and unregisters.
func (ps *PacketSplitter) Close() {
	fin := &Frame{
		ConnID: ps.connID,
		SeqNum: ps.txSeq.Add(1) - 1,
		Flags:  FlagFIN,
	}

	ps.connMu.Lock()
	for _, conn := range ps.instConns {
		SendFrameTo(conn, fin)
	}
	ps.connMu.Unlock()

	// Unregister handler first, then close connections
	ps.pool.UnregisterConn(ps.connID)

	for _, cleanup := range ps.cleanups {
		cleanup()
	}
}

// SendSYN sends a SYN frame (with target address) through all instances.
func (ps *PacketSplitter) SendSYN(atyp byte, addr []byte, port []byte) error {
	payload := EncodeSYNPayload(atyp, addr, port)
	frame := &Frame{
		ConnID:  ps.connID,
		SeqNum:  ps.txSeq.Add(1) - 1,
		Flags:   FlagSYN,
		Payload: payload,
	}

	ps.connMu.Lock()
	defer ps.connMu.Unlock()

	sent := 0
	for id, conn := range ps.instConns {
		if err := SendFrameTo(conn, frame); err != nil {
			log.Printf("[splitter] conn=%d: SYN to instance %d failed: %v",
				ps.connID, id, err)
			continue
		}
		sent++
	}

	if sent == 0 {
		return io.ErrClosedPipe
	}
	return nil
}

// sendFrame sends a frame through a specific instance's connection.
func (ps *PacketSplitter) sendFrame(instID int, f *Frame) error {
	ps.connMu.Lock()
	conn, ok := ps.instConns[instID]
	ps.connMu.Unlock()

	if !ok {
		return io.ErrClosedPipe
	}

	return SendFrameTo(conn, f)
}

// RelayClientToUpstream reads from the client, splits into chunks, and sends
// through instances. Returns total bytes transferred.
func (ps *PacketSplitter) RelayClientToUpstream(ctx context.Context, client io.Reader) int64 {
	buf := make([]byte, ps.chunkSize)
	var totalBytes int64

	for {
		select {
		case <-ctx.Done():
			return totalBytes
		default:
		}

		n, err := client.Read(buf)
		if n > 0 {
			totalBytes += int64(n)

			inst := ps.pickInstance()
			if inst == nil {
				log.Printf("[splitter] conn=%d: no healthy instance available", ps.connID)
				return totalBytes
			}

			frame := &Frame{
				ConnID:  ps.connID,
				SeqNum:  ps.txSeq.Add(1) - 1,
				Flags:   FlagData,
				Payload: make([]byte, n),
			}
			copy(frame.Payload, buf[:n])

			if sendErr := ps.sendFrame(inst.ID(), frame); sendErr != nil {
				log.Printf("[splitter] conn=%d: send to instance %d failed: %v",
					ps.connID, inst.ID(), sendErr)
				// Try another instance
				inst2 := ps.pickInstanceExcluding(inst.ID())
				if inst2 != nil {
					ps.sendFrame(inst2.ID(), frame)
				}
			}
		}

		if err != nil {
			if err != io.EOF {
				log.Printf("[splitter] conn=%d: client read error: %v", ps.connID, err)
			}
			return totalBytes
		}
	}
}

// RelayUpstreamToClient reads frames from the incoming channel (reverse direction),
// reorders them, and writes to the client. Returns total bytes transferred.
func (ps *PacketSplitter) RelayUpstreamToClient(ctx context.Context, client io.Writer) int64 {
	reorderer := NewReorderer()
	var totalBytes int64

	for {
		select {
		case <-ctx.Done():
			return totalBytes
		case frame, ok := <-ps.incoming:
			if !ok {
				return totalBytes
			}

			if frame.IsFIN() || frame.IsRST() {
				// Drain remaining ordered frames
				for {
					data := reorderer.Next()
					if data == nil {
						break
					}
					client.Write(data)
					totalBytes += int64(len(data))
				}
				return totalBytes
			}

			// Skip control frames (ACK, SYN) — only process data
			if frame.IsACK() || frame.IsSYN() {
				continue
			}

			// Skip empty payloads
			if len(frame.Payload) == 0 {
				continue
			}

			reorderer.Insert(frame.SeqNum, frame.Payload)

			// Write all available in-order data
			for {
				data := reorderer.Next()
				if data == nil {
					break
				}
				n, err := client.Write(data)
				totalBytes += int64(n)
				if err != nil {
					return totalBytes
				}
			}
		}
	}
}

// recalcWeights updates the per-instance weights based on latency.
func (ps *PacketSplitter) recalcWeights() {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	ps.weights = make([]int, len(ps.instances))
	maxPing := int64(1)
	for _, inst := range ps.instances {
		p := inst.LastPingMs()
		if p > maxPing {
			maxPing = p
		}
	}

	for i, inst := range ps.instances {
		ping := inst.LastPingMs()
		if ping <= 0 {
			ping = maxPing
		}
		w := int(maxPing / ping)
		if w < 1 {
			w = 1
		}
		ps.weights[i] = w
	}
}

// pickInstance selects the next instance using weighted round-robin.
func (ps *PacketSplitter) pickInstance() *engine.Instance {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	if len(ps.instances) == 0 {
		return nil
	}

	for attempts := 0; attempts < len(ps.instances)*2; attempts++ {
		if ps.counter >= ps.weights[ps.current] {
			ps.counter = 0
			ps.current = (ps.current + 1) % len(ps.instances)
		}
		ps.counter++

		inst := ps.instances[ps.current]
		// Check both health AND that we have a connection
		ps.connMu.Lock()
		_, hasConn := ps.instConns[inst.ID()]
		ps.connMu.Unlock()

		if inst.IsHealthy() && hasConn {
			return inst
		}
		ps.counter = 0
		ps.current = (ps.current + 1) % len(ps.instances)
	}

	// Fallback
	for _, inst := range ps.instances {
		ps.connMu.Lock()
		_, hasConn := ps.instConns[inst.ID()]
		ps.connMu.Unlock()
		if inst.IsHealthy() && hasConn {
			return inst
		}
	}
	return nil
}

// pickInstanceExcluding picks any healthy instance except the excluded one.
func (ps *PacketSplitter) pickInstanceExcluding(excludeID int) *engine.Instance {
	for _, inst := range ps.instances {
		if inst.ID() != excludeID && inst.IsHealthy() {
			ps.connMu.Lock()
			_, hasConn := ps.instConns[inst.ID()]
			ps.connMu.Unlock()
			if hasConn {
				return inst
			}
		}
	}
	return nil
}

// Reorderer buffers out-of-order frames and delivers them in sequence order.
type Reorderer struct {
	nextSeq uint32
	buffer  map[uint32][]byte
	timeout time.Duration
}

// NewReorderer creates a new frame reorderer starting from sequence 0.
func NewReorderer() *Reorderer {
	return &Reorderer{
		nextSeq: 0,
		buffer:  make(map[uint32][]byte),
		timeout: 10 * time.Second,
	}
}

// NewReordererAt creates a reorderer starting from a specific sequence number.
func NewReordererAt(startSeq uint32) *Reorderer {
	return &Reorderer{
		nextSeq: startSeq,
		buffer:  make(map[uint32][]byte),
		timeout: 10 * time.Second,
	}
}

// Insert adds a frame to the reorder buffer.
func (r *Reorderer) Insert(seq uint32, data []byte) {
	if seq < r.nextSeq {
		return
	}
	r.buffer[seq] = data
}

// Next returns the next in-order payload, or nil if waiting for a gap.
func (r *Reorderer) Next() []byte {
	data, ok := r.buffer[r.nextSeq]
	if !ok {
		return nil
	}
	delete(r.buffer, r.nextSeq)
	r.nextSeq++
	return data
}

// Pending returns the number of buffered out-of-order frames.
func (r *Reorderer) Pending() int {
	return len(r.buffer)
}

// SkipGap advances past a missing sequence number.
func (r *Reorderer) SkipGap() {
	r.nextSeq++
}
