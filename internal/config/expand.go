package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

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
