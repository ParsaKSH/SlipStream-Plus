package proxy

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync/atomic"
	"time"

	"github.com/ParsaKSH/SlipStream-Plus/internal/balancer"
	"github.com/ParsaKSH/SlipStream-Plus/internal/engine"
	"github.com/ParsaKSH/SlipStream-Plus/internal/tunnel"
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

	// Packet-split mode fields
	packetSplit bool
	tunnelPool  *tunnel.TunnelPool
	connIDGen   *tunnel.ConnIDGenerator
	chunkSize   int
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

// EnablePacketSplit activates packet-level load balancing mode.
func (s *Server) EnablePacketSplit(pool *tunnel.TunnelPool, chunkSize int) {
	s.packetSplit = true
	s.tunnelPool = pool
	s.connIDGen = tunnel.NewConnIDGenerator()
	s.chunkSize = chunkSize
	log.Printf("[proxy] packet-split mode enabled (chunk_size=%d)", chunkSize)
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
	mode := "connection"
	if s.packetSplit {
		mode = "packet-split"
	}
	log.Printf("[proxy] SOCKS5 proxy listening on %s (auth=%s, mode=%s, buffer=%d, max_conns=%d)",
		s.listenAddr, authMode, mode, s.bufferSize, s.maxConnections)

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
		tc.SetReadBuffer(s.bufferSize)
		tc.SetWriteBuffer(s.bufferSize)
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

	// ──── Route based on mode ────
	if s.packetSplit {
		s.handlePacketSplit(clientConn, connID, atyp, addrBytes, portBytes, user)
	} else {
		s.handleConnectionLevel(clientConn, connID, atyp, addrBytes, portBytes, user)
	}
}

func (s *Server) ActiveConnections() int64 {
	return s.activeConns.Load()
}
