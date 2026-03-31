package gui

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ParsaKSH/SlipStream-Plus/internal/config"
	"github.com/ParsaKSH/SlipStream-Plus/internal/health"
	"github.com/ParsaKSH/SlipStream-Plus/internal/users"
)

func (s *APIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	status := s.manager.StatusAll()
	json.NewEncoder(w).Encode(map[string]any{"instances": status, "strategy": s.cfg.Strategy, "socks": s.cfg.Socks.Listen})
}

func (s *APIServer) handleBandwidth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	s.bwMu.RLock()
	data := make([]bwPoint, len(s.bwHistory))
	copy(data, s.bwHistory)
	s.bwMu.RUnlock()
	json.NewEncoder(w).Encode(data)
}

func (s *APIServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}
	if r.Method == "GET" {
		json.NewEncoder(w).Encode(s.cfg)
		return
	}
	if r.Method == "POST" {
		var newCfg config.Config
		if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
			http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
			return
		}
		if err := newCfg.Validate(); err != nil {
			http.Error(w, fmt.Sprintf("validation: %v", err), http.StatusBadRequest)
			return
		}
		if err := newCfg.Save(s.configPath); err != nil {
			http.Error(w, fmt.Sprintf("save: %v", err), http.StatusInternalServerError)
			return
		}
		*s.cfg = newCfg
		json.NewEncoder(w).Encode(map[string]string{"status": "saved"})
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (s *APIServer) handleReload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	newCfg, err := config.Load(s.configPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("load config: %v", err), http.StatusBadRequest)
		return
	}
	if newCfg.SlipstreamBinary == "" {
		newCfg.SlipstreamBinary = s.cfg.SlipstreamBinary
	}

	// Stop health checker before reload to prevent race conditions
	if s.checker != nil {
		s.checker.Stop()
	}

	if err := s.manager.Reload(newCfg); err != nil {
		http.Error(w, fmt.Sprintf("reload: %v", err), http.StatusInternalServerError)
		return
	}
	*s.cfg = *newCfg

	// Restart health checker with fresh state
	if s.checker != nil {
		s.checker = health.NewChecker(s.manager, &s.cfg.HealthCheck)
		s.checker.Start()
	}

	// Reload user manager if users changed
	if s.userMgr != nil && len(s.cfg.Socks.Users) > 0 {
		s.userMgr = users.NewManager(s.cfg.Socks.Users)
	}

	log.Printf("[gui] config reloaded and instances restarted")
	json.NewEncoder(w).Encode(map[string]string{"status": "reloaded"})
}

func (s *APIServer) handleRestart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "restarting"})

	// Schedule restart after response is sent
	go func() {
		time.Sleep(500 * time.Millisecond)
		log.Printf("[gui] full restart requested, shutting down before re-exec...")

		// Stop health checker first
		if s.checker != nil {
			s.checker.Stop()
		}

		// Shutdown manager — this kills all child processes and frees ports
		s.manager.Shutdown()

		time.Sleep(200 * time.Millisecond) // brief pause for port release

		exe, err := os.Executable()
		if err != nil {
			log.Printf("[gui] restart failed: %v", err)
			return
		}
		syscall.Exec(exe, os.Args, os.Environ())
	}()
}

func (s *APIServer) handleInstance(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 || parts[3] != "restart" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	id, err := strconv.Atoi(parts[2])
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.manager.RestartInstance(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "restarting"})
}

func (s *APIServer) handleUsers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}
	if r.Method == "GET" {
		if s.userMgr == nil {
			json.NewEncoder(w).Encode([]any{})
			return
		}
		allUsers := s.userMgr.AllUsers()
		result := make([]users.UserStatus, len(allUsers))
		for i, u := range allUsers {
			result[i] = u.Status()
		}
		json.NewEncoder(w).Encode(result)
		return
	}
	if r.Method == "POST" {
		// Add new user
		var uc config.UserConfig
		if err := json.NewDecoder(r.Body).Decode(&uc); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if uc.Username == "" || uc.Password == "" {
			http.Error(w, "username and password required", http.StatusBadRequest)
			return
		}
		// Check duplicate
		for _, u := range s.cfg.Socks.Users {
			if u.Username == uc.Username {
				http.Error(w, "user already exists", http.StatusConflict)
				return
			}
		}
		s.cfg.Socks.Users = append(s.cfg.Socks.Users, uc)
		if err := s.cfg.Save(s.configPath); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.userMgr = users.NewManager(s.cfg.Socks.Users)
		json.NewEncoder(w).Encode(map[string]string{"status": "added"})
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (s *APIServer) handleUserAction(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	username := parts[2]
	action := parts[3]

	switch action {
	case "reset":
		if s.userMgr == nil {
			http.Error(w, "no users", http.StatusNotFound)
			return
		}
		user := s.userMgr.GetUser(username)
		if user == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		user.ResetUsedBytes()
		json.NewEncoder(w).Encode(map[string]string{"status": "reset"})

	case "edit":
		var uc config.UserConfig
		if err := json.NewDecoder(r.Body).Decode(&uc); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		found := false
		for i, u := range s.cfg.Socks.Users {
			if u.Username == username {
				uc.Username = username // can't change username
				s.cfg.Socks.Users[i] = uc
				found = true
				break
			}
		}
		if !found {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err := s.cfg.Save(s.configPath); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.userMgr = users.NewManager(s.cfg.Socks.Users)
		json.NewEncoder(w).Encode(map[string]string{"status": "updated"})

	case "delete":
		newUsers := make([]config.UserConfig, 0)
		for _, u := range s.cfg.Socks.Users {
			if u.Username != username {
				newUsers = append(newUsers, u)
			}
		}
		s.cfg.Socks.Users = newUsers
		if err := s.cfg.Save(s.configPath); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(s.cfg.Socks.Users) > 0 {
			s.userMgr = users.NewManager(s.cfg.Socks.Users)
		} else {
			s.userMgr = nil
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})

	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (s *APIServer) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(dashboardHTML))
}
