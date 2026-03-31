package health

import (
	"context"
	"log"
	"net"
	"sync"
	"sync/atomic"
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
	manager     *engine.Manager
	interval    time.Duration
	timeout     time.Duration
	target      string // health_check.target (e.g. "google.com")
	packetSplit bool   // true when strategy=packet_split
	ctx         context.Context
	cancel      context.CancelFunc

	mu       sync.Mutex
	failures map[int]int

	probeSeq atomic.Uint32 // unique probe counter to avoid ConnID collisions
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
	mode := "connection"
	if c.packetSplit {
		mode = "packet-split"
	}
	log.Printf("[health] checker started (interval=%s, tunnel_timeout=%s, target=%s, mode=%s, unhealthy_after=%d failures)",
		c.interval, c.timeout, c.target, mode, maxConsecutiveFailures)
}

// SetPacketSplit enables framing protocol health checks.
// Must be called before Start().
func (c *Checker) SetPacketSplit(enabled bool) {
	c.packetSplit = enabled
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

	// Step 3: End-to-end probe.
	// In packet_split mode: test if instance's upstream speaks our framing protocol.
	// In normal mode: full SOCKS5 CONNECT + HTTP through the tunnel.
	if c.target != "" && inst.Config.Mode != "ssh" {
		var e2eRtt time.Duration
		var e2eErr error

		if c.packetSplit {
			e2eRtt, e2eErr = c.probeFramingProtocol(inst)
		} else {
			e2eRtt, e2eErr = c.probeEndToEnd(inst)
		}

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
