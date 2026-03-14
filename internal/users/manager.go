package users

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/ParsaKSH/SlipStream-Plus/internal/config"
)

// User represents a runtime user with usage tracking.
type User struct {
	Config  config.UserConfig
	limiter *rate.Limiter // bandwidth rate limiter (nil = unlimited)

	usedBytes atomic.Int64 // total bytes consumed
	dataLimit int64        // max bytes (0 = unlimited)

	mu          sync.Mutex
	activeIPs   map[string]int       // IP → active connection count
	cooldownIPs map[string]time.Time // IP → disconnect time (for cooldown)
	ipLimit     int                  // max concurrent IPs (0 = unlimited)
}

// Manager handles user auth, rate limiting, quotas, and connection limits.
type Manager struct {
	mu       sync.RWMutex
	users    map[string]*User // username → User
	ordering []string         // insertion order of usernames
}

// NewManager creates a UserManager from config.
func NewManager(cfgUsers []config.UserConfig) *Manager {
	m := &Manager{
		users:    make(map[string]*User, len(cfgUsers)),
		ordering: make([]string, 0, len(cfgUsers)),
	}
	for _, cu := range cfgUsers {
		u := &User{
			Config:      cu,
			dataLimit:   cu.DataLimitBytes(),
			activeIPs:   make(map[string]int),
			cooldownIPs: make(map[string]time.Time),
			ipLimit:     cu.IPLimit,
		}

		// Setup bandwidth rate limiter
		bps := cu.BandwidthBytesPerSec()
		if bps > 0 {
			// burst = min(bps, 64KB) for smoother rate limiting
			burst := int(bps)
			if burst > 65536 {
				burst = 65536
			}
			if burst < 4096 {
				burst = 4096
			}
			u.limiter = rate.NewLimiter(rate.Limit(bps), burst)
			log.Printf("[users] user %q: bandwidth limit %d %s (%d bytes/sec, burst=%d)",
				cu.Username, cu.BandwidthLimit, cu.BandwidthUnit, bps, burst)
		}

		m.users[cu.Username] = u
		m.ordering = append(m.ordering, cu.Username)
	}

	log.Printf("[users] loaded %d users", len(m.users))
	return m
}

// HasUsers returns true if user auth is configured.
func (m *Manager) HasUsers() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.users) > 0
}

// Authenticate checks username/password.
func (m *Manager) Authenticate(username, password string) (*User, bool) {
	m.mu.RLock()
	u, ok := m.users[username]
	m.mu.RUnlock()

	if !ok || u.Config.Password != password {
		return nil, false
	}
	return u, true
}

// CheckConnect verifies a user can open a new connection from the given IP.
func (u *User) CheckConnect(clientIP string) string {
	// Check data quota
	if u.dataLimit > 0 && u.usedBytes.Load() >= u.dataLimit {
		return fmt.Sprintf("data quota exceeded (%d bytes used of %d)",
			u.usedBytes.Load(), u.dataLimit)
	}

	// Check IP limit
	if u.ipLimit <= 0 {
		return ""
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	now := time.Now()
	cooldown := 10 * time.Second

	// Clean up expired cooldowns
	for ip, disconnectTime := range u.cooldownIPs {
		if now.Sub(disconnectTime) > cooldown {
			delete(u.cooldownIPs, ip)
		}
	}

	// If this IP already has active connections, allow
	if u.activeIPs[clientIP] > 0 {
		return ""
	}

	// If this IP is in cooldown, deny
	if disconnectTime, inCooldown := u.cooldownIPs[clientIP]; inCooldown {
		remaining := cooldown - now.Sub(disconnectTime)
		return fmt.Sprintf("ip cooldown (%s remaining)", remaining.Round(time.Second))
	}

	// Count distinct active IPs
	activeCount := len(u.activeIPs)
	if activeCount >= u.ipLimit {
		return fmt.Sprintf("ip limit reached (%d/%d active IPs)", activeCount, u.ipLimit)
	}

	return ""
}

// MarkConnect records that a connection from this IP is active.
func (u *User) MarkConnect(clientIP string) {
	u.mu.Lock()
	u.activeIPs[clientIP]++
	// Remove from cooldown if reconnecting
	delete(u.cooldownIPs, clientIP)
	u.mu.Unlock()
}

// MarkDisconnect decrements active count; when zero, start cooldown.
func (u *User) MarkDisconnect(clientIP string) {
	u.mu.Lock()
	u.activeIPs[clientIP]--
	if u.activeIPs[clientIP] <= 0 {
		delete(u.activeIPs, clientIP)
		if u.ipLimit > 0 {
			u.cooldownIPs[clientIP] = time.Now()
		}
	}
	u.mu.Unlock()
}

// AddUsedBytes adds to the total bytes consumed.
func (u *User) AddUsedBytes(n int64) {
	u.usedBytes.Add(n)
}

// UsedBytes returns total bytes consumed.
func (u *User) UsedBytes() int64 {
	return u.usedBytes.Load()
}

// ResetUsedBytes resets the data counter.
func (u *User) ResetUsedBytes() {
	u.usedBytes.Store(0)
}

// NeedsRateLimit returns true if this user has an active bandwidth limiter.
// When false, the proxy can use zero-copy (splice) relay for much better performance.
func (u *User) NeedsRateLimit() bool {
	return u.limiter != nil
}

// WrapReader wraps a reader with rate limiting and byte tracking for this user.
func (u *User) WrapReader(r io.Reader) io.Reader {
	if u.limiter == nil {
		return &trackingReader{r: r, user: u}
	}
	return &rateLimitedReader{r: r, limiter: u.limiter, user: u}
}

// WrapWriter wraps a writer with rate limiting and byte tracking for this user.
func (u *User) WrapWriter(w io.Writer) io.Writer {
	if u.limiter == nil {
		return &trackingWriter{w: w, user: u}
	}
	return &rateLimitedWriter{w: w, limiter: u.limiter, user: u}
}

// AllUsers returns all users in config insertion order.
func (m *Manager) AllUsers() []*User {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*User, 0, len(m.ordering))
	for _, name := range m.ordering {
		if u, ok := m.users[name]; ok {
			result = append(result, u)
		}
	}
	return result
}

// GetUser returns a user by username.
func (m *Manager) GetUser(username string) *User {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.users[username]
}

// UserStatus returns JSON-friendly status for a user.
type UserStatus struct {
	Username       string `json:"username"`
	BandwidthLimit int    `json:"bandwidth_limit"`
	BandwidthUnit  string `json:"bandwidth_unit"`
	DataLimit      int    `json:"data_limit"`
	DataUnit       string `json:"data_unit"`
	DataUsedBytes  int64  `json:"data_used_bytes"`
	IPLimit        int    `json:"ip_limit"`
	ActiveIPs      int    `json:"active_ips"`
}

func (u *User) Status() UserStatus {
	u.mu.Lock()
	activeCount := len(u.activeIPs)
	u.mu.Unlock()

	return UserStatus{
		Username:       u.Config.Username,
		BandwidthLimit: u.Config.BandwidthLimit,
		BandwidthUnit:  u.Config.BandwidthUnit,
		DataLimit:      u.Config.DataLimit,
		DataUnit:       u.Config.DataUnit,
		DataUsedBytes:  u.UsedBytes(),
		IPLimit:        u.ipLimit,
		ActiveIPs:      activeCount,
	}
}

// --- Rate-limited I/O ---

type rateLimitedReader struct {
	r       io.Reader
	limiter *rate.Limiter
	user    *User
}

func (r *rateLimitedReader) Read(p []byte) (int, error) {
	// Limit read size to burst for proper WaitN
	burst := r.limiter.Burst()
	if burst > 0 && len(p) > burst {
		p = p[:burst]
	}
	n, err := r.r.Read(p)
	if n > 0 {
		r.user.AddUsedBytes(int64(n))
		// Block until tokens available — this creates backpressure
		if waitErr := r.limiter.WaitN(context.Background(), n); waitErr != nil {
			// If WaitN fails (shouldn't with Background()), sleep as fallback
			time.Sleep(time.Duration(n) * time.Second / time.Duration(r.limiter.Limit()))
		}
	}
	return n, err
}

type rateLimitedWriter struct {
	w       io.Writer
	limiter *rate.Limiter
	user    *User
}

func (w *rateLimitedWriter) Write(p []byte) (int, error) {
	total := 0
	burst := w.limiter.Burst()
	for len(p) > 0 {
		chunk := len(p)
		if burst > 0 && chunk > burst {
			chunk = burst
		}
		// Wait BEFORE writing — this is the correct rate-limiting approach
		if waitErr := w.limiter.WaitN(context.Background(), chunk); waitErr != nil {
			time.Sleep(time.Duration(chunk) * time.Second / time.Duration(w.limiter.Limit()))
		}
		n, err := w.w.Write(p[:chunk])
		total += n
		if n > 0 {
			w.user.AddUsedBytes(int64(n))
		}
		if err != nil {
			return total, err
		}
		p = p[n:]
	}
	return total, nil
}

type trackingReader struct {
	r    io.Reader
	user *User
}

func (r *trackingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.user.AddUsedBytes(int64(n))
	}
	return n, err
}

type trackingWriter struct {
	w    io.Writer
	user *User
}

func (w *trackingWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if n > 0 {
		w.user.AddUsedBytes(int64(n))
	}
	return n, err
}

// ExtractIP gets the IP portion from a net.Addr.
func ExtractIP(addr net.Addr) string {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}
