package main

import (
	"context"
	"log"
	"net"
	"sync"
	"time"

	"github.com/ParsaKSH/SlipStream-Plus/internal/tunnel"
)

// t
// sourceConn wraps a tunnel connection with a write mutex so that
// concurrent WriteFrame calls from different ConnIDs don't interleave bytes.
type sourceConn struct {
	conn    net.Conn
	writeMu sync.Mutex
}

// writeTimeout prevents tunnel writes from blocking forever on congested TCP.
const sourceWriteTimeout = 10 * time.Second

func (sc *sourceConn) WriteFrame(f *tunnel.Frame) error {
	sc.writeMu.Lock()
	defer sc.writeMu.Unlock()
	sc.conn.SetWriteDeadline(time.Now().Add(sourceWriteTimeout))
	err := tunnel.WriteFrame(sc.conn, f)
	sc.conn.SetWriteDeadline(time.Time{})
	return err
}

// connState tracks a single reassembled connection.
type connState struct {
	mu         sync.Mutex
	target     net.Conn // connection to the SOCKS upstream
	reorderer  *tunnel.Reorderer
	txSeq      uint32 // next sequence number for reverse data
	cancel     context.CancelFunc
	created    time.Time
	lastActive time.Time // last time data was sent or received

	// Sources: all tunnel connections that can carry reverse data.
	// We round-robin responses across them (not broadcast).
	sources   []*sourceConn
	sourceIdx int

	// Async write queue: handleData sends chunks here instead of writing
	// to target synchronously (which would block the frame dispatch loop).
	// The upstreamWriter goroutine drains this channel and writes to target.
	writeCh chan []byte
}

// centralServer manages all active connections.
type centralServer struct {
	socksUpstream string

	mu    sync.RWMutex
	conns map[uint32]*connState // ConnID → state

	// sourceMu protects the sources map (net.Conn → *sourceConn).
	sourceMu  sync.Mutex
	sourceMap map[net.Conn]*sourceConn
}

// upstreamWriteTimeout prevents writes to Xray upstream from blocking forever.
const upstreamWriteTimeout = 10 * time.Second

// sendFrame picks ONE source via round-robin and writes the frame.
// CRITICAL: state.mu is only held briefly to pick the source and advance
// the index — the actual TCP write happens OUTSIDE the lock to prevent
// cascading lock contention that freezes frame dispatch for other ConnIDs.
func (cs *centralServer) sendFrame(connID uint32, frame *tunnel.Frame) {
	cs.mu.RLock()
	state, ok := cs.conns[connID]
	cs.mu.RUnlock()
	if !ok {
		return
	}

	// Snapshot sources under lock, then write outside lock
	state.mu.Lock()
	n := len(state.sources)
	if n == 0 {
		state.mu.Unlock()
		return
	}
	// Build ordered list starting from current sourceIdx
	sources := make([]*sourceConn, n)
	startIdx := state.sourceIdx % n
	state.sourceIdx++
	for i := 0; i < n; i++ {
		sources[i] = state.sources[(startIdx+i)%n]
	}
	state.mu.Unlock()

	// Try each source — write happens outside state.mu
	for _, sc := range sources {
		if err := sc.WriteFrame(frame); err != nil {
			// Remove dead source under lock
			state.mu.Lock()
			for i, s := range state.sources {
				if s == sc {
					state.sources = append(state.sources[:i], state.sources[i+1:]...)
					break
				}
			}
			state.mu.Unlock()
			continue
		}
		return // success
	}
	log.Printf("[central] conn=%d: all sources failed", connID)
}
