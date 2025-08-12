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

func TestClient_AddFrontendRule(t *testing.T) {
	// Track API calls to verify transaction workflow
	var transactionCreated, aclsUpdated, rulesUpdated, transactionCommitted bool
	var transactionID string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/configuration/version"):
			// Mock version endpoint
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("12"))

		case r.Method == "POST" && strings.Contains(r.URL.Path, "/transactions"):
			// Create transaction
			transactionCreated = true
			transactionID = "test-tx-123"
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			response := map[string]interface{}{
				"id":       transactionID,
				"status":   "in_progress",
				"_version": 1,
			}
			_ = json.NewEncoder(w).Encode(response)

		case r.Method == "PUT" && strings.Contains(r.URL.Path, "/frontends/https/acls"):
			// Update ACLs
			aclsUpdated = true
			if r.URL.Query().Get("transaction_id") != transactionID {
				t.Errorf("Expected transaction_id %s", transactionID)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			response := []map[string]interface{}{
				{
					"acl_name":  "is_example_com",
					"criterion": "hdr(host)",
					"value":     "example.com",
				},
			}
			_ = json.NewEncoder(w).Encode(response)

		case r.Method == "PUT" && strings.Contains(r.URL.Path, "/frontends/https/backend_switching_rules"):
			// Update backend switching rules
			rulesUpdated = true
			if r.URL.Query().Get("transaction_id") != transactionID {
				t.Errorf("Expected transaction_id %s", transactionID)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			response := []map[string]interface{}{
				{
					"cond":      "if",
					"cond_test": "is_example_com",
					"name":      "example_backend",
				},
			}
			_ = json.NewEncoder(w).Encode(response)

		case r.Method == "GET" && strings.Contains(r.URL.Path, "/frontends/https/acls"):
			// Mock getting current ACLs (empty initially)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]interface{}{})

		case r.Method == "GET" && strings.Contains(r.URL.Path, "/frontends/https/backend_switching_rules"):
			// Mock getting current backend switching rules (empty initially)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]interface{}{})

		case r.Method == "PUT" && strings.Contains(r.URL.Path, "/transactions/"+transactionID):
			// Commit transaction
			transactionCommitted = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			response := map[string]interface{}{
				"id":     transactionID,
				"status": "success",
			}
			_ = json.NewEncoder(w).Encode(response)

		default:
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")

	// This should fail initially - method doesn't exist yet
	err := client.AddFrontendRule("https", "example.com", "example_backend")
	if err != nil {
		t.Fatalf("AddFrontendRule failed: %v", err)
	}

	// Verify all expected API calls were made
	if !transactionCreated {
		t.Error("Expected transaction to be created")
	}
	if !aclsUpdated {
		t.Error("Expected ACLs to be updated")
	}
	if !rulesUpdated {
		t.Error("Expected backend switching rules to be updated")
	}
	if !transactionCommitted {
		t.Error("Expected transaction to be committed")
	}
}

func TestClient_RemoveFrontendRule(t *testing.T) {
	var transactionCreated, aclsUpdated, rulesUpdated, transactionCommitted bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/configuration/version"):
			// Mock version endpoint
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("12"))

		case r.Method == "GET" && strings.Contains(r.URL.Path, "/frontends/https/acls"):
			// Mock getting current ACLs (one rule that will be removed)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			response := []map[string]interface{}{
				{
					"acl_name":  "is_example_com",
					"criterion": "hdr(host)",
					"value":     "example.com",
				},
			}
			_ = json.NewEncoder(w).Encode(response)

		case r.Method == "GET" && strings.Contains(r.URL.Path, "/frontends/https/backend_switching_rules"):
			// Mock getting current backend switching rules (one rule that will be removed)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			response := []map[string]interface{}{
				{
					"cond":      "if",
					"cond_test": "is_example_com",
					"name":      "example_backend",
				},
			}
			_ = json.NewEncoder(w).Encode(response)

		case r.Method == "POST" && strings.Contains(r.URL.Path, "/transactions"):
			transactionCreated = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			response := map[string]interface{}{
				"id":       "test-tx-456",
				"status":   "in_progress",
				"_version": 1,
			}
			_ = json.NewEncoder(w).Encode(response)

		case r.Method == "PUT" && strings.Contains(r.URL.Path, "/frontends/https/acls"):
			aclsUpdated = true
			// Should receive empty array when removing last rule
			var acls []interface{}
			_ = json.NewDecoder(r.Body).Decode(&acls)
			if len(acls) != 0 {
				t.Errorf("Expected empty ACL array when removing rule, got %d items", len(acls))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]interface{}{})

		case r.Method == "PUT" && strings.Contains(r.URL.Path, "/frontends/https/backend_switching_rules"):
			rulesUpdated = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]interface{}{})

		case r.Method == "PUT" && strings.Contains(r.URL.Path, "/transactions/"):
			transactionCommitted = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			response := map[string]interface{}{
				"id":     "test-tx-456",
				"status": "success",
			}
			_ = json.NewEncoder(w).Encode(response)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")

	// This should fail initially - method doesn't exist yet
	err := client.RemoveFrontendRule("https", "example.com")
	if err != nil {
		t.Fatalf("RemoveFrontendRule failed: %v", err)
	}

	// Verify transaction workflow was followed
	if !transactionCreated || !aclsUpdated || !rulesUpdated || !transactionCommitted {
		t.Error("Expected complete transaction workflow for rule removal")
	}
}

func TestClient_GetFrontendRules(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("Expected GET, got %s", r.Method)
		}

		switch {
		case strings.Contains(r.URL.Path, "/frontends/https/acls"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			response := []map[string]interface{}{
				{
					"acl_name":  "is_example_com",
					"criterion": "hdr(host)",
					"value":     "example.com",
				},
				{
					"acl_name":  "is_test_com",
					"criterion": "hdr(host)",
					"value":     "test.com",
				},
			}
			_ = json.NewEncoder(w).Encode(response)

		case strings.Contains(r.URL.Path, "/frontends/https/backend_switching_rules"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			response := []map[string]interface{}{
				{
					"cond":      "if",
					"cond_test": "is_example_com",
					"name":      "example_backend",
				},
				{
					"cond":      "if",
					"cond_test": "is_test_com",
					"name":      "test_backend",
				},
			}
			_ = json.NewEncoder(w).Encode(response)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")

	// This should fail initially - method doesn't exist yet
	rules, err := client.GetFrontendRules("https")
	if err != nil {
		t.Fatalf("GetFrontendRules failed: %v", err)
	}

	expectedRules := []FrontendRule{
		{Domain: "example.com", Backend: "example_backend"},
		{Domain: "test.com", Backend: "test_backend"},
	}

	if len(rules) != len(expectedRules) {
		t.Fatalf("Expected %d rules, got %d", len(expectedRules), len(rules))
	}

	for i, rule := range rules {
		if rule.Domain != expectedRules[i].Domain {
			t.Errorf("Expected domain %s, got %s", expectedRules[i].Domain, rule.Domain)
		}
		if rule.Backend != expectedRules[i].Backend {
			t.Errorf("Expected backend %s, got %s", expectedRules[i].Backend, rule.Backend)
		}
	}
}
