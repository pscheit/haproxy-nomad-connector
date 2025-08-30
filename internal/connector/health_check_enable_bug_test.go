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

// TestHealthCheckEnabledAfterServerCreation verifies the fix for Bug #1
// where servers are properly set to ready state after creation in HAProxy 2.x
func TestHealthCheckEnabledAfterServerCreation(t *testing.T) {
	mock := &mockHAProxyClientWithReadyTracking{}

	// Create a service registration event for custom backend
	event := &ServiceEvent{
		Type: EventTypeServiceRegistration,
		Service: Service{
			ServiceName: "test-service",
			Address:     "192.168.1.10",
			Port:        8080,
			Tags:        []string{"haproxy.enable=true", "haproxy.backend=custom"},
		},
	}

	cfg := &config.Config{}

	// Process the service registration
	_, err := ProcessServiceEvent(context.Background(), mock, event, cfg)
	if err != nil {
		t.Fatalf("ProcessServiceEvent failed: %v", err)
	}

	// FIXED: ReadyServer should now be called after server creation
	if !mock.readyServerCalled {
		t.Errorf("REGRESSION: ReadyServer was not called after server creation - server would remain in MAINT mode in HAProxy 2.x")
	}

	if mock.readyServerCallCount != 1 {
		t.Errorf("Expected ReadyServer to be called exactly once, got %d calls", mock.readyServerCallCount)
	}
}
