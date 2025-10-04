package connector

import (
	"context"
	"testing"

	"github.com/pscheit/haproxy-nomad-connector/internal/config"
	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
)

// testConfig returns a default config for testing
func testConfig() *config.Config {
	return &config.Config{
		HAProxy: config.HAProxyConfig{
			Frontend: "https",
		},
	}
}

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

// Runtime server management methods
func (m *MockHAProxyClient) GetRuntimeServer(backendName, serverName string) (*haproxy.RuntimeServer, error) {
	return &haproxy.RuntimeServer{
		AdminState: "ready",
		ServerName: serverName,
	}, nil
}

func (m *MockHAProxyClient) SetServerState(ctx context.Context, backendName, serverName, adminState string) error {
	// For mock, just return success
	return nil
}

func (m *MockHAProxyClient) DrainServer(backendName, serverName string) error {
	return nil
}

func (m *MockHAProxyClient) ReadyServer(backendName, serverName string) error {
	return nil
}

func (m *MockHAProxyClient) MaintainServer(backendName, serverName string) error {
	return nil
}

// Frontend rule management methods (required by ClientInterface)
func (m *MockHAProxyClient) AddFrontendRule(frontend, domain, backend string) error {
	// Mock implementation - no-op for existing tests
	return nil
}

func (m *MockHAProxyClient) AddFrontendRuleWithType(frontend, domain, backend string, domainType haproxy.DomainType) error {
	return m.AddFrontendRule(frontend, domain, backend)
}

func (m *MockHAProxyClient) RemoveFrontendRule(frontend, domain string) error {
	// Mock implementation - no-op for existing tests
	return nil
}

func (m *MockHAProxyClient) GetFrontendRules(frontend string) ([]haproxy.FrontendRule, error) {
	// Mock implementation - return empty rules for existing tests
	return []haproxy.FrontendRule{}, nil
}

func TestServiceRegistrationWithDomainMapping(t *testing.T) {
	// Setup
	client := NewMockHAProxyClient()

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
	result, err := ProcessServiceEvent(context.Background(), client, &event, testConfig())
	if err != nil {
		t.Fatalf("ProcessServiceEvent() failed: %v", err)
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
}

func TestServiceDeregistrationWithDomainMapping(t *testing.T) {
	// Setup with existing service
	client := NewMockHAProxyClient()

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

	_, err := ProcessServiceEvent(context.Background(), client, &registerEvent, testConfig())
	if err != nil {
		t.Fatalf("Service registration failed: %v", err)
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

	result, err := ProcessServiceEvent(context.Background(), client, &deregisterEvent, testConfig())
	if err != nil {
		t.Fatalf("Service deregistration failed: %v", err)
	}

	// Verify result
	resultMap, ok := result.(map[string]string)
	if !ok {
		t.Fatalf("Expected result to be map[string]string, got %T", result)
	}

	// With the new drain functionality, status should be "draining" instead of "deleted"
	if resultMap["status"] != StatusDraining {
		t.Errorf("Expected status '%s', got %s", StatusDraining, resultMap["status"])
	}

	if resultMap["method"] != MethodGracefulDrain {
		t.Errorf("Expected method '%s', got %s", MethodGracefulDrain, resultMap["method"])
	}

	// Server should still be present immediately after drain (will be removed after timeout)
	servers, err := client.GetServers("api_service")
	if err != nil {
		t.Errorf("Failed to get servers: %v", err)
	}
	if len(servers) != 1 {
		t.Errorf("Expected 1 server during drain period, got %d", len(servers))
	}
}

func TestServiceRegistrationWithoutDomainMapping(t *testing.T) {
	// Test that services without domain tags work normally
	client := NewMockHAProxyClient()

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

	result, err := ProcessServiceEvent(context.Background(), client, &event, testConfig())
	if err != nil {
		t.Fatalf("ProcessServiceEvent() failed: %v", err)
	}

	resultMap, ok := result.(map[string]string)
	if !ok {
		t.Fatalf("Expected result to be map[string]string, got %T", result)
	}

	if resultMap["status"] != StatusCreated {
		t.Errorf("Expected status 'created', got %s", resultMap["status"])
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
	result, err := ProcessServiceEvent(context.Background(), client, &event, testConfig())
	if err != nil {
		t.Fatalf("ProcessServiceEvent() failed: %v", err)
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
