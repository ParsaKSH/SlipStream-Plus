package health

import (
	"context"
	"log"
	"net"
	"time"

	"github.com/slipstreamplus/slipstreamplus/internal/config"
	"github.com/slipstreamplus/slipstreamplus/internal/engine"
)

type Checker struct {
	manager  *engine.Manager
	interval time.Duration
	timeout  time.Duration
	ctx      context.Context
	cancel   context.CancelFunc
}

func NewChecker(mgr *engine.Manager, cfg *config.HealthCheckConfig) *Checker {
	ctx, cancel := context.WithCancel(context.Background())
	return &Checker{
		manager:  mgr,
		interval: cfg.IntervalDuration(),
		timeout:  cfg.TimeoutDuration(),
		ctx:      ctx,
		cancel:   cancel,
	}
}

func (c *Checker) Start() {
	go c.run()
	log.Printf("[health] checker started (interval=%s)", c.interval)
}

func (c *Checker) Stop() {
	c.cancel()
}

func (c *Checker) run() {
	// Wait for instances to start up
	select {
	case <-time.After(5 * time.Second):
	case <-c.ctx.Done():
		return
	}

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
			continue
		}
		go c.checkOne(inst)
	}
}

func (c *Checker) checkOne(inst *engine.Instance) {
	start := time.Now()

	// Simple TCP connect test to the instance's local port.
	// We do NOT send any data through the tunnel (no HTTP) because
	// the remote target may be a SOCKS proxy or other protocol
	// that would reject raw HTTP requests.
	conn, err := net.DialTimeout("tcp", inst.Addr(), c.timeout)
	if err != nil {
		log.Printf("[health] instance %d (%s) TCP probe failed: %v",
			inst.ID(), inst.Config.Domain, err)
		// Do NOT mark unhealthy here — let the process supervisor handle state.
		// Health checker only updates latency for the least_ping strategy.
		return
	}
	conn.Close()

	elapsed := time.Since(start)
	pingMs := elapsed.Milliseconds()
	if pingMs == 0 {
		pingMs = 1 // avoid zero which means "no data"
	}
	inst.SetLastPingMs(pingMs)
}
