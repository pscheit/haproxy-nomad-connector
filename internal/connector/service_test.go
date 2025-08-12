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

// Test constants
const (
	testDomain  = "api.example.com"
	testBackend = "api_service"
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
	mu                      sync.Mutex
	drainCalled             bool
	deleteCalled            bool
	drainError              error
	deleteError             error
	getVersionError         error
	getServersServers       []haproxy.Server
	getServersError         error
	addFrontendRuleCalls    []FrontendRuleCall
	addFrontendRuleError    error
	removeFrontendRuleCalls []RemoveFrontendRuleCall
	removeFrontendRuleError error
}

type FrontendRuleCall struct {
	Frontend string
	Domain   string
	Backend  string
}

type RemoveFrontendRuleCall struct {
	Frontend string
	Domain   string
}

func (m *mockHAProxyClient) GetConfigVersion() (int, error) {
	return 1, m.getVersionError
}

func (m *mockHAProxyClient) GetBackend(name string) (*haproxy.Backend, error) {
	return nil, &haproxy.APIError{StatusCode: 404}
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
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addFrontendRuleCalls = append(m.addFrontendRuleCalls, FrontendRuleCall{
		Frontend: frontend,
		Domain:   domain,
		Backend:  backend,
	})
	return m.addFrontendRuleError
}

func (m *mockHAProxyClient) RemoveFrontendRule(frontend, domain string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeFrontendRuleCalls = append(m.removeFrontendRuleCalls, RemoveFrontendRuleCall{
		Frontend: frontend,
		Domain:   domain,
	})
	return m.removeFrontendRuleError
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

func (m *mockHAProxyClient) getAddFrontendRuleCalls() []FrontendRuleCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]FrontendRuleCall{}, m.addFrontendRuleCalls...)
}

func (m *mockHAProxyClient) getRemoveFrontendRuleCalls() []RemoveFrontendRuleCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]RemoveFrontendRuleCall{}, m.removeFrontendRuleCalls...)
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
		2, // 2 second drain timeout for test
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
		2, // 2 second drain timeout for test
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

func TestProcessServiceEventWithDomainTag_CreatesFrontendRule(t *testing.T) {
	mockClient := &mockHAProxyClient{}

	event := &ServiceEvent{
		Type: "ServiceRegistration",
		Service: Service{
			ServiceName: "api-service",
			Address:     "10.0.0.1",
			Port:        8080,
			Tags:        []string{"haproxy.enable=true", "haproxy.domain=" + testDomain},
		},
	}

	result, err := ProcessServiceEvent(context.Background(), mockClient, event)
	if err != nil {
		t.Fatalf("ProcessServiceEvent() failed: %v", err)
	}

	resultMap, ok := result.(map[string]string)
	if !ok {
		t.Fatal("Expected result to be map[string]string")
	}

	if resultMap["status"] != StatusCreated {
		t.Errorf("Expected status '%s', got %s", StatusCreated, resultMap["status"])
	}

	// Verify that AddFrontendRule was called correctly
	calls := mockClient.getAddFrontendRuleCalls()
	if len(calls) != 1 {
		t.Errorf("Expected 1 AddFrontendRule call, got %d", len(calls))
	}

	if len(calls) > 0 {
		call := calls[0]
		const expectedFrontend = "https"
		expectedDomain := testDomain
		expectedBackend := testBackend

		if call.Frontend != expectedFrontend {
			t.Errorf("Expected frontend '%s', got '%s'", expectedFrontend, call.Frontend)
		}
		if call.Domain != expectedDomain {
			t.Errorf("Expected domain '%s', got '%s'", expectedDomain, call.Domain)
		}
		if call.Backend != expectedBackend {
			t.Errorf("Expected backend '%s', got '%s'", expectedBackend, call.Backend)
		}
	}
}

func TestProcessServiceEventWithDomainTag_RemovesFrontendRule(t *testing.T) {
	// Configure mock to return 1 server (the one being removed)
	mockClient := &mockHAProxyClient{
		getServersServers: []haproxy.Server{
			{Name: testBackend + "_10_0_0_1_8080"},
		},
	}

	event := &ServiceEvent{
		Type: "ServiceDeregistration",
		Service: Service{
			ServiceName: "api-service",
			Address:     "10.0.0.1",
			Port:        8080,
			Tags:        []string{"haproxy.enable=true", "haproxy.domain=" + testDomain},
		},
	}

	result, err := ProcessServiceEvent(context.Background(), mockClient, event)
	if err != nil {
		t.Fatalf("ProcessServiceEvent() failed: %v", err)
	}

	resultMap, ok := result.(map[string]string)
	if !ok {
		t.Fatal("Expected result to be map[string]string")
	}

	// Should be draining status for graceful deregistration
	if resultMap["status"] != StatusDraining {
		t.Errorf("Expected status '%s', got %s", StatusDraining, resultMap["status"])
	}

	// Verify that RemoveFrontendRule was called correctly
	calls := mockClient.getRemoveFrontendRuleCalls()
	if len(calls) != 1 {
		t.Errorf("Expected 1 RemoveFrontendRule call, got %d", len(calls))
	}

	if len(calls) > 0 {
		call := calls[0]
		const expectedFrontend = "https"
		expectedDomain := testDomain

		if call.Frontend != expectedFrontend {
			t.Errorf("Expected frontend '%s', got '%s'", expectedFrontend, call.Frontend)
		}
		if call.Domain != expectedDomain {
			t.Errorf("Expected domain '%s', got '%s'", expectedDomain, call.Domain)
		}
	}
}

func TestProcessServiceEventWithDomainTag_ExistingServer_ShouldStillCreateFrontendRule(t *testing.T) {
	// This test simulates the production bug where servers already exist in HAProxy
	// (either added manually or from a previous connector run without domain support)
	// and verifies that frontend rules are STILL created even when server already exists

	mockClient := &mockHAProxyClient{
		// Simulate that the server already exists in the backend
		getServersServers: []haproxy.Server{
			{Name: testBackend + "_10_0_0_1_8080", Address: "10.0.0.1", Port: 8080},
		},
	}

	event := &ServiceEvent{
		Type: "ServiceRegistration",
		Service: Service{
			ServiceName: "api-service",
			Address:     "10.0.0.1",
			Port:        8080,
			Tags:        []string{"haproxy.enable=true", "haproxy.domain=" + testDomain},
		},
	}

	result, err := ProcessServiceEvent(context.Background(), mockClient, event)
	if err != nil {
		t.Fatalf("ProcessServiceEvent() failed: %v", err)
	}

	resultMap, ok := result.(map[string]string)
	if !ok {
		t.Fatal("Expected result to be map[string]string")
	}

	// Server already exists, so status should be "already_exists"
	if resultMap["status"] != "already_exists" {
		t.Errorf("Expected status 'already_exists', got %s", resultMap["status"])
	}

	// BUT frontend rule should STILL be created!
	calls := mockClient.getAddFrontendRuleCalls()
	if len(calls) != 1 {
		t.Errorf("CRITICAL BUG: Frontend rule not created for existing server! Expected 1 AddFrontendRule call, got %d", len(calls))
		t.Error("This is the production bug - when servers already exist, frontend rules are NOT created!")
	}

	if len(calls) > 0 {
		call := calls[0]
		if call.Frontend != "https" || call.Domain != testDomain || call.Backend != testBackend {
			t.Errorf("Frontend rule has wrong parameters: %+v", call)
		}
	}
}
