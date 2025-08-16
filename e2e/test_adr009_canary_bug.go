//go:build integration
// +build integration

// E2E test for ADR-009 Canary Deployment Bug
// This test reproduces the exact sequence that happened in production:
// 1. Service registration creates frontend rule
// 2. During canary deployment, old server deregisters
// 3. BUG: Frontend rule gets removed even though new server still exists
// 4. Service becomes unreachable despite having healthy servers
//
// Run with: go test -tags=integration -v ./e2e/ -run TestADR009_CanaryDeploymentRemovesFrontendRule

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/pscheit/haproxy-nomad-connector/internal/config"
	"github.com/pscheit/haproxy-nomad-connector/internal/connector"
	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
)

func TestADR009_CanaryDeploymentRemovesFrontendRule(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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
}
