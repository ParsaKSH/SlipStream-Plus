package balancer

import (
	"github.com/slipstreamplus/slipstreamplus/internal/engine"
)

// Balancer selects an instance for a new connection.
type Balancer interface {
	Pick(instances []*engine.Instance) *engine.Instance
}

// New creates a balancer by strategy name.
func New(strategy string) Balancer {
	switch strategy {
	case "round_robin":
		return NewRoundRobin()
	case "random":
		return NewRandom()
	case "least_ping":
		return NewLeastPing()
	case "least_load":
		return NewLeastLoad()
	default:
		return NewRoundRobin()
	}
}
