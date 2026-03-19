package engine

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ParsaKSH/SlipStream-Plus/internal/config"
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

	// Per-instance buffer pool — isolated per instance, fresh on restart.
	BufPool sync.Pool

	// ConnCtx is cancelled when the instance is stopped, killing all in-flight relays.
	ConnCtx    context.Context
	connCancel context.CancelFunc

	mu          sync.RWMutex
	state       InstanceState
	cmd         *exec.Cmd
	activeConns atomic.Int64
	lastPingMs  atomic.Int64
	txBytes     atomic.Int64 // total bytes uploaded (client → upstream)
	rxBytes     atomic.Int64 // total bytes downloaded (upstream → client)
	stopCh      chan struct{}
	id          int
	bufferSize  int
}

func NewInstance(id int, cfg config.ExpandedInstance, binary string, bufferSize int) *Instance {
	ctx, cancel := context.WithCancel(context.Background())
	inst := &Instance{
		Config:     cfg,
		Binary:     binary,
		id:         id,
		state:      StateDead,
		stopCh:     make(chan struct{}),
		bufferSize: bufferSize,
		ConnCtx:    ctx,
		connCancel: cancel,
	}
	inst.BufPool = sync.Pool{
		New: func() any {
			buf := make([]byte, inst.bufferSize)
			return &buf
		},
	}
	return inst
}

func (inst *Instance) ID() int                { return inst.id }
func (inst *Instance) ActiveConns() int64     { return inst.activeConns.Load() }
func (inst *Instance) IncrConns()             { inst.activeConns.Add(1) }
func (inst *Instance) DecrConns()             { inst.activeConns.Add(-1) }
func (inst *Instance) LastPingMs() int64      { return inst.lastPingMs.Load() }
func (inst *Instance) SetLastPingMs(ms int64) { inst.lastPingMs.Store(ms) }
func (inst *Instance) Addr() string           { return fmt.Sprintf("127.0.0.1:%d", inst.Config.Port) }

func (inst *Instance) TxBytes() int64 { return inst.txBytes.Load() }
func (inst *Instance) RxBytes() int64 { return inst.rxBytes.Load() }
func (inst *Instance) AddTx(n int64)  { inst.txBytes.Add(n) }
func (inst *Instance) AddRx(n int64)  { inst.rxBytes.Add(n) }

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

func (inst *Instance) Dial() (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", inst.Addr(), 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial instance %d (%s): %w", inst.id, inst.Config.Domain, err)
	}
	return conn, nil
}

func (inst *Instance) Start() error {
	inst.mu.Lock()
	defer inst.mu.Unlock()

	// Fresh context + buffer pool for this lifecycle.
	ctx, cancel := context.WithCancel(context.Background())
	inst.ConnCtx = ctx
	inst.connCancel = cancel
	inst.BufPool = sync.Pool{
		New: func() any {
			buf := make([]byte, inst.bufferSize)
			return &buf
		},
	}

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
	cmd.SysProcAttr = procAttr()

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
	inst.state = StateStarting // wait for health checker to verify

	prefix := fmt.Sprintf("[instance-%d/%s:%d]", inst.id, inst.Config.Domain, inst.Config.Port)
	log.Printf("%s started (pid=%d, mode=%s)", prefix, cmd.Process.Pid, inst.Config.Mode)

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

	return nil
}

func (inst *Instance) Stop() error {
	inst.mu.Lock()
	defer inst.mu.Unlock()

	// Cancel all in-flight relay goroutines for this instance.
	if inst.connCancel != nil {
		inst.connCancel()
	}

	select {
	case <-inst.stopCh:
	default:
		close(inst.stopCh)
	}

	if inst.cmd != nil && inst.cmd.Process != nil {
		pid := inst.cmd.Process.Pid
		killProcessGroup(pid)
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

type StatusInfo struct {
	ID            int    `json:"id"`
	Domain        string `json:"domain"`
	Resolver      string `json:"resolver"`
	Port          int    `json:"port"`
	Mode          string `json:"mode"`
	State         string `json:"state"`
	ActiveConns   int64  `json:"active_conns"`
	LastPingMs    int64  `json:"last_ping_ms"`
	TxBytes       int64  `json:"tx_bytes"`
	RxBytes       int64  `json:"rx_bytes"`
	OriginalIndex int    `json:"original_index"`
	ReplicaIndex  int    `json:"replica_index"`
}

func (inst *Instance) StatusInfo() StatusInfo {
	return StatusInfo{
		ID:            inst.id,
		Domain:        inst.Config.Domain,
		Resolver:      inst.Config.Resolver,
		Port:          inst.Config.Port,
		Mode:          inst.Config.Mode,
		State:         inst.State().String(),
		ActiveConns:   inst.ActiveConns(),
		LastPingMs:    inst.LastPingMs(),
		TxBytes:       inst.TxBytes(),
		RxBytes:       inst.RxBytes(),
		OriginalIndex: inst.Config.OriginalIndex,
		ReplicaIndex:  inst.Config.ReplicaIndex,
	}
}
