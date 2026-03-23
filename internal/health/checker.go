package health

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/ParsaKSH/SlipStream-Plus/internal/config"
	"github.com/ParsaKSH/SlipStream-Plus/internal/engine"
)

// An instance is HEALTHY only after a successful tunnel probe (SOCKS5/SSH).
// An instance is UNHEALTHY if:
//   - TCP connect to local port fails (process dead)
//   - Tunnel probe fails 3 consecutive times (tunnel broken)
//
// Latency is only set from successful tunnel probes (real RTT).
const maxConsecutiveFailures = 3

type Checker struct {
	manager  *engine.Manager
	interval time.Duration
	timeout  time.Duration
	target   string // health_check.target (e.g. "google.com")
	ctx      context.Context
	cancel   context.CancelFunc

	mu       sync.Mutex
	failures map[int]int
}

func NewChecker(mgr *engine.Manager, cfg *config.HealthCheckConfig) *Checker {
	ctx, cancel := context.WithCancel(context.Background())

	timeout := cfg.TimeoutDuration()
	if timeout < 30*time.Second {
		timeout = 30 * time.Second
	}

	return &Checker{
		manager:  mgr,
		interval: cfg.IntervalDuration(),
		timeout:  timeout,
		target:   cfg.Target,
		ctx:      ctx,
		cancel:   cancel,
		failures: make(map[int]int),
	}
}

func (c *Checker) Start() {
	go c.run()
	log.Printf("[health] checker started (interval=%s, tunnel_timeout=%s, target=%s, unhealthy_after=%d failures)",
		c.interval, c.timeout, c.target, maxConsecutiveFailures)
}

func (c *Checker) Stop() {
	c.cancel()
}

func (c *Checker) run() {
	select {
	case <-time.After(8 * time.Second):
	case <-c.ctx.Done():
		return
	}

	c.checkAll()

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.checkAll()
		}
	}
}

func (c *Checker) checkAll() {
	instances := c.manager.AllInstances()
	for _, inst := range instances {
		if inst.State() == engine.StateDead {
			inst.SetLastPingMs(-1)
			continue
		}
		go c.checkOne(inst)
	}
}

func (c *Checker) recordSuccess(id int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures[id] = 0
}

func (c *Checker) recordFailure(id int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures[id]++
	return c.failures[id]
}

func (c *Checker) checkOne(inst *engine.Instance) {
	// Step 1: Quick TCP connect — is the process even running?
	conn, err := net.DialTimeout("tcp", inst.Addr(), 3*time.Second)
	if err != nil {
		failCount := c.recordFailure(inst.ID())
		if inst.State() != engine.StateUnhealthy {
			log.Printf("[health] instance %d (%s:%d) UNHEALTHY: process not listening: %v",
				inst.ID(), inst.Config.Domain, inst.Config.Port, err)
			inst.SetState(engine.StateUnhealthy)
			inst.SetLastPingMs(-1)
			go func() {
				log.Printf("[health] auto-restarting instance %d", inst.ID())
				c.manager.RestartInstance(inst.ID())
			}()
		}
		_ = failCount
		return
	}
	conn.Close()

	// Step 2: Tunnel probe — does the tunnel actually work?
	var rtt time.Duration
	switch inst.Config.Mode {
	case "ssh":
		rtt, err = c.probeSSH(inst)
	default:
		rtt, err = c.probeSOCKS(inst)
	}

	if err != nil {
		failCount := c.recordFailure(inst.ID())
		if failCount >= maxConsecutiveFailures {
			if inst.State() != engine.StateUnhealthy {
				log.Printf("[health] instance %d (%s:%d) UNHEALTHY after %d tunnel failures: %v",
					inst.ID(), inst.Config.Domain, inst.Config.Port, failCount, err)
				inst.SetState(engine.StateUnhealthy)
				inst.SetLastPingMs(-1)
				go func() {
					log.Printf("[health] auto-restarting instance %d", inst.ID())
					c.manager.RestartInstance(inst.ID())
				}()
			}
		} else {
			log.Printf("[health] instance %d (%s:%d) tunnel probe failed (%d/%d): %v",
				inst.ID(), inst.Config.Domain, inst.Config.Port,
				failCount, maxConsecutiveFailures, err)
		}
		return
	}

	// Step 3: End-to-end probe — full SOCKS5 CONNECT + HTTP through tunnel.
	// Tests entire path: instance → DNS tunnel → target server → SOCKS → internet.
	if c.target != "" && inst.Config.Mode != "ssh" {
		e2eRtt, e2eErr := c.probeEndToEnd(inst)
		if e2eErr != nil {
			failCount := c.recordFailure(inst.ID())
			if failCount >= maxConsecutiveFailures {
				if inst.State() != engine.StateUnhealthy {
					log.Printf("[health] instance %d (%s:%d) UNHEALTHY after %d e2e failures: %v",
						inst.ID(), inst.Config.Domain, inst.Config.Port, failCount, e2eErr)
					inst.SetState(engine.StateUnhealthy)
					inst.SetLastPingMs(-1)
				}
			} else {
				log.Printf("[health] instance %d (%s:%d) e2e probe failed (%d/%d): %v",
					inst.ID(), inst.Config.Domain, inst.Config.Port,
					failCount, maxConsecutiveFailures, e2eErr)
			}
			return
		}
		if e2eRtt > rtt {
			rtt = e2eRtt
		}
	}

	// All probes succeeded → HEALTHY
	c.recordSuccess(inst.ID())

	pingMs := rtt.Milliseconds()
	if pingMs <= 0 {
		pingMs = 1
	}
	inst.SetLastPingMs(pingMs)

	if inst.State() != engine.StateHealthy {
		log.Printf("[health] instance %d (%s:%d) now HEALTHY (rtt=%dms)",
			inst.ID(), inst.Config.Domain, inst.Config.Port, pingMs)
		inst.SetState(engine.StateHealthy)
	}
}

func (c *Checker) probeSOCKS(inst *engine.Instance) (time.Duration, error) {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", inst.Addr(), c.timeout)
	if err != nil {
		return 0, fmt.Errorf("tcp connect: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(c.timeout))

	_, err = conn.Write([]byte{0x05, 0x01, 0x00})
	if err != nil {
		return 0, fmt.Errorf("socks5 write: %w", err)
	}
	resp := make([]byte, 2)
	_, err = io.ReadFull(conn, resp)
	if err != nil {
		return 0, fmt.Errorf("socks5 read: %w", err)
	}
	if resp[0] != 0x05 {
		return 0, fmt.Errorf("socks5 bad version: %d", resp[0])
	}
	return time.Since(start), nil
}

func (c *Checker) probeSSH(inst *engine.Instance) (time.Duration, error) {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", inst.Addr(), c.timeout)
	if err != nil {
		return 0, fmt.Errorf("tcp connect: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(c.timeout))

	banner := make([]byte, 64)
	n, err := conn.Read(banner)
	if err != nil {
		return 0, fmt.Errorf("ssh banner read: %w", err)
	}
	if n < 4 || string(banner[:4]) != "SSH-" {
		return 0, fmt.Errorf("ssh bad banner: %q", string(banner[:n]))
	}
	return time.Since(start), nil
}

// probeEndToEnd does a full SOCKS5 CONNECT through the tunnel to the health
// check target (port 80), sends an HTTP HEAD request, and verifies a response.
// This tests the entire path: instance → DNS tunnel → centralserver → SOCKS upstream → internet.
func (c *Checker) probeEndToEnd(inst *engine.Instance) (time.Duration, error) {
	start := time.Now()

	conn, err := net.DialTimeout("tcp", inst.Addr(), c.timeout)
	if err != nil {
		return 0, fmt.Errorf("e2e connect: %w", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(c.timeout))

	// SOCKS5 greeting (no auth)
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return 0, fmt.Errorf("e2e socks greeting: %w", err)
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(conn, greeting); err != nil {
		return 0, fmt.Errorf("e2e socks greeting resp: %w", err)
	}
	if greeting[0] != 0x05 {
		return 0, fmt.Errorf("e2e bad socks version: %d", greeting[0])
	}

	// SOCKS5 CONNECT to target:80
	domain := c.target
	connectReq := make([]byte, 0, 4+1+len(domain)+2)
	connectReq = append(connectReq, 0x05, 0x01, 0x00, 0x03) // VER CMD RSV ATYP(domain)
	connectReq = append(connectReq, byte(len(domain)))      // domain length
	connectReq = append(connectReq, []byte(domain)...)      // domain
	portBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(portBuf, 80)
	connectReq = append(connectReq, portBuf...)

	if _, err := conn.Write(connectReq); err != nil {
		return 0, fmt.Errorf("e2e socks connect: %w", err)
	}

	// Read CONNECT response (VER REP RSV ATYP)
	connectResp := make([]byte, 4)
	if _, err := io.ReadFull(conn, connectResp); err != nil {
		return 0, fmt.Errorf("e2e socks connect resp: %w", err)
	}
	if connectResp[1] != 0x00 {
		return 0, fmt.Errorf("e2e socks connect rejected: 0x%02x", connectResp[1])
	}

	// Drain bind address
	switch connectResp[3] {
	case 0x01:
		io.ReadFull(conn, make([]byte, 4+2))
	case 0x03:
		lb := make([]byte, 1)
		io.ReadFull(conn, lb)
		io.ReadFull(conn, make([]byte, int(lb[0])+2))
	case 0x04:
		io.ReadFull(conn, make([]byte, 16+2))
	default:
		io.ReadFull(conn, make([]byte, 4+2))
	}

	// Send HTTP HEAD request
	httpReq := fmt.Sprintf("HEAD / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", domain)
	if _, err := conn.Write([]byte(httpReq)); err != nil {
		return 0, fmt.Errorf("e2e http write: %w", err)
	}

	// Read HTTP response (at least status line)
	respBuf := make([]byte, 128)
	n, err := conn.Read(respBuf)
	if err != nil && n == 0 {
		return 0, fmt.Errorf("e2e http read: %w", err)
	}
	if n < 12 || string(respBuf[:4]) != "HTTP" {
		return 0, fmt.Errorf("e2e bad http response: %q", string(respBuf[:n]))
	}

	return time.Since(start), nil
}
