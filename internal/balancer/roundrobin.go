package balancer

import (
	"sync/atomic"

	"github.com/slipstreamplus/slipstreamplus/internal/engine"
)

type RoundRobin struct {
	counter atomic.Uint64
}

func NewRoundRobin() *RoundRobin {
	return &RoundRobin{}
}

func (rr *RoundRobin) Pick(instances []*engine.Instance) *engine.Instance {
	if len(instances) == 0 {
		return nil
	}
	idx := rr.counter.Add(1) - 1
	return instances[idx%uint64(len(instances))]
}
