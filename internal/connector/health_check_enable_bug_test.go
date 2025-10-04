package connector

import (
	"context"
	"sync"
	"testing"

	"github.com/pscheit/haproxy-nomad-connector/internal/config"
	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
)

// mockHAProxyClientWithReadyTracking extends the existing mock to track ReadyServer calls
type mockHAProxyClientWithReadyTracking struct {
	mockHAProxyClient
	readyServerCalled    bool
	readyServerCallCount int
	readyServerBackend   string
	readyServerName      string
	mu                   sync.Mutex
}

func (m *mockHAProxyClientWithReadyTracking) ReadyServer(backendName, serverName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readyServerCalled = true
	m.readyServerCallCount++
	m.readyServerBackend = backendName
	m.readyServerName = serverName
	return nil
}

func (m *mockHAProxyClientWithReadyTracking) GetBackend(name string) (*haproxy.Backend, error) {
	// Return a valid backend so the test proceeds to server creation
	return &haproxy.Backend{Name: name}, nil
}

// mockHAProxyClientWithBackendTracking extends the existing mock to track backend creation
type mockHAProxyClientWithBackendTracking struct {
	mockHAProxyClient
	backendCreated bool
	createdBackend haproxy.Backend
	mu             sync.Mutex
}

func (m *mockHAProxyClientWithBackendTracking) GetBackend(name string) (*haproxy.Backend, error) {
	// Return 404 to force backend creation
	return nil, &haproxy.APIError{StatusCode: 404}
}

//nolint:gocritic // Matches interface signature
func (m *mockHAProxyClientWithBackendTracking) CreateBackend(backend haproxy.Backend, version int) (*haproxy.Backend, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.backendCreated = true
	m.createdBackend = backend
	return &backend, nil
}

// TestHealthCheckEnabledAfterServerCreation verifies the fix for Bug #1
// where servers are properly configured with health checks via default_server in HAProxy 3.0
func TestHealthCheckEnabledAfterServerCreation(t *testing.T) {
	mock := &mockHAProxyClientWithBackendTracking{}

	// Create a service registration event for dynamic backend (auto-created)
	event := &ServiceEvent{
		Type: EventTypeServiceRegistration,
		Service: Service{
			ServiceName: "test-service",
			Address:     "192.168.1.10",
			Port:        8080,
			Tags:        []string{"haproxy.enable=true", "haproxy.backend=dynamic"},
		},
	}

	cfg := &config.Config{}

	// Process the service registration
	_, err := ProcessServiceEvent(context.Background(), mock, event, cfg)
	if err != nil {
		t.Fatalf("ProcessServiceEvent failed: %v", err)
	}

	// FIXED: Backend should be created with default_server.check=enabled
	// This ensures health checks work automatically without needing ReadyServer() calls
	if !mock.backendCreated {
		t.Fatalf("Backend was not created")
	}

	if mock.createdBackend.DefaultServer == nil {
		t.Errorf("REGRESSION: Backend created without default_server - health checks won't work")
	} else if mock.createdBackend.DefaultServer.Check != "enabled" {
		t.Errorf("REGRESSION: Backend default_server.check is not 'enabled' (got: %s) - servers would remain in MAINT mode",
			mock.createdBackend.DefaultServer.Check)
	}
}
