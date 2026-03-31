package main

import (
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	listenAddr := flag.String("listen", "0.0.0.0:9500", "listen address for tunnel connections")
	socksUpstream := flag.String("socks-upstream", "127.0.0.1:1080", "upstream SOCKS5 proxy address")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("CentralServer starting...")
	log.Printf("  Listen:         %s", *listenAddr)
	log.Printf("  SOCKS upstream: %s", *socksUpstream)

	cs := &centralServer{
		socksUpstream: *socksUpstream,
		conns:         make(map[uint32]*connState),
		sourceMap:     make(map[net.Conn]*sourceConn),
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		cs.closeAll()
		os.Exit(0)
	}()

	go cs.cleanupLoop()

	ln, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", *listenAddr, err)
	}
	defer ln.Close()
	log.Printf("CentralServer listening on %s", *listenAddr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("[central] accept error: %v", err)
			continue
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			tc.SetKeepAlive(true)
			tc.SetKeepAlivePeriod(30 * time.Second)
			tc.SetNoDelay(true)
		}
		go cs.handleIncoming(conn)
	}
}
