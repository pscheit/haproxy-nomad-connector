package connector

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
	"github.com/pscheit/haproxy-nomad-connector/internal/nomad"
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
}

// ProcessServiceEvent processes a Nomad service event and updates HAProxy
func ProcessServiceEvent(ctx context.Context, client *haproxy.Client, event ServiceEvent) (interface{}, error) {
	// Classify service based on tags
	serviceType := classifyService(event.Service.Tags)
	
	switch serviceType {
	case haproxy.ServiceTypeDynamic:
		return processDynamicService(ctx, client, event)
	case haproxy.ServiceTypeCustom:
		return processCustomService(ctx, client, event)
	case haproxy.ServiceTypeStatic:
		// Static services - no action needed
		return map[string]string{"status": "ignored", "reason": "static service"}, nil
	default:
		return map[string]string{"status": "ignored", "reason": "no haproxy.enable tag"}, nil
	}
}

// ProcessNomadServiceEvent processes a Nomad service event using the nomad event structure
func ProcessNomadServiceEvent(ctx context.Context, haproxyClient *haproxy.Client, event nomad.ServiceEvent, logger *log.Logger) (interface{}, error) {
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
		},
	}

	logger.Printf("Processing %s for service %s at %s:%d", 
		event.Type, svc.ServiceName, svc.Address, svc.Port)

	return ProcessServiceEvent(ctx, haproxyClient, serviceEvent)
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
func processDynamicService(ctx context.Context, client *haproxy.Client, event ServiceEvent) (interface{}, error) {
	switch event.Type {
	case "ServiceRegistration":
		return handleServiceRegistration(ctx, client, event)
	case "ServiceDeregistration":
		return handleServiceDeregistration(ctx, client, event)
	default:
		return map[string]string{"status": "skipped", "reason": "unknown event type"}, nil
	}
}

func handleServiceRegistration(ctx context.Context, client *haproxy.Client, event ServiceEvent) (interface{}, error) {
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
		Check:   "enabled",
	}
	
	_, err = client.CreateServer(backendName, server, version)
	if err != nil {
		return nil, fmt.Errorf("failed to create server %s in backend %s: %w", serverName, backendName, err)
	}
	
	return map[string]string{
		"status":  "created", 
		"backend": backendName,
		"server":  server.Name,
	}, nil
}

func handleServiceDeregistration(ctx context.Context, client *haproxy.Client, event ServiceEvent) (interface{}, error) {
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
	
	return map[string]string{
		"status":  "deleted", 
		"backend": backendName,
		"server":  serverName,
	}, nil
}

// processCustomService adds servers to existing backends
func processCustomService(ctx context.Context, client *haproxy.Client, event ServiceEvent) (interface{}, error) {
	// TODO: Implement custom backend server management
	return map[string]string{"status": "todo", "reason": "custom backend not implemented"}, nil
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