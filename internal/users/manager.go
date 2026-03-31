package users

import (
	"log"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/ParsaKSH/SlipStream-Plus/internal/config"
)

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
