package proxy

import (
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/slipstreamplus/slipstreamplus/internal/balancer"
	"github.com/slipstreamplus/slipstreamplus/internal/engine"
)

// Server is a transparent TCP load balancer.
type Server struct {
	listenAddr     string
	bufferSize     int
	maxConnections int
	manager        *engine.Manager
	balancer       balancer.Balancer
	activeConns    atomic.Int64
	bufPool        sync.Pool
	connID         atomic.Uint64
}

func NewServer(listenAddr string, bufferSize int, maxConns int, mgr *engine.Manager, bal balancer.Balancer) *Server {
	return &Server{
		listenAddr:     listenAddr,
		bufferSize:     bufferSize,
		maxConnections: maxConns,
		manager:        mgr,
		balancer:       bal,
		bufPool: sync.Pool{
			New: func() any {
				buf := make([]byte, bufferSize)
				return &buf
			},
		},
	}
}

func (s *Server) ListenAndServe() error {
	lc := listenConfig()

	ln, err := lc.Listen(nil, "tcp", s.listenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.listenAddr, err)
	}
	defer ln.Close()

	log.Printf("[proxy] TCP load balancer listening on %s (buffer=%d, max_conns=%d)",
		s.listenAddr, s.bufferSize, s.maxConnections)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("[proxy] accept error: %v", err)
			continue
		}

		current := s.activeConns.Load()
		if current >= int64(s.maxConnections) {
			log.Printf("[proxy] max connections reached (%d), rejecting", current)
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

	log.Printf("[proxy] conn#%d: new connection from %s", connID, clientConn.RemoteAddr())

	if tc, ok := clientConn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
		tc.SetNoDelay(true)
	}

	healthy := s.manager.HealthyInstances()
	if len(healthy) == 0 {
		log.Printf("[proxy] conn#%d: no healthy instances available", connID)
		return
	}

	inst := s.balancer.Pick(healthy)
	if inst == nil {
		log.Printf("[proxy] conn#%d: balancer returned nil", connID)
		return
	}

	log.Printf("[proxy] conn#%d: routing to instance %d (%s) at %s",
		connID, inst.ID(), inst.Config.Domain, inst.Addr())

	upstreamConn, err := inst.Dial()
	if err != nil {
		log.Printf("[proxy] conn#%d: failed to connect to instance %d: %v",
			connID, inst.ID(), err)
		return
	}
	defer upstreamConn.Close()

	inst.IncrConns()
	defer inst.DecrConns()

	log.Printf("[proxy] conn#%d: connected to upstream %s, starting relay", connID, inst.Addr())

	if tc, ok := upstreamConn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
		tc.SetNoDelay(true)
	}

	var clientToUpstream, upstreamToClient int64
	done := make(chan struct{}, 2)

	go func() {
		bufPtr := s.bufPool.Get().(*[]byte)
		n, err := io.CopyBuffer(upstreamConn, clientConn, *bufPtr)
		s.bufPool.Put(bufPtr)
		clientToUpstream = n
		if err != nil {
			log.Printf("[proxy] conn#%d: client→upstream error after %d bytes: %v", connID, n, err)
		}
		if tc, ok := upstreamConn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()

	go func() {
		bufPtr := s.bufPool.Get().(*[]byte)
		n, err := io.CopyBuffer(clientConn, upstreamConn, *bufPtr)
		s.bufPool.Put(bufPtr)
		upstreamToClient = n
		if err != nil {
			log.Printf("[proxy] conn#%d: upstream→client error after %d bytes: %v", connID, n, err)
		}
		if tc, ok := clientConn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		done <- struct{}{}
	}()

	<-done
	<-done

	log.Printf("[proxy] conn#%d: relay finished (c→u: %d bytes, u→c: %d bytes)",
		connID, clientToUpstream, upstreamToClient)
}

func (s *Server) ActiveConnections() int64 {
	return s.activeConns.Load()
}
