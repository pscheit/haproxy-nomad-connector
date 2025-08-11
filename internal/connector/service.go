package connector

import (
	"context"
	"fmt"
	"log"
	"strings"

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
	StatusCreated = "created"
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
	JobID       string  // Job ID for health check lookup
}

// ProcessServiceEvent processes a Nomad service event and updates HAProxy
func ProcessServiceEvent(ctx context.Context, client haproxy.ClientInterface, event ServiceEvent) (interface{}, error) {
	return ProcessServiceEventWithDomainMap(ctx, client, event, nil)
}

// ProcessServiceEventWithDomainMap processes a Nomad service event and updates HAProxy and domain mapping
func ProcessServiceEventWithDomainMap(ctx context.Context, client haproxy.ClientInterface, event ServiceEvent, domainMapManager *DomainMapManager) (interface{}, error) {
	// Classify service based on tags
	serviceType := classifyService(event.Service.Tags)
	
	switch serviceType {
	case haproxy.ServiceTypeDynamic:
		return processDynamicServiceWithDomainMap(ctx, client, event, domainMapManager)
	case haproxy.ServiceTypeCustom:
		return processCustomServiceWithDomainMap(ctx, client, event, domainMapManager)
	case haproxy.ServiceTypeStatic:
		// Static services - no action needed
		return map[string]string{"status": "ignored", "reason": "static service"}, nil
	default:
		return map[string]string{"status": "ignored", "reason": "no haproxy.enable tag"}, nil
	}
}

// ProcessNomadServiceEvent processes a Nomad service event using the nomad event structure
func ProcessNomadServiceEvent(ctx context.Context, haproxyClient haproxy.ClientInterface, nomadClient *nomad.Client, event nomad.ServiceEvent, logger *log.Logger) (interface{}, error) {
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
			JobID:       svc.JobID,  // Pass JobID for health check lookup
		},
	}

	logger.Printf("Processing %s for service %s at %s:%d", 
		event.Type, svc.ServiceName, svc.Address, svc.Port)

	return ProcessServiceEventWithHealthCheck(ctx, haproxyClient, nomadClient, serviceEvent, logger)
}

// ProcessServiceEventWithHealthCheck processes a service event with health check synchronization from Nomad
func ProcessServiceEventWithHealthCheck(ctx context.Context, haproxyClient haproxy.ClientInterface, nomadClient *nomad.Client, event ServiceEvent, logger *log.Logger) (interface{}, error) {
	// Classify service based on tags
	serviceType := classifyService(event.Service.Tags)
	
	switch serviceType {
	case haproxy.ServiceTypeDynamic:
		return processDynamicServiceWithHealthCheck(ctx, haproxyClient, nomadClient, event, logger)
	case haproxy.ServiceTypeCustom:
		// TODO: Implement custom service with health check
		return ProcessServiceEvent(ctx, haproxyClient, event)
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
func processDynamicService(ctx context.Context, client haproxy.ClientInterface, event ServiceEvent) (interface{}, error) {
	return processDynamicServiceWithDomainMap(ctx, client, event, nil)
}

// processDynamicServiceWithDomainMap creates a new backend for the service and updates domain mapping
func processDynamicServiceWithDomainMap(ctx context.Context, client haproxy.ClientInterface, event ServiceEvent, domainMapManager *DomainMapManager) (interface{}, error) {
	switch event.Type {
	case "ServiceRegistration":
		return handleServiceRegistrationWithDomainMap(ctx, client, event, domainMapManager)
	case "ServiceDeregistration":
		return handleServiceDeregistrationWithDomainMap(ctx, client, event, domainMapManager)
	default:
		return map[string]string{"status": "skipped", "reason": "unknown event type"}, nil
	}
}

// processDynamicServiceWithHealthCheck creates a new backend for the service with health check synchronization
func processDynamicServiceWithHealthCheck(ctx context.Context, client haproxy.ClientInterface, nomadClient *nomad.Client, event ServiceEvent, logger *log.Logger) (interface{}, error) {
	switch event.Type {
	case "ServiceRegistration":
		return handleServiceRegistrationWithHealthCheck(ctx, client, nomadClient, event, logger)
	case "ServiceDeregistration":
		return handleServiceDeregistrationWithDomainMap(ctx, client, event, nil)
	default:
		return map[string]string{"status": "skipped", "reason": "unknown event type"}, nil
	}
}

func handleServiceRegistration(ctx context.Context, client haproxy.ClientInterface, event ServiceEvent) (interface{}, error) {
	return handleServiceRegistrationWithDomainMap(ctx, client, event, nil)
}

func handleServiceRegistrationWithDomainMap(ctx context.Context, client haproxy.ClientInterface, event ServiceEvent, domainMapManager *DomainMapManager) (interface{}, error) {
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
			return map[string]string{
				"status":  "already_exists",
				"backend": backendName,
				"server":  serverName,
			}, nil
		}
	}
	
	server := haproxy.Server{
		Name:    serverName,
		Address: event.Service.Address,
		Port:    event.Service.Port,
		Check:   "enabled", // Default - will be updated by health check logic
	}
	
	_, err = client.CreateServer(backendName, server, version)
	if err != nil {
		return nil, fmt.Errorf("failed to create server %s in backend %s: %w", serverName, backendName, err)
	}
	
	result := map[string]string{
		"status":  "created", 
		"backend": backendName,
		"server":  server.Name,
	}

	// Handle domain mapping if manager is provided and service has domain tags
	if domainMapManager != nil {
		domainMapping := parseDomainMapping(event.Service.ServiceName, event.Service.Tags)
		if domainMapping != nil {
			domainMapManager.AddMapping(domainMapping)
			if err := domainMapManager.WriteToFile(); err != nil {
				// Log error but don't fail the entire operation
				result["domain_map_warning"] = fmt.Sprintf("failed to update domain map: %v", err)
			} else {
				result["domain_mapping"] = fmt.Sprintf("%s -> %s", domainMapping.Domain, domainMapping.BackendName)
			}
		}
	}

	return result, nil
}

func handleServiceDeregistration(ctx context.Context, client haproxy.ClientInterface, event ServiceEvent) (interface{}, error) {
	return handleServiceDeregistrationWithDomainMap(ctx, client, event, nil)
}

func handleServiceDeregistrationWithDomainMap(ctx context.Context, client haproxy.ClientInterface, event ServiceEvent, domainMapManager *DomainMapManager) (interface{}, error) {
	// Get current config version
	version, err := client.GetConfigVersion()
	if err != nil {
		return nil, err
	}
	
	backendName := sanitizeServiceName(event.Service.ServiceName)
	serverName := generateServerName(event.Service.ServiceName, event.Service.Address, event.Service.Port)
	
	// Remove server from backend
	err = client.DeleteServer(backendName, serverName, version)
	if err != nil {
		return nil, fmt.Errorf("failed to delete server %s from backend %s: %w", serverName, backendName, err)
	}
	
	result := map[string]string{
		"status":  "deleted", 
		"backend": backendName,
		"server":  serverName,
	}

	// Handle domain mapping removal if this was the only instance of the service
	if domainMapManager != nil {
		domainMapping := parseDomainMapping(event.Service.ServiceName, event.Service.Tags)
		if domainMapping != nil {
			// Check if there are other servers in the backend
			existingServers, err := client.GetServers(backendName)
			if err == nil && len(existingServers) == 0 {
				// No more servers in this backend, remove domain mapping
				domainMapManager.RemoveMapping(domainMapping.Domain)
				if err := domainMapManager.WriteToFile(); err != nil {
					result["domain_map_warning"] = fmt.Sprintf("failed to update domain map: %v", err)
				} else {
					result["domain_mapping_removed"] = domainMapping.Domain
				}
			}
		}
	}

	return result, nil
}

// processCustomService adds servers to existing backends
func processCustomService(ctx context.Context, client haproxy.ClientInterface, event ServiceEvent) (interface{}, error) {
	return processCustomServiceWithDomainMap(ctx, client, event, nil)
}

// processCustomServiceWithDomainMap adds servers to existing backends and updates domain mapping
func processCustomServiceWithDomainMap(ctx context.Context, client haproxy.ClientInterface, event ServiceEvent, domainMapManager *DomainMapManager) (interface{}, error) {
	// TODO: Implement custom backend server management with domain mapping
	return map[string]string{"status": "todo", "reason": "custom backend not implemented"}, nil
}

// handleServiceRegistrationWithHealthCheck handles service registration with health check synchronization
func handleServiceRegistrationWithHealthCheck(ctx context.Context, client haproxy.ClientInterface, nomadClient *nomad.Client, event ServiceEvent, logger *log.Logger) (interface{}, error) {
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
			return map[string]string{
				"status":  "already_exists",
				"backend": backendName,
				"server":  serverName,
			}, nil
		}
	}
	
	// Try to fetch health check configuration from Nomad
	var serviceCheck *nomad.ServiceCheck
	if event.Service.JobID != "" {
		serviceCheck, err = nomadClient.GetServiceCheckFromJob(event.Service.JobID, event.Service.ServiceName)
		if err != nil {
			logger.Printf("Warning: Failed to get health check from Nomad for service %s in job %s: %v", 
				event.Service.ServiceName, event.Service.JobID, err)
			// Continue with default health check
		}
	}

	// Create server with health check configuration
	server := createServerWithHealthCheck(event.Service, serverName, serviceCheck, event.Service.Tags, logger)
	
	_, err = client.CreateServer(backendName, server, version)
	if err != nil {
		return nil, fmt.Errorf("failed to create server %s in backend %s: %w", serverName, backendName, err)
	}
	
	return map[string]string{
		"status":     StatusCreated,
		"backend":    backendName,
		"server":     serverName,
		"check_type": server.CheckType,
	}, nil
}

// createServerWithHealthCheck creates a server with appropriate health check configuration
func createServerWithHealthCheck(service Service, serverName string, nomadCheck *nomad.ServiceCheck, tags []string, logger *log.Logger) haproxy.Server {
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
	var config HealthCheckConfig
	found := false

	for _, tag := range tags {
		if strings.HasPrefix(tag, "haproxy.check.") {
			found = true
			switch {
			case strings.HasPrefix(tag, "haproxy.check.disabled"):
				config.Disabled = true
			case strings.HasPrefix(tag, "haproxy.check.path="):
				config.Path = strings.TrimPrefix(tag, "haproxy.check.path=")
			case strings.HasPrefix(tag, "haproxy.check.method="):
				config.Method = strings.TrimPrefix(tag, "haproxy.check.method=")
			case strings.HasPrefix(tag, "haproxy.check.host="):
				config.Host = strings.TrimPrefix(tag, "haproxy.check.host=")
			case strings.HasPrefix(tag, "haproxy.check.type="):
				config.Type = strings.TrimPrefix(tag, "haproxy.check.type=")
			}
		}
	}

	if !found {
		return nil
	}
	
	// If disabled, return special config
	if config.Disabled {
		config.Type = CheckTypeDisabled
	} else if config.Type == "" {
		// If path is specified but no type, assume HTTP
		if config.Path != "" {
			config.Type = CheckTypeHTTP
		} else {
			config.Type = CheckTypeTCP
		}
	}

	return &config
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
	config := &HealthCheckConfig{
		Type:   nomadCheck.Type,
		Path:   nomadCheck.Path,
		Method: nomadCheck.Method,
	}

	// Map Nomad check types to HAProxy equivalents
	switch nomadCheck.Type {
	case "http", "https":
		config.Type = CheckTypeHTTP
		if config.Method == "" {
			config.Method = "GET"
		}
	case "tcp":
		config.Type = CheckTypeTCP
	case "grpc":
		config.Type = CheckTypeTCP // HAProxy doesn't have native gRPC checks, use TCP
	default:
		config.Type = CheckTypeTCP // Default fallback
	}

	return config
}

// applyHealthCheckToServer applies health check configuration to HAProxy server
func applyHealthCheckToServer(server *haproxy.Server, config *HealthCheckConfig, source string, logger *log.Logger) {
	if config.Disabled {
		server.Check = CheckTypeDisabled
		server.CheckType = CheckTypeDisabled
		logger.Printf("Disabled health checks for server %s (source: %s)", server.Name, source)
		return
	}

	switch config.Type {
	case CheckTypeHTTP:
		server.CheckType = CheckTypeHTTP
		server.CheckPath = config.Path
		server.CheckMethod = config.Method
		if config.Host != "" {
			server.CheckHost = config.Host
		}
		logger.Printf("Configured HTTP health check for server %s: %s %s (source: %s)", 
			server.Name, config.Method, config.Path, source)
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