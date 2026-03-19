package proxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"sync/atomic"
	"time"

	"github.com/ParsaKSH/SlipStream-Plus/internal/balancer"
	"github.com/ParsaKSH/SlipStream-Plus/internal/engine"
	"github.com/ParsaKSH/SlipStream-Plus/internal/users"
)

// idleTimeout is how long a relay connection can be idle before being closed.
const idleTimeout = 5 * time.Minute

// Server is a SOCKS5 proxy with optional user auth, load balancing across instances.
type Server struct {
	listenAddr     string
	bufferSize     int
	maxConnections int
	manager        *engine.Manager
	balancer       balancer.Balancer
	userMgr        *users.Manager
	activeConns    atomic.Int64
	connID         atomic.Uint64
}

func NewServer(listenAddr string, bufferSize int, maxConns int, mgr *engine.Manager, bal balancer.Balancer, umgr *users.Manager) *Server {
	return &Server{
		listenAddr:     listenAddr,
		bufferSize:     bufferSize,
		maxConnections: maxConns,
		manager:        mgr,
		balancer:       bal,
		userMgr:        umgr,
	}
}

func (s *Server) ListenAndServe() error {
	lc := listenConfig()
	ln, err := lc.Listen(context.Background(), "tcp", s.listenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.listenAddr, err)
	}
	defer ln.Close()

	authMode := "no-auth"
	if s.userMgr != nil && s.userMgr.HasUsers() {
		authMode = "username/password"
	}
	log.Printf("[proxy] SOCKS5 proxy listening on %s (auth=%s, buffer=%d, max_conns=%d)",
		s.listenAddr, authMode, s.bufferSize, s.maxConnections)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("[proxy] accept error: %v", err)
			continue
		}
		current := s.activeConns.Load()
		if current >= int64(s.maxConnections) {
			conn.Close()
			continue
		}
		s.activeConns.Add(1)
		id := s.connID.Add(1)
		go s.handleConnection(conn, id)
	}
}

func (s *Server) handleConnection(clientConn net.Conn, connID uint64) {
	defer func() {
		clientConn.Close()
		s.activeConns.Add(-1)
	}()

	if tc, ok := clientConn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
		tc.SetNoDelay(true)
	}

	clientIP := users.ExtractIP(clientConn.RemoteAddr())

	// ──── SOCKS5 Greeting ────
	buf := make([]byte, 258)
	if _, err := io.ReadFull(clientConn, buf[:2]); err != nil {
		return
	}
	if buf[0] != 0x05 {
		return
	}
	nMethods := int(buf[1])
	if _, err := io.ReadFull(clientConn, buf[:nMethods]); err != nil {
		return
	}

	requireAuth := s.userMgr != nil && s.userMgr.HasUsers()
	var user *users.User

	if requireAuth {
		// Require method 0x02 (username/password)
		found := false
		for i := 0; i < nMethods; i++ {
			if buf[i] == 0x02 {
				found = true
				break
			}
		}
		if !found {
			clientConn.Write([]byte{0x05, 0xFF})
			return
		}
		clientConn.Write([]byte{0x05, 0x02})

		// ──── RFC 1929 Username/Password ────
		if _, err := io.ReadFull(clientConn, buf[:2]); err != nil {
			return
		}
		uLen := int(buf[1])
		if _, err := io.ReadFull(clientConn, buf[:uLen]); err != nil {
			return
		}
		username := string(buf[:uLen])

		if _, err := io.ReadFull(clientConn, buf[:1]); err != nil {
			return
		}
		pLen := int(buf[0])
		if _, err := io.ReadFull(clientConn, buf[:pLen]); err != nil {
			return
		}
		password := string(buf[:pLen])

		var ok bool
		user, ok = s.userMgr.Authenticate(username, password)
		if !ok {
			clientConn.Write([]byte{0x01, 0x01})
			log.Printf("[proxy] conn#%d: auth failed user=%q ip=%s", connID, username, clientIP)
			return
		}
		clientConn.Write([]byte{0x01, 0x00})

		if reason := user.CheckConnect(clientIP); reason != "" {
			log.Printf("[proxy] conn#%d: user %q denied: %s", connID, username, reason)
			// Wait for CONNECT request then reply with general failure
			io.ReadFull(clientConn, buf[:4]) // read VER+CMD+RSV+ATYP
			clientConn.Write([]byte{0x05, 0x02, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			return
		}

		user.MarkConnect(clientIP)
		defer user.MarkDisconnect(clientIP)
	} else {
		clientConn.Write([]byte{0x05, 0x00})
	}

	// ──── SOCKS5 CONNECT Request ────
	// Read: VER(1) CMD(1) RSV(1) ATYP(1) + ADDR + PORT(2)
	if _, err := io.ReadFull(clientConn, buf[:4]); err != nil {
		return
	}
	if buf[1] != 0x01 { // only CONNECT supported
		clientConn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	atyp := buf[3]

	// Capture target address raw bytes for replaying to upstream
	var addrBytes []byte
	switch atyp {
	case 0x01: // IPv4
		addrBytes = make([]byte, 4)
		if _, err := io.ReadFull(clientConn, addrBytes); err != nil {
			return
		}
	case 0x03: // Domain
		if _, err := io.ReadFull(clientConn, buf[:1]); err != nil {
			return
		}
		domLen := int(buf[0])
		addrBytes = make([]byte, 1+domLen)
		addrBytes[0] = buf[0]
		if _, err := io.ReadFull(clientConn, addrBytes[1:]); err != nil {
			return
		}
	case 0x04: // IPv6
		addrBytes = make([]byte, 16)
		if _, err := io.ReadFull(clientConn, addrBytes); err != nil {
			return
		}
	default:
		clientConn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(clientConn, portBytes); err != nil {
		return
	}

	// ──── Pick upstream slipstream instance ────
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
	}

	// ──── Pipelined SOCKS5 negotiation with upstream ────
	// Combine greeting + CONNECT into ONE write to minimize DNS tunnel round-trips.
	// Greeting: VER=5, NMETHODS=1, METHOD=0x00 (no auth)
	// CONNECT:  VER=5, CMD=1, RSV=0, ATYP, ADDR, PORT
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
	// resp[0..1] = greeting reply, resp[2..5] = CONNECT reply header

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

	var clientToUpstream, upstreamToClient int64
	done := make(chan struct{}, 2)

	go func() {
		var src io.Reader = idleClient
		if user != nil {
			src = user.WrapReader(src)
		}
		bufPtr := inst.BufPool.Get().(*[]byte)
		n, _ := io.CopyBuffer(idleUpstream, src, *bufPtr)
		inst.BufPool.Put(bufPtr)
		clientToUpstream = n
		if tc, ok := upstreamConn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()

	go func() {
		var dst io.Writer = idleClient
		if user != nil {
			dst = user.WrapWriter(dst)
		}
		bufPtr := inst.BufPool.Get().(*[]byte)
		n, _ := io.CopyBuffer(dst, idleUpstream, *bufPtr)
		inst.BufPool.Put(bufPtr)
		upstreamToClient = n
		if tc, ok := clientConn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()

	<-done
	<-done

	inst.AddTx(clientToUpstream)
	inst.AddRx(upstreamToClient)
}

func (s *Server) ActiveConnections() int64 {
	return s.activeConns.Load()
}
