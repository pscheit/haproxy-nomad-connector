package connector

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/pscheit/haproxy-nomad-connector/internal/config"
	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
	"github.com/pscheit/haproxy-nomad-connector/internal/nomad"
)

// Connector manages the integration between Nomad and HAProxy
type Connector struct {
	config        *config.Config
	nomadClient   *nomad.Client
	haproxyClient *haproxy.Client
	logger        *log.Logger
	
	// Metrics and state
	mu              sync.RWMutex
	processedEvents int64
	errors          int64
	lastEventTime   time.Time
}

// New creates a new connector instance
func New(cfg *config.Config) (*Connector, error) {
	logger := log.New(log.Writer(), "[connector] ", log.LstdFlags|log.Lshortfile)

	// Create HAProxy client
	haproxyClient := haproxy.NewClient(
		cfg.HAProxy.Address,
		cfg.HAProxy.Username,
		cfg.HAProxy.Password,
	)

	// Test HAProxy connection
	info, err := haproxyClient.GetInfo()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to HAProxy Data Plane API: %w", err)
	}
	logger.Printf("Connected to HAProxy Data Plane API version %s", info.API.Version)

	// Create Nomad client
	nomadClient, err := nomad.NewClient(
		cfg.Nomad.Address,
		cfg.Nomad.Token,
		cfg.Nomad.Region,
		logger,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create Nomad client: %w", err)
	}

	return &Connector{
		config:        cfg,
		nomadClient:   nomadClient,
		haproxyClient: haproxyClient,
		logger:        logger,
	}, nil
}

// Start begins the connector's main processing loop
func (c *Connector) Start(ctx context.Context) error {
	c.logger.Println("Starting haproxy-nomad-connector")

	// Perform initial sync of existing services
	if err := c.syncExistingServices(ctx); err != nil {
		c.logger.Printf("Warning: Initial sync failed: %v", err)
	}

	// Start health check server
	go c.startHealthServer(ctx)

	// Start event processing
	eventChan := make(chan nomad.ServiceEvent, 100)

	// Start event stream in background
	go func() {
		if err := c.nomadClient.StreamServiceEvents(ctx, eventChan); err != nil && ctx.Err() == nil {
			c.logger.Printf("Event stream ended: %v", err)
		}
	}()

	// Process events
	for {
		select {
		case <-ctx.Done():
			c.logger.Println("Connector stopping...")
			return nil
			
		case event := <-eventChan:
			c.processEvent(ctx, event)
		}
	}
}

// syncExistingServices performs initial sync of all registered Nomad services
func (c *Connector) syncExistingServices(ctx context.Context) error {
	c.logger.Println("Performing initial sync of existing services...")
	
	services, err := c.nomadClient.GetServices()
	if err != nil {
		return fmt.Errorf("failed to get existing services: %w", err)
	}

	synced := 0
	for _, svc := range services {
		// Create fake registration event for existing services
		event := nomad.ServiceEvent{
			Type:  "ServiceRegistration",
			Topic: "Service",
			Payload: nomad.Payload{
				Service: svc,
			},
		}

		if result, err := ProcessNomadServiceEvent(ctx, c.haproxyClient, event, c.logger); err != nil {
			c.logger.Printf("Failed to sync service %s: %v", svc.ServiceName, err)
		} else {
			if resultMap, ok := result.(map[string]string); ok && resultMap["status"] == "created" {
				synced++
			}
		}
	}

	c.logger.Printf("Initial sync complete: %d services synced", synced)
	return nil
}

// processEvent handles individual Nomad service events
func (c *Connector) processEvent(ctx context.Context, event nomad.ServiceEvent) {
	c.mu.Lock()
	c.processedEvents++
	c.lastEventTime = time.Now()
	c.mu.Unlock()

	result, err := ProcessNomadServiceEvent(ctx, c.haproxyClient, event, c.logger)
	if err != nil {
		c.mu.Lock()
		c.errors++
		c.mu.Unlock()
		
		c.logger.Printf("Error processing event for service %s: %v", 
			event.Payload.Service.ServiceName, err)
		return
	}

	// Log successful processing
	if resultMap, ok := result.(map[string]string); ok {
		c.logger.Printf("Successfully processed %s for service %s: %s",
			event.Type, event.Payload.Service.ServiceName, resultMap["status"])
	}
}

// startHealthServer starts HTTP server for health checks and metrics
func (c *Connector) startHealthServer(ctx context.Context) {
	mux := http.NewServeMux()
	
	// Health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"healthy","service":"haproxy-nomad-connector"}`)
	})
	
	// Metrics endpoint
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		c.mu.RLock()
		processed := c.processedEvents
		errors := c.errors
		lastEvent := c.lastEventTime
		c.mu.RUnlock()
		
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{
			"processed_events": %d,
			"errors": %d,
			"last_event_time": "%s",
			"uptime_seconds": %.0f
		}`, processed, errors, lastEvent.Format(time.RFC3339), time.Since(lastEvent).Seconds())
	})

	server := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	c.logger.Printf("Starting health server on :8080")
	
	go func() {
		<-ctx.Done()
		server.Shutdown(context.Background())
	}()
	
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		c.logger.Printf("Health server error: %v", err)
	}
}

// GetStats returns connector statistics
func (c *Connector) GetStats() (processed, errors int64, lastEvent time.Time) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.processedEvents, c.errors, c.lastEventTime
}