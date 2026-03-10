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
)

// SSHServer is a SOCKS5 proxy that tunnels connections through SSH over slipstream.
type SSHServer struct {
	listenAddr     string
	bufferSize     int
	maxConnections int
	manager        *engine.Manager
	balancer       balancer.Balancer
	activeConns    atomic.Int64
	connID         atomic.Uint64
	bufPool        sync.Pool

	sshMu      sync.RWMutex
	sshClients map[int]*ssh.Client
}

func NewSSHServer(listenAddr string, bufferSize int, maxConns int, mgr *engine.Manager, bal balancer.Balancer) *SSHServer {
	return &SSHServer{
		listenAddr:     listenAddr,
		bufferSize:     bufferSize,
		maxConnections: maxConns,
		manager:        mgr,
		balancer:       bal,
		sshClients:     make(map[int]*ssh.Client),
		bufPool: sync.Pool{
			New: func() any {
				buf := make([]byte, bufferSize)
				return &buf
			},
		},
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

	// Build SSH auth methods: key file or password
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
	}

	// SOCKS5 Auth
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
	clientConn.Write([]byte{0x05, 0x00})

	// SOCKS5 CONNECT
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

	var txN, rxN int64
	done := make(chan struct{}, 2)
	go func() {
		bufPtr := s.bufPool.Get().(*[]byte)
		n, _ := io.CopyBuffer(upstreamConn, clientConn, *bufPtr)
		s.bufPool.Put(bufPtr)
		txN = n
		done <- struct{}{}
	}()
	go func() {
		bufPtr := s.bufPool.Get().(*[]byte)
		n, _ := io.CopyBuffer(clientConn, upstreamConn, *bufPtr)
		s.bufPool.Put(bufPtr)
		rxN = n
		done <- struct{}{}
	}()

	<-done
	<-done

	// Track bandwidth per instance
	inst.AddTx(txN)
	inst.AddRx(rxN)
}

func (s *SSHServer) Close() {
	s.sshMu.Lock()
	defer s.sshMu.Unlock()
	for id, client := range s.sshClients {
		client.Close()
		delete(s.sshClients, id)
	}
}
