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

// ackTimeout is how long to wait for a frame ACK before retrying via another instance.
const ackTimeout = 3 * time.Second

// maxFrameRetries is the maximum number of retry attempts per frame.
const maxFrameRetries = 2

// pendingFrame tracks a sent frame awaiting ACK from the CentralServer.
type pendingFrame struct {
	frame   *Frame
	sentAt  time.Time
	instID  int
	retries int
}

// PacketSplitter distributes data from a client connection across multiple
// instances at the packet/chunk level, and reassembles reverse-direction
// frames back to the client.
type PacketSplitter struct {
	connID    uint32
	pool      *TunnelPool
	instances []*engine.Instance
	chunkSize int
	incoming  chan *Frame

	txSeq atomic.Uint32

	// ACK tracking: frames awaiting confirmation from CentralServer
	pendingMu sync.Mutex
	pending   map[uint32]*pendingFrame // SeqNum → pending
	stopRetry chan struct{}

	// Weighted round-robin state
	mu      sync.Mutex
	weights []int
	current int
	counter int
}

func NewPacketSplitter(connID uint32, pool *TunnelPool, instances []*engine.Instance, chunkSize int) *PacketSplitter {
	ps := &PacketSplitter{
		connID:    connID,
		pool:      pool,
		instances: instances,
		chunkSize: chunkSize,
		incoming:  pool.RegisterConn(connID),
		pending:   make(map[uint32]*pendingFrame),
		stopRetry: make(chan struct{}),
	}
	ps.recalcWeights()
	go ps.retryLoop()
	return ps
}

func (ps *PacketSplitter) Close() {
	close(ps.stopRetry)

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

// retryLoop periodically checks for unACKed frames and resends them
// through a different instance.
func (ps *PacketSplitter) retryLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ps.stopRetry:
			return
		case <-ticker.C:
			ps.retryPending()
		}
	}
}

func (ps *PacketSplitter) retryPending() {
	now := time.Now()

	ps.pendingMu.Lock()
	var toRetry []*pendingFrame
	for seq, pf := range ps.pending {
		if now.Sub(pf.sentAt) < ackTimeout {
			continue
		}
		if pf.retries >= maxFrameRetries {
			delete(ps.pending, seq)
			continue
		}
		toRetry = append(toRetry, pf)
	}
	ps.pendingMu.Unlock()

	for _, pf := range toRetry {
		// Pick a different instance than the one that failed
		inst := ps.pickInstanceExcluding(pf.instID)
		if inst == nil {
			inst = ps.pickInstance()
		}
		if inst == nil {
			continue
		}

		if err := ps.pool.SendFrame(inst.ID(), pf.frame); err != nil {
			continue
		}

		ps.pendingMu.Lock()
		pf.retries++
		pf.sentAt = now
		pf.instID = inst.ID()
		ps.pendingMu.Unlock()

		log.Printf("[splitter] conn=%d: retried seq=%d via instance %d (attempt %d/%d)",
			ps.connID, pf.frame.SeqNum, inst.ID(), pf.retries, maxFrameRetries)
	}
}

func (ps *PacketSplitter) SendSYN(atyp byte, addr []byte, port []byte) error {
	payload := EncodeSYNPayload(atyp, addr, port)
	frame := &Frame{
		ConnID:  ps.connID,
		SeqNum:  ps.txSeq.Add(1) - 1,
		Flags:   FlagSYN,
		Payload: payload,
	}

	sent := 0
	for _, inst := range ps.instances {
		if err := ps.pool.SendFrame(inst.ID(), frame); err != nil {
			continue
		}
		sent++
	}
	if sent == 0 {
		return io.ErrClosedPipe
	}
	return nil
}

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
				return totalBytes
			}

			frame := &Frame{
				ConnID:  ps.connID,
				SeqNum:  ps.txSeq.Add(1) - 1,
				Flags:   FlagData,
				Payload: make([]byte, n),
			}
			copy(frame.Payload, buf[:n])

			sentInstID := inst.ID()
			if sendErr := ps.pool.SendFrame(inst.ID(), frame); sendErr != nil {
				inst2 := ps.pickInstanceExcluding(inst.ID())
				if inst2 != nil {
					ps.pool.SendFrame(inst2.ID(), frame)
					sentInstID = inst2.ID()
				}
			}

			// Track frame for ACK — retryLoop will resend if no ACK arrives
			ps.pendingMu.Lock()
			ps.pending[frame.SeqNum] = &pendingFrame{
				frame:  frame,
				sentAt: time.Now(),
				instID: sentInstID,
			}
			ps.pendingMu.Unlock()
		}

		if err != nil {
			return totalBytes
		}
	}
}

func (ps *PacketSplitter) RelayUpstreamToClient(ctx context.Context, client io.Writer) int64 {
	reorderer := NewReorderer()
	var totalBytes int64

	// Periodic ticker to:
	// 1. Drive gap-skip timeouts even when no new frames arrive
	// 2. Detect idle connections (no data for idleLimit → give up)
	gapTicker := time.NewTicker(500 * time.Millisecond)
	defer gapTicker.Stop()

	const idleLimit = 30 * time.Second
	lastActivity := time.Now()

	for {
		select {
		case <-ctx.Done():
			return totalBytes

		case <-gapTicker.C:
			// Periodically drive gap-skip even without new frames
			for {
				data := reorderer.Next()
				if data == nil {
					break
				}
				n, err := client.Write(data)
				totalBytes += int64(n)
				lastActivity = time.Now()
				if err != nil {
					return totalBytes
				}
			}
			// Idle timeout: if nothing happened for too long, give up
			if time.Since(lastActivity) > idleLimit {
				return totalBytes
			}

		case frame, ok := <-ps.incoming:
			if !ok {
				return totalBytes
			}

			lastActivity = time.Now()

			if frame.IsFIN() || frame.IsRST() {
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

			if frame.IsACK() {
				ps.pendingMu.Lock()
				delete(ps.pending, frame.SeqNum)
				ps.pendingMu.Unlock()
				continue
			}
			if frame.IsSYN() {
				continue
			}
			if len(frame.Payload) == 0 {
				continue
			}

			reorderer.Insert(frame.SeqNum, frame.Payload)

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

// blockedInstances returns the set of instance IDs that have at least one
// unACKed frame past ackTimeout. These instances are considered unreliable
// at this moment and must not receive new packets.
// Lock order: pendingMu must be acquired BEFORE mu to avoid deadlock.
func (ps *PacketSplitter) blockedInstances() map[int]bool {
	now := time.Now()
	ps.pendingMu.Lock()
	defer ps.pendingMu.Unlock()
	var blocked map[int]bool
	for _, pf := range ps.pending {
		if now.Sub(pf.sentAt) > ackTimeout {
			if blocked == nil {
				blocked = make(map[int]bool)
			}
			blocked[pf.instID] = true
		}
	}
	return blocked
}

func (ps *PacketSplitter) pickInstance() *engine.Instance {
	blocked := ps.blockedInstances()

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
		if inst.IsHealthy() && !blocked[inst.ID()] {
			return inst
		}
		ps.counter = 0
		ps.current = (ps.current + 1) % len(ps.instances)
	}

	// Fallback: any healthy non-blocked instance
	for _, inst := range ps.instances {
		if inst.IsHealthy() && !blocked[inst.ID()] {
			return inst
		}
	}
	// Last resort: any healthy instance even if blocked (avoid total stall)
	for _, inst := range ps.instances {
		if inst.IsHealthy() {
			return inst
		}
	}
	return nil
}

func (ps *PacketSplitter) pickInstanceExcluding(excludeID int) *engine.Instance {
	blocked := ps.blockedInstances()
	for _, inst := range ps.instances {
		if inst.ID() != excludeID && inst.IsHealthy() && !blocked[inst.ID()] {
			return inst
		}
	}
	// Fallback: any healthy instance excluding the specified one
	for _, inst := range ps.instances {
		if inst.ID() != excludeID && inst.IsHealthy() {
			return inst
		}
	}
	return nil
}

// Reorderer buffers out-of-order frames and delivers them in sequence order.
// If a frame is missing for longer than gapTimeout, it is skipped to prevent
// permanent stalls from lost frames.
type Reorderer struct {
	nextSeq    uint32
	buffer     map[uint32][]byte
	gapTimeout time.Duration
	waitingSince time.Time // when we first started waiting for nextSeq
}

func NewReorderer() *Reorderer {
	return &Reorderer{
		nextSeq:    0,
		buffer:     make(map[uint32][]byte),
		gapTimeout: 2 * time.Second,
	}
}

func NewReordererAt(startSeq uint32) *Reorderer {
	return &Reorderer{
		nextSeq:    startSeq,
		buffer:     make(map[uint32][]byte),
		gapTimeout: 2 * time.Second,
	}
}

func (r *Reorderer) Insert(seq uint32, data []byte) {
	if seq < r.nextSeq {
		return
	}
	r.buffer[seq] = data
}

func (r *Reorderer) Next() []byte {
	// Fast path: next seq is available
	data, ok := r.buffer[r.nextSeq]
	if ok {
		delete(r.buffer, r.nextSeq)
		r.nextSeq++
		r.waitingSince = time.Time{} // reset wait timer
		return data
	}

	// Nothing buffered at all — nothing to skip to
	if len(r.buffer) == 0 {
		r.waitingSince = time.Time{}
		return nil
	}

	// There are buffered frames but nextSeq is missing.
	// Start or check the gap timer.
	now := time.Now()
	if r.waitingSince.IsZero() {
		r.waitingSince = now
		return nil
	}

	if now.Sub(r.waitingSince) < r.gapTimeout {
		return nil // still within grace period
	}

	// Gap timeout expired — skip to the lowest available seq
	r.skipToLowest()
	r.waitingSince = time.Time{}

	data, ok = r.buffer[r.nextSeq]
	if ok {
		delete(r.buffer, r.nextSeq)
		r.nextSeq++
		return data
	}
	return nil
}

// skipToLowest advances nextSeq to the lowest seq number in the buffer.
func (r *Reorderer) skipToLowest() {
	minSeq := r.nextSeq
	found := false
	for seq := range r.buffer {
		if !found || seq < minSeq {
			minSeq = seq
			found = true
		}
	}
	if found && minSeq > r.nextSeq {
		r.nextSeq = minSeq
	}
}

func (r *Reorderer) Pending() int {
	return len(r.buffer)
}

func (r *Reorderer) SkipGap() {
	r.nextSeq++
}
