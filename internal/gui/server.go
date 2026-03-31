package gui

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/ParsaKSH/SlipStream-Plus/internal/config"
	"github.com/ParsaKSH/SlipStream-Plus/internal/engine"
	"github.com/ParsaKSH/SlipStream-Plus/internal/health"
	"github.com/ParsaKSH/SlipStream-Plus/internal/users"
)

type APIServer struct {
	manager    *engine.Manager
	cfg        *config.Config
	configPath string
	listenAddr string
	userMgr    *users.Manager
	checker    *health.Checker

	bwMu      sync.RWMutex
	bwHistory []bwPoint
	lastTx    int64
	lastRx    int64
}

type bwPoint struct {
	Time int64 `json:"t"`
	Tx   int64 `json:"tx"`
	Rx   int64 `json:"rx"`
}

func NewAPIServer(mgr *engine.Manager, cfg *config.Config, configPath string, umgr *users.Manager, chk *health.Checker) *APIServer {
	s := &APIServer{
		manager:    mgr,
		cfg:        cfg,
		configPath: configPath,
		listenAddr: cfg.GUI.Listen,
		userMgr:    umgr,
		checker:    chk,
		bwHistory:  make([]bwPoint, 0, 8640),
	}
	go s.collectBandwidth()
	return s
}

func (s *APIServer) collectBandwidth() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		instances := s.manager.AllInstances()
		var totalTx, totalRx int64
		for _, inst := range instances {
			totalTx += inst.TxBytes()
			totalRx += inst.RxBytes()
		}
		s.bwMu.Lock()
		txRate := (totalTx - s.lastTx) / 10
		rxRate := (totalRx - s.lastRx) / 10
		s.lastTx = totalTx
		s.lastRx = totalRx
		s.bwHistory = append(s.bwHistory, bwPoint{Time: time.Now().Unix(), Tx: txRate, Rx: rxRate})
		if len(s.bwHistory) > 8640 {
			s.bwHistory = s.bwHistory[len(s.bwHistory)-8640:]
		}
		s.bwMu.Unlock()
	}
}

func (s *APIServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/reload", s.handleReload)
	mux.HandleFunc("/api/restart", s.handleRestart)
	mux.HandleFunc("/api/bandwidth", s.handleBandwidth)
	mux.HandleFunc("/api/users", s.handleUsers)
	mux.HandleFunc("/api/users/", s.handleUserAction)
	mux.HandleFunc("/api/instance/", s.handleInstance)
	mux.HandleFunc("/", s.handleDashboard)

	var handler http.Handler = mux
	if s.cfg.GUI.Username != "" && s.cfg.GUI.Password != "" {
		guiUser := s.cfg.GUI.Username
		guiPass := s.cfg.GUI.Password
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, p, ok := r.BasicAuth()
			if !ok || u != guiUser || p != guiPass {
				w.Header().Set("WWW-Authenticate", `Basic realm="SlipstreamPlus"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			mux.ServeHTTP(w, r)
		})
		log.Printf("[gui] basic auth enabled for dashboard")
	}

	log.Printf("[gui] web dashboard available at http://%s", s.listenAddr)

	go func() {
		if err := http.ListenAndServe(s.listenAddr, handler); err != nil {
			log.Printf("[gui] server error: %v", err)
		}
	}()
	return nil
}
