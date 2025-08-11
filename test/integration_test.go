//go:build integration
// +build integration

package test

import (
	"context"
	"testing"
	"time"

	"github.com/pscheit/haproxy-nomad-connector/internal/connector"
	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
)

// TestDataPlaneAPI_RealConnection tests against actual running Data Plane API
// Run with: go test -tags=integration ./test/
func TestDataPlaneAPI_RealConnection(t *testing.T) {
	// Skip if no integration test environment
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := haproxy.NewClient("http://localhost:5555", "admin", "adminpwd")

	t.Run("GetInfo", func(t *testing.T) {
		info, err := client.GetInfo()
		if err != nil {
			t.Fatalf("Failed to get API info: %v", err)
		}

		if info.API.Version == "" {
			t.Error("Expected API version, got empty string")
		}
		t.Logf("Connected to Data Plane API version: %s", info.API.Version)
	})

	t.Run("ListBackends", func(t *testing.T) {
		backends, err := client.GetBackends()
		if err != nil {
			t.Fatalf("Failed to get backends: %v", err)
		}

		t.Logf("Found %d backends", len(backends))
		for _, backend := range backends {
			t.Logf("Backend: %s (algorithm: %s)", backend.Name, backend.Balance.Algorithm)
		}
	})

	t.Run("CreateAndDeleteBackend", func(t *testing.T) {
		// Get current version
		version, err := client.GetConfigVersion()
		if err != nil {
			t.Fatalf("Failed to get config version: %v", err)
		}

		// Create test backend
		testBackend := haproxy.Backend{
			Name: "integration-test-backend",
			Balance: haproxy.Balance{
				Algorithm: "roundrobin",
			},
		}

		created, err := client.CreateBackend(testBackend, version)
		if err != nil {
			t.Fatalf("Failed to create backend: %v", err)
		}

		if created.Name != testBackend.Name {
			t.Errorf("Expected backend name %s, got %s", testBackend.Name, created.Name)
		}

		// Verify backend exists
		backends, err := client.GetBackends()
		if err != nil {
			t.Fatalf("Failed to list backends: %v", err)
		}

		found := false
		for _, backend := range backends {
			if backend.Name == "integration-test-backend" {
				found = true
				break
			}
		}
		if !found {
			t.Error("Created backend not found in backend list")
		}

		// Cleanup: delete backend
		newVersion, err := client.GetConfigVersion()
		if err != nil {
			t.Fatalf("Failed to get new config version: %v", err)
		}

		if err := client.DeleteBackend("integration-test-backend", newVersion); err != nil {
			t.Errorf("Failed to delete test backend: %v", err)
		}
	})
}

// TestE2E_ServiceLifecycle tests the full service lifecycle
func TestE2E_ServiceLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("SimpleServiceRegistration", func(t *testing.T) {
		// Create HAProxy client
		client := haproxy.NewClient("http://localhost:5555", "admin", "adminpwd")

		// Create service registration event
		serviceEvent := connector.ServiceEvent{
			Type: "ServiceRegistration",
			Service: connector.Service{
				ServiceName: "test-api",
				Address:     "192.168.1.100",
				Port:        8080,
				Tags: []string{
					"haproxy.enable=true",
					"haproxy.backend=dynamic",
					"haproxy.check.path=/health",
				},
			},
		}

		// Process event through connector
		result, err := connector.ProcessServiceEvent(ctx, client, serviceEvent)
		if err != nil {
			t.Fatalf("Failed to process service event: %v", err)
		}

		// Verify HAProxy was updated
		backends, err := client.GetBackends()
		if err != nil {
			t.Fatalf("Failed to get backends: %v", err)
		}

		// Should have created backend for the service
		found := false
		for _, backend := range backends {
			if backend.Name == "test_api" { // service names get sanitized
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected backend 'test_api' was not created")
		}

		t.Logf("Service lifecycle test result: %+v", result)
	})
}
