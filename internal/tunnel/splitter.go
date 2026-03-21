package tunnel

import (
	"context"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ParsaKSH/SlipStream-Plus/internal/engine"
)

// PacketSplitter distributes data from a client connection across multiple
// instances at the packet/chunk level, and reassembles reverse-direction
// frames back to the client.
type PacketSplitter struct {
	connID    uint32
	pool      *TunnelPool
	instances []*engine.Instance
	chunkSize int
	incoming  chan *Frame // frames coming back from instances for this ConnID

	txSeq atomic.Uint32 // next send sequence number
	rxSeq uint32        // next expected receive sequence (for reorder)

	// Weighted round-robin state
	mu      sync.Mutex
	weights []int // weight per instance (inversely proportional to latency)
	current int   // current instance index
	counter int   // current weight counter
}

// NewPacketSplitter creates a splitter for one client connection.
func NewPacketSplitter(connID uint32, pool *TunnelPool, instances []*engine.Instance, chunkSize int) *PacketSplitter {
	ps := &PacketSplitter{
		connID:    connID,
		pool:      pool,
		instances: instances,
		chunkSize: chunkSize,
		incoming:  pool.RegisterConn(connID),
	}
	ps.recalcWeights()
	return ps
}

// Close unregisters this ConnID from the pool.
func (ps *PacketSplitter) Close() {
	// Send FIN to all instances
	fin := &Frame{
		ConnID: ps.connID,
		SeqNum: ps.txSeq.Add(1) - 1,
		Flags:  FlagFIN,
	}
	for _, inst := range ps.instances {
		ps.pool.SendFrame(inst.ID(), fin)
	}
	ps.pool.UnregisterConn(ps.connID)
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

	// Send SYN through ALL instances so the central server knows about this ConnID
	// from any path that might arrive first
	for _, inst := range ps.instances {
		if err := ps.pool.SendFrame(inst.ID(), frame); err != nil {
			log.Printf("[splitter] conn=%d: SYN to instance %d failed: %v",
				ps.connID, inst.ID(), err)
			// Continue; we only need one to succeed
		}
	}
	return nil
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

			if sendErr := ps.pool.SendFrame(inst.ID(), frame); sendErr != nil {
				log.Printf("[splitter] conn=%d: send to instance %d failed: %v",
					ps.connID, inst.ID(), sendErr)
				// Try another instance
				inst2 := ps.pickInstanceExcluding(inst.ID())
				if inst2 != nil {
					ps.pool.SendFrame(inst2.ID(), frame)
				}
			}

			// Track TX on the selected instance
			inst.AddTx(int64(n))
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
// Faster instances get more weight (more packets routed through them).
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
			ping = maxPing // Unknown latency = worst case
		}
		// Weight = inversely proportional to latency
		// Higher weight = more packets through this instance
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
		if inst.IsHealthy() {
			return inst
		}
		// Skip unhealthy, move to next
		ps.counter = 0
		ps.current = (ps.current + 1) % len(ps.instances)
	}

	// Fallback: return any available
	for _, inst := range ps.instances {
		if inst.IsHealthy() {
			return inst
		}
	}
	return nil
}

// pickInstanceExcluding picks any healthy instance except the excluded one.
func (ps *PacketSplitter) pickInstanceExcluding(excludeID int) *engine.Instance {
	for _, inst := range ps.instances {
		if inst.ID() != excludeID && inst.IsHealthy() {
			return inst
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

// NewReorderer creates a new frame reorderer.
func NewReorderer() *Reorderer {
	return &Reorderer{
		nextSeq: 0,
		buffer:  make(map[uint32][]byte),
		timeout: 10 * time.Second,
	}
}

// Insert adds a frame to the reorder buffer.
func (r *Reorderer) Insert(seq uint32, data []byte) {
	if seq < r.nextSeq {
		return // Already delivered, drop duplicate
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

// SkipGap advances past a missing sequence number (for timeout recovery).
func (r *Reorderer) SkipGap() {
	r.nextSeq++
}
