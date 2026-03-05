package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/slipstreamplus/slipstreamplus/internal/balancer"
	"github.com/slipstreamplus/slipstreamplus/internal/config"
	"github.com/slipstreamplus/slipstreamplus/internal/embedded"
	"github.com/slipstreamplus/slipstreamplus/internal/engine"
	"github.com/slipstreamplus/slipstreamplus/internal/gui"
	"github.com/slipstreamplus/slipstreamplus/internal/health"
	"github.com/slipstreamplus/slipstreamplus/internal/proxy"
)

func main() {
	configPath := flag.String("config", "config.json", "path to config file")
	enableGUI := flag.Bool("gui", false, "enable web dashboard")
	guiPort := flag.String("gui-listen", "", "override GUI listen address (e.g., 127.0.0.1:8384)")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("SlipstreamPlus starting...")

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Handle embedded binary
	if embedded.IsEmbedded() {
		binPath, cleanup, err := embedded.ExtractBinary()
		if err != nil {
			log.Fatalf("Failed to extract embedded binary: %v", err)
		}
		defer cleanup()
		cfg.SlipstreamBinary = binPath
		log.Printf("Using embedded slipstream-client binary")
	} else {
		// Verify external binary exists
		if cfg.SlipstreamBinary == "" {
			log.Fatalf("slipstream_binary is required in config (or build with -tags embed_slipstream)")
		}
		if _, err := os.Stat(cfg.SlipstreamBinary); os.IsNotExist(err) {
			log.Fatalf("Slipstream binary not found at: %s", cfg.SlipstreamBinary)
		}
	}

	expanded, err := cfg.ExpandInstances()
	if err != nil {
		log.Fatalf("Failed to expand instances: %v", err)
	}

	log.Printf("Loaded config: %d instances (%d expanded), strategy=%s, socks=%s",
		len(cfg.Instances), len(expanded), cfg.Strategy, cfg.Socks.Listen)

	// Create process manager
	mgr, err := engine.NewManager(cfg)
	if err != nil {
		log.Fatalf("Failed to create manager: %v", err)
	}

	// Start all slipstream instances
	if err := mgr.StartAll(); err != nil {
		log.Fatalf("Failed to start instances: %v", err)
	}

	// Create health checker
	checker := health.NewChecker(mgr, &cfg.HealthCheck)
	checker.Start()

	// Create load balancer
	bal := balancer.New(cfg.Strategy)
	log.Printf("Using load balancing strategy: %s", cfg.Strategy)

	// Start GUI if enabled
	if *enableGUI || cfg.GUI.Enabled {
		if *guiPort != "" {
			cfg.GUI.Listen = *guiPort
		}
		apiServer := gui.NewAPIServer(mgr, cfg, *configPath)
		if err := apiServer.Start(); err != nil {
			log.Fatalf("Failed to start GUI: %v", err)
		}
	}

	// Create TCP proxy
	proxyServer := proxy.NewServer(
		cfg.Socks.Listen,
		cfg.Socks.BufferSize,
		cfg.Socks.MaxConnections,
		mgr,
		bal,
	)

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		checker.Stop()
		mgr.Shutdown()
		os.Exit(0)
	}()

	// Start proxy (blocking)
	if err := proxyServer.ListenAndServe(); err != nil {
		log.Fatalf("Proxy server error: %v", err)
	}
}
