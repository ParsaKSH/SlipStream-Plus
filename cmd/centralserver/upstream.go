package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"github.com/ParsaKSH/SlipStream-Plus/internal/tunnel"
)

func (cs *centralServer) handleSYN(frame *tunnel.Frame, source *sourceConn) {
	connID := frame.ConnID

	cs.mu.Lock()
	if existing, ok := cs.conns[connID]; ok {
		// Another instance's SYN → just register additional source
		existing.mu.Lock()
		existing.sources = append(existing.sources, source)
		existing.mu.Unlock()
		cs.mu.Unlock()
		return
	}

	atyp, addr, port, err := tunnel.DecodeSYNPayload(frame.Payload)
	if err != nil {
		cs.mu.Unlock()
		log.Printf("[central] conn=%d: bad SYN payload: %v", connID, err)
		return
	}

	var targetAddr string
	switch atyp {
	case 0x01:
		targetAddr = fmt.Sprintf("%s:%d", net.IP(addr).String(), binary.BigEndian.Uint16(port))
	case 0x03:
		domLen := int(addr[0])
		targetAddr = fmt.Sprintf("%s:%d", string(addr[1:1+domLen]), binary.BigEndian.Uint16(port))
	case 0x04:
		targetAddr = fmt.Sprintf("[%s]:%d", net.IP(addr).String(), binary.BigEndian.Uint16(port))
	}

	now := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	state := &connState{
		reorderer:  tunnel.NewReordererAt(frame.SeqNum + 1), // skip SYN's SeqNum
		sources:    []*sourceConn{source},
		cancel:     cancel,
		created:    now,
		lastActive: now,
	}
	cs.conns[connID] = state
	cs.mu.Unlock()

	log.Printf("[central] conn=%d: SYN → target=%s", connID, targetAddr)
	go cs.connectUpstream(ctx, connID, state, atyp, addr, port, targetAddr)
}

func (cs *centralServer) connectUpstream(ctx context.Context, connID uint32, state *connState,
	atyp byte, addr, port []byte, targetAddr string) {

	upConn, err := net.DialTimeout("tcp", cs.socksUpstream, 10*time.Second)
	if err != nil {
		log.Printf("[central] conn=%d: upstream dial failed: %v", connID, err)
		cs.sendFrame(connID, &tunnel.Frame{ConnID: connID, Flags: tunnel.FlagRST | tunnel.FlagReverse})
		cs.removeConn(connID)
		return
	}
	if tc, ok := upConn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
		tc.SetNoDelay(true)
	}

	// SOCKS5 handshake
	pipelined := make([]byte, 0, 3+4+len(addr)+2)
	pipelined = append(pipelined, 0x05, 0x01, 0x00)
	pipelined = append(pipelined, 0x05, 0x01, 0x00, atyp)
	pipelined = append(pipelined, addr...)
	pipelined = append(pipelined, port...)

	if _, err := upConn.Write(pipelined); err != nil {
		log.Printf("[central] conn=%d: upstream write failed: %v", connID, err)
		upConn.Close()
		cs.sendFrame(connID, &tunnel.Frame{ConnID: connID, Flags: tunnel.FlagRST | tunnel.FlagReverse})
		cs.removeConn(connID)
		return
	}

	// Read greeting response (2 bytes) + CONNECT response header (4 bytes)
	resp := make([]byte, 6)
	if _, err := io.ReadFull(upConn, resp); err != nil {
		log.Printf("[central] conn=%d: upstream response read failed: %v", connID, err)
		upConn.Close()
		cs.sendFrame(connID, &tunnel.Frame{ConnID: connID, Flags: tunnel.FlagRST | tunnel.FlagReverse})
		cs.removeConn(connID)
		return
	}

	// Check CONNECT result BEFORE draining bind address.
	// resp[3] = REP field (0x00 = success). If non-zero, upstream may close
	// without sending bind address, so don't try to drain it.
	if resp[3] != 0x00 {
		log.Printf("[central] conn=%d: upstream CONNECT rejected: 0x%02x", connID, resp[3])
		upConn.Close()
		cs.sendFrame(connID, &tunnel.Frame{ConnID: connID, Flags: tunnel.FlagRST | tunnel.FlagReverse})
		cs.removeConn(connID)
		return
	}

	// Drain bind address (only on success)
	switch resp[5] {
	case 0x01:
		io.ReadFull(upConn, make([]byte, 6))
	case 0x03:
		lb := make([]byte, 1)
		io.ReadFull(upConn, lb)
		io.ReadFull(upConn, make([]byte, int(lb[0])+2))
	case 0x04:
		io.ReadFull(upConn, make([]byte, 18))
	default:
		io.ReadFull(upConn, make([]byte, 6))
	}

	// Create async write channel and set target under lock
	writeCh := make(chan []byte, 256)

	state.mu.Lock()
	state.target = upConn
	state.writeCh = writeCh

	// Drain any data that arrived before upstream was ready
	var flushChunks [][]byte
	for {
		data := state.reorderer.Next()
		if data == nil {
			break
		}
		flushChunks = append(flushChunks, data)
	}
	state.mu.Unlock()

	// Start async writer goroutine — all writes to upstream go through writeCh
	go cs.upstreamWriter(ctx, connID, upConn, writeCh)

	// Send flush data through the channel (channel is empty, won't block)
	for _, data := range flushChunks {
		select {
		case writeCh <- data:
		default:
		}
	}

	log.Printf("[central] conn=%d: upstream connected to %s", connID, targetAddr)

	// Read upstream data and send back through tunnel (NO broadcast — round-robin)
	cs.relayUpstreamToTunnel(ctx, connID, state, upConn)
}

func (cs *centralServer) relayUpstreamToTunnel(ctx context.Context, connID uint32,
	state *connState, upstream net.Conn) {

	defer func() {
		upstream.Close()
		cs.sendFrame(connID, &tunnel.Frame{
			ConnID: connID,
			Flags:  tunnel.FlagFIN | tunnel.FlagReverse,
		})
		cs.removeConn(connID)
	}()

	buf := make([]byte, tunnel.MaxPayloadSize)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		n, err := upstream.Read(buf)
		if n > 0 {
			state.mu.Lock()
			seq := state.txSeq
			state.txSeq++
			state.lastActive = time.Now()
			state.mu.Unlock()

			frame := &tunnel.Frame{
				ConnID:  connID,
				SeqNum:  seq,
				Flags:   tunnel.FlagReverse,
				Payload: make([]byte, n),
			}
			copy(frame.Payload, buf[:n])

			// Send through ONE source (round-robin), NOT all
			cs.sendFrame(connID, frame)
		}
		if err != nil {
			if err != io.EOF && !isClosedConnErr(err) {
				log.Printf("[central] conn=%d: upstream read error: %v", connID, err)
			}
			return
		}
	}
}

// upstreamWriter is a dedicated goroutine that drains writeCh and writes
// to the upstream (Xray) connection. This decouples upstream write speed
// from frame dispatch speed — handleData never blocks the frame loop.
func (cs *centralServer) upstreamWriter(ctx context.Context, connID uint32, upstream net.Conn, writeCh chan []byte) {
	for {
		select {
		case <-ctx.Done():
			// Context cancelled (removeConn or cleanup) — drain any remaining data best-effort
			for {
				select {
				case data := <-writeCh:
					upstream.SetWriteDeadline(time.Now().Add(2 * time.Second))
					upstream.Write(data)
				default:
					return
				}
			}
		case data, ok := <-writeCh:
			if !ok {
				return
			}
			upstream.SetWriteDeadline(time.Now().Add(upstreamWriteTimeout))
			if _, err := upstream.Write(data); err != nil {
				log.Printf("[central] conn=%d: upstream write failed: %v", connID, err)
				upstream.SetWriteDeadline(time.Time{})
				// Drain channel to unblock any senders, then exit
				for {
					select {
					case <-writeCh:
					default:
						return
					}
				}
			}
			upstream.SetWriteDeadline(time.Time{})
		}
	}
}

func (cs *centralServer) handleData(frame *tunnel.Frame, source *sourceConn) {
	cs.mu.RLock()
	state, ok := cs.conns[frame.ConnID]
	cs.mu.RUnlock()
	if !ok {
		return
	}

	// Insert frame and collect ready data under lock (fast, no I/O)
	state.mu.Lock()

	// If this source isn't known yet (e.g., after tunnel recycling), add it.
	found := false
	for _, s := range state.sources {
		if s == source {
			found = true
			break
		}
	}
	if !found {
		state.sources = append(state.sources, source)
	}

	state.lastActive = time.Now()
	state.reorderer.Insert(frame.SeqNum, frame.Payload)

	// If upstream not connected yet, data stays in reorderer for later flush
	writeCh := state.writeCh
	if writeCh == nil {
		state.mu.Unlock()
		return
	}

	// Drain all ready data from reorderer
	var chunks [][]byte
	for {
		data := state.reorderer.Next()
		if data == nil {
			break
		}
		chunks = append(chunks, data)
	}
	state.mu.Unlock()

	// Send to async writer (non-blocking — MUST NOT stall the frame dispatch loop)
	for _, data := range chunks {
		select {
		case writeCh <- data:
		default:
			log.Printf("[central] conn=%d: write queue full, dropping %d bytes", frame.ConnID, len(data))
		}
	}
}

func (cs *centralServer) handleFIN(frame *tunnel.Frame) {
	cs.mu.RLock()
	state, ok := cs.conns[frame.ConnID]
	cs.mu.RUnlock()
	if !ok {
		return
	}

	// Drain remaining data and send to async writer (non-blocking)
	state.mu.Lock()
	var chunks [][]byte
	writeCh := state.writeCh
	if writeCh != nil {
		for {
			data := state.reorderer.Next()
			if data == nil {
				break
			}
			chunks = append(chunks, data)
		}
	}
	state.writeCh = nil // prevent further sends from handleData
	state.mu.Unlock()

	// Send remaining data to writer (non-blocking, best-effort)
	if writeCh != nil {
		for _, data := range chunks {
			select {
			case writeCh <- data:
			default:
			}
		}
	}

	// removeConn cancels ctx → upstreamWriter exits → upstream closed by relayUpstreamToTunnel
	cs.removeConn(frame.ConnID)
	log.Printf("[central] conn=%d: FIN received, cleaned up", frame.ConnID)
}

func (cs *centralServer) handleRST(frame *tunnel.Frame) {
	cs.removeConn(frame.ConnID)
	log.Printf("[central] conn=%d: RST received", frame.ConnID)
}
