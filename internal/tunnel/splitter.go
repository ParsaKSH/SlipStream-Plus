package tunnel

import (
	"context"
	"io"
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
	incoming  chan *Frame

	txSeq atomic.Uint32

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
	}
	ps.recalcWeights()
	return ps
}

func (ps *PacketSplitter) Close() {
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

			if sendErr := ps.pool.SendFrame(inst.ID(), frame); sendErr != nil {
				inst2 := ps.pickInstanceExcluding(inst.ID())
				if inst2 != nil {
					ps.pool.SendFrame(inst2.ID(), frame)
				}
			}
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

			if frame.IsACK() || frame.IsSYN() {
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
		ps.counter = 0
		ps.current = (ps.current + 1) % len(ps.instances)
	}

	for _, inst := range ps.instances {
		if inst.IsHealthy() {
			return inst
		}
	}
	return nil
}

func (ps *PacketSplitter) pickInstanceExcluding(excludeID int) *engine.Instance {
	for _, inst := range ps.instances {
		if inst.ID() != excludeID && inst.IsHealthy() {
			return inst
		}
	}
	return nil
}

