package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/ParsaKSH/SlipStream-Plus/internal/tunnel"
)

// connState tracks a single reassembled connection.
type connState struct {
	mu        sync.Mutex
	target    net.Conn // connection to the SOCKS upstream
	reorderer *tunnel.Reorderer
	txSeq     uint32 // next sequence number for reverse data
	cancel    context.CancelFunc
	created   time.Time

	// Sources: all tunnel connections that can carry reverse data.
	// We round-robin responses across them (not broadcast).
	sources   []io.Writer
	sourceIdx int
}

// centralServer manages all active connections.
type centralServer struct {
	socksUpstream string

	mu    sync.RWMutex
	conns map[uint32]*connState // ConnID → state
}

func main() {
	listenAddr := flag.String("listen", "0.0.0.0:9500", "listen address for tunnel connections")
	socksUpstream := flag.String("socks-upstream", "127.0.0.1:1080", "upstream SOCKS5 proxy address")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("CentralServer starting...")
	log.Printf("  Listen:         %s", *listenAddr)
	log.Printf("  SOCKS upstream: %s", *socksUpstream)

	cs := &centralServer{
		socksUpstream: *socksUpstream,
		conns:         make(map[uint32]*connState),
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		cs.closeAll()
		os.Exit(0)
	}()

	go cs.cleanupLoop()

	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", *listenAddr, err)
	}
	defer ln.Close()
	log.Printf("CentralServer listening on %s", *listenAddr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("[central] accept error: %v", err)
			continue
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			tc.SetKeepAlive(true)
			tc.SetKeepAlivePeriod(30 * time.Second)
			tc.SetNoDelay(true)
		}
		go cs.handleIncoming(conn)
	}
}

// handleIncoming detects the protocol from the first byte:
//
//	0x05 → SOCKS5 health probe → passthrough to socks-upstream
//	else → framing protocol → read frames
func (cs *centralServer) handleIncoming(conn net.Conn) {
	defer conn.Close()
	remoteAddr := conn.RemoteAddr().String()

	firstByte := make([]byte, 1)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(conn, firstByte); err != nil {
		return
	}
	conn.SetReadDeadline(time.Time{})

	if firstByte[0] == 0x05 {
		cs.handleSOCKS5Passthrough(conn, firstByte[0], remoteAddr)
	} else {
		cs.handleFrameConn(conn, firstByte[0], remoteAddr)
	}
}

// handleSOCKS5Passthrough transparently proxies a SOCKS5 connection.
func (cs *centralServer) handleSOCKS5Passthrough(clientConn net.Conn, firstByte byte, remoteAddr string) {
	upstream, err := net.DialTimeout("tcp", cs.socksUpstream, 10*time.Second)
	if err != nil {
		return
	}
	defer upstream.Close()
	if tc, ok := upstream.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	upstream.Write([]byte{firstByte})

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(upstream, clientConn)
		if tc, ok := upstream.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		io.Copy(clientConn, upstream)
		if tc, ok := clientConn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
}

// handleFrameConn reads framed packets from a tunnel connection.
func (cs *centralServer) handleFrameConn(conn net.Conn, firstByte byte, remoteAddr string) {
	log.Printf("[central] frame connection from %s", remoteAddr)

	// Read remaining header bytes (we already read 1)
	var hdrRest [tunnel.HeaderSize - 1]byte
	if _, err := io.ReadFull(conn, hdrRest[:]); err != nil {
		log.Printf("[central] %s: frame header read error: %v", remoteAddr, err)
		return
	}

	var fullHdr [tunnel.HeaderSize]byte
	fullHdr[0] = firstByte
	copy(fullHdr[1:], hdrRest[:])

	firstFrame := cs.parseHeader(fullHdr, conn, remoteAddr)
	if firstFrame != nil {
		cs.dispatchFrame(firstFrame, conn)
	}

	for {
		frame, err := tunnel.ReadFrame(conn)
		if err != nil {
			if err != io.EOF {
				log.Printf("[central] %s: read error: %v", remoteAddr, err)
			}
			return
		}
		cs.dispatchFrame(frame, conn)
	}
}

func (cs *centralServer) parseHeader(hdr [tunnel.HeaderSize]byte, conn net.Conn, remoteAddr string) *tunnel.Frame {
	length := binary.BigEndian.Uint16(hdr[9:11])
	if length > tunnel.MaxPayloadSize {
		log.Printf("[central] %s: frame payload too large: %d", remoteAddr, length)
		return nil
	}
	var payload []byte
	if length > 0 {
		payload = make([]byte, length)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return nil
		}
	}
	return &tunnel.Frame{
		ConnID:  binary.BigEndian.Uint32(hdr[0:4]),
		SeqNum:  binary.BigEndian.Uint32(hdr[4:8]),
		Flags:   hdr[8],
		Payload: payload,
	}
}

func (cs *centralServer) dispatchFrame(frame *tunnel.Frame, source net.Conn) {
	if frame.IsSYN() {
		cs.handleSYN(frame, source)
		return
	}
	if frame.IsFIN() {
		cs.handleFIN(frame)
		return
	}
	if frame.IsRST() {
		cs.handleRST(frame)
		return
	}
	cs.handleData(frame, source)
}

func (cs *centralServer) handleSYN(frame *tunnel.Frame, source net.Conn) {
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

	ctx, cancel := context.WithCancel(context.Background())
	state := &connState{
		reorderer: tunnel.NewReordererAt(frame.SeqNum + 1), // skip SYN's SeqNum
		sources:   []io.Writer{source},
		cancel:    cancel,
		created:   time.Now(),
	}
	cs.conns[connID] = state
	cs.mu.Unlock()

	log.Printf("[central] conn=%d: SYN → target=%s", connID, targetAddr)
	go cs.connectUpstream(ctx, connID, state, atyp, addr, port, targetAddr, source)
}

func (cs *centralServer) connectUpstream(ctx context.Context, connID uint32, state *connState,
	atyp byte, addr, port []byte, targetAddr string, source net.Conn) {

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

	resp := make([]byte, 6)
	if _, err := io.ReadFull(upConn, resp); err != nil {
		log.Printf("[central] conn=%d: upstream response read failed: %v", connID, err)
		upConn.Close()
		cs.sendFrame(connID, &tunnel.Frame{ConnID: connID, Flags: tunnel.FlagRST | tunnel.FlagReverse})
		cs.removeConn(connID)
		return
	}

	// Drain bind address
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

	if resp[3] != 0x00 {
		log.Printf("[central] conn=%d: upstream CONNECT rejected: 0x%02x", connID, resp[3])
		upConn.Close()
		cs.sendFrame(connID, &tunnel.Frame{ConnID: connID, Flags: tunnel.FlagRST | tunnel.FlagReverse})
		cs.removeConn(connID)
		return
	}

	state.mu.Lock()
	state.target = upConn

	// Flush any data that arrived before upstream was ready
	for {
		data := state.reorderer.Next()
		if data == nil {
			break
		}
		if _, err := upConn.Write(data); err != nil {
			state.mu.Unlock()
			log.Printf("[central] conn=%d: flush failed: %v", connID, err)
			upConn.Close()
			cs.removeConn(connID)
			return
		}
	}
	state.mu.Unlock()

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
			if err != io.EOF {
				log.Printf("[central] conn=%d: upstream read error: %v", connID, err)
			}
			return
		}
	}
}

// sendFrame picks ONE source via round-robin and writes the frame.
// If that source fails, tries the next one. Much better than broadcasting.
func (cs *centralServer) sendFrame(connID uint32, frame *tunnel.Frame) {
	cs.mu.RLock()
	state, ok := cs.conns[connID]
	cs.mu.RUnlock()
	if !ok {
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	if len(state.sources) == 0 {
		return
	}

	// Try each source once, starting from current index
	for tries := 0; tries < len(state.sources); tries++ {
		idx := state.sourceIdx % len(state.sources)
		state.sourceIdx++
		w := state.sources[idx]

		if err := tunnel.WriteFrame(w, frame); err != nil {
			// Remove dead source
			state.sources = append(state.sources[:idx], state.sources[idx+1:]...)
			if state.sourceIdx > 0 {
				state.sourceIdx--
			}
			continue
		}
		return // success
	}
	log.Printf("[central] conn=%d: all sources failed", connID)
}

func (cs *centralServer) handleData(frame *tunnel.Frame, source net.Conn) {
	cs.mu.RLock()
	state, ok := cs.conns[frame.ConnID]
	cs.mu.RUnlock()
	if !ok {
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	// If this source isn't known yet (e.g., after tunnel recycling), add it.
	// This ensures responses can flow back through the new connection.
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

	state.reorderer.Insert(frame.SeqNum, frame.Payload)
	if state.target == nil {
		return // buffered, flushed when upstream connects
	}

	for {
		data := state.reorderer.Next()
		if data == nil {
			break
		}
		if _, err := state.target.Write(data); err != nil {
			log.Printf("[central] conn=%d: write to upstream failed: %v", frame.ConnID, err)
			return
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
	state.mu.Lock()
	if state.target != nil {
		for {
			data := state.reorderer.Next()
			if data == nil {
				break
			}
			state.target.Write(data)
		}
		state.target.Close()
	}
	state.mu.Unlock()
	log.Printf("[central] conn=%d: FIN received", frame.ConnID)
}

func (cs *centralServer) handleRST(frame *tunnel.Frame) {
	cs.removeConn(frame.ConnID)
	log.Printf("[central] conn=%d: RST received", frame.ConnID)
}

func (cs *centralServer) removeConn(connID uint32) {
	cs.mu.Lock()
	state, ok := cs.conns[connID]
	if ok {
		delete(cs.conns, connID)
	}
	cs.mu.Unlock()
	if ok && state.cancel != nil {
		state.cancel()
	}
}

func (cs *centralServer) closeAll() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for id, state := range cs.conns {
		if state.cancel != nil {
			state.cancel()
		}
		if state.target != nil {
			state.target.Close()
		}
		delete(cs.conns, id)
	}
}

func (cs *centralServer) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		cs.mu.Lock()
		now := time.Now()
		cleaned := 0
		for id, state := range cs.conns {
			state.mu.Lock()
			shouldClean := false

			// No upstream established after 2 minutes = stuck
			if state.target == nil && now.Sub(state.created) > 2*time.Minute {
				shouldClean = true
			}
			// No sources left = all tunnel connections died
			if len(state.sources) == 0 && now.Sub(state.created) > 30*time.Second {
				shouldClean = true
			}
			// Connection too old (5 min max lifetime)
			if now.Sub(state.created) > 5*time.Minute {
				shouldClean = true
			}

			state.mu.Unlock()
			if shouldClean {
				if state.cancel != nil {
					state.cancel()
				}
				if state.target != nil {
					state.target.Close()
				}
				delete(cs.conns, id)
				cleaned++
			}
		}
		if cleaned > 0 {
			log.Printf("[central] cleanup: removed %d stale connections (%d active)", cleaned, len(cs.conns))
		}
		cs.mu.Unlock()
	}
}
