package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Socks            SocksConfig       `json:"socks"`
	SlipstreamBinary string            `json:"slipstream_binary,omitempty"`
	Strategy         string            `json:"strategy"`
	HealthCheck      HealthCheckConfig `json:"health_check"`
	Instances        []InstanceConfig  `json:"instances"`
	GUI              GUIConfig         `json:"gui,omitempty"`
}

type SocksConfig struct {
	Listen         string       `json:"listen"`
	BufferSize     int          `json:"buffer_size"`
	MaxConnections int          `json:"max_connections"`
	Users          []UserConfig `json:"users,omitempty"`
}

type UserConfig struct {
	Username       string `json:"username"`
	Password       string `json:"password"`
	BandwidthLimit int    `json:"bandwidth_limit,omitempty"` // 0 = unlimited
	BandwidthUnit  string `json:"bandwidth_unit,omitempty"`  // "kbit" or "mbit"
	DataLimit      int    `json:"data_limit,omitempty"`      // 0 = unlimited
	DataUnit       string `json:"data_unit,omitempty"`       // "mb" or "gb"
	IPLimit        int    `json:"ip_limit,omitempty"`        // 0 = unlimited concurrent IPs
}

// BandwidthBytesPerSec returns the bandwidth limit in bytes/sec.
func (u *UserConfig) BandwidthBytesPerSec() int64 {
	if u.BandwidthLimit <= 0 {
		return 0
	}
	switch u.BandwidthUnit {
	case "mbit":
		return int64(u.BandwidthLimit) * 1000 * 1000 / 8
	default: // kbit
		return int64(u.BandwidthLimit) * 1000 / 8
	}
}

// DataLimitBytes returns the data limit in bytes.
func (u *UserConfig) DataLimitBytes() int64 {
	if u.DataLimit <= 0 {
		return 0
	}
	switch u.DataUnit {
	case "gb":
		return int64(u.DataLimit) * 1024 * 1024 * 1024
	default: // mb
		return int64(u.DataLimit) * 1024 * 1024
	}
}

type GUIConfig struct {
	Enabled bool   `json:"enabled"`
	Listen  string `json:"listen"`
}

type HealthCheckConfig struct {
	Interval string `json:"interval"`
	Target   string `json:"target"`
	Timeout  string `json:"timeout"`
}

type InstanceConfig struct {
	Domain        string          `json:"domain"`
	Resolver      string          `json:"resolver"`
	Port          json.RawMessage `json:"port"`
	Replicas      int             `json:"replicas,omitempty"`
	Mode          string          `json:"mode,omitempty"`
	Authoritative bool            `json:"authoritative"`
	Cert          string          `json:"cert,omitempty"`
	SSHPort       int             `json:"ssh_port,omitempty"`
	SSHUser       string          `json:"ssh_user,omitempty"`
	SSHPassword   string          `json:"ssh_password,omitempty"`
	SSHKey        string          `json:"ssh_key,omitempty"`
}

func (ic *InstanceConfig) ParsePorts() ([]int, error) {
	raw := strings.TrimSpace(string(ic.Port))
	if port, err := strconv.Atoi(raw); err == nil {
		return []int{port}, nil
	}
	var portStr string
	if err := json.Unmarshal(ic.Port, &portStr); err == nil {
		return parsePortRange(portStr)
	}
	var portNum int
	if err := json.Unmarshal(ic.Port, &portNum); err == nil {
		return []int{portNum}, nil
	}
	return nil, fmt.Errorf("invalid port value: %s", raw)
}

func parsePortRange(s string) ([]int, error) {
	s = strings.TrimSpace(s)
	if port, err := strconv.Atoi(s); err == nil {
		return []int{port}, nil
	}
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid port range: %s", s)
	}
	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return nil, fmt.Errorf("invalid port range start: %s", parts[0])
	}
	end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return nil, fmt.Errorf("invalid port range end: %s", parts[1])
	}
	if start > end {
		return nil, fmt.Errorf("port range start (%d) must be <= end (%d)", start, end)
	}
	if start <= 0 || end > 65535 {
		return nil, fmt.Errorf("ports must be between 1 and 65535")
	}
	ports := make([]int, 0, end-start+1)
	for p := start; p <= end; p++ {
		ports = append(ports, p)
	}
	return ports, nil
}

type ExpandedInstance struct {
	Domain        string
	Resolver      string
	Port          int
	Mode          string // "socks" or "ssh" (per-instance)
	Authoritative bool
	Cert          string
	SSHPort       int
	SSHUser       string
	SSHPassword   string
	SSHKey        string
	OriginalIndex int
	ReplicaIndex  int
}

func (c *Config) ExpandInstances() ([]ExpandedInstance, error) {
	var result []ExpandedInstance

	for i, inst := range c.Instances {
		replicas := inst.Replicas
		if replicas <= 0 {
			replicas = 1
		}

		ports, err := inst.ParsePorts()
		if err != nil {
			return nil, fmt.Errorf("instances[%d]: %w", i, err)
		}

		if len(ports) == 1 && replicas > 1 {
			basePort := ports[0]
			ports = make([]int, replicas)
			for r := 0; r < replicas; r++ {
				ports[r] = basePort + r
			}
		}

		if len(ports) < replicas {
			return nil, fmt.Errorf("instances[%d]: port range provides %d ports but replicas=%d",
				i, len(ports), replicas)
		}

		// Resolve mode: per-instance overrides default "socks"
		mode := inst.Mode
		if mode == "" {
			mode = "socks"
		}

		for r := 0; r < replicas; r++ {
			result = append(result, ExpandedInstance{
				Domain:        inst.Domain,
				Resolver:      inst.Resolver,
				Port:          ports[r],
				Mode:          mode,
				Authoritative: inst.Authoritative,
				Cert:          inst.Cert,
				SSHPort:       inst.SSHPort,
				SSHUser:       inst.SSHUser,
				SSHPassword:   inst.SSHPassword,
				SSHKey:        inst.SSHKey,
				OriginalIndex: i,
				ReplicaIndex:  r,
			})
		}
	}

	portSet := make(map[int]bool)
	for _, ei := range result {
		if portSet[ei.Port] {
			return nil, fmt.Errorf("duplicate port %d after expansion", ei.Port)
		}
		portSet[ei.Port] = true
	}

	return result, nil
}

func (c *HealthCheckConfig) IntervalDuration() time.Duration {
	d, err := time.ParseDuration(c.Interval)
	if err != nil {
		return 10 * time.Second
	}
	return d
}

func (c *HealthCheckConfig) TimeoutDuration() time.Duration {
	d, err := time.ParseDuration(c.Timeout)
	if err != nil {
		return 5 * time.Second
	}
	return d
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c.Socks.Listen == "" {
		return fmt.Errorf("socks.listen is required")
	}
	if len(c.Instances) == 0 {
		return fmt.Errorf("at least one instance is required")
	}

	switch c.Strategy {
	case "round_robin", "random", "least_ping", "least_load":
	case "":
		c.Strategy = "round_robin"
	default:
		return fmt.Errorf("invalid strategy: %s (valid: round_robin, random, least_ping, least_load)", c.Strategy)
	}

	if c.Socks.BufferSize <= 0 {
		c.Socks.BufferSize = 65536
	}
	if c.Socks.MaxConnections <= 0 {
		c.Socks.MaxConnections = 10000
	}

	for i, inst := range c.Instances {
		if inst.Domain == "" {
			return fmt.Errorf("instances[%d].domain is required", i)
		}
		if inst.Resolver == "" {
			return fmt.Errorf("instances[%d].resolver is required", i)
		}
		if inst.Port == nil {
			return fmt.Errorf("instances[%d].port is required", i)
		}

		// Validate per-instance mode
		switch inst.Mode {
		case "", "socks", "ssh":
		default:
			return fmt.Errorf("instances[%d].mode invalid: %s (valid: socks, ssh)", i, inst.Mode)
		}

		if inst.Mode == "ssh" {
			if inst.SSHUser == "" {
				return fmt.Errorf("instances[%d].ssh_user is required in ssh mode", i)
			}
			if inst.SSHPassword == "" && inst.SSHKey == "" {
				return fmt.Errorf("instances[%d]: ssh_password or ssh_key is required in ssh mode", i)
			}
			if inst.SSHPort <= 0 {
				c.Instances[i].SSHPort = 22
			}
		}
	}

	if _, err := c.ExpandInstances(); err != nil {
		return err
	}

	if c.HealthCheck.Interval == "" {
		c.HealthCheck.Interval = "10s"
	}
	if c.HealthCheck.Target == "" {
		c.HealthCheck.Target = "google.com"
	}
	if c.HealthCheck.Timeout == "" {
		c.HealthCheck.Timeout = "5s"
	}
	if c.GUI.Listen == "" {
		c.GUI.Listen = "127.0.0.1:8384"
	}

	return nil
}

func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "    ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}
