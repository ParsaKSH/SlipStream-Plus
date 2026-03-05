package balancer

import (
	"math"

	"github.com/slipstreamplus/slipstreamplus/internal/engine"
)

type LeastLoad struct{}

func NewLeastLoad() *LeastLoad {
	return &LeastLoad{}
}

func (ll *LeastLoad) Pick(instances []*engine.Instance) *engine.Instance {
	if len(instances) == 0 {
		return nil
	}

	var best *engine.Instance
	bestLoad := int64(math.MaxInt64)

	for _, inst := range instances {
		load := inst.ActiveConns()
		if load < bestLoad {
			bestLoad = load
			best = inst
		}
	}

	if best == nil {
		return instances[0]
	}
	return best
}
