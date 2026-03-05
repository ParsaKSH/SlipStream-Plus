package balancer

import (
	"math"

	"github.com/slipstreamplus/slipstreamplus/internal/engine"
)

type LeastPing struct{}

func NewLeastPing() *LeastPing {
	return &LeastPing{}
}

func (lp *LeastPing) Pick(instances []*engine.Instance) *engine.Instance {
	if len(instances) == 0 {
		return nil
	}

	var best *engine.Instance
	bestPing := int64(math.MaxInt64)

	for _, inst := range instances {
		ping := inst.LastPingMs()
		if ping <= 0 {
			// No ping data yet, treat as high latency
			ping = math.MaxInt64 - 1
		}
		if ping < bestPing {
			bestPing = ping
			best = inst
		}
	}

	if best == nil {
		return instances[0]
	}
	return best
}
