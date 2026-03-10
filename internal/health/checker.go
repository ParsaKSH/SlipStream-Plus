package health

import (
	"context"
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
		ctx:      ctx,
		cancel:   cancel,
		failures: make(map[int]int),
	}
}

func (c *Checker) Start() {
	go c.run()
	log.Printf("[health] checker started (interval=%s, tunnel_timeout=%s, unhealthy_after=%d failures)",
		c.interval, c.timeout, maxConsecutiveFailures)
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
		// Process is not listening → immediately unhealthy
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
	// This sends data through the DNS tunnel and measures real RTT.
	var rtt time.Duration
	switch inst.Config.Mode {
	case "ssh":
		rtt, err = c.probeSSH(inst)
	default:
		rtt, err = c.probeSOCKS(inst)
	}

	if err != nil {
		// Tunnel probe failed
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

	// Tunnel probe succeeded → HEALTHY with real latency
	c.recordSuccess(inst.ID())

	pingMs := rtt.Milliseconds()
	if pingMs <= 0 {
		pingMs = 1
	}
	inst.SetLastPingMs(pingMs)

	if inst.State() != engine.StateHealthy {
		log.Printf("[health] instance %d (%s:%d) now HEALTHY (tunnel_rtt=%dms)",
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
