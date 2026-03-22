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
	txSeq     uint32             // next sequence number for reverse data
	sources   map[io.Writer]bool // all tunnel connections that can send back
	cancel    context.CancelFunc
	created   time.Time
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

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		cs.closeAll()
		os.Exit(0)
	}()

	// Cleanup stale connections periodically
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
//   - 0x05 → SOCKS5 (health probe or legacy) → proxy to socks-upstream
//   - anything else → our framing protocol → read frames
func (cs *centralServer) handleIncoming(conn net.Conn) {
	defer conn.Close()
	remoteAddr := conn.RemoteAddr().String()

	// Peek at the first byte to detect protocol
	firstByte := make([]byte, 1)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(conn, firstByte); err != nil {
		return
	}
	conn.SetReadDeadline(time.Time{}) // clear deadline

	if firstByte[0] == 0x05 {
		// SOCKS5 handshake detected — proxy this through to socks-upstream
		// This handles health checker probes transparently
		cs.handleSOCKS5Passthrough(conn, firstByte[0], remoteAddr)
	} else {
		// Frame protocol — prepend the first byte back and process frames
		cs.handleFrameConn(conn, firstByte[0], remoteAddr)
	}
}

// handleSOCKS5Passthrough transparently proxies a SOCKS5 connection to socks-upstream.
// This allows health checker probes to work even though target-address now points to us.
func (cs *centralServer) handleSOCKS5Passthrough(clientConn net.Conn, firstByte byte, remoteAddr string) {
	upstream, err := net.DialTimeout("tcp", cs.socksUpstream, 10*time.Second)
	if err != nil {
		log.Printf("[central] %s: socks passthrough dial failed: %v", remoteAddr, err)
		return
	}
	defer upstream.Close()

	if tc, ok := upstream.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}

	// Send the first byte we already read to the upstream
	if _, err := upstream.Write([]byte{firstByte}); err != nil {
		return
	}

	// Bidirectional relay
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

	// We already read the first byte, so we need to construct the first frame
	// header manually by reading the remaining 10 bytes
	var hdrRest [tunnel.HeaderSize - 1]byte
	if _, err := io.ReadFull(conn, hdrRest[:]); err != nil {
		log.Printf("[central] %s: frame header read error: %v", remoteAddr, err)
		return
	}

	// Reconstruct full header
	var fullHdr [tunnel.HeaderSize]byte
	fullHdr[0] = firstByte
	copy(fullHdr[1:], hdrRest[:])

	// Parse first frame manually
	firstFrame := cs.parseHeader(fullHdr, conn, remoteAddr)
	if firstFrame != nil {
		cs.dispatchFrame(firstFrame, conn)
	}

	// Continue reading frames normally
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

// parseHeader builds a Frame from a raw header + reads payload.
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
			log.Printf("[central] %s: payload read error: %v", remoteAddr, err)
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

// dispatchFrame routes a frame to the appropriate connection handler.
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
	// Regular data frame
	cs.handleData(frame)
}

// handleSYN initiates a new upstream connection for this ConnID.
func (cs *centralServer) handleSYN(frame *tunnel.Frame, source net.Conn) {
	connID := frame.ConnID

	cs.mu.Lock()
	if _, exists := cs.conns[connID]; exists {
		// SYN for already-known ConnID — another instance's SYN arrived.
		state := cs.conns[connID]
		state.mu.Lock()
		state.sources[source] = true
		state.mu.Unlock()
		cs.mu.Unlock()
		return
	}

	// Parse target from SYN payload
	atyp, addr, port, err := tunnel.DecodeSYNPayload(frame.Payload)
	if err != nil {
		cs.mu.Unlock()
		log.Printf("[central] conn=%d: bad SYN payload: %v", connID, err)
		return
	}

	// Build target address for SOCKS5 CONNECT
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
		reorderer: tunnel.NewReorderer(),
		sources:   map[io.Writer]bool{source: true},
		cancel:    cancel,
		created:   time.Now(),
	}
	cs.conns[connID] = state
	cs.mu.Unlock()

	log.Printf("[central] conn=%d: SYN → target=%s", connID, targetAddr)

	go cs.connectUpstream(ctx, connID, state, atyp, addr, port, targetAddr, source)
}

// connectUpstream establishes a SOCKS5 connection to the upstream proxy.
func (cs *centralServer) connectUpstream(ctx context.Context, connID uint32, state *connState,
	atyp byte, addr, port []byte, targetAddr string, source net.Conn) {

	upConn, err := net.DialTimeout("tcp", cs.socksUpstream, 10*time.Second)
	if err != nil {
		log.Printf("[central] conn=%d: upstream dial failed: %v", connID, err)
		cs.sendRST(connID, source)
		cs.removeConn(connID)
		return
	}

	if tc, ok := upConn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
		tc.SetNoDelay(true)
	}

	// SOCKS5 handshake with upstream
	pipelined := make([]byte, 0, 3+4+len(addr)+2)
	pipelined = append(pipelined, 0x05, 0x01, 0x00)       // greeting: no auth
	pipelined = append(pipelined, 0x05, 0x01, 0x00, atyp) // CONNECT
	pipelined = append(pipelined, addr...)
	pipelined = append(pipelined, port...)

	if _, err := upConn.Write(pipelined); err != nil {
		log.Printf("[central] conn=%d: upstream write failed: %v", connID, err)
		upConn.Close()
		cs.sendRST(connID, source)
		cs.removeConn(connID)
		return
	}

	// Read greeting + CONNECT response
	resp := make([]byte, 6)
	if _, err := io.ReadFull(upConn, resp); err != nil {
		log.Printf("[central] conn=%d: upstream response read failed: %v", connID, err)
		upConn.Close()
		cs.sendRST(connID, source)
		cs.removeConn(connID)
		return
	}

	// Drain bind address
	repAtyp := resp[5]
	switch repAtyp {
	case 0x01:
		io.ReadFull(upConn, make([]byte, 4+2))
	case 0x03:
		lenBuf := make([]byte, 1)
		io.ReadFull(upConn, lenBuf)
		io.ReadFull(upConn, make([]byte, int(lenBuf[0])+2))
	case 0x04:
		io.ReadFull(upConn, make([]byte, 16+2))
	default:
		io.ReadFull(upConn, make([]byte, 4+2))
	}

	if resp[3] != 0x00 {
		log.Printf("[central] conn=%d: upstream CONNECT rejected: 0x%02x", connID, resp[3])
		upConn.Close()
		cs.sendRST(connID, source)
		cs.removeConn(connID)
		return
	}

	state.mu.Lock()
	state.target = upConn

	// Flush any buffered data that arrived before upstream was ready
	for {
		data := state.reorderer.Next()
		if data == nil {
			break
		}
		if _, err := upConn.Write(data); err != nil {
			state.mu.Unlock()
			log.Printf("[central] conn=%d: flush buffered data failed: %v", connID, err)
			upConn.Close()
			cs.removeConn(connID)
			return
		}
	}
	state.mu.Unlock()

	log.Printf("[central] conn=%d: upstream connected to %s", connID, targetAddr)

	// Send ACK back
	ack := &tunnel.Frame{
		ConnID: connID,
		SeqNum: 0,
		Flags:  tunnel.FlagACK,
	}
	cs.broadcastFrame(connID, ack)

	// Read upstream responses and send back through tunnel
	go cs.relayUpstreamToTunnel(ctx, connID, state, upConn)
}

// relayUpstreamToTunnel reads from the upstream SOCKS connection and sends
// data back through the tunnel as reverse frames.
func (cs *centralServer) relayUpstreamToTunnel(ctx context.Context, connID uint32,
	state *connState, upstream net.Conn) {

	defer func() {
		upstream.Close()
		fin := &tunnel.Frame{
			ConnID: connID,
			Flags:  tunnel.FlagFIN | tunnel.FlagReverse,
		}
		cs.broadcastFrame(connID, fin)
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
			cs.broadcastFrame(connID, frame)
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("[central] conn=%d: upstream read error: %v", connID, err)
			}
			return
		}
	}
}

// handleData processes incoming data frame.
func (cs *centralServer) handleData(frame *tunnel.Frame) {
	cs.mu.RLock()
	state, ok := cs.conns[frame.ConnID]
	cs.mu.RUnlock()
	if !ok {
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	state.reorderer.Insert(frame.SeqNum, frame.Payload)

	if state.target == nil {
		return // buffered, will flush when upstream connects
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

// handleFIN handles connection teardown.
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

// handleRST handles connection reset.
func (cs *centralServer) handleRST(frame *tunnel.Frame) {
	cs.removeConn(frame.ConnID)
	log.Printf("[central] conn=%d: RST received", frame.ConnID)
}

// broadcastFrame sends a frame back through all registered sources.
func (cs *centralServer) broadcastFrame(connID uint32, frame *tunnel.Frame) {
	cs.mu.RLock()
	state, ok := cs.conns[connID]
	cs.mu.RUnlock()
	if !ok {
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	for w := range state.sources {
		if err := tunnel.WriteFrame(w, frame); err != nil {
			delete(state.sources, w)
		}
	}
}

// sendRST sends a RST frame back to a specific source.
func (cs *centralServer) sendRST(connID uint32, dest io.Writer) {
	rst := &tunnel.Frame{
		ConnID: connID,
		Flags:  tunnel.FlagRST | tunnel.FlagReverse,
	}
	tunnel.WriteFrame(dest, rst)
}

// removeConn cleans up a connection.
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

// closeAll closes all active connections.
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

// cleanupLoop removes stale connections.
func (cs *centralServer) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		cs.mu.Lock()
		now := time.Now()
		for id, state := range cs.conns {
			state.mu.Lock()
			if state.target == nil && now.Sub(state.created) > 10*time.Minute {
				state.mu.Unlock()
				if state.cancel != nil {
					state.cancel()
				}
				delete(cs.conns, id)
				log.Printf("[central] conn=%d: removed stale connection", id)
				continue
			}
			state.mu.Unlock()
		}
		cs.mu.Unlock()
	}
}
