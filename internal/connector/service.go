package connector

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/pscheit/haproxy-nomad-connector/internal/config"
	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
	"github.com/pscheit/haproxy-nomad-connector/internal/nomad"
)

// Health check type constants
const (
	CheckTypeHTTP     = "http"
	CheckTypeTCP      = "tcp"
	CheckTypeDisabled = "disabled"
)

// Status constants
const (
	StatusCreated           = "created"
	StatusDeleted           = "deleted"
	StatusDraining          = "draining"
	MethodGracefulDrain     = "graceful_drain"
	MethodImmediateDeletion = "immediate_deletion"
)

// ServiceEvent represents a Nomad service registration/deregistration event
type ServiceEvent struct {
	Type    string
	Service Service
}

type Service struct {
	ServiceName string
	Address     string
	Port        int
	Tags        []string
	JobID       string // Job ID for health check lookup
}

// ProcessServiceEvent processes a Nomad service event and updates HAProxy
func ProcessServiceEvent(
	ctx context.Context,
	client haproxy.ClientInterface,
	event *ServiceEvent,
	cfg *config.Config,
) (interface{}, error) {
	// Classify service based on tags
	serviceType := classifyService(event.Service.Tags)
	fmt.Printf("DEBUG: Service %s classified as %s with tags: %v\n", event.Service.ServiceName, serviceType, event.Service.Tags)

	switch serviceType {
	case haproxy.ServiceTypeDynamic:
		return processDynamicService(ctx, client, event, cfg)
	case haproxy.ServiceTypeCustom:
		return processCustomService(ctx, client, event, cfg)
	case haproxy.ServiceTypeStatic:
		// Static services - no action needed
		return map[string]string{"status": "ignored", "reason": "static service"}, nil
	default:
		return map[string]string{"status": "ignored", "reason": "no haproxy.enable tag"}, nil
	}
}

// ProcessNomadServiceEvent processes a Nomad service event using the nomad event structure
func ProcessNomadServiceEvent(
	ctx context.Context,
	haproxyClient haproxy.ClientInterface,
	nomadClient *nomad.Client,
	event nomad.ServiceEvent,
	logger *log.Logger,
	cfg *config.Config,
) (interface{}, error) {
	if event.Payload.Service == nil {
		return nil, fmt.Errorf("event payload missing service data")
	}

	svc := event.Payload.Service

	// Convert to our internal event structure
	serviceEvent := ServiceEvent{
		Type: event.Type,
		Service: Service{
			ServiceName: svc.ServiceName,
			Address:     svc.Address,
			Port:        svc.Port,
			Tags:        svc.Tags,
			JobID:       svc.JobID, // Pass JobID for health check lookup
		},
	}

	logger.Printf("Processing %s for service %s at %s:%d",
		event.Type, svc.ServiceName, svc.Address, svc.Port)

	return ProcessServiceEventWithHealthCheckAndConfig(ctx, haproxyClient, nomadClient, &serviceEvent, logger, cfg)
}

// ProcessServiceEventWithHealthCheck processes a service event with health check synchronization from Nomad

// ProcessServiceEventWithHealthCheckAndConfig processes a service event with configurable drain timeout
func ProcessServiceEventWithHealthCheckAndConfig(
	ctx context.Context,
	haproxyClient haproxy.ClientInterface,
	nomadClient *nomad.Client,
	event *ServiceEvent,
	logger *log.Logger,
	cfg *config.Config,
) (interface{}, error) {
	// Classify service based on tags
	serviceType := classifyService(event.Service.Tags)

	switch serviceType {
	case haproxy.ServiceTypeDynamic:
		return processDynamicServiceWithHealthCheckAndConfig(ctx, haproxyClient, nomadClient, event, logger, cfg.HAProxy.DrainTimeoutSec, cfg)
	case haproxy.ServiceTypeCustom:
		// TODO: Implement custom service with health check and drain timeout
		return ProcessServiceEvent(ctx, haproxyClient, event, cfg)
	case haproxy.ServiceTypeStatic:
		return map[string]string{"status": "ignored", "reason": "static service"}, nil
	default:
		return map[string]string{"status": "ignored", "reason": "no haproxy.enable tag"}, nil
	}
}

// classifyService determines service type from tags
func classifyService(tags []string) haproxy.ServiceType {
	hasEnable := false
	backendType := ""

	for _, tag := range tags {
		if tag == "haproxy.enable=true" {
			hasEnable = true
		}
		if strings.HasPrefix(tag, "haproxy.backend=") {
			backendType = strings.TrimPrefix(tag, "haproxy.backend=")
		}
	}

	if !hasEnable {
		return haproxy.ServiceTypeStatic
	}

	switch backendType {
	case "dynamic":
		return haproxy.ServiceTypeDynamic
	case "custom":
		return haproxy.ServiceTypeCustom
	default:
		return haproxy.ServiceTypeDynamic // default to dynamic
	}
}

// processDynamicService creates a new backend for the service
func processDynamicService(
	ctx context.Context,
	client haproxy.ClientInterface,
	event *ServiceEvent,
	cfg *config.Config,
) (interface{}, error) {
	switch event.Type {
	case "ServiceRegistration":
		return handleServiceRegistration(ctx, client, event, cfg)
	case "ServiceDeregistration":
		return handleServiceDeregistration(ctx, client, event, cfg)
	default:
		return map[string]string{"status": "skipped", "reason": "unknown event type"}, nil
	}
}

// processDynamicServiceWithHealthCheckAndConfig creates a new backend for the service with configurable drain timeout
func processDynamicServiceWithHealthCheckAndConfig(
	ctx context.Context,
	client haproxy.ClientInterface,
	nomadClient *nomad.Client,
	event *ServiceEvent,
	logger *log.Logger,
	drainTimeoutSec int,
	cfg *config.Config,
) (interface{}, error) {
	switch event.Type {
	case "ServiceRegistration":
		return handleServiceRegistrationWithHealthCheck(ctx, client, nomadClient, event, logger, cfg.HAProxy.Frontend)
	case "ServiceDeregistration":
		return handleServiceDeregistrationWithDrainTimeout(ctx, client, event, cfg, drainTimeoutSec, logger)
	default:
		return map[string]string{"status": "skipped", "reason": "unknown event type"}, nil
	}
}

func handleServiceRegistration(
	_ context.Context,
	client haproxy.ClientInterface,
	event *ServiceEvent,
	cfg *config.Config,
) (interface{}, error) {
	version, err := client.GetConfigVersion()
	if err != nil {
		return nil, err
	}

	backendName := sanitizeServiceName(event.Service.ServiceName)

	// Ensure backend exists and is compatible
	version, err = ensureBackend(client, backendName, version)
	if err != nil {
		return nil, err
	}

	serverName := generateServerName(event.Service.ServiceName, event.Service.Address, event.Service.Port)

	// Initialize result map
	result := map[string]string{
		"backend": backendName,
		"server":  serverName,
	}

	// Ensure server exists
	serverExists, err := ensureServer(client, backendName, serverName, event.Service.Address, event.Service.Port, version)
	if err != nil {
		return nil, err
	}
	if serverExists {
		result["status"] = "already_exists"
	} else {
		result["status"] = "created"
	}

	// ALWAYS reconcile frontend rules (regardless of server existence)
	err = reconcileFrontendRule(client, event.Service.ServiceName, event.Service.Tags, backendName, result, cfg.HAProxy.Frontend)
	if err != nil {
		return nil, err
	}

	return result, nil
}

// ensureBackend ensures the backend exists and is compatible
func ensureBackend(client haproxy.ClientInterface, backendName string, version int) (int, error) {
	existingBackend, err := client.GetBackend(backendName)
	if err == nil {
		if !haproxy.IsBackendCompatibleForDynamicService(existingBackend) {
			return version, fmt.Errorf("backend %s already exists with incompatible configuration (algorithm: %s, expected: roundrobin)",
				backendName, existingBackend.Balance.Algorithm)
		}
		return version, nil
	}

	backend := haproxy.Backend{
		Name: backendName,
		Balance: haproxy.Balance{
			Algorithm: "roundrobin",
		},
	}

	_, err = client.CreateBackend(backend, version)
	if err != nil {
		return version, fmt.Errorf("failed to create backend %s: %w", backendName, err)
	}

	return client.GetConfigVersion()
}

// ensureServer ensures the server exists in the backend
func ensureServer(client haproxy.ClientInterface, backendName, serverName, address string, port, version int) (bool, error) {
	existingServers, err := client.GetServers(backendName)
	if err != nil {
		return false, fmt.Errorf("failed to get existing servers for backend %s: %w", backendName, err)
	}

	for _, existingServer := range existingServers {
		if existingServer.Name == serverName {
			return true, nil
		}
	}

	server := haproxy.Server{
		Name:    serverName,
		Address: address,
		Port:    port,
		Check:   "enabled",
	}

	_, err = client.CreateServer(backendName, &server, version)
	if err != nil {
		return false, fmt.Errorf("failed to create server %s in backend %s: %w", serverName, backendName, err)
	}
	return false, nil
}

// reconcileFrontendRule ensures the frontend rule exists for domain-tagged services
func reconcileFrontendRule(
	client haproxy.ClientInterface,
	serviceName string,
	tags []string,
	backendName string,
	result map[string]string,
	frontendName string,
) error {
	domainMapping := parseDomainMapping(serviceName, tags)
	if domainMapping == nil {
		fmt.Printf("DEBUG: No domain mapping found for service %s with tags: %v\n", serviceName, tags)
		return nil
	}

	fmt.Printf("DEBUG: Reconciling frontend rule for service %s: %s -> %s\n", serviceName, domainMapping.Domain, backendName)

	// Check if rule already exists
	existingRules, err := client.GetFrontendRules(frontendName)
	if err != nil {
		fmt.Printf("DEBUG: Failed to get existing rules: %v\n", err)
	}

	for _, rule := range existingRules {
		if rule.Domain == domainMapping.Domain && rule.Backend == backendName {
			result["frontend_rule"] = fmt.Sprintf("rule exists: %s -> %s", domainMapping.Domain, backendName)
			fmt.Printf("DEBUG: Frontend rule already exists: %s -> %s\n", domainMapping.Domain, backendName)
			return nil
		}
	}

	err = client.AddFrontendRuleWithType(frontendName, domainMapping.Domain, backendName, domainMapping.Type)
	if err != nil {
		return fmt.Errorf("failed to create frontend rule for domain %s: %w", domainMapping.Domain, err)
	}
	result["frontend_rule"] = fmt.Sprintf("added rule: %s -> %s", domainMapping.Domain, backendName)
	fmt.Printf("DEBUG: Successfully created frontend rule: %s -> %s\n", domainMapping.Domain, backendName)
	return nil
}

func handleServiceDeregistration(
	ctx context.Context,
	client haproxy.ClientInterface,
	event *ServiceEvent,
	cfg *config.Config,
) (interface{}, error) {
	return handleServiceDeregistrationWithDrainTimeout(ctx, client, event, cfg, config.DefaultDrainTimeoutSec, nil)
}

func handleServiceDeregistrationWithDrainTimeout(
	_ context.Context,
	client haproxy.ClientInterface,
	event *ServiceEvent,
	cfg *config.Config,
	drainTimeoutSec int,
	logger *log.Logger,
) (interface{}, error) {
	backendName := sanitizeServiceName(event.Service.ServiceName)
	serverName := generateServerName(event.Service.ServiceName, event.Service.Address, event.Service.Port)

	result := map[string]string{
		"backend": backendName,
		"server":  serverName,
	}

	// Handle server drain/deletion
	if err := drainAndRemoveServer(client, backendName, serverName, drainTimeoutSec, logger, result); err != nil {
		return nil, err
	}

	// Check if this was the last server
	existingServers, err := client.GetServers(backendName)
	isLastServer := err == nil && len(existingServers) <= 1

	// Handle frontend rule removal if this was the only instance
	if isLastServer {
		removeFrontendRule(client, event.Service.ServiceName, event.Service.Tags, result, cfg.HAProxy.Frontend)
	}

	return result, nil
}

// drainAndRemoveServer handles graceful draining and removal of a server
func drainAndRemoveServer(
	client haproxy.ClientInterface,
	backendName, serverName string,
	drainTimeoutSec int,
	logger *log.Logger,
	result map[string]string,
) error {
	// Try to drain the server to allow existing connections to complete
	err := client.DrainServer(backendName, serverName)
	if err != nil {
		// If drain fails (maybe server doesn't exist), try direct deletion
		version, versionErr := client.GetConfigVersion()
		if versionErr != nil {
			return fmt.Errorf("failed to get config version for fallback deletion: %w", versionErr)
		}

		err = client.DeleteServer(backendName, serverName, version)
		if err != nil {
			return fmt.Errorf("failed to delete server %s from backend %s: %w", serverName, backendName, err)
		}

		result["status"] = StatusDeleted
		result["method"] = MethodImmediateDeletion
		return nil
	}

	result["status"] = StatusDraining
	result["method"] = MethodGracefulDrain

	// Schedule delayed removal after drain period
	go scheduleDelayedServerRemoval(client, backendName, serverName, drainTimeoutSec, logger)
	return nil
}

// scheduleDelayedServerRemoval removes a server after drain timeout
func scheduleDelayedServerRemoval(
	client haproxy.ClientInterface,
	backendName, serverName string,
	drainTimeoutSec int,
	logger *log.Logger,
) {
	drainDuration := time.Duration(drainTimeoutSec) * time.Second
	time.Sleep(drainDuration)

	version, versionErr := client.GetConfigVersion()
	if versionErr != nil {
		if logger != nil {
			logger.Printf("Warning: failed to get config version for delayed deletion: %v", versionErr)
		}
		return
	}

	deleteErr := client.DeleteServer(backendName, serverName, version)
	if deleteErr != nil {
		if logger != nil {
			logger.Printf("Warning: failed delayed deletion of server %s from backend %s: %v", serverName, backendName, deleteErr)
		}
	} else {
		if logger != nil {
			logger.Printf("Gracefully removed server %s from backend %s after %ds drain",
				serverName, backendName, drainTimeoutSec)
		}
	}
}

// removeFrontendRule removes frontend rule when service has domain tags
func removeFrontendRule(client haproxy.ClientInterface, serviceName string, tags []string, result map[string]string, frontendName string) {
	domainMapping := parseDomainMapping(serviceName, tags)
	if domainMapping == nil {
		return
	}

	err := client.RemoveFrontendRule(frontendName, domainMapping.Domain)
	if err != nil {
		result["frontend_rule_warning"] = fmt.Sprintf("failed to remove frontend rule: %v", err)
	} else {
		result["frontend_rule_removed"] = domainMapping.Domain
	}
}

// processCustomService adds servers to existing backends
func processCustomService(
	_ context.Context,
	_ haproxy.ClientInterface,
	_ *ServiceEvent,
	_ *config.Config,
) (interface{}, error) {
	// TODO: Implement custom backend server management with domain mapping
	return map[string]string{"status": "todo", "reason": "custom backend not implemented"}, nil
}

// handleServiceRegistrationWithHealthCheck handles service registration with health check synchronization
func handleServiceRegistrationWithHealthCheck(
	_ context.Context,
	client haproxy.ClientInterface,
	nomadClient *nomad.Client,
	event *ServiceEvent,
	logger *log.Logger,
	frontendName string,
) (interface{}, error) {
	version, err := client.GetConfigVersion()
	if err != nil {
		return nil, err
	}

	backendName := sanitizeServiceName(event.Service.ServiceName)

	existingBackend, err := client.GetBackend(backendName)
	if err == nil {
		if !haproxy.IsBackendCompatibleForDynamicService(existingBackend) {
			return nil, fmt.Errorf("backend %s already exists with incompatible configuration (algorithm: %s, expected: roundrobin)",
				backendName, existingBackend.Balance.Algorithm)
		}
	} else {
		backend := haproxy.Backend{
			Name: backendName,
			Balance: haproxy.Balance{
				Algorithm: "roundrobin",
			},
		}

		_, err = client.CreateBackend(backend, version)
		if err != nil {
			return nil, fmt.Errorf("failed to create backend %s: %w", backendName, err)
		}

		version, err = client.GetConfigVersion()
		if err != nil {
			return nil, err
		}
	}

	serverName := generateServerName(event.Service.ServiceName, event.Service.Address, event.Service.Port)

	existingServers, err := client.GetServers(backendName)
	if err != nil {
		return nil, fmt.Errorf("failed to get existing servers for backend %s: %w", backendName, err)
	}

	for _, existingServer := range existingServers {
		if existingServer.Name == serverName {
			// Initialize result map for existing server
			result := map[string]string{
				"status":  "already_exists",
				"backend": backendName,
				"server":  serverName,
			}

			// ALWAYS reconcile frontend rules (regardless of server existence)
			reconcileErr := reconcileFrontendRule(client, event.Service.ServiceName, event.Service.Tags, backendName, result, frontendName)
			if reconcileErr != nil {
				return nil, reconcileErr
			}

			return result, nil
		}
	}

	// Try to fetch health check configuration from Nomad
	var serviceCheck *nomad.ServiceCheck
	if event.Service.JobID != "" && nomadClient != nil {
		serviceCheck, err = nomadClient.GetServiceCheckFromJob(event.Service.JobID, event.Service.ServiceName)
		if err != nil {
			logger.Printf("Warning: Failed to get health check from Nomad for service %s in job %s: %v",
				event.Service.ServiceName, event.Service.JobID, err)
			// Continue with default health check
		}
	}

	// Create server with health check configuration
	server := createServerWithHealthCheck(&event.Service, serverName, serviceCheck, event.Service.Tags, logger)

	_, err = client.CreateServer(backendName, &server, version)
	if err != nil {
		return nil, fmt.Errorf("failed to create server %s in backend %s: %w", serverName, backendName, err)
	}

	// Initialize result map
	result := map[string]string{
		"status":     StatusCreated,
		"backend":    backendName,
		"server":     serverName,
		"check_type": server.CheckType,
	}

	// ALWAYS reconcile frontend rules (regardless of server existence)
	if err := reconcileFrontendRule(client, event.Service.ServiceName, event.Service.Tags, backendName, result, frontendName); err != nil {
		return nil, err
	}

	return result, nil
}

// createServerWithHealthCheck creates a server with appropriate health check configuration
func createServerWithHealthCheck(
	service *Service,
	serverName string,
	nomadCheck *nomad.ServiceCheck,
	tags []string,
	logger *log.Logger,
) haproxy.Server {
	server := haproxy.Server{
		Name:    serverName,
		Address: service.Address,
		Port:    service.Port,
		Check:   "enabled", // Default
	}

	// Priority: Tags > Nomad job spec > default
	// Check for tag overrides first
	tagCheck := parseHealthCheckFromTags(tags)
	if tagCheck != nil {
		applyHealthCheckToServer(&server, tagCheck, "tag", logger)
		return server
	}

	// Use Nomad job spec health check
	if nomadCheck != nil {
		haproxyCheck := convertNomadToHAProxyCheck(nomadCheck)
		applyHealthCheckToServer(&server, haproxyCheck, "nomad", logger)
		return server
	}

	// Default: basic TCP check
	server.CheckType = CheckTypeTCP
	logger.Printf("Using default TCP health check for server %s", serverName)

	return server
}

// parseHealthCheckFromTags parses health check configuration from Nomad tags
func parseHealthCheckFromTags(tags []string) *HealthCheckConfig {
	var healthConfig HealthCheckConfig
	found := false

	for _, tag := range tags {
		if strings.HasPrefix(tag, "haproxy.check.") {
			found = true
			switch {
			case strings.HasPrefix(tag, "haproxy.check.disabled"):
				healthConfig.Disabled = true
			case strings.HasPrefix(tag, "haproxy.check.path="):
				healthConfig.Path = strings.TrimPrefix(tag, "haproxy.check.path=")
			case strings.HasPrefix(tag, "haproxy.check.method="):
				healthConfig.Method = strings.TrimPrefix(tag, "haproxy.check.method=")
			case strings.HasPrefix(tag, "haproxy.check.host="):
				healthConfig.Host = strings.TrimPrefix(tag, "haproxy.check.host=")
			case strings.HasPrefix(tag, "haproxy.check.type="):
				healthConfig.Type = strings.TrimPrefix(tag, "haproxy.check.type=")
			}
		}
	}

	if !found {
		return nil
	}

	// If disabled, return special config
	if healthConfig.Disabled {
		healthConfig.Type = CheckTypeDisabled
	} else if healthConfig.Type == "" {
		// If path is specified but no type, assume HTTP
		if healthConfig.Path != "" {
			healthConfig.Type = CheckTypeHTTP
		} else {
			healthConfig.Type = CheckTypeTCP
		}
	}

	return &healthConfig
}

// HealthCheckConfig represents parsed health check configuration
type HealthCheckConfig struct {
	Type     string
	Path     string
	Method   string
	Host     string
	Disabled bool
}

// convertNomadToHAProxyCheck converts Nomad check to HAProxy format
func convertNomadToHAProxyCheck(nomadCheck *nomad.ServiceCheck) *HealthCheckConfig {
	healthConfig := &HealthCheckConfig{
		Type:   nomadCheck.Type,
		Path:   nomadCheck.Path,
		Method: nomadCheck.Method,
	}

	// Map Nomad check types to HAProxy equivalents
	switch nomadCheck.Type {
	case "http", "https":
		healthConfig.Type = CheckTypeHTTP
		if healthConfig.Method == "" {
			healthConfig.Method = "GET"
		}
	case "tcp":
		healthConfig.Type = CheckTypeTCP
	case "grpc":
		healthConfig.Type = CheckTypeTCP // HAProxy doesn't have native gRPC checks, use TCP
	default:
		healthConfig.Type = CheckTypeTCP // Default fallback
	}

	return healthConfig
}

// applyHealthCheckToServer applies health check configuration to HAProxy server
func applyHealthCheckToServer(server *haproxy.Server, healthCheckConfig *HealthCheckConfig, source string, logger *log.Logger) {
	if healthCheckConfig.Disabled {
		server.Check = CheckTypeDisabled
		server.CheckType = CheckTypeDisabled
		logger.Printf("Disabled health checks for server %s (source: %s)", server.Name, source)
		return
	}

	switch healthCheckConfig.Type {
	case CheckTypeHTTP:
		server.CheckType = CheckTypeHTTP
		server.CheckPath = healthCheckConfig.Path
		server.CheckMethod = healthCheckConfig.Method
		if healthCheckConfig.Host != "" {
			server.CheckHost = healthCheckConfig.Host
		}
		logger.Printf("Configured HTTP health check for server %s: %s %s (source: %s)",
			server.Name, healthCheckConfig.Method, healthCheckConfig.Path, source)
	case CheckTypeTCP:
		server.CheckType = CheckTypeTCP
		logger.Printf("Configured TCP health check for server %s (source: %s)", server.Name, source)
	default:
		server.CheckType = CheckTypeTCP
		logger.Printf("Using TCP fallback health check for server %s (source: %s)", server.Name, source)
	}
}

// sanitizeServiceName converts service name to valid HAProxy backend name
func sanitizeServiceName(name string) string {
	// Replace hyphens with underscores for HAProxy compatibility
	return strings.ReplaceAll(name, "-", "_")
}

// generateServerName creates a unique server name based on service, address, and port
func generateServerName(serviceName, address string, port int) string {
	// Create deterministic server name: servicename_address_port
	sanitizedService := sanitizeServiceName(serviceName)
	sanitizedAddress := strings.ReplaceAll(address, ".", "_")
	return fmt.Sprintf("%s_%s_%d", sanitizedService, sanitizedAddress, port)
}
