package users

import (
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

	mu        sync.Mutex
	activeIPs map[string]time.Time // IP → last-seen time
	ipLimit   int                  // max concurrent IPs (0 = unlimited)
}

// Manager handles user auth, rate limiting, quotas, and connection limits.
type Manager struct {
	mu    sync.RWMutex
	users map[string]*User // username → User
}

// NewManager creates a UserManager from config.
func NewManager(cfgUsers []config.UserConfig) *Manager {
	m := &Manager{
		users: make(map[string]*User, len(cfgUsers)),
	}
	for _, cu := range cfgUsers {
		u := &User{
			Config:    cu,
			dataLimit: cu.DataLimitBytes(),
			activeIPs: make(map[string]time.Time),
			ipLimit:   cu.IPLimit,
		}

		// Setup bandwidth rate limiter
		bps := cu.BandwidthBytesPerSec()
		if bps > 0 {
			// rate.Limit is events/sec; burst = 1 second worth of bytes
			u.limiter = rate.NewLimiter(rate.Limit(bps), int(bps))
		}

		m.users[cu.Username] = u
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
// Returns an error string if denied, or "" if allowed.
func (u *User) CheckConnect(clientIP string) string {
	// Check data quota
	if u.dataLimit > 0 && u.usedBytes.Load() >= u.dataLimit {
		return fmt.Sprintf("data quota exceeded (%d bytes used of %d)",
			u.usedBytes.Load(), u.dataLimit)
	}

	// Check IP limit
	if u.ipLimit <= 0 {
		return "" // no limit
	}

	u.mu.Lock()
	defer u.mu.Unlock()

	now := time.Now()
	cooldown := 10 * time.Second

	// Clean up stale IPs (disconnected > 10s ago)
	for ip, lastSeen := range u.activeIPs {
		if now.Sub(lastSeen) > cooldown {
			delete(u.activeIPs, ip)
		}
	}

	// If this IP is already active, allow
	if _, exists := u.activeIPs[clientIP]; exists {
		return ""
	}

	// Check if we have room for a new IP
	if len(u.activeIPs) >= u.ipLimit {
		return fmt.Sprintf("ip limit reached (%d/%d active IPs)", len(u.activeIPs), u.ipLimit)
	}

	return ""
}

// MarkConnect records that a connection from this IP is active.
func (u *User) MarkConnect(clientIP string) {
	if u.ipLimit <= 0 {
		return
	}
	u.mu.Lock()
	u.activeIPs[clientIP] = time.Time{} // zero = still connected
	u.mu.Unlock()
}

// MarkDisconnect records disconnection time for cooldown.
func (u *User) MarkDisconnect(clientIP string) {
	if u.ipLimit <= 0 {
		return
	}
	u.mu.Lock()
	u.activeIPs[clientIP] = time.Now() // start cooldown countdown
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

// WrapReader wraps a reader with rate limiting for this user.
func (u *User) WrapReader(r io.Reader) io.Reader {
	if u.limiter == nil {
		return r
	}
	return &rateLimitedReader{r: r, limiter: u.limiter, user: u}
}

// WrapWriter wraps a writer with rate limiting for this user.
func (u *User) WrapWriter(w io.Writer) io.Writer {
	if u.limiter == nil {
		return &trackingWriter{w: w, user: u}
	}
	return &rateLimitedWriter{w: w, limiter: u.limiter, user: u}
}

// AllUsers returns all users with their stats.
func (m *Manager) AllUsers() []*User {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*User, 0, len(m.users))
	for _, u := range m.users {
		result = append(result, u)
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
	activeCount := 0
	now := time.Now()
	for _, lastSeen := range u.activeIPs {
		if lastSeen.IsZero() || now.Sub(lastSeen) <= 10*time.Second {
			activeCount++
		}
	}
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
	// Limit chunk size to burst size for smoother rate limiting
	if r.limiter.Burst() > 0 && len(p) > r.limiter.Burst() {
		p = p[:r.limiter.Burst()]
	}
	n, err := r.r.Read(p)
	if n > 0 {
		r.user.AddUsedBytes(int64(n))
		// Wait for tokens
		r.limiter.WaitN(nil, n)
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
	for len(p) > 0 {
		chunk := len(p)
		if w.limiter.Burst() > 0 && chunk > w.limiter.Burst() {
			chunk = w.limiter.Burst()
		}
		w.limiter.WaitN(nil, chunk)
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
