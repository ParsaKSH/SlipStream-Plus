package balancer

import (
	"math/rand"

	"github.com/slipstreamplus/slipstreamplus/internal/engine"
)

type Random struct{}

func NewRandom() *Random {
	return &Random{}
}

func (r *Random) Pick(instances []*engine.Instance) *engine.Instance {
	if len(instances) == 0 {
		return nil
	}
	return instances[rand.Intn(len(instances))]
}
