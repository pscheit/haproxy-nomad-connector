package connector

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/pscheit/haproxy-nomad-connector/internal/config"
	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
	"github.com/pscheit/haproxy-nomad-connector/internal/nomad"
)

// Buffer sizes and timeouts
const (
	EventChannelBuffer    = 100
	HealthCheckTimeoutSec = 10
)

// Connector manages the integration between Nomad and HAProxy
type Connector struct {
	config        *config.Config
	nomadClient   nomad.NomadClient
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
	eventChan := make(chan nomad.ServiceEvent, EventChannelBuffer)

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

// processNomadServiceEventWithConfig processes a Nomad service event using connector configuration
func (c *Connector) processNomadServiceEventWithConfig(ctx context.Context, event nomad.ServiceEvent) (interface{}, error) {
	if event.Payload.Service == nil {
		return nil, fmt.Errorf("event payload missing service data")
	}

	svc := event.Payload.Service

	// Convert to internal event structure
	serviceEvent := ServiceEvent{
		Type: event.Type,
		Service: Service{
			ServiceName: svc.ServiceName,
			Address:     svc.Address,
			Port:        svc.Port,
			Tags:        svc.Tags,
			JobID:       svc.JobID,
		},
	}

	result, err := ProcessServiceEventWithHealthCheckAndConfig(
		ctx,
		c.haproxyClient,
		c.nomadClient,
		&serviceEvent,
		c.logger,
		c.config,
	)

	// Enhanced logging with frontend rule status
	frontendInfo := ""
	if resultMap, ok := result.(map[string]string); ok {
		if ruleInfo, exists := resultMap["frontend_rule"]; exists {
			frontendInfo = fmt.Sprintf(" (%s)", ruleInfo)
		}
	}

	c.logger.Printf("Successfully processed %s for service %s at %s:%d%s",
		event.Type, svc.ServiceName, svc.Address, svc.Port, frontendInfo)

	return result, err
}

// syncExistingServices performs initial sync of all registered Nomad services
// and cleans up stale servers that no longer exist in Nomad
func (c *Connector) syncExistingServices(ctx context.Context) error {
	c.logger.Println("Performing initial sync of existing services...")

	services, err := c.nomadClient.GetServices()
	if err != nil {
		return fmt.Errorf("failed to get existing services: %w", err)
	}

	// Build a map of backend -> expected server names from Nomad
	// This allows us to identify stale servers after syncing
	expectedServersByBackend := buildExpectedServersMap(services)

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

		if result, err := ProcessNomadServiceEvent(ctx, c.haproxyClient, c.nomadClient, event, c.logger, c.config); err != nil {
			c.logger.Printf("Failed to sync service %s: %v", svc.ServiceName, err)
		} else {
			if resultMap, ok := result.(map[string]string); ok && resultMap["status"] == StatusCreated {
				synced++
			}
		}
	}

	// Clean up stale servers from HAProxy that no longer exist in Nomad
	removed, cleanupErr := c.cleanupStaleServers(expectedServersByBackend)
	if cleanupErr != nil {
		c.logger.Printf("Warning: Error during stale server cleanup: %v", cleanupErr)
	}

	c.logger.Printf("Initial sync complete: %d services synced, %d stale servers removed", synced, removed)
	return nil
}

// buildExpectedServersMap creates a map of backend name -> set of expected server names
// based on current Nomad service instances
func buildExpectedServersMap(services []*nomad.Service) map[string]map[string]bool {
	result := make(map[string]map[string]bool)

	for _, svc := range services {
		// Only process services that are managed by the connector
		if !hasTag(svc.Tags, "haproxy.enable=true") {
			continue
		}

		backendName := sanitizeServiceName(svc.ServiceName)
		serverName := generateServerName(svc.ServiceName, svc.Address, svc.Port)

		if result[backendName] == nil {
			result[backendName] = make(map[string]bool)
		}
		result[backendName][serverName] = true
	}

	return result
}

// cleanupStaleServers removes servers from HAProxy backends that are not in the expected set
// Returns the number of servers removed and any error encountered
func (c *Connector) cleanupStaleServers(expectedServersByBackend map[string]map[string]bool) (int, error) {
	return cleanupStaleServersFromBackends(c.haproxyClient, expectedServersByBackend, c.logger)
}

// SyncAndCleanupStaleServers performs a full sync cycle: registers current Nomad services
// and removes stale servers from HAProxy that no longer exist in Nomad.
// This is exported for testing purposes.
func SyncAndCleanupStaleServers(
	ctx context.Context,
	haproxyClient haproxy.ClientInterface,
	nomadClient nomad.NomadClient,
	logger *log.Logger,
	cfg *config.Config,
) (synced, removed int, err error) {
	logger.Println("Performing sync and cleanup of services...")

	services, err := nomadClient.GetServices()
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get existing services: %w", err)
	}

	// Build a map of backend -> expected server names from Nomad
	expectedServersByBackend := buildExpectedServersMap(services)

	// Sync all services from Nomad
	for _, svc := range services {
		event := nomad.ServiceEvent{
			Type:  "ServiceRegistration",
			Topic: "Service",
			Payload: nomad.Payload{
				Service: svc,
			},
		}

		if result, procErr := ProcessNomadServiceEvent(ctx, haproxyClient, nomadClient, event, logger, cfg); procErr != nil {
			logger.Printf("Failed to sync service %s: %v", svc.ServiceName, procErr)
		} else {
			if resultMap, ok := result.(map[string]string); ok && resultMap["status"] == StatusCreated {
				synced++
			}
		}
	}

	// Clean up stale servers
	removed, cleanupErr := cleanupStaleServersFromBackends(haproxyClient, expectedServersByBackend, logger)
	if cleanupErr != nil {
		logger.Printf("Warning: Error during stale server cleanup: %v", cleanupErr)
	}

	logger.Printf("Sync complete: %d services synced, %d stale servers removed", synced, removed)
	return synced, removed, cleanupErr
}

// cleanupStaleServersFromBackends removes servers from HAProxy backends that are not in the expected set
// This is a standalone function that can be used by both the Connector method and the exported function
func cleanupStaleServersFromBackends(
	haproxyClient haproxy.ClientInterface,
	expectedServersByBackend map[string]map[string]bool,
	logger *log.Logger,
) (int, error) {
	removed := 0
	var lastErr error

	for backendName, expectedServers := range expectedServersByBackend {
		// Get current servers in HAProxy for this backend
		haproxyServers, err := haproxyClient.GetServers(backendName)
		if err != nil {
			// Backend might not exist yet, skip
			logger.Printf("Could not get servers for backend %s: %v", backendName, err)
			continue
		}

		// Find and remove stale servers
		for _, server := range haproxyServers {
			if expectedServers[server.Name] {
				// Server exists in Nomad, keep it
				continue
			}

			// This server is in HAProxy but not in Nomad - it's stale
			logger.Printf("Removing stale server %s from backend %s", server.Name, backendName)

			version, err := haproxyClient.GetConfigVersion()
			if err != nil {
				logger.Printf("Failed to get config version for stale server removal: %v", err)
				lastErr = err
				continue
			}

			if err := haproxyClient.DeleteServer(backendName, server.Name, version); err != nil {
				logger.Printf("Failed to remove stale server %s: %v", server.Name, err)
				lastErr = err
				continue
			}

			removed++
		}
	}

	return removed, lastErr
}

// processEvent handles individual Nomad service events
func (c *Connector) processEvent(ctx context.Context, event nomad.ServiceEvent) {
	c.mu.Lock()
	c.processedEvents++
	c.lastEventTime = time.Now()
	c.mu.Unlock()

	result, err := c.processNomadServiceEventWithConfig(ctx, event)
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
		var logDetails []string

		// Add status
		if status := resultMap["status"]; status != "" {
			logDetails = append(logDetails, "status="+status)
		}

		// Add frontend rule info if present
		if frontendRule := resultMap["frontend_rule"]; frontendRule != "" {
			logDetails = append(logDetails, "frontend_rule=true")
		}
		if frontendRuleRemoved := resultMap["frontend_rule_removed"]; frontendRuleRemoved != "" {
			logDetails = append(logDetails, "frontend_rule_removed="+frontendRuleRemoved)
		}

		// Add backend info if present
		if backend := resultMap["backend"]; backend != "" {
			logDetails = append(logDetails, "backend="+backend)
		}

		// Add server info if present
		if server := resultMap["server"]; server != "" {
			logDetails = append(logDetails, "server="+server)
		}

		// Add domain mapping info if present
		if domainMapping := resultMap["domain_mapping"]; domainMapping != "" {
			logDetails = append(logDetails, "domain_mapped=true")
		}
		if domainMappingRemoved := resultMap["domain_mapping_removed"]; domainMappingRemoved != "" {
			logDetails = append(logDetails, "domain_mapping_removed="+domainMappingRemoved)
		}

		detailsStr := strings.Join(logDetails, ", ")
		c.logger.Printf("Successfully processed %s for service %s (%s)",
			event.Type, event.Payload.Service.ServiceName, detailsStr)
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
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: HealthCheckTimeoutSec * time.Second,
	}

	c.logger.Printf("Starting health server on :8080")

	go func() {
		<-ctx.Done()
		if err := server.Shutdown(context.Background()); err != nil {
			c.logger.Printf("Error shutting down HTTP server: %v", err)
		}
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
