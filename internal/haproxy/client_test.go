package haproxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClient_CreateBackend(t *testing.T) {
	// Mock server that simulates Data Plane API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		if r.Method != "POST" {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/backends") {
			t.Errorf("Expected /backends in path, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("version") == "" {
			t.Errorf("Expected version parameter")
		}

		// Verify request body
		var backend Backend
		if err := json.NewDecoder(r.Body).Decode(&backend); err != nil {
			t.Errorf("Failed to decode request body: %v", err)
		}
		if backend.Name != "test-backend" {
			t.Errorf("Expected backend name 'test-backend', got %s", backend.Name)
		}

		// Return success response
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(backend)
	}))
	defer server.Close()

	// Create client
	client := NewClient(server.URL, "admin", "password")

	// Test backend creation
	backend := Backend{
		Name: "test-backend",
		Balance: Balance{
			Algorithm: "roundrobin",
		},
	}

	createdBackend, err := client.CreateBackend(backend, 1)
	if err != nil {
		t.Fatalf("Failed to create backend: %v", err)
	}

	if createdBackend.Name != "test-backend" {
		t.Errorf("Expected 'test-backend', got %s", createdBackend.Name)
	}
}

func TestClient_GetBackends(t *testing.T) {
	// Mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("Expected GET, got %s", r.Method)
		}

		backends := []Backend{
			{Name: "backend1", Balance: Balance{Algorithm: "roundrobin"}},
			{Name: "backend2", Balance: Balance{Algorithm: "leastconn"}},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(backends)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")

	backends, err := client.GetBackends()
	if err != nil {
		t.Fatalf("Failed to get backends: %v", err)
	}

	if len(backends) != 2 {
		t.Errorf("Expected 2 backends, got %d", len(backends))
	}

	if backends[0].Name != "backend1" {
		t.Errorf("Expected 'backend1', got %s", backends[0].Name)
	}
}

func TestClient_CreateServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify URL path contains backend name
		expectedPath := "/v3/services/haproxy/configuration/backends/test-backend/servers"
		if !strings.Contains(r.URL.Path, expectedPath) {
			t.Errorf("Expected path to contain %s, got %s", expectedPath, r.URL.Path)
		}

		// Verify server data
		var server Server
		_ = json.NewDecoder(r.Body).Decode(&server)
		if server.Name != "server1" {
			t.Errorf("Expected 'server1', got %s", server.Name)
		}

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(server)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")

	testServer := Server{
		Name:    "server1",
		Address: "192.168.1.10",
		Port:    8080,
		Check:   "enabled",
	}

	_, err := client.CreateServer("test-backend", &testServer, 2)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
}

func TestClient_DrainServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != HTTPMethodPUT {
			t.Errorf("Expected %s, got %s", HTTPMethodPUT, r.Method)
		}
		if !strings.Contains(r.URL.Path, "/runtime/backends/test-backend/servers/server1") {
			t.Errorf("Expected runtime server path, got %s", r.URL.Path)
		}

		var runtimeServer RuntimeServer
		if err := json.NewDecoder(r.Body).Decode(&runtimeServer); err != nil {
			t.Errorf("Failed to decode request body: %v", err)
		}
		if runtimeServer.AdminState != "drain" {
			t.Errorf("Expected admin_state 'drain', got %s", runtimeServer.AdminState)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")

	err := client.DrainServer("test-backend", "server1")
	if err != nil {
		t.Fatalf("Failed to drain server: %v", err)
	}
}

func TestClient_ReadyServer(t *testing.T) {
	testServerStateChange(t, "ready", func(client *Client) error {
		return client.ReadyServer("test-backend", "server1")
	})
}

func TestClient_MaintainServer(t *testing.T) {
	testServerStateChange(t, "maint", func(client *Client) error {
		return client.MaintainServer("test-backend", "server1")
	})
}

// Helper function to test server state changes
func testServerStateChange(t *testing.T, expectedState string, operation func(*Client) error) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != HTTPMethodPUT {
			t.Errorf("Expected %s, got %s", HTTPMethodPUT, r.Method)
		}

		var runtimeServer RuntimeServer
		if err := json.NewDecoder(r.Body).Decode(&runtimeServer); err != nil {
			t.Errorf("Failed to decode request body: %v", err)
		}
		if runtimeServer.AdminState != expectedState {
			t.Errorf("Expected admin_state '%s', got %s", expectedState, runtimeServer.AdminState)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")

	err := operation(client)
	if err != nil {
		t.Fatalf("Server state change operation failed: %v", err)
	}
}

func TestClient_GetRuntimeServer(t *testing.T) {
	expectedServer := RuntimeServer{
		Address:          "192.168.1.10",
		AdminState:       "ready",
		OperationalState: "up",
		Port:             8080,
		ServerID:         1,
		ServerName:       "server1",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("Expected GET, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/runtime/backends/test-backend/servers/server1") {
			t.Errorf("Expected runtime server path, got %s", r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(expectedServer)
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")

	result, err := client.GetRuntimeServer("test-backend", "server1")
	if err != nil {
		t.Fatalf("Failed to get runtime server: %v", err)
	}

	if result.AdminState != expectedServer.AdminState {
		t.Errorf("Expected AdminState %s, got %s", expectedServer.AdminState, result.AdminState)
	}
	if result.Address != expectedServer.Address {
		t.Errorf("Expected Address %s, got %s", expectedServer.Address, result.Address)
	}
}
