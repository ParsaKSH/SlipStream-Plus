package engine

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/ParsaKSH/SlipStream-Plus/internal/config"
)

type Manager struct {
	instances  []*Instance
	binary     string
	bufferSize int
	mu         sync.RWMutex
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

func NewManager(cfg *config.Config) (*Manager, error) {
	ctx, cancel := context.WithCancel(context.Background())

	binary := cfg.SlipstreamBinary

	expanded, err := cfg.ExpandInstances()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("expand instances: %w", err)
	}

	bufSize := cfg.Socks.BufferSize
	if bufSize <= 0 {
		bufSize = 65536
	}

	m := &Manager{
		binary:     binary,
		bufferSize: bufSize,
		ctx:        ctx,
		cancel:     cancel,
	}

	for i, ei := range expanded {
		inst := NewInstance(i, ei, binary, bufSize)
		m.instances = append(m.instances, inst)
	}

	log.Printf("[manager] expanded %d config entries into %d instances",
		len(cfg.Instances), len(m.instances))

	return m, nil
}

func (m *Manager) SetBinary(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.binary = path
	for _, inst := range m.instances {
		inst.Binary = path
	}
}

func (m *Manager) StartAll() error {
	for _, inst := range m.instances {
		if err := inst.Start(); err != nil {
			return fmt.Errorf("start instance %d: %w", inst.ID(), err)
		}

		m.wg.Add(1)
		go m.supervise(inst)
	}

	log.Printf("[manager] started %d instances", len(m.instances))
	return nil
}

func (m *Manager) supervise(inst *Instance) {
	defer m.wg.Done()

	for {
		select {
		case <-m.ctx.Done():
			return
		default:
		}

		err := inst.WaitForExit()

		select {
		case <-m.ctx.Done():
			return
		default:
		}

		if err != nil {
			log.Printf("[manager] instance %d (%s:%d) exited: %v",
				inst.ID(), inst.Config.Domain, inst.Config.Port, err)
		} else {
			log.Printf("[manager] instance %d (%s:%d) exited cleanly",
				inst.ID(), inst.Config.Domain, inst.Config.Port)
		}

		inst.SetState(StateDead)

		backoff := 1 * time.Second
		maxBackoff := 30 * time.Second
		for attempt := 1; ; attempt++ {
			select {
			case <-m.ctx.Done():
				return
			case <-time.After(backoff):
			}

			log.Printf("[manager] restarting instance %d (%s:%d), attempt %d",
				inst.ID(), inst.Config.Domain, inst.Config.Port, attempt)

			if err := inst.Start(); err != nil {
				log.Printf("[manager] restart failed for instance %d: %v", inst.ID(), err)
				backoff = backoff * 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}

			log.Printf("[manager] instance %d restarted successfully", inst.ID())
			break
		}
	}
}

func (m *Manager) HealthyInstances() []*Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var healthy []*Instance
	for _, inst := range m.instances {
		if inst.IsHealthy() {
			healthy = append(healthy, inst)
		}
	}
	return healthy
}

func (m *Manager) AllInstances() []*Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*Instance, len(m.instances))
	copy(result, m.instances)
	return result
}

func (m *Manager) RestartInstance(id int) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, inst := range m.instances {
		if inst.ID() == id {
			log.Printf("[manager] manual restart of instance %d", id)
			if err := inst.Stop(); err != nil {
				return err
			}
			// Supervisor goroutine will detect exit and restart
			return nil
		}
	}
	return fmt.Errorf("instance %d not found", id)
}

func (m *Manager) StatusAll() []StatusInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]StatusInfo, len(m.instances))
	for i, inst := range m.instances {
		result[i] = inst.StatusInfo()
	}
	return result
}

func (m *Manager) Shutdown() {
	log.Printf("[manager] shutting down...")
	m.cancel()

	for _, inst := range m.instances {
		if err := inst.Stop(); err != nil {
			log.Printf("[manager] error stopping instance %d: %v", inst.ID(), err)
		}
	}

	m.wg.Wait()
	log.Printf("[manager] all instances stopped")
}

// Reload stops all instances, reloads from config, and restarts.
func (m *Manager) Reload(cfg *config.Config) error {
	log.Printf("[manager] reloading config...")

	// IMPORTANT: cancel context FIRST so supervisor goroutines exit
	// instead of trying to restart instances after we stop them.
	m.cancel()

	// Stop all current instances
	for _, inst := range m.instances {
		if err := inst.Stop(); err != nil {
			log.Printf("[manager] error stopping instance %d during reload: %v", inst.ID(), err)
		}
	}

	// Wait for all supervisor goroutines to exit
	m.wg.Wait()

	// Re-expand from new config
	m.mu.Lock()
	binary := cfg.SlipstreamBinary
	if binary == "" {
		binary = m.binary
	} else {
		m.binary = binary
	}

	expanded, err := cfg.ExpandInstances()
	if err != nil {
		m.mu.Unlock()
		return fmt.Errorf("expand instances: %w", err)
	}

	// Create fresh context for new supervisors
	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	m.cancel = cancel

	m.instances = make([]*Instance, len(expanded))
	for i, ei := range expanded {
		m.instances[i] = NewInstance(i, ei, binary, m.bufferSize)
	}
	m.mu.Unlock()

	log.Printf("[manager] reload: %d instances expanded", len(expanded))

	for _, inst := range m.instances {
		if err := inst.Start(); err != nil {
			log.Printf("[manager] reload: failed to start instance %d: %v", inst.ID(), err)
			continue
		}
		m.wg.Add(1)
		go m.supervise(inst)
	}

	log.Printf("[manager] reload complete: %d instances started", len(m.instances))
	return nil
}
