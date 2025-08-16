//go:build integration
// +build integration

// E2E test for ADR-009: Frontend Rule Missing When Backend Pre-exists
// This test reproduces the exact scenario described in ADR-009 where:
// 1. A HAProxy backend already exists
// 2. New service registers with domain tags
// 3. Server gets added to existing backend
// 4. Frontend rule creation should happen but might be skipped (the bug)
//
// Run with: go test -tags=integration -v ./e2e/ -run TestADR009_FrontendRuleMissingWhenBackendPreexists

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/pscheit/haproxy-nomad-connector/internal/config"
	"github.com/pscheit/haproxy-nomad-connector/internal/connector"
	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
)

func TestADR009_FrontendRuleMissingWhenBackendPreexists(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := haproxy.NewClient("http://localhost:5555", "admin", "adminpwd")

	// Test configuration matching production setup
	cfg := &config.Config{
		HAProxy: config.HAProxyConfig{
			Frontend: "https",
		},
	}

	testBackendName := "crm_prod"
	testDomain := "crm.ps-webforge.net"

	// Setup: Clean up any existing test resources
	t.Cleanup(func() {
		version, _ := client.GetConfigVersion()
		client.DeleteBackend(testBackendName, version)
		client.RemoveFrontendRule("https", testDomain)
	})

	t.Run("ReproduceBug_BackendExistsButFrontendRuleMissing", func(t *testing.T) {
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

		// Step 2: Verify backend exists but no frontend rule exists
		existingBackend, err := client.GetBackend(testBackendName)
		if err != nil {
			t.Fatalf("Backend should exist: %v", err)
		}
		t.Logf("✅ Confirmed backend exists: %s", existingBackend.Name)

		// Verify no frontend rule exists for our domain
		rules, err := client.GetFrontendRules("https")
		if err != nil {
			t.Logf("Warning: Could not get frontend rules: %v", err)
		} else {
			for _, rule := range rules {
				if rule.Domain == testDomain && rule.Backend == testBackendName {
					t.Fatalf("❌ Frontend rule already exists before test - clean up failed")
				}
			}
			t.Logf("✅ Confirmed no frontend rule exists for domain %s", testDomain)
		}

		// Step 3: Register service with domain tags (the critical test)
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

		// Step 4: Verify the server was added to the existing backend
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

		// Step 5: CRITICAL TEST - Verify frontend rule was created despite backend pre-existing
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

	t.Run("VerifyFixByForcingReconciliation", func(t *testing.T) {
		// This simulates the workaround described in ADR-009: restart connector
		// In our test, we'll re-process the same service registration

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

		// Re-process the same service (simulating connector restart)
		result, err := connector.ProcessServiceEvent(ctx, client, &serviceEvent, cfg)
		if err != nil {
			t.Fatalf("Failed to re-process service registration: %v", err)
		}
		t.Logf("Re-processing result: %+v", result)

		// Verify frontend rule exists after re-processing
		rules, err := client.GetFrontendRules("https")
		if err != nil {
			t.Fatalf("Failed to get frontend rules: %v", err)
		}

		frontendRuleFound := false
		for _, rule := range rules {
			if rule.Domain == testDomain && rule.Backend == testBackendName {
				frontendRuleFound = true
				t.Logf("✅ Frontend rule exists after re-processing: %s -> %s", rule.Domain, rule.Backend)
				break
			}
		}

		if !frontendRuleFound {
			t.Errorf("❌ Frontend rule still missing after re-processing")
		}
	})
}
