package balancer

import (
	"math"

	"github.com/ParsaKSH/SlipStream-Plus/internal/engine"
)

// WeightedRoundRobin distributes packets across instances proportionally
// to their inverse latency: faster instances receive more packets.
type WeightedRoundRobin struct {
	current int
	counter int
}

func NewWeightedRoundRobin() *WeightedRoundRobin {
	return &WeightedRoundRobin{}
}

// Pick selects an instance using weighted round-robin based on latency.
// In packet_split mode this is called per-packet, not per-connection.
func (w *WeightedRoundRobin) Pick(instances []*engine.Instance) *engine.Instance {
	if len(instances) == 0 {
		return nil
	}
	if len(instances) == 1 {
		return instances[0]
	}

	weights := computeWeights(instances)

	for attempts := 0; attempts < len(instances)*2; attempts++ {
		idx := w.current % len(instances)
		if w.counter < weights[idx] {
			w.counter++
			return instances[idx]
		}
		w.counter = 0
		w.current++
	}
	return instances[0]
}

// computeWeights calculates weights inversely proportional to latency.
func computeWeights(instances []*engine.Instance) []int {
	weights := make([]int, len(instances))
	maxPing := int64(1)
	for _, inst := range instances {
		p := inst.LastPingMs()
		if p > maxPing {
			maxPing = p
		}
	}
	if maxPing <= 0 {
		maxPing = 1
	}

	for i, inst := range instances {
		ping := inst.LastPingMs()
		if ping <= 0 {
			ping = math.MaxInt64 / 2
		}
		w := int(maxPing / ping)
		if w < 1 {
			w = 1
		}
		weights[i] = w
	}
	return weights
}
