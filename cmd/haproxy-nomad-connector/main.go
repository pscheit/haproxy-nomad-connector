package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/pscheit/haproxy-nomad-connector/internal/config"
	"github.com/pscheit/haproxy-nomad-connector/internal/connector"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	var (
		configFile = flag.String("config", "", "Configuration file path")
		showVersion = flag.Bool("version", false, "Show version information")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("haproxy-nomad-connector %s (%s)\n", version, commit)
		os.Exit(0)
	}

	// Load configuration
	cfg, err := config.Load(*configFile)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	log.Printf("Starting haproxy-nomad-connector %s", version)
	log.Printf("Nomad URL: %s", cfg.Nomad.Address)
	log.Printf("HAProxy Data Plane API URL: %s", cfg.HAProxy.Address)

	// Create connector
	conn, err := connector.New(cfg)
	if err != nil {
		log.Fatalf("Failed to create connector: %v", err)
	}

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start connector in background
	go func() {
		if err := conn.Start(ctx); err != nil {
			log.Fatalf("Connector failed: %v", err)
		}
	}()

	// Wait for shutdown signal
	<-sigCh
	log.Println("Shutdown signal received, stopping connector...")
	cancel()

	log.Println("haproxy-nomad-connector stopped")
}