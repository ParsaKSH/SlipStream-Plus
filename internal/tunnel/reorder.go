package tunnel

import "time"

// Reorderer buffers out-of-order frames and delivers them in sequence order.
// If a frame is missing for longer than gapTimeout, it is skipped to prevent
// permanent stalls from lost frames.
type Reorderer struct {
	nextSeq      uint32
	buffer       map[uint32][]byte
	gapTimeout   time.Duration
	waitingSince time.Time // when we first started waiting for nextSeq
}

func NewReorderer() *Reorderer {
	return &Reorderer{
		nextSeq:    0,
		buffer:     make(map[uint32][]byte),
		gapTimeout: 2 * time.Second,
	}
}

func NewReordererAt(startSeq uint32) *Reorderer {
	return &Reorderer{
		nextSeq:    startSeq,
		buffer:     make(map[uint32][]byte),
		gapTimeout: 2 * time.Second,
	}
}

func (r *Reorderer) Insert(seq uint32, data []byte) {
	if seq < r.nextSeq {
		return
	}
	r.buffer[seq] = data
}

func (r *Reorderer) Next() []byte {
	// Fast path: next seq is available
	data, ok := r.buffer[r.nextSeq]
	if ok {
		delete(r.buffer, r.nextSeq)
		r.nextSeq++
		r.waitingSince = time.Time{} // reset wait timer
		return data
	}

	// Nothing buffered at all — nothing to skip to
	if len(r.buffer) == 0 {
		r.waitingSince = time.Time{}
		return nil
	}

	// There are buffered frames but nextSeq is missing.
	// Start or check the gap timer.
	now := time.Now()
	if r.waitingSince.IsZero() {
		r.waitingSince = now
		return nil
	}

	if now.Sub(r.waitingSince) < r.gapTimeout {
		return nil // still within grace period
	}

	// Gap timeout expired — skip to the lowest available seq
	r.skipToLowest()
	r.waitingSince = time.Time{}

	data, ok = r.buffer[r.nextSeq]
	if ok {
		delete(r.buffer, r.nextSeq)
		r.nextSeq++
		return data
	}
	return nil
}

// skipToLowest advances nextSeq to the lowest seq number in the buffer.
func (r *Reorderer) skipToLowest() {
	minSeq := r.nextSeq
	found := false
	for seq := range r.buffer {
		if !found || seq < minSeq {
			minSeq = seq
			found = true
		}
	}
	if found && minSeq > r.nextSeq {
		r.nextSeq = minSeq
	}
}

func (r *Reorderer) Pending() int {
	return len(r.buffer)
}

func (r *Reorderer) SkipGap() {
	r.nextSeq++
}
