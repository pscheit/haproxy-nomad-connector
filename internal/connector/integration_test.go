package connector

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
)

// MockHAProxyClient for testing
type MockHAProxyClient struct {
	backends map[string]*haproxy.Backend
	servers  map[string][]haproxy.Server
	version  int
}

func NewMockHAProxyClient() *MockHAProxyClient {
	return &MockHAProxyClient{
		backends: make(map[string]*haproxy.Backend),
		servers:  make(map[string][]haproxy.Server),
		version:  1,
	}
}

func (m *MockHAProxyClient) GetConfigVersion() (int, error) {
	return m.version, nil
}

func (m *MockHAProxyClient) GetBackend(name string) (*haproxy.Backend, error) {
	backend, exists := m.backends[name]
	if !exists {
		return nil, &haproxy.APIError{StatusCode: 404}
	}
	return backend, nil
}

func (m *MockHAProxyClient) CreateBackend(backend haproxy.Backend, version int) (*haproxy.Backend, error) {
	m.backends[backend.Name] = &backend
	m.version++
	return &backend, nil
}

func (m *MockHAProxyClient) GetServers(backendName string) ([]haproxy.Server, error) {
	servers, exists := m.servers[backendName]
	if !exists {
		return []haproxy.Server{}, nil
	}
	return servers, nil
}

func (m *MockHAProxyClient) CreateServer(backendName string, server *haproxy.Server, version int) (*haproxy.Server, error) {
	if _, exists := m.servers[backendName]; !exists {
		m.servers[backendName] = []haproxy.Server{}
	}
	m.servers[backendName] = append(m.servers[backendName], *server)
	m.version++
	return server, nil
}

func (m *MockHAProxyClient) DeleteServer(backendName, serverName string, version int) error {
	servers, exists := m.servers[backendName]
	if !exists {
		return &haproxy.APIError{StatusCode: 404}
	}

	for i, server := range servers {
		if server.Name == serverName {
			m.servers[backendName] = append(servers[:i], servers[i+1:]...)
			m.version++
			return nil
		}
	}

	return &haproxy.APIError{StatusCode: 404}
}

func TestServiceRegistrationWithDomainMapping(t *testing.T) {
	tmpDir := t.TempDir()
	mapFile := filepath.Join(tmpDir, "domain-backend.map")

	// Setup
	client := NewMockHAProxyClient()
	domainMapManager := NewDomainMapManager(mapFile)

	// Test service registration with domain mapping
	event := ServiceEvent{
		Type: "ServiceRegistration",
		Service: Service{
			ServiceName: "crm-prod",
			Address:     "10.0.0.5",
			Port:        8080,
			Tags: []string{
				"haproxy.enable=true",
				"haproxy.backend=dynamic",
				"haproxy.domain=crm.ps-webforge.net",
			},
		},
	}

	// Process event
	result, err := ProcessServiceEventWithDomainMap(context.Background(), client, &event, domainMapManager)
	if err != nil {
		t.Fatalf("ProcessServiceEventWithDomainMap() failed: %v", err)
	}

	// Verify result
	resultMap, ok := result.(map[string]string)
	if !ok {
		t.Fatalf("Expected result to be map[string]string, got %T", result)
	}

	if resultMap["status"] != StatusCreated {
		t.Errorf("Expected status 'created', got %s", resultMap["status"])
	}

	if resultMap["backend"] != "crm_prod" {
		t.Errorf("Expected backend 'crm_prod', got %s", resultMap["backend"])
	}

	// Verify domain mapping was created
	expectedDomainMapping := "crm.ps-webforge.net -> crm_prod"
	if !strings.Contains(resultMap["domain_mapping"], expectedDomainMapping) {
		t.Errorf("Expected domain mapping '%s', got %s", expectedDomainMapping, resultMap["domain_mapping"])
	}

	// Verify backend was created
	backend, err := client.GetBackend("crm_prod")
	if err != nil {
		t.Errorf("Backend should have been created: %v", err)
	}
	if backend.Name != "crm_prod" {
		t.Errorf("Expected backend name 'crm_prod', got %s", backend.Name)
	}

	// Verify server was added
	servers, err := client.GetServers("crm_prod")
	if err != nil {
		t.Errorf("Failed to get servers: %v", err)
	}
	if len(servers) != 1 {
		t.Errorf("Expected 1 server, got %d", len(servers))
	}
	if len(servers) > 0 {
		expectedServerName := "crm_prod_10_0_0_5_8080"
		if servers[0].Name != expectedServerName {
			t.Errorf("Expected server name '%s', got %s", expectedServerName, servers[0].Name)
		}
	}

	// Verify domain map file was created
	if _, statErr := os.Stat(mapFile); os.IsNotExist(statErr) {
		t.Error("Domain map file should have been created")
	}

	// Read and verify map file content
	content, err := os.ReadFile(mapFile)
	if err != nil {
		t.Fatalf("Failed to read map file: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "crm.ps-webforge.net") {
		t.Error("Map file should contain domain")
	}
	if !strings.Contains(contentStr, "crm_prod") {
		t.Error("Map file should contain backend name")
	}
}

func TestServiceDeregistrationWithDomainMapping(t *testing.T) {
	tmpDir := t.TempDir()
	mapFile := filepath.Join(tmpDir, "domain-backend.map")

	// Setup with existing service
	client := NewMockHAProxyClient()
	domainMapManager := NewDomainMapManager(mapFile)

	// First register a service
	registerEvent := ServiceEvent{
		Type: "ServiceRegistration",
		Service: Service{
			ServiceName: "api-service",
			Address:     "10.0.0.3",
			Port:        3000,
			Tags: []string{
				"haproxy.enable=true",
				"haproxy.domain=api.example.com",
			},
		},
	}

	_, err := ProcessServiceEventWithDomainMap(context.Background(), client, &registerEvent, domainMapManager)
	if err != nil {
		t.Fatalf("Service registration failed: %v", err)
	}

	// Verify initial state
	mapping, exists := domainMapManager.GetMapping("api.example.com")
	if !exists {
		t.Fatal("Domain mapping should exist after registration")
	}
	if mapping.BackendName != "api_service" {
		t.Errorf("Expected backend 'api_service', got %s", mapping.BackendName)
	}

	// Now deregister the service
	deregisterEvent := ServiceEvent{
		Type: "ServiceDeregistration",
		Service: Service{
			ServiceName: "api-service",
			Address:     "10.0.0.3",
			Port:        3000,
			Tags: []string{
				"haproxy.enable=true",
				"haproxy.domain=api.example.com",
			},
		},
	}

	result, err := ProcessServiceEventWithDomainMap(context.Background(), client, &deregisterEvent, domainMapManager)
	if err != nil {
		t.Fatalf("Service deregistration failed: %v", err)
	}

	// Verify result
	resultMap, ok := result.(map[string]string)
	if !ok {
		t.Fatalf("Expected result to be map[string]string, got %T", result)
	}

	if resultMap["status"] != "deleted" {
		t.Errorf("Expected status 'deleted', got %s", resultMap["status"])
	}

	// Verify server was removed
	servers, err := client.GetServers("api_service")
	if err != nil {
		t.Errorf("Failed to get servers: %v", err)
	}
	if len(servers) != 0 {
		t.Errorf("Expected 0 servers after deregistration, got %d", len(servers))
	}

	// Verify domain mapping was removed (since no servers left)
	if _, exists := domainMapManager.GetMapping("api.example.com"); exists {
		t.Error("Domain mapping should have been removed when no servers remain")
	}
}

func TestServiceRegistrationWithoutDomainMapping(t *testing.T) {
	// Test that services without domain tags work normally
	client := NewMockHAProxyClient()
	domainMapManager := NewDomainMapManager("/tmp/test.map")

	event := ServiceEvent{
		Type: "ServiceRegistration",
		Service: Service{
			ServiceName: "database",
			Address:     "10.0.0.10",
			Port:        5432,
			Tags: []string{
				"haproxy.enable=true",
				"haproxy.backend=dynamic",
			},
		},
	}

	result, err := ProcessServiceEventWithDomainMap(context.Background(), client, &event, domainMapManager)
	if err != nil {
		t.Fatalf("ProcessServiceEventWithDomainMap() failed: %v", err)
	}

	resultMap, ok := result.(map[string]string)
	if !ok {
		t.Fatalf("Expected result to be map[string]string, got %T", result)
	}

	if resultMap["status"] != StatusCreated {
		t.Errorf("Expected status 'created', got %s", resultMap["status"])
	}

	// Should not have domain mapping info
	if _, exists := resultMap["domain_mapping"]; exists {
		t.Error("Should not have domain mapping for service without domain tags")
	}

	// Domain map manager should be empty
	if domainMapManager.Size() != 0 {
		t.Errorf("Expected domain map to be empty, got %d mappings", domainMapManager.Size())
	}
}

func TestProcessServiceEventWithoutDomainMapManager(t *testing.T) {
	// Test backward compatibility - should work without domain map manager
	client := NewMockHAProxyClient()

	event := ServiceEvent{
		Type: "ServiceRegistration",
		Service: Service{
			ServiceName: "web-app",
			Address:     "10.0.0.7",
			Port:        8080,
			Tags: []string{
				"haproxy.enable=true",
				"haproxy.backend=dynamic",
				"haproxy.domain=web.example.com",
			},
		},
	}

	// Process without domain map manager (nil)
	result, err := ProcessServiceEventWithDomainMap(context.Background(), client, &event, nil)
	if err != nil {
		t.Fatalf("ProcessServiceEventWithDomainMap() failed: %v", err)
	}

	resultMap, ok := result.(map[string]string)
	if !ok {
		t.Fatalf("Expected result to be map[string]string, got %T", result)
	}

	if resultMap["status"] != StatusCreated {
		t.Errorf("Expected status 'created', got %s", resultMap["status"])
	}

	// Should not have domain mapping info since manager is nil
	if _, exists := resultMap["domain_mapping"]; exists {
		t.Error("Should not have domain mapping when manager is nil")
	}

	// Backend and server should still be created
	backend, err := client.GetBackend("web_app")
	if err != nil {
		t.Errorf("Backend should have been created: %v", err)
	}
	if backend.Name != "web_app" {
		t.Errorf("Expected backend name 'web_app', got %s", backend.Name)
	}
}
