package connector

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
)

func TestClassifyService(t *testing.T) {
	tests := []struct {
		name     string
		tags     []string
		expected haproxy.ServiceType
	}{
		{
			name:     "no haproxy tags",
			tags:     []string{"web", "api"},
			expected: haproxy.ServiceTypeStatic,
		},
		{
			name:     "haproxy enabled with dynamic backend",
			tags:     []string{"haproxy.enable=true", "haproxy.backend=dynamic"},
			expected: haproxy.ServiceTypeDynamic,
		},
		{
			name:     "haproxy enabled with custom backend",
			tags:     []string{"haproxy.enable=true", "haproxy.backend=custom"},
			expected: haproxy.ServiceTypeCustom,
		},
		{
			name:     "haproxy enabled with no backend specified",
			tags:     []string{"haproxy.enable=true"},
			expected: haproxy.ServiceTypeDynamic,
		},
		{
			name:     "haproxy not enabled",
			tags:     []string{"haproxy.backend=dynamic"},
			expected: haproxy.ServiceTypeStatic,
		},
		{
			name:     "haproxy enabled false",
			tags:     []string{"haproxy.enable=false", "haproxy.backend=dynamic"},
			expected: haproxy.ServiceTypeStatic,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifyService(tt.tags)
			if result != tt.expected {
				t.Errorf("classifyService() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestSanitizeServiceName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"api-service", "api_service"},
		{"web-app-v2", "web_app_v2"},
		{"simple", "simple"},
		{"already_sanitized", "already_sanitized"},
		{"multi-dash-name", "multi_dash_name"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := sanitizeServiceName(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeServiceName(%q) = %q, expected %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGenerateServerName(t *testing.T) {
	tests := []struct {
		serviceName string
		address     string
		port        int
		expected    string
	}{
		{"api-service", "192.168.1.10", 8080, "api_service_192_168_1_10_8080"},
		{"web", "127.0.0.1", 3000, "web_127_0_0_1_3000"},
		{"database", "10.0.0.5", 5432, "database_10_0_0_5_5432"},
	}

	for _, tt := range tests {
		t.Run(tt.serviceName, func(t *testing.T) {
			result := generateServerName(tt.serviceName, tt.address, tt.port)
			if result != tt.expected {
				t.Errorf("generateServerName(%q, %q, %d) = %q, expected %q",
					tt.serviceName, tt.address, tt.port, result, tt.expected)
			}
		})
	}
}

// mockHAProxyClient implements haproxy.ClientInterface for testing
type mockHAProxyClient struct {
	mu                sync.Mutex
	drainCalled       bool
	deleteCalled      bool
	drainError        error
	deleteError       error
	getVersionError   error
	getServersServers []haproxy.Server
	getServersError   error
}

func (m *mockHAProxyClient) GetConfigVersion() (int, error) {
	return 1, m.getVersionError
}

func (m *mockHAProxyClient) GetBackend(name string) (*haproxy.Backend, error) {
	return &haproxy.Backend{Name: name}, nil
}

func (m *mockHAProxyClient) CreateBackend(backend haproxy.Backend, version int) (*haproxy.Backend, error) {
	return &backend, nil
}

func (m *mockHAProxyClient) GetServers(backendName string) ([]haproxy.Server, error) {
	return m.getServersServers, m.getServersError
}

func (m *mockHAProxyClient) CreateServer(backendName string, server *haproxy.Server, version int) (*haproxy.Server, error) {
	return server, nil
}

func (m *mockHAProxyClient) DeleteServer(backendName, serverName string, version int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteCalled = true
	return m.deleteError
}

func (m *mockHAProxyClient) GetRuntimeServer(backendName, serverName string) (*haproxy.RuntimeServer, error) {
	return &haproxy.RuntimeServer{}, nil
}

func (m *mockHAProxyClient) SetServerState(backendName, serverName, adminState string) error {
	return nil
}

func (m *mockHAProxyClient) DrainServer(backendName, serverName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.drainCalled = true
	return m.drainError
}

func (m *mockHAProxyClient) ReadyServer(backendName, serverName string) error {
	return nil
}

func (m *mockHAProxyClient) MaintainServer(backendName, serverName string) error {
	return nil
}

// Frontend rule management methods (required by ClientInterface)
func (m *mockHAProxyClient) AddFrontendRule(frontend, domain, backend string) error {
	// Mock implementation - no-op for existing tests
	return nil
}

func (m *mockHAProxyClient) RemoveFrontendRule(frontend, domain string) error {
	// Mock implementation - no-op for existing tests
	return nil
}

func (m *mockHAProxyClient) GetFrontendRules(frontend string) ([]haproxy.FrontendRule, error) {
	// Mock implementation - return empty rules for existing tests
	return []haproxy.FrontendRule{}, nil
}

// Helper methods for thread-safe access to test state
func (m *mockHAProxyClient) wasDrainCalled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.drainCalled
}

func (m *mockHAProxyClient) wasDeleteCalled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deleteCalled
}

func TestHandleServiceDeregistrationWithDrainTimeout_DrainSuccess(t *testing.T) {
	mockClient := &mockHAProxyClient{}
	logger := log.New(os.Stdout, "test: ", log.LstdFlags)

	event := &ServiceEvent{
		Type: "ServiceDeregistration",
		Service: Service{
			ServiceName: "test-service",
			Address:     "10.0.0.1",
			Port:        8080,
			Tags:        []string{"haproxy.enable=true"},
		},
	}

	result, err := handleServiceDeregistrationWithDrainTimeout(
		context.Background(),
		mockClient,
		event,
		nil, // no domain manager
		2,   // 2 second drain timeout for test
		logger,
	)

	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if !mockClient.wasDrainCalled() {
		t.Error("Expected DrainServer to be called")
	}

	resultMap, ok := result.(map[string]string)
	if !ok {
		t.Fatal("Expected result to be map[string]string")
	}

	if resultMap["status"] != StatusDraining {
		t.Errorf("Expected status '%s', got %s", StatusDraining, resultMap["status"])
	}

	if resultMap["method"] != MethodGracefulDrain {
		t.Errorf("Expected method '%s', got %s", MethodGracefulDrain, resultMap["method"])
	}

	// Wait for delayed deletion to occur
	time.Sleep(3 * time.Second)

	if !mockClient.wasDeleteCalled() {
		t.Error("Expected DeleteServer to be called after drain timeout")
	}
}

func TestHandleServiceDeregistrationWithDrainTimeout_DrainFails(t *testing.T) {
	mockClient := &mockHAProxyClient{
		drainError: fmt.Errorf("drain failed"),
	}
	logger := log.New(os.Stdout, "test: ", log.LstdFlags)

	event := &ServiceEvent{
		Type: "ServiceDeregistration",
		Service: Service{
			ServiceName: "test-service",
			Address:     "10.0.0.1",
			Port:        8080,
			Tags:        []string{"haproxy.enable=true"},
		},
	}

	result, err := handleServiceDeregistrationWithDrainTimeout(
		context.Background(),
		mockClient,
		event,
		nil, // no domain manager
		2,   // 2 second drain timeout for test
		logger,
	)

	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if !mockClient.wasDrainCalled() {
		t.Error("Expected DrainServer to be called")
	}

	if !mockClient.wasDeleteCalled() {
		t.Error("Expected DeleteServer to be called as fallback")
	}

	resultMap, ok := result.(map[string]string)
	if !ok {
		t.Fatal("Expected result to be map[string]string")
	}

	if resultMap["status"] != StatusDeleted {
		t.Errorf("Expected status '%s', got %s", StatusDeleted, resultMap["status"])
	}

	if resultMap["method"] != MethodImmediateDeletion {
		t.Errorf("Expected method '%s', got %s", MethodImmediateDeletion, resultMap["method"])
	}
}
