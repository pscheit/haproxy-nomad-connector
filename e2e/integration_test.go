//go:build integration
// +build integration

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pscheit/haproxy-nomad-connector/internal/config"
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
		cfg := &config.Config{
			HAProxy: config.HAProxyConfig{
				Frontend: "https",
			},
		}
		result, err := connector.ProcessServiceEvent(ctx, client, &serviceEvent, cfg)
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

	t.Run("ServiceWithRegexDomain", func(t *testing.T) {
		// Create HAProxy client
		client := haproxy.NewClient("http://localhost:5555", "admin", "adminpwd")

		// Create service registration event with regex domain (production case)
		serviceEvent := connector.ServiceEvent{
			Type: "ServiceRegistration",
			Service: connector.Service{
				ServiceName: "ps-webforge",
				Address:     "192.168.1.200",
				Port:        80,
				Tags: []string{
					"haproxy.enable=true",
					"haproxy.domain=^(www\\.)?ps-webforge\\.com$",
					"haproxy.domain.type=regex",
					"haproxy.backend=dynamic",
				},
			},
		}

		// Process event through connector - this should NOT fail with ACL name errors
		cfg := &config.Config{
			HAProxy: config.HAProxyConfig{
				Frontend: "https",
			},
		}
		result, err := connector.ProcessServiceEvent(ctx, client, &serviceEvent, cfg)
		if err != nil {
			// Check if the error is the original ACL name bug
			if strings.Contains(err.Error(), "character '^' is not permitted in acl name") {
				t.Fatalf("ACL name bug still exists: %v", err)
			}
			// Other errors might be expected (like missing frontend config)
			t.Logf("Expected error (not ACL bug): %v", err)
		}

		// Verify backend was created (connector should sanitize service name)
		backends, err := client.GetBackends()
		if err != nil {
			t.Fatalf("Failed to get backends: %v", err)
		}

		// Should have created backend with sanitized name
		backendFound := false
		for _, backend := range backends {
			if backend.Name == "ps_webforge" { // sanitized service name
				backendFound = true
				t.Logf("✅ Backend created: %s", backend.Name)
				break
			}
		}
		if !backendFound {
			t.Error("Expected backend 'ps_webforge' was not created")
		}

		// Try to verify frontend rules (if supported)
		rules, err := client.GetFrontendRules("https")
		if err != nil {
			t.Logf("Could not get frontend rules (might not be configured): %v", err)
		} else {
			// Look for our regex domain rule
			for _, rule := range rules {
				if rule.Domain == "^(www\\.)?ps-webforge\\.com$" && rule.Backend == "ps_webforge" {
					t.Logf("✅ Frontend rule created: %s -> %s", rule.Domain, rule.Backend)
					break
				}
			}
		}

		t.Logf("Regex domain service test result: %+v", result)
	})

	// ADR-009 Test: Frontend Rule Missing When Backend Pre-exists
	t.Run("ADR009_FrontendRuleMissingWhenBackendPreexists", func(t *testing.T) {
		client := haproxy.NewClient("http://localhost:5555", "admin", "adminpwd")

		cfg := &config.Config{
			HAProxy: config.HAProxyConfig{
				Frontend: "https",
			},
		}

		testBackendName := "crm_prod"
		testDomain := "crm.ps-webforge.net"

		// Cleanup
		defer func() {
			version, _ := client.GetConfigVersion()
			client.DeleteBackend(testBackendName, version)
			client.RemoveFrontendRule("https", testDomain)
		}()

		// Step 1: Pre-create the backend (simulating existing static backend)
		version, err := client.GetConfigVersion()
		if err != nil {
			t.Fatalf("Failed to get config version: %v", err)
		}

		preExistingBackend := haproxy.Backend{
			Name: testBackendName,
			Balance: haproxy.Balance{
				Algorithm: "roundrobin",
			},
		}

		_, err = client.CreateBackend(preExistingBackend, version)
		if err != nil {
			t.Fatalf("Failed to pre-create backend: %v", err)
		}
		t.Logf("✅ Pre-created backend: %s", testBackendName)

		// Step 2: Register service with domain tags (the critical test)
		serviceEvent := connector.ServiceEvent{
			Type: "ServiceRegistration",
			Service: connector.Service{
				ServiceName: "crm-prod",
				Address:     "192.168.1.50",
				Port:        8080,
				Tags: []string{
					"haproxy.enable=true",
					"haproxy.backend=dynamic",
					"haproxy.domain=" + testDomain,
				},
			},
		}

		// Process service registration - this is where the bug would manifest
		result, err := connector.ProcessServiceEvent(ctx, client, &serviceEvent, cfg)
		if err != nil {
			t.Fatalf("Failed to process service registration: %v", err)
		}
		t.Logf("Service registration result: %+v", result)

		// Step 3: Verify the server was added to the existing backend
		servers, err := client.GetServers(testBackendName)
		if err != nil {
			t.Fatalf("Failed to get servers: %v", err)
		}

		serverFound := false
		expectedServerName := "crm_prod_192_168_1_50_8080"
		for _, server := range servers {
			if server.Name == expectedServerName {
				serverFound = true
				t.Logf("✅ Server added to existing backend: %s", server.Name)
				break
			}
		}
		if !serverFound {
			t.Errorf("❌ Server was not added to existing backend. Expected: %s", expectedServerName)
		}

		// Step 4: CRITICAL TEST - Verify frontend rule was created despite backend pre-existing
		rules, err := client.GetFrontendRules("https")
		if err != nil {
			t.Fatalf("Failed to get frontend rules: %v", err)
		}

		frontendRuleFound := false
		for _, rule := range rules {
			if rule.Domain == testDomain && rule.Backend == testBackendName {
				frontendRuleFound = true
				t.Logf("✅ Frontend rule created: %s -> %s", rule.Domain, rule.Backend)
				break
			}
		}

		if !frontendRuleFound {
			t.Errorf("❌ BUG REPRODUCED: Frontend rule missing despite domain tag present")
			t.Errorf("   Expected rule: %s -> %s", testDomain, testBackendName)
			t.Errorf("   This is the exact bug described in ADR-009")

			// Log current rules for debugging
			t.Logf("Current frontend rules:")
			for _, rule := range rules {
				t.Logf("   %s -> %s", rule.Domain, rule.Backend)
			}
		} else {
			t.Logf("✅ Frontend rule correctly created despite pre-existing backend")
		}
	})

	// Additional test: Re-register same service to test idempotency
	t.Run("ADR009_ReRegisterExistingService", func(t *testing.T) {
		client := haproxy.NewClient("http://localhost:5555", "admin", "adminpwd")

		cfg := &config.Config{
			HAProxy: config.HAProxyConfig{
				Frontend: "https",
			},
		}

		testBackendName := "existing_service_prod"
		testDomain := "existing.ps-webforge.net"

		// Cleanup
		defer func() {
			version, _ := client.GetConfigVersion()
			client.DeleteBackend(testBackendName, version)
			client.RemoveFrontendRule("https", testDomain)
		}()

		// Step 1: Register service first time (normal case)
		serviceEvent := connector.ServiceEvent{
			Type: "ServiceRegistration",
			Service: connector.Service{
				ServiceName: "existing-service-prod",
				Address:     "192.168.1.60",
				Port:        8080,
				Tags: []string{
					"haproxy.enable=true",
					"haproxy.backend=dynamic",
					"haproxy.domain=" + testDomain,
				},
			},
		}

		// First registration - creates backend, server, and frontend rule
		result1, err := connector.ProcessServiceEvent(ctx, client, &serviceEvent, cfg)
		if err != nil {
			t.Fatalf("Failed to process first service registration: %v", err)
		}
		t.Logf("First registration result: %+v", result1)

		// Step 2: Re-register the SAME service (simulating restart or re-deployment)
		result2, err := connector.ProcessServiceEvent(ctx, client, &serviceEvent, cfg)
		if err != nil {
			t.Fatalf("Failed to process service re-registration: %v", err)
		}
		t.Logf("Re-registration result: %+v", result2)

		// Verify frontend rule still exists after re-registration
		rules, err := client.GetFrontendRules("https")
		if err != nil {
			t.Fatalf("Failed to get frontend rules: %v", err)
		}

		frontendRuleFound := false
		for _, rule := range rules {
			if rule.Domain == testDomain && rule.Backend == testBackendName {
				frontendRuleFound = true
				t.Logf("✅ Frontend rule persists after re-registration: %s -> %s", rule.Domain, rule.Backend)
				break
			}
		}

		if !frontendRuleFound {
			t.Errorf("❌ Frontend rule missing after re-registration")
		}
	})

	// Test based on exact production scenario from ADR-009
	t.Run("ADR009_ProductionScenario_CRMService", func(t *testing.T) {
		client := haproxy.NewClient("http://localhost:5555", "admin", "adminpwd")

		cfg := &config.Config{
			HAProxy: config.HAProxyConfig{
				Frontend: "https",
			},
		}

		// Simulate the exact production scenario:
		// 1. Backend crm_prod exists from previous deployment/static config
		// 2. Service registers with haproxy.domain=crm.ps-webforge.net
		// 3. Before fix: frontend rule was skipped due to early return
		// 4. After fix: frontend rule should be created

		testBackendName := "crm_prod"
		testDomain := "crm.ps-webforge.net"

		// Cleanup
		defer func() {
			version, _ := client.GetConfigVersion()
			client.DeleteBackend(testBackendName, version)
			client.RemoveFrontendRule("https", testDomain)
		}()

		// Step 1: Create backend manually (as if from previous deployment)
		version, err := client.GetConfigVersion()
		if err != nil {
			t.Fatalf("Failed to get config version: %v", err)
		}

		backend := haproxy.Backend{
			Name: testBackendName,
			Balance: haproxy.Balance{
				Algorithm: "roundrobin",
			},
		}

		_, err = client.CreateBackend(backend, version)
		if err != nil {
			t.Fatalf("Failed to create backend: %v", err)
		}
		t.Logf("✅ Created backend %s (simulating previous deployment)", testBackendName)

		// Step 2: Add a server manually (simulating previous service registration)
		version, err = client.GetConfigVersion()
		if err != nil {
			t.Fatalf("Failed to get config version: %v", err)
		}

		existingServer := haproxy.Server{
			Name:    "crm_prod_192_168_1_50_8080",
			Address: "192.168.1.50",
			Port:    8080,
			Check:   "enabled",
		}

		_, err = client.CreateServer(testBackendName, &existingServer, version)
		if err != nil {
			t.Fatalf("Failed to create existing server: %v", err)
		}
		t.Logf("✅ Added existing server (simulating server from deployment without domain support)")

		// Step 3: Verify no frontend rule exists (production state before fix)
		rules, err := client.GetFrontendRules("https")
		if err != nil {
			t.Logf("Warning: Could not get frontend rules: %v", err)
		} else {
			for _, rule := range rules {
				if rule.Domain == testDomain && rule.Backend == testBackendName {
					t.Fatalf("❌ Frontend rule already exists - this invalidates the test")
				}
			}
			t.Logf("✅ Confirmed no frontend rule exists initially")
		}

		// Step 4: Register service with domain tag (simulating CRM service registration)
		// In the broken version, this would skip frontend rule creation due to early return
		serviceEvent := connector.ServiceEvent{
			Type: "ServiceRegistration",
			Service: connector.Service{
				ServiceName: "crm-prod",
				Address:     "192.168.1.50",
				Port:        8080,
				Tags: []string{
					"haproxy.enable=true",
					"haproxy.backend=dynamic",
					"haproxy.domain=" + testDomain, // This is the key - domain tag
				},
			},
		}

		// This should NOT skip frontend rule creation even though server exists
		result, err := connector.ProcessServiceEvent(ctx, client, &serviceEvent, cfg)
		if err != nil {
			t.Fatalf("Failed to process service registration: %v", err)
		}
		t.Logf("Service registration result: %+v", result)

		// Step 5: Verify frontend rule was created (the fix verification)
		rules, err = client.GetFrontendRules("https")
		if err != nil {
			t.Fatalf("Failed to get frontend rules: %v", err)
		}

		frontendRuleFound := false
		for _, rule := range rules {
			if rule.Domain == testDomain && rule.Backend == testBackendName {
				frontendRuleFound = true
				t.Logf("✅ Frontend rule created: %s -> %s", rule.Domain, rule.Backend)
				break
			}
		}

		if !frontendRuleFound {
			t.Errorf("❌ PRODUCTION BUG REPRODUCED: Frontend rule missing despite domain tag")
			t.Errorf("   This means the fix is incomplete or there's another code path")
			t.Errorf("   Expected rule: %s -> %s", testDomain, testBackendName)

			// Log current state for debugging
			t.Logf("Debug - Service registration result: %+v", result)
			t.Logf("Debug - Current frontend rules:")
			for _, rule := range rules {
				t.Logf("   %s -> %s", rule.Domain, rule.Backend)
			}
		} else {
			t.Logf("✅ Production bug is FIXED - frontend rule created despite existing server")
		}
	})

	// Test for CURRENT production bug: custom backend services don't create frontend rules
	t.Run("ADR009_CurrentBug_CustomBackendWithDomain", func(t *testing.T) {
		client := haproxy.NewClient("http://localhost:5555", "admin", "adminpwd")

		cfg := &config.Config{
			HAProxy: config.HAProxyConfig{
				Frontend: "https",
			},
		}

		// NEW BUG: Services with haproxy.backend=custom AND haproxy.domain tags
		// don't get frontend rules created because processCustomService is not implemented

		testBackendName := "existing_custom_backend"
		testDomain := "custom-service.ps-webforge.net"

		// Cleanup
		defer func() {
			version, _ := client.GetConfigVersion()
			client.DeleteBackend(testBackendName, version)
			client.RemoveFrontendRule("https", testDomain)
		}()

		// Step 1: Create the custom backend (as it would exist in static config)
		version, err := client.GetConfigVersion()
		if err != nil {
			t.Fatalf("Failed to get config version: %v", err)
		}

		backend := haproxy.Backend{
			Name: testBackendName,
			Balance: haproxy.Balance{
				Algorithm: "roundrobin",
			},
		}

		_, err = client.CreateBackend(backend, version)
		if err != nil {
			t.Fatalf("Failed to create custom backend: %v", err)
		}
		t.Logf("✅ Created custom backend %s", testBackendName)

		// Step 2: Register service with CUSTOM backend and domain tags
		serviceEvent := connector.ServiceEvent{
			Type: "ServiceRegistration",
			Service: connector.Service{
				ServiceName: "custom-service",
				Address:     "192.168.1.70",
				Port:        8080,
				Tags: []string{
					"haproxy.enable=true",
					"haproxy.backend=custom",       // ❌ This causes the bug!
					"haproxy.domain=" + testDomain, // Domain tag that should create frontend rule
				},
			},
		}

		// Process service - this should create frontend rule but currently doesn't
		result, err := connector.ProcessServiceEvent(ctx, client, &serviceEvent, cfg)
		if err != nil {
			t.Fatalf("Failed to process service registration: %v", err)
		}
		t.Logf("Service registration result: %+v", result)

		// Check if we got the TODO response (indicating the bug)
		resultMap, ok := result.(map[string]string)
		if ok && resultMap["status"] == "todo" && resultMap["reason"] == "custom backend not implemented" {
			t.Logf("✅ Confirmed: Custom backend service returns TODO (indicating incomplete implementation)")
		}

		// Step 3: Verify NO frontend rule was created (this is the bug)
		rules, err := client.GetFrontendRules("https")
		if err != nil {
			t.Fatalf("Failed to get frontend rules: %v", err)
		}

		frontendRuleFound := false
		for _, rule := range rules {
			if rule.Domain == testDomain && rule.Backend == testBackendName {
				frontendRuleFound = true
				t.Logf("Frontend rule found: %s -> %s", rule.Domain, rule.Backend)
				break
			}
		}

		if frontendRuleFound {
			t.Logf("✅ Frontend rule was created (bug is fixed)")
		} else {
			t.Errorf("❌ CURRENT PRODUCTION BUG REPRODUCED:")
			t.Errorf("   Service with haproxy.backend=custom and haproxy.domain tag")
			t.Errorf("   does NOT create frontend rule because processCustomService is TODO")
			t.Errorf("   Expected rule: %s -> %s", testDomain, testBackendName)
			t.Errorf("   This explains why services are unreachable despite being healthy")
		}
	})

	// Test for the CURRENT canary deployment bug discovered in production
	t.Run("ADR009_CanaryDeploymentRemovesFrontendRule", func(t *testing.T) {
		client := haproxy.NewClient("http://localhost:5555", "admin", "adminpwd")

		cfg := &config.Config{
			HAProxy: config.HAProxyConfig{
				Frontend: "https",
			},
		}

		testBackendName := "test_service_prod"
		testDomain := "test-service.ps-webforge.net"

		// Cleanup
		defer func() {
			version, _ := client.GetConfigVersion()
			client.DeleteBackend(testBackendName, version)
			client.RemoveFrontendRule("https", testDomain)
		}()

		// Step 1: Simulate initial service registration (like 09:06:41 in production)
		t.Log("Step 1: Register initial service instance")

		oldServerEvent := connector.ServiceEvent{
			Type: "ServiceRegistration",
			Service: connector.Service{
				ServiceName: "test-service-prod",
				Address:     "192.168.1.100", // Old server
				Port:        8080,
				Tags: []string{
					"haproxy.enable=true",
					"haproxy.backend=dynamic",
					"haproxy.domain=" + testDomain,
				},
			},
		}

		// Register the first server - this should create backend AND frontend rule
		result1, err := connector.ProcessServiceEvent(ctx, client, &oldServerEvent, cfg)
		if err != nil {
			t.Fatalf("Failed to register initial service: %v", err)
		}
		t.Logf("Initial registration result: %+v", result1)

		// Verify frontend rule was created
		rules, err := client.GetFrontendRules("https")
		if err != nil {
			t.Fatalf("Failed to get frontend rules: %v", err)
		}

		frontendRuleExists := false
		for _, rule := range rules {
			if rule.Domain == testDomain && rule.Backend == testBackendName {
				frontendRuleExists = true
				t.Logf("✅ Frontend rule created: %s -> %s", rule.Domain, rule.Backend)
				break
			}
		}
		if !frontendRuleExists {
			t.Fatalf("Frontend rule was not created during initial registration")
		}

		// Step 2: Simulate canary deployment - register new server
		t.Log("Step 2: Register new server (canary deployment)")

		newServerEvent := connector.ServiceEvent{
			Type: "ServiceRegistration",
			Service: connector.Service{
				ServiceName: "test-service-prod",
				Address:     "192.168.1.200", // New server (canary)
				Port:        8080,
				Tags: []string{
					"haproxy.enable=true",
					"haproxy.backend=dynamic",
					"haproxy.domain=" + testDomain,
				},
			},
		}

		// Register the canary server
		result2, err := connector.ProcessServiceEvent(ctx, client, &newServerEvent, cfg)
		if err != nil {
			t.Fatalf("Failed to register canary service: %v", err)
		}
		t.Logf("Canary registration result: %+v", result2)

		// Verify we now have 2 servers in the backend
		servers, err := client.GetServers(testBackendName)
		if err != nil {
			t.Fatalf("Failed to get servers: %v", err)
		}

		if len(servers) != 2 {
			t.Fatalf("Expected 2 servers after canary registration, got %d", len(servers))
		}
		t.Logf("✅ Both servers registered: %d servers in backend", len(servers))

		// Step 3: Simulate old server deregistration (the critical bug scenario)
		t.Log("Step 3: Deregister old server (this should NOT remove frontend rule)")

		oldServerDeregEvent := connector.ServiceEvent{
			Type: "ServiceDeregistration",
			Service: connector.Service{
				ServiceName: "test-service-prod",
				Address:     "192.168.1.100", // Old server being removed
				Port:        8080,
				Tags: []string{
					"haproxy.enable=true",
					"haproxy.backend=dynamic",
					"haproxy.domain=" + testDomain,
				},
			},
		}

		// Deregister the old server - THIS IS WHERE THE BUG OCCURS
		result3, err := connector.ProcessServiceEvent(ctx, client, &oldServerDeregEvent, cfg)
		if err != nil {
			t.Fatalf("Failed to deregister old service: %v", err)
		}
		t.Logf("Deregistration result: %+v", result3)

		// Step 4: CRITICAL TEST - Verify frontend rule still exists
		t.Log("Step 4: Check if frontend rule still exists (THE BUG TEST)")

		rules, err = client.GetFrontendRules("https")
		if err != nil {
			t.Fatalf("Failed to get frontend rules: %v", err)
		}

		frontendRuleStillExists := false
		for _, rule := range rules {
			if rule.Domain == testDomain && rule.Backend == testBackendName {
				frontendRuleStillExists = true
				t.Logf("✅ Frontend rule still exists: %s -> %s", rule.Domain, rule.Backend)
				break
			}
		}

		// Step 5: Verify we still have 1 healthy server
		servers, err = client.GetServers(testBackendName)
		if err != nil {
			t.Fatalf("Failed to get servers after deregistration: %v", err)
		}

		healthyServers := 0
		for _, server := range servers {
			if server.Name == "test_service_prod_192_168_1_200_8080" {
				healthyServers++
				t.Logf("✅ New server still exists: %s", server.Name)
			}
		}

		if healthyServers != 1 {
			t.Fatalf("Expected 1 healthy server after deregistration, got %d", healthyServers)
		}

		// THE ACTUAL BUG TEST
		if !frontendRuleStillExists {
			t.Errorf("❌ BUG REPRODUCED: Frontend rule was incorrectly removed!")
			t.Errorf("   During canary deployment, deregistering old server removed frontend rule")
			t.Errorf("   even though new healthy server (192.168.1.200:8080) is still running")
			t.Errorf("   This would cause 503 errors despite having healthy backends")

			// Log current state for debugging
			t.Logf("Current frontend rules:")
			for _, rule := range rules {
				t.Logf("   %s -> %s", rule.Domain, rule.Backend)
			}
			t.Logf("Current servers in %s:", testBackendName)
			for _, server := range servers {
				t.Logf("   %s (%s:%d)", server.Name, server.Address, server.Port)
			}
		} else {
			t.Logf("✅ Bug NOT reproduced - frontend rule correctly preserved during canary deployment")
		}

		// Step 6: Verify service would be accessible (simulate HTTP request routing)
		t.Log("Step 6: Verify service accessibility through HAProxy routing")

		if frontendRuleStillExists && healthyServers > 0 {
			t.Logf("✅ Service should be accessible: frontend rule exists + healthy servers")
		} else {
			t.Errorf("❌ Service would be UNREACHABLE: frontend_rule=%v, healthy_servers=%d",
				frontendRuleStillExists, healthyServers)
		}
	})
}
