package main

import (
	"encoding/binary"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"github.com/ParsaKSH/SlipStream-Plus/internal/tunnel"
)

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
// When this function returns (source TCP died), it cleans up all
// connStates that had this as their only source.
func (cs *centralServer) handleFrameConn(conn net.Conn, firstByte byte, remoteAddr string) {
	log.Printf("[central] frame connection from %s", remoteAddr)

	sc := cs.getSourceConn(conn)

	// Track which ConnIDs this source served
	servedIDs := make(map[uint32]bool)

	defer func() {
		// Source TCP died — clean up sourceConn and connStates
		cs.removeSourceConn(conn)
		cs.cleanupSource(sc, servedIDs, remoteAddr)
	}()

	// Read remaining header bytes (we already read 1)
	var hdrRest [tunnel.HeaderSize - 1]byte
	if _, err := io.ReadFull(conn, hdrRest[:]); err != nil {
		return
	}

	var fullHdr [tunnel.HeaderSize]byte
	fullHdr[0] = firstByte
	copy(fullHdr[1:], hdrRest[:])

	firstFrame := cs.parseHeader(fullHdr, conn, remoteAddr)
	if firstFrame != nil {
		servedIDs[firstFrame.ConnID] = true
		cs.dispatchFrame(firstFrame, sc)
	}

	for {
		frame, err := tunnel.ReadFrame(conn)
		if err != nil {
			if err != io.EOF && !isClosedConnErr(err) {
				log.Printf("[central] %s: read error: %v", remoteAddr, err)
			}
			return
		}
		servedIDs[frame.ConnID] = true
		cs.dispatchFrame(frame, sc)
	}
}

// cleanupSource removes a dead source connection from all connStates.
// If a connState has no remaining sources, it is fully cleaned up.
func (cs *centralServer) cleanupSource(deadSource *sourceConn, servedIDs map[uint32]bool, remoteAddr string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	cleaned := 0
	for connID := range servedIDs {
		state, ok := cs.conns[connID]
		if !ok {
			continue
		}

		state.mu.Lock()
		// Remove dead source from sources list
		for i, src := range state.sources {
			if src == deadSource {
				state.sources = append(state.sources[:i], state.sources[i+1:]...)
				break
			}
		}

		// If no sources left, fully clean up this connState
		if len(state.sources) == 0 {
			state.mu.Unlock()
			if state.cancel != nil {
				state.cancel()
			}
			if state.target != nil {
				state.target.Close()
			}
			delete(cs.conns, connID)
			cleaned++
		} else {
			state.mu.Unlock()
		}
	}

	if cleaned > 0 {
		log.Printf("[central] %s: source disconnected, cleaned %d orphaned connections", remoteAddr, cleaned)
	}
}

// getSourceConn returns the sourceConn wrapper for a raw net.Conn,
// creating one if it doesn't exist yet.
func (cs *centralServer) getSourceConn(conn net.Conn) *sourceConn {
	cs.sourceMu.Lock()
	defer cs.sourceMu.Unlock()
	sc, ok := cs.sourceMap[conn]
	if !ok {
		sc = &sourceConn{conn: conn}
		cs.sourceMap[conn] = sc
	}
	return sc
}

// removeSourceConn removes the sourceConn wrapper when the raw conn dies.
func (cs *centralServer) removeSourceConn(conn net.Conn) {
	cs.sourceMu.Lock()
	delete(cs.sourceMap, conn)
	cs.sourceMu.Unlock()
}

func isClosedConnErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "use of closed network connection") ||
		strings.Contains(s, "connection reset by peer")
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

func (cs *centralServer) dispatchFrame(frame *tunnel.Frame, source *sourceConn) {
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

func (cs *centralServer) cleanupLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		cs.mu.Lock()
		now := time.Now()
		cleaned := 0
		for id, state := range cs.conns {
			state.mu.Lock()
			shouldClean := false

			// No upstream established after 60 seconds = stuck
			if state.target == nil && now.Sub(state.created) > 60*time.Second {
				shouldClean = true
			}
			// No sources left = all tunnel connections died
			if len(state.sources) == 0 && now.Sub(state.created) > 30*time.Second {
				shouldClean = true
			}
			// Idle for too long — no data sent or received in 60 seconds.
			// This catches stuck connections where both sides stopped talking.
			if now.Sub(state.lastActive) > 60*time.Second {
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
