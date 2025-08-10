package connector

import (
	"context"
	"strings"

	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
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
	if event.Type != "ServiceRegistration" {
		// For now, just handle registration
		return map[string]string{"status": "skipped", "reason": "not registration"}, nil
	}
	
	// Get current config version
	version, err := client.GetConfigVersion()
	if err != nil {
		return nil, err
	}
	
	// Create backend name from service name (sanitize)
	backendName := sanitizeServiceName(event.Service.ServiceName)
	
	// Create backend
	backend := haproxy.Backend{
		Name: backendName,
		Balance: haproxy.Balance{
			Algorithm: "roundrobin",
		},
	}
	
	_, err = client.CreateBackend(backend, version)
	if err != nil {
		// Backend might already exist - that's ok for now
		// TODO: Handle this more gracefully
	}
	
	// Get new version for server creation
	newVersion, err := client.GetConfigVersion()
	if err != nil {
		return nil, err
	}
	
	// Add server to backend
	server := haproxy.Server{
		Name:    sanitizeServiceName(event.Service.ServiceName) + "_1", // TODO: better naming
		Address: event.Service.Address,
		Port:    event.Service.Port,
		Check:   "enabled",
	}
	
	_, err = client.CreateServer(backendName, server, newVersion)
	if err != nil {
		return nil, err
	}
	
	return map[string]string{
		"status":  "created", 
		"backend": backendName,
		"server":  server.Name,
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