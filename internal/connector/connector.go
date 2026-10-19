package connector

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"regexp"
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
	haproxyClient haproxy.ClientInterface
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

// NewForTesting creates a connector with injected dependencies for testing.
func NewForTesting(
	cfg *config.Config, nomadClient nomad.NomadClient,
	haproxyClient haproxy.ClientInterface, logger *log.Logger,
) *Connector {
	return &Connector{
		config:        cfg,
		nomadClient:   nomadClient,
		haproxyClient: haproxyClient,
		logger:        logger,
	}
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

	// Set up debounced cleanup timer: fires N seconds after the last ServiceDeregistration
	var cleanupTimer *time.Timer
	var cleanupChan <-chan time.Time
	if c.config.HAProxy.CleanupDelaySec > 0 {
		cleanupTimer = time.NewTimer(0)
		cleanupTimer.Stop()
		// drain the channel in case the timer fired before Stop()
		select {
		case <-cleanupTimer.C:
		default:
		}
		cleanupChan = cleanupTimer.C
		defer cleanupTimer.Stop()
		c.logger.Printf("Debounced stale server cleanup enabled (%ds after last deregistration)", c.config.HAProxy.CleanupDelaySec)
	}

	// Process events
	for {
		select {
		case <-ctx.Done():
			c.logger.Println("Connector stopping...")
			return nil

		case event := <-eventChan:
			if event.Type == nomad.EventTypeStreamReconnected {
				// events of the time without a stream are lost, only a full sync catches up
				c.logger.Println("Event stream reconnected, resyncing all services")
				if err := c.syncExistingServices(ctx); err != nil {
					c.logger.Printf("Warning: Resync after reconnect failed: %v", err)
				}
				continue
			}
			c.processEvent(ctx, event)
			if event.Type == "ServiceDeregistration" && cleanupTimer != nil {
				cleanupTimer.Reset(time.Duration(c.config.HAProxy.CleanupDelaySec) * time.Second)
			}

		case <-cleanupChan:
			c.periodicCleanup()
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

	// Repair rule order built up before the connector sorted rules on write
	if err := c.haproxyClient.ReorderFrontendRules(c.config.HAProxy.Frontend); err != nil {
		c.logger.Printf("Warning: Failed to reorder frontend rules: %v", err)
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

// periodicCleanup removes stale servers from HAProxy that no longer exist in Nomad.
// Unlike syncExistingServices, this only performs cleanup without re-registering services,
// making it lightweight enough for frequent execution after deployments.
func (c *Connector) periodicCleanup() {
	services, err := c.nomadClient.GetServices()
	if err != nil {
		c.logger.Printf("Periodic cleanup: failed to get services from Nomad: %v", err)
		return
	}

	expectedServersByBackend := buildExpectedServersMap(services)

	removed, cleanupErr := c.cleanupStaleServers(expectedServersByBackend)
	if cleanupErr != nil {
		c.logger.Printf("Periodic cleanup: error during stale server removal: %v", cleanupErr)
	}

	if removed > 0 {
		c.logger.Printf("Periodic cleanup: removed %d stale servers", removed)
	}
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

	// All backends, not only those in Nomad: a stopped job has no services left, but its
	// servers stay in HAProxy when their delayed deletion failed.
	backends, err := haproxyClient.GetBackends()
	if err != nil {
		return 0, fmt.Errorf("failed to list backends: %w", err)
	}

	for i := range backends {
		backendName := backends[i].Name
		expectedServers := expectedServersByBackend[backendName]

		haproxyServers, err := haproxyClient.GetServers(backendName)
		if err != nil {
			logger.Printf("Could not get servers for backend %s: %v", backendName, err)
			lastErr = err
			continue
		}

		for j := range haproxyServers {
			serverName := haproxyServers[j].Name
			if expectedServers[serverName] ||
				!isConnectorServerName(backendName, serverName) ||
				pendingDeletions.isPending(backendName, serverName) {
				continue
			}

			logger.Printf("Removing stale server %s from backend %s", serverName, backendName)

			if err := deleteServerWithRetry(haproxyClient, backendName, serverName); err != nil {
				logger.Printf("Failed to remove stale server %s: %v", serverName, err)
				lastErr = err
				continue
			}

			removed++
		}
	}

	return removed, lastErr
}

var connectorServerSuffix = regexp.MustCompile(`^\d{1,3}_\d{1,3}_\d{1,3}_\d{1,3}_\d+$`)

// isConnectorServerName matches the names from generateServerName ({backend}_{ip}_{port}).
// Servers with other names come from haproxy.cfg and are not managed by the connector.
func isConnectorServerName(backendName, serverName string) bool {
	suffix, found := strings.CutPrefix(serverName, backendName+"_")
	return found && connectorServerSuffix.MatchString(suffix)
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

	address := c.config.Health.Address
	if address == "" {
		address = config.DefaultHealthAddress
	}

	server := &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: HealthCheckTimeoutSec * time.Second,
	}

	c.logger.Printf("Starting health server on %s", address)

	go func() { //nolint:gosec // fresh context needed for graceful shutdown after parent cancellation
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), HealthCheckTimeoutSec*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
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
