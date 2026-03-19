package proxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ParsaKSH/SlipStream-Plus/internal/balancer"
	"github.com/ParsaKSH/SlipStream-Plus/internal/engine"
	"github.com/ParsaKSH/SlipStream-Plus/internal/users"
)

// SSHServer is a SOCKS5 proxy that tunnels connections through SSH over slipstream.
type SSHServer struct {
	listenAddr     string
	bufferSize     int
	maxConnections int
	manager        *engine.Manager
	balancer       balancer.Balancer
	userMgr        *users.Manager
	activeConns    atomic.Int64
	connID         atomic.Uint64

	sshMu      sync.RWMutex
	sshClients map[int]*ssh.Client
}

func NewSSHServer(listenAddr string, bufferSize int, maxConns int, mgr *engine.Manager, bal balancer.Balancer, umgr *users.Manager) *SSHServer {
	return &SSHServer{
		listenAddr:     listenAddr,
		bufferSize:     bufferSize,
		maxConnections: maxConns,
		manager:        mgr,
		balancer:       bal,
		userMgr:        umgr,
		sshClients:     make(map[int]*ssh.Client),
	}
}

func (s *SSHServer) getSSHClient(inst *engine.Instance) (*ssh.Client, error) {
	s.sshMu.RLock()
	client, ok := s.sshClients[inst.ID()]
	s.sshMu.RUnlock()

	if ok {
		_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
		if err == nil {
			return client, nil
		}
		client.Close()
		s.sshMu.Lock()
		delete(s.sshClients, inst.ID())
		s.sshMu.Unlock()
	}

	tunnelConn, err := inst.Dial()
	if err != nil {
		return nil, fmt.Errorf("dial slipstream instance: %w", err)
	}

	var authMethods []ssh.AuthMethod
	if inst.Config.SSHKey != "" {
		keyData, err := os.ReadFile(inst.Config.SSHKey)
		if err != nil {
			tunnelConn.Close()
			return nil, fmt.Errorf("read ssh key %s: %w", inst.Config.SSHKey, err)
		}
		signer, err := ssh.ParsePrivateKey(keyData)
		if err != nil {
			tunnelConn.Close()
			return nil, fmt.Errorf("parse ssh key: %w", err)
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if inst.Config.SSHPassword != "" {
		authMethods = append(authMethods, ssh.Password(inst.Config.SSHPassword))
	}

	sshAddr := fmt.Sprintf("127.0.0.1:%d", inst.Config.SSHPort)
	sshConfig := &ssh.ClientConfig{
		User:            inst.Config.SSHUser,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(tunnelConn, sshAddr, sshConfig)
	if err != nil {
		tunnelConn.Close()
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}

	client = ssh.NewClient(sshConn, chans, reqs)

	s.sshMu.Lock()
	s.sshClients[inst.ID()] = client
	s.sshMu.Unlock()

	log.Printf("[ssh] established SSH connection to %s through instance %d (%s)",
		sshAddr, inst.ID(), inst.Config.Domain)

	return client, nil
}

func (s *SSHServer) ListenAndServe() error {
	lc := listenConfig()
	ln, err := lc.Listen(context.Background(), "tcp", s.listenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.listenAddr, err)
	}
	defer ln.Close()

	log.Printf("[ssh-proxy] SOCKS5-over-SSH proxy listening on %s", s.listenAddr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("[ssh-proxy] accept error: %v", err)
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

func (s *SSHServer) handleConnection(clientConn net.Conn, connID uint64) {
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

		// RFC 1929 auth
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
			log.Printf("[ssh-proxy] conn#%d: auth failed user=%q ip=%s", connID, username, clientIP)
			return
		}
		clientConn.Write([]byte{0x01, 0x00})

		if reason := user.CheckConnect(clientIP); reason != "" {
			log.Printf("[ssh-proxy] conn#%d: user %q denied: %s", connID, username, reason)
			io.ReadFull(clientConn, buf[:4])
			clientConn.Write([]byte{0x05, 0x02, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			return
		}

		user.MarkConnect(clientIP)
		defer user.MarkDisconnect(clientIP)
	} else {
		clientConn.Write([]byte{0x05, 0x00})
	}

	// ──── SOCKS5 CONNECT ────
	if _, err := io.ReadFull(clientConn, buf[:4]); err != nil {
		return
	}
	if buf[1] != 0x01 {
		clientConn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	var targetAddr string
	atyp := buf[3]
	switch atyp {
	case 0x01:
		if _, err := io.ReadFull(clientConn, buf[:4]); err != nil {
			return
		}
		targetAddr = net.IP(buf[:4]).String()
	case 0x03:
		if _, err := io.ReadFull(clientConn, buf[:1]); err != nil {
			return
		}
		domLen := int(buf[0])
		if _, err := io.ReadFull(clientConn, buf[:domLen]); err != nil {
			return
		}
		targetAddr = string(buf[:domLen])
	case 0x04:
		if _, err := io.ReadFull(clientConn, buf[:16]); err != nil {
			return
		}
		targetAddr = net.IP(buf[:16]).String()
	default:
		clientConn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	if _, err := io.ReadFull(clientConn, buf[:2]); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(buf[:2])
	target := fmt.Sprintf("%s:%d", targetAddr, port)

	// Pick SSH instance only
	healthy := s.manager.HealthyInstances()
	sshHealthy := make([]*engine.Instance, 0)
	for _, inst := range healthy {
		if inst.Config.Mode == "ssh" {
			sshHealthy = append(sshHealthy, inst)
		}
	}
	if len(sshHealthy) == 0 {
		clientConn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	inst := s.balancer.Pick(sshHealthy)
	if inst == nil {
		clientConn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	sshClient, err := s.getSSHClient(inst)
	if err != nil {
		log.Printf("[ssh-proxy] conn#%d: SSH connect failed on instance %d: %v", connID, inst.ID(), err)
		clientConn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	upstreamConn, err := sshClient.Dial("tcp", target)
	if err != nil {
		log.Printf("[ssh-proxy] conn#%d: SSH dial %s failed: %v", connID, target, err)
		clientConn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstreamConn.Close()

	inst.IncrConns()
	defer inst.DecrConns()

	clientConn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	// Relay with per-instance buffers, idle timeout, and context cancellation.
	idleClient := newIdleConn(clientConn, idleTimeout)
	idleUpstream := newIdleConn(upstreamConn, idleTimeout)

	// If the instance is stopped, close connections to unblock relay goroutines.
	go func() {
		select {
		case <-inst.ConnCtx.Done():
			clientConn.Close()
			upstreamConn.Close()
		}
	}()

	needsWrap := user != nil && user.NeedsRateLimit()

	var txN, rxN int64
	done := make(chan struct{}, 2)
	go func() {
		if needsWrap {
			src := user.WrapReader(idleClient)
			bufPtr := inst.BufPool.Get().(*[]byte)
			n, _ := io.CopyBuffer(idleUpstream, src, *bufPtr)
			inst.BufPool.Put(bufPtr)
			txN = n
		} else {
			// Zero-copy path with idle timeout
			n, _ := io.Copy(idleUpstream, idleClient)
			txN = n
		}
		done <- struct{}{}
	}()
	go func() {
		if needsWrap {
			dst := user.WrapWriter(idleClient)
			bufPtr := inst.BufPool.Get().(*[]byte)
			n, _ := io.CopyBuffer(dst, idleUpstream, *bufPtr)
			inst.BufPool.Put(bufPtr)
			rxN = n
		} else {
			// Zero-copy path with idle timeout
			n, _ := io.Copy(idleClient, idleUpstream)
			rxN = n
		}
		done <- struct{}{}
	}()

	<-done
	<-done

	inst.AddTx(txN)
	inst.AddRx(rxN)

	// Track bytes for user data quota (non-rate-limited path)
	if user != nil && !needsWrap {
		user.AddUsedBytes(txN + rxN)
	}
}

func (s *SSHServer) Close() {
	s.sshMu.Lock()
	defer s.sshMu.Unlock()
	for id, client := range s.sshClients {
		client.Close()
		delete(s.sshClients, id)
	}
}
