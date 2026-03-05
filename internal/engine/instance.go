package engine

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/slipstreamplus/slipstreamplus/internal/config"
)

type InstanceState int

const (
	StateStarting InstanceState = iota
	StateHealthy
	StateUnhealthy
	StateDead
)

func (s InstanceState) String() string {
	switch s {
	case StateStarting:
		return "starting"
	case StateHealthy:
		return "healthy"
	case StateUnhealthy:
		return "unhealthy"
	case StateDead:
		return "dead"
	default:
		return "unknown"
	}
}

type Instance struct {
	Config config.ExpandedInstance
	Binary string

	mu          sync.RWMutex
	state       InstanceState
	cmd         *exec.Cmd
	activeConns atomic.Int64
	lastPingMs  atomic.Int64
	stopCh      chan struct{}
	id          int
}

func NewInstance(id int, cfg config.ExpandedInstance, binary string) *Instance {
	return &Instance{
		Config: cfg,
		Binary: binary,
		id:     id,
		state:  StateDead,
		stopCh: make(chan struct{}),
	}
}

func (inst *Instance) ID() int {
	return inst.id
}

func (inst *Instance) State() InstanceState {
	inst.mu.RLock()
	defer inst.mu.RUnlock()
	return inst.state
}

func (inst *Instance) SetState(s InstanceState) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	inst.state = s
}

func (inst *Instance) IsHealthy() bool {
	return inst.State() == StateHealthy
}

func (inst *Instance) ActiveConns() int64 {
	return inst.activeConns.Load()
}

func (inst *Instance) IncrConns() {
	inst.activeConns.Add(1)
}

func (inst *Instance) DecrConns() {
	inst.activeConns.Add(-1)
}

func (inst *Instance) LastPingMs() int64 {
	return inst.lastPingMs.Load()
}

func (inst *Instance) SetLastPingMs(ms int64) {
	inst.lastPingMs.Store(ms)
}

func (inst *Instance) Addr() string {
	return fmt.Sprintf("127.0.0.1:%d", inst.Config.Port)
}

func (inst *Instance) Dial() (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", inst.Addr(), 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial instance %d (%s): %w", inst.id, inst.Config.Domain, err)
	}
	return conn, nil
}

func (inst *Instance) Start() error {
	inst.mu.Lock()
	defer inst.mu.Unlock()

	args := []string{
		"--tcp-listen-port", fmt.Sprintf("%d", inst.Config.Port),
		"--tcp-listen-host", "127.0.0.1",
		"--domain", inst.Config.Domain,
	}

	if inst.Config.Authoritative {
		args = append(args, "--authoritative", inst.Config.Resolver)
	} else {
		args = append(args, "--resolver", inst.Config.Resolver)
	}

	if inst.Config.Cert != "" {
		args = append(args, "--cert", inst.Config.Cert)
	}

	cmd := exec.Command(inst.Binary, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start instance %d: %w", inst.id, err)
	}

	inst.cmd = cmd
	inst.state = StateStarting

	prefix := fmt.Sprintf("[instance-%d/%s:%d]", inst.id, inst.Config.Domain, inst.Config.Port)

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			log.Printf("%s stdout: %s", prefix, scanner.Text())
		}
	}()

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			log.Printf("%s stderr: %s", prefix, scanner.Text())
		}
	}()

	go func() {
		time.Sleep(2 * time.Second)
		inst.mu.RLock()
		currentCmd := inst.cmd
		inst.mu.RUnlock()
		if currentCmd != nil && currentCmd.ProcessState == nil {
			inst.SetState(StateHealthy)
			log.Printf("%s marked healthy", prefix)
		}
	}()

	return nil
}

func (inst *Instance) Stop() error {
	inst.mu.Lock()
	defer inst.mu.Unlock()

	select {
	case <-inst.stopCh:
	default:
		close(inst.stopCh)
	}

	if inst.cmd != nil && inst.cmd.Process != nil {
		if err := inst.cmd.Process.Kill(); err != nil {
			return fmt.Errorf("kill instance %d: %w", inst.id, err)
		}
		inst.cmd.Wait()
	}

	inst.state = StateDead
	inst.cmd = nil
	return nil
}

func (inst *Instance) WaitForExit() error {
	inst.mu.RLock()
	cmd := inst.cmd
	inst.mu.RUnlock()

	if cmd == nil {
		return fmt.Errorf("instance %d not started", inst.id)
	}

	err := cmd.Wait()
	inst.SetState(StateDead)
	return err
}

// StatusInfo returns a snapshot of the instance status for the GUI API.
type StatusInfo struct {
	ID            int    `json:"id"`
	Domain        string `json:"domain"`
	Resolver      string `json:"resolver"`
	Port          int    `json:"port"`
	State         string `json:"state"`
	ActiveConns   int64  `json:"active_conns"`
	LastPingMs    int64  `json:"last_ping_ms"`
	OriginalIndex int    `json:"original_index"`
	ReplicaIndex  int    `json:"replica_index"`
}

func (inst *Instance) StatusInfo() StatusInfo {
	return StatusInfo{
		ID:            inst.id,
		Domain:        inst.Config.Domain,
		Resolver:      inst.Config.Resolver,
		Port:          inst.Config.Port,
		State:         inst.State().String(),
		ActiveConns:   inst.ActiveConns(),
		LastPingMs:    inst.LastPingMs(),
		OriginalIndex: inst.Config.OriginalIndex,
		ReplicaIndex:  inst.Config.ReplicaIndex,
	}
}
