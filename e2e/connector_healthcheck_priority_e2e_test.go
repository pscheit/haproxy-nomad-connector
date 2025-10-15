//go:build integration
// +build integration

package e2e

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	"github.com/pscheit/haproxy-nomad-connector/internal/config"
	"github.com/pscheit/haproxy-nomad-connector/internal/connector"
	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
	"github.com/pscheit/haproxy-nomad-connector/internal/nomad"
)

// TestConnector_HealthCheckPriority_ExplicitTagOverDomain_E2E reproduces the priority bug where:
// 1. Service has domain tag (haproxy.domain=test.local) → auto-generates "/" as fallback
// 2. Service ALSO has explicit check tag (haproxy.check.path=/api/health) → user's explicit intent
// 3. BUG: Current code treats domain fallback and explicit tags equally
// 4. Result: Which one wins is unpredictable based on parse order
//
// This is the SIMPLIFIED version of the badaba bug without needing Nomad mocking.
// The core issue: parseHealthCheckFromTags() doesn't distinguish between:
// - Explicit user tags (haproxy.check.path=...)
// - Auto-generated fallbacks (domain → "/" convenience)
//
// CORRECT priority should be:
// 1. Explicit haproxy.check.path tag (user said it!)
// 2. Domain tag fallback (convenience)
func TestConnector_HealthCheckPriority_ExplicitTagOverDomain_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := haproxy.NewClient("http://localhost:5555", "admin", "adminpwd")
	logger := log.New(os.Stderr, "[test] ", log.LstdFlags)

	cfg := &config.Config{
		HAProxy: config.HAProxyConfig{
			Address:         "http://localhost:5555",
			Username:        "admin",
			Password:        "adminpwd",
			BackendStrategy: "create_new",
			Frontend:        "https",
		},
	}

	serviceName := "priority-test"
	backendName := "priority_test"

	t.Run("0_Setup_CleanSlate", func(t *testing.T) {
		setupCleanSlate(t, client)
	})

	t.Run("1_RegisterServiceWithDomainAndExplicitCheck", func(t *testing.T) {
		nomadEvent := nomad.ServiceEvent{
			Type:  "ServiceRegistration",
			Topic: "Service",
			Payload: nomad.Payload{
				Service: &nomad.Service{
					ServiceName: serviceName,
					Address:     "192.168.5.100",
					Port:        8080,
					JobID:       "",
					Tags: []string{
						"haproxy.enable=true",
						"haproxy.backend=dynamic",
						"haproxy.domain=priority.test.local", // Auto-generates "/" fallback
						"haproxy.check.path=/api/health",     // Explicit user intent!
					},
				},
			},
		}

		ctx := context.Background()
		result, err := connector.ProcessNomadServiceEvent(ctx, client, nil, nomadEvent, logger, cfg)
		if err != nil {
			t.Fatalf("ProcessNomadServiceEvent failed: %v", err)
		}

		resultMap, ok := result.(map[string]string)
		if !ok {
			t.Fatalf("Expected result to be map[string]string, got %T", result)
		}

		t.Logf("✓ Service registered with domain tag + explicit check tag")
		t.Logf("  Status: %s", resultMap["status"])
		t.Logf("  Backend: %s", resultMap["backend"])
		t.Logf("  Domain fallback would use: /")
		t.Logf("  Explicit check tag specifies: /api/health")
		t.Logf("  Question: Which one wins?")
	})

	t.Run("2_VerifyExplicitCheckPathWins", func(t *testing.T) {
		time.Sleep(2 * time.Second)

		// EXPECTED: Explicit tag /api/health should win, NOT domain's /
		assertBackendConfigEquals(t, backendName, ActualBackendConfig{
			Name:               backendName,
			Mode:               "http",
			Algorithm:          "roundrobin",
			HealthCheckType:    "http",
			HTTPCheckMethod:    "GET",
			HTTPCheckPath:      "/api/health", // ← Explicit tag should win!
			HTTPCheckHost:      "priority.test.local",
			DefaultServerCheck: true,
		})

		t.Logf("✓ Backend uses explicit check path '/api/health'")
		t.Logf("  Explicit user tags correctly override domain fallback")
		t.Logf("  BUG FOUND: Host header missing when explicit check tags used!")
	})
}

// TestConnector_HealthCheckPriority_BackendReconciliation_E2E tests the backend reconciliation scenario:
// 1. Service registered with domain tag only (creates backend with "/" fallback)
// 2. Service re-registered with domain + explicit check tag (simulating config update or connector restart)
// 3. EXPECTED: Explicit check tag should update the backend and win over domain fallback
//
// This simulates the badaba scenario where:
// - Backend already exists (from previous deployment)
// - Service configuration gets updated with more specific health check
// - System must reconcile existing backend with new health check config
func TestConnector_HealthCheckPriority_BackendReconciliation_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := haproxy.NewClient("http://localhost:5555", "admin", "adminpwd")
	logger := log.New(os.Stderr, "[test] ", log.LstdFlags)

	cfg := &config.Config{
		HAProxy: config.HAProxyConfig{
			Address:         "http://localhost:5555",
			Username:        "admin",
			Password:        "adminpwd",
			BackendStrategy: "create_new",
			Frontend:        "https",
		},
	}

	serviceName := "reconciliation-test"
	backendName := "reconciliation_test"

	t.Run("0_Setup_CleanSlate", func(t *testing.T) {
		setupCleanSlate(t, client)
	})

	t.Run("1_InitialRegistration_DomainOnly", func(t *testing.T) {
		nomadEvent := nomad.ServiceEvent{
			Type:  "ServiceRegistration",
			Topic: "Service",
			Payload: nomad.Payload{
				Service: &nomad.Service{
					ServiceName: serviceName,
					Address:     "192.168.5.200",
					Port:        9090,
					JobID:       "",
					Tags: []string{
						"haproxy.enable=true",
						"haproxy.backend=dynamic",
						"haproxy.domain=reconciliation.test.local", // Only domain tag
					},
				},
			},
		}

		ctx := context.Background()
		result, err := connector.ProcessNomadServiceEvent(ctx, client, nil, nomadEvent, logger, cfg)
		if err != nil {
			t.Fatalf("Initial registration failed: %v", err)
		}

		resultMap, ok := result.(map[string]string)
		if !ok {
			t.Fatalf("Expected result to be map[string]string, got %T", result)
		}

		t.Logf("✓ Initial service registered with domain tag only")
		t.Logf("  Status: %s", resultMap["status"])
		t.Logf("  Backend: %s", resultMap["backend"])
		t.Logf("  Health check: / (domain fallback)")
	})

	t.Run("2_VerifyInitialBackendUsesDomainFallback", func(t *testing.T) {
		time.Sleep(2 * time.Second)

		assertBackendConfigEquals(t, backendName, ActualBackendConfig{
			Name:               backendName,
			Mode:               "http",
			Algorithm:          "roundrobin",
			HealthCheckType:    "http",
			HTTPCheckMethod:    "GET",
			HTTPCheckPath:      "/", // Domain fallback
			HTTPCheckHost:      "reconciliation.test.local",
			DefaultServerCheck: true,
		})

		t.Logf("✓ Backend created with domain fallback '/'")
	})

	t.Run("3_ReRegistration_WithExplicitCheck", func(t *testing.T) {
		nomadEvent := nomad.ServiceEvent{
			Type:  "ServiceRegistration",
			Topic: "Service",
			Payload: nomad.Payload{
				Service: &nomad.Service{
					ServiceName: serviceName,
					Address:     "192.168.5.200",
					Port:        9090,
					JobID:       "",
					Tags: []string{
						"haproxy.enable=true",
						"haproxy.backend=dynamic",
						"haproxy.domain=reconciliation.test.local",
						"haproxy.check.path=/healthcheck", // Now with explicit check!
					},
				},
			},
		}

		ctx := context.Background()
		result, err := connector.ProcessNomadServiceEvent(ctx, client, nil, nomadEvent, logger, cfg)
		if err != nil {
			t.Fatalf("Re-registration failed: %v", err)
		}

		resultMap, ok := result.(map[string]string)
		if !ok {
			t.Fatalf("Expected result to be map[string]string, got %T", result)
		}

		t.Logf("✓ Service re-registered with explicit check tag")
		t.Logf("  Status: %s", resultMap["status"])
		t.Logf("  Backend: %s", resultMap["backend"])
		t.Logf("  Health check: /healthcheck (explicit tag)")
	})

	t.Run("4_VerifyBackendReconciled_ExplicitCheckWins", func(t *testing.T) {
		time.Sleep(2 * time.Second)

		// EXPECTED: Backend should be reconciled to use explicit check path
		assertBackendConfigEquals(t, backendName, ActualBackendConfig{
			Name:               backendName,
			Mode:               "http",
			Algorithm:          "roundrobin",
			HealthCheckType:    "http",
			HTTPCheckMethod:    "GET",
			HTTPCheckPath:      "/healthcheck", // Explicit tag should win!
			HTTPCheckHost:      "reconciliation.test.local",
			DefaultServerCheck: true,
		})

		t.Logf("✓ Backend reconciled: explicit check path '/healthcheck' wins")
		t.Logf("  This proves backend reconciliation works correctly")
	})
}

// TestConnector_HealthCheckPriority_NomadCheckOverDomainFallback_E2E reproduces the EXACT badaba bug:
// 1. Service has domain tag ONLY (generates "/" fallback health check)
// 2. Nomad job definition has check block with path="/healthcheck"
// 3. BUG: Domain fallback "/" incorrectly wins over Nomad check "/healthcheck"
// 4. Result: Health checks hit wrong endpoint → 404 errors → badaba.ps-webforge.net DOWN
//
// This test simulates the connector restart scenario where:
// - Backend already exists with domain fallback "/"
// - Service re-registers with Nomad check available
// - Expected: Nomad check should update backend to use "/healthcheck"
// - Actual (BUG): Domain fallback "/" remains, causing health check failures
func TestConnector_HealthCheckPriority_NomadCheckOverDomainFallback_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := haproxy.NewClient("http://localhost:5555", "admin", "adminpwd")
	logger := log.New(os.Stderr, "[test] ", log.LstdFlags)

	cfg := &config.Config{
		HAProxy: config.HAProxyConfig{
			Address:         "http://localhost:5555",
			Username:        "admin",
			Password:        "adminpwd",
			BackendStrategy: "create_new",
			Frontend:        "https",
		},
	}

	serviceName := "badaba-bug-test"
	backendName := "badaba_bug_test"

	t.Run("0_Setup_CleanSlate", func(t *testing.T) {
		setupCleanSlate(t, client)
	})

	t.Run("1_InitialRegistration_DomainOnlyWithoutNomadCheck", func(t *testing.T) {
		nomadEvent := nomad.ServiceEvent{
			Type:  "ServiceRegistration",
			Topic: "Service",
			Payload: nomad.Payload{
				Service: &nomad.Service{
					ServiceName: serviceName,
					Address:     "192.168.5.250",
					Port:        8888,
					JobID:       "", // No JobID = no Nomad check lookup
					Tags: []string{
						"haproxy.enable=true",
						"haproxy.backend=dynamic",
						"haproxy.domain=badaba.test.local", // Only domain tag
					},
				},
			},
		}

		ctx := context.Background()
		result, err := connector.ProcessNomadServiceEvent(ctx, client, nil, nomadEvent, logger, cfg)
		if err != nil {
			t.Fatalf("Initial registration failed: %v", err)
		}

		resultMap, ok := result.(map[string]string)
		if !ok {
			t.Fatalf("Expected result to be map[string]string, got %T", result)
		}

		t.Logf("✓ Initial service registered with domain tag only (no Nomad check)")
		t.Logf("  Status: %s", resultMap["status"])
		t.Logf("  Backend: %s", resultMap["backend"])
		t.Logf("  Health check: / (domain fallback)")
	})

	t.Run("2_VerifyBackendCreatedWithDomainFallback", func(t *testing.T) {
		time.Sleep(2 * time.Second)

		assertBackendConfigEquals(t, backendName, ActualBackendConfig{
			Name:               backendName,
			Mode:               "http",
			Algorithm:          "roundrobin",
			HealthCheckType:    "http",
			HTTPCheckMethod:    "GET",
			HTTPCheckPath:      "/", // Domain fallback
			HTTPCheckHost:      "badaba.test.local",
			DefaultServerCheck: true,
		})

		t.Logf("✓ Backend created with domain fallback '/' health check")
	})

	t.Run("3_SimulateConnectorRestart_NomadCheckNowAvailable", func(t *testing.T) {
		// Create mock Nomad client that returns health check from job definition
		mockNomad := NewMockNomadClient()
		mockNomad.SetServiceCheck(serviceName, &nomad.ServiceCheck{
			Type:   "http",
			Path:   "/healthcheck",
			Method: "GET",
		})

		// Simulate connector restart: same service re-registers, but now with JobID
		// so Nomad check is fetched
		nomadEvent := nomad.ServiceEvent{
			Type:  "ServiceRegistration",
			Topic: "Service",
			Payload: nomad.Payload{
				Service: &nomad.Service{
					ServiceName: serviceName,
					Address:     "192.168.5.250",
					Port:        8888,
					JobID:       "badaba-prod", // Now has JobID → triggers Nomad check lookup
					Tags: []string{
						"haproxy.enable=true",
						"haproxy.backend=dynamic",
						"haproxy.domain=badaba.test.local", // Still has domain tag
					},
				},
			},
		}

		ctx := context.Background()
		result, err := connector.ProcessNomadServiceEvent(ctx, client, mockNomad, nomadEvent, logger, cfg)
		if err != nil {
			t.Fatalf("Re-registration with Nomad check failed: %v", err)
		}

		resultMap, ok := result.(map[string]string)
		if !ok {
			t.Fatalf("Expected result to be map[string]string, got %T", result)
		}

		t.Logf("✓ Service re-registered with Nomad check available")
		t.Logf("  Status: %s", resultMap["status"])
		t.Logf("  Backend: %s", resultMap["backend"])
		t.Logf("  Nomad check: GET /healthcheck")
		t.Logf("  Domain fallback: GET /")
		t.Logf("  Question: Which one wins?")
	})

	t.Run("4_VerifyNomadCheckWinsOverDomainFallback", func(t *testing.T) {
		time.Sleep(2 * time.Second)

		// EXPECTED: Nomad check "/healthcheck" should win over domain fallback "/"
		// This is the badaba bug: domain fallback incorrectly has higher priority
		assertBackendConfigEquals(t, backendName, ActualBackendConfig{
			Name:               backendName,
			Mode:               "http",
			Algorithm:          "roundrobin",
			HealthCheckType:    "http",
			HTTPCheckMethod:    "GET",
			HTTPCheckPath:      "/healthcheck", // Nomad check should win!
			HTTPCheckHost:      "badaba.test.local",
			DefaultServerCheck: true,
		})

		t.Logf("✓ Backend reconciled: Nomad check '/healthcheck' wins over domain fallback")
		t.Logf("  This proves the priority logic is correct:")
		t.Logf("  1. Explicit tags (haproxy.check.path=...) - highest priority")
		t.Logf("  2. Nomad job check blocks - medium priority")
		t.Logf("  3. Domain tag fallback (/) - lowest priority")
	})
}
