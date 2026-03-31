package proxy

import (
	"context"
	"encoding/binary"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/ParsaKSH/SlipStream-Plus/internal/engine"
	"github.com/ParsaKSH/SlipStream-Plus/internal/tunnel"
	"github.com/ParsaKSH/SlipStream-Plus/internal/users"
)

// handlePacketSplit handles a connection using packet-level load balancing.
func (s *Server) handlePacketSplit(clientConn net.Conn, connID uint64, atyp byte, addrBytes, portBytes []byte, user *users.User) {
	// Fast-fail if no tunnels are connected yet (e.g., right after restart)
	if !s.tunnelPool.HasTunnels() {
		clientConn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	healthy := s.manager.HealthyInstances()
	socksHealthy := make([]*engine.Instance, 0, len(healthy))
	for _, inst := range healthy {
		if inst.Config.Mode != "ssh" {
			socksHealthy = append(socksHealthy, inst)
		}
	}
	if len(socksHealthy) == 0 {
		clientConn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// Track connection count on ONE instance only (first healthy one).
	// In packet_split, the single user connection is multiplexed across all
	// instances, so incrementing all of them inflates the count by N×.
	trackInst := socksHealthy[0]
	trackInst.IncrConns()
	defer trackInst.DecrConns()

	// Create a packet splitter for this connection
	tunnelConnID := s.connIDGen.Next()
	splitter := tunnel.NewPacketSplitter(tunnelConnID, s.tunnelPool, socksHealthy, s.chunkSize)
	defer splitter.Close()

	// Send SYN with target address info
	if err := splitter.SendSYN(atyp, addrBytes, portBytes); err != nil {
		log.Printf("[proxy] conn#%d: SYN failed: %v", connID, err)
		clientConn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// Tell client: connection successful
	clientConn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	log.Printf("[proxy] conn#%d: packet-split SYN sent, connID=%d", connID, tunnelConnID)

	port := binary.BigEndian.Uint16(portBytes)
	log.Printf("[proxy] conn#%d: packet-split mode, %d instances, port %d",
		connID, len(socksHealthy), port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// When context is cancelled (either direction finished), close the client
	// connection so that blocking Read/Write calls unblock immediately.
	go func() {
		<-ctx.Done()
		clientConn.Close()
	}()

	var txN, rxN int64
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if user != nil && user.NeedsRateLimit() {
			wrapped := user.WrapReader(clientConn)
			txN = splitter.RelayClientToUpstream(ctx, wrapped)
		} else {
			txN = splitter.RelayClientToUpstream(ctx, clientConn)
		}
		cancel()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if user != nil && user.NeedsRateLimit() {
			wrapped := user.WrapWriter(clientConn)
			rxN = splitter.RelayUpstreamToClient(ctx, wrapped)
		} else {
			rxN = splitter.RelayUpstreamToClient(ctx, clientConn)
		}
		cancel()
	}()

	wg.Wait()
	log.Printf("[proxy] conn#%d: packet-split done, tx=%d rx=%d", connID, txN, rxN)

	// Track TX/RX on instances (distributed proportionally)
	nInstances := int64(len(socksHealthy))
	if nInstances > 0 {
		txPer := txN / nInstances
		rxPer := rxN / nInstances
		for _, inst := range socksHealthy {
			inst.AddTx(txPer)
			inst.AddRx(rxPer)
		}
	}

	// Track bytes for user data quota
	if user != nil {
		user.AddUsedBytes(txN + rxN)
	}
}

// handleConnectionLevel handles a connection using traditional per-connection load balancing.
func (s *Server) handleConnectionLevel(clientConn net.Conn, connID uint64, atyp byte, addrBytes, portBytes []byte, user *users.User) {
	healthy := s.manager.HealthyInstances()
	socksHealthy := make([]*engine.Instance, 0, len(healthy))
	for _, inst := range healthy {
		if inst.Config.Mode != "ssh" {
			socksHealthy = append(socksHealthy, inst)
		}
	}
	if len(socksHealthy) == 0 {
		clientConn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	inst := s.balancer.Pick(socksHealthy)
	if inst == nil {
		clientConn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	upstreamConn, err := inst.Dial()
	if err != nil {
		log.Printf("[proxy] conn#%d: dial instance %d failed: %v", connID, inst.ID(), err)
		clientConn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstreamConn.Close()

	if tc, ok := upstreamConn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
		tc.SetNoDelay(true)
		tc.SetReadBuffer(s.bufferSize)
		tc.SetWriteBuffer(s.bufferSize)
	}

	// ──── Pipelined SOCKS5 negotiation with upstream ────
	pipelined := make([]byte, 0, 3+4+len(addrBytes)+2)
	pipelined = append(pipelined, 0x05, 0x01, 0x00)       // greeting
	pipelined = append(pipelined, 0x05, 0x01, 0x00, atyp) // CONNECT header
	pipelined = append(pipelined, addrBytes...)           // target addr
	pipelined = append(pipelined, portBytes...)           // target port
	if _, err := upstreamConn.Write(pipelined); err != nil {
		clientConn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// Read greeting response (2 bytes) + CONNECT response header (4 bytes) = 6 bytes
	resp := make([]byte, 6)
	if _, err := io.ReadFull(upstreamConn, resp); err != nil {
		clientConn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// Drain the CONNECT reply's bind address + port
	repAtyp := resp[5]
	switch repAtyp {
	case 0x01:
		io.ReadFull(upstreamConn, make([]byte, 4+2))
	case 0x03:
		lenBuf := make([]byte, 1)
		io.ReadFull(upstreamConn, lenBuf)
		io.ReadFull(upstreamConn, make([]byte, int(lenBuf[0])+2))
	case 0x04:
		io.ReadFull(upstreamConn, make([]byte, 16+2))
	default:
		io.ReadFull(upstreamConn, make([]byte, 4+2))
	}

	if resp[3] != 0x00 { // CONNECT reply status
		clientConn.Write([]byte{0x05, resp[3], 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// ──── Success! Tell client and start relay ────
	clientConn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	inst.IncrConns()
	defer inst.DecrConns()

	port := binary.BigEndian.Uint16(portBytes)
	log.Printf("[proxy] conn#%d: connected via instance %d, port %d", connID, inst.ID(), port)

	s.relay(inst.ConnCtx, clientConn, upstreamConn, inst, user, connID)
}

func (s *Server) relay(ctx context.Context, clientConn, upstreamConn net.Conn, inst *engine.Instance, user *users.User, connID uint64) {
	// Wrap both connections with idle timeout so stuck connections are cleaned up.
	idleClient := newIdleConn(clientConn, idleTimeout)
	idleUpstream := newIdleConn(upstreamConn, idleTimeout)

	// If the instance is stopped (context cancelled), close both connections
	// so the relay goroutines unblock and exit.
	go func() {
		select {
		case <-ctx.Done():
			clientConn.Close()
			upstreamConn.Close()
		}
	}()

	// Determine if we need rate-limited (wrapped) relay or can use zero-copy.
	needsWrap := user != nil && user.NeedsRateLimit()

	var clientToUpstream, upstreamToClient int64
	done := make(chan struct{}, 2)

	go func() {
		if needsWrap {
			src := user.WrapReader(idleClient)
			bufPtr := inst.BufPool.Get().(*[]byte)
			n, _ := io.CopyBuffer(idleUpstream, src, *bufPtr)
			inst.BufPool.Put(bufPtr)
			clientToUpstream = n
		} else {
			// Zero-copy path with idle timeout
			n, _ := io.Copy(idleUpstream, idleClient)
			clientToUpstream = n
		}
		if tc, ok := upstreamConn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()

	go func() {
		if needsWrap {
			dst := user.WrapWriter(idleClient)
			bufPtr := inst.BufPool.Get().(*[]byte)
			n, _ := io.CopyBuffer(dst, idleUpstream, *bufPtr)
			inst.BufPool.Put(bufPtr)
			upstreamToClient = n
		} else {
			// Zero-copy path with idle timeout
			n, _ := io.Copy(idleClient, idleUpstream)
			upstreamToClient = n
		}
		if tc, ok := clientConn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()

	<-done
	<-done

	inst.AddTx(clientToUpstream)
	inst.AddRx(upstreamToClient)

	// Track bytes for user data quota (non-rate-limited path)
	if user != nil && !needsWrap {
		user.AddUsedBytes(clientToUpstream + upstreamToClient)
	}
}
