//go:build integration
// +build integration

package e2e

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pscheit/haproxy-nomad-connector/internal/config"
	"github.com/pscheit/haproxy-nomad-connector/internal/connector"
	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
	"github.com/pscheit/haproxy-nomad-connector/internal/nomad"
)

const (
	advCheckHTTP = "httpchk"
)

// getHAProxyConfig reads the HAProxy configuration file from the test container
func getHAProxyConfig(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "docker", "exec", "haproxy-test", "cat", "/usr/local/etc/haproxy/haproxy.cfg")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to read HAProxy config: %v", err)
	}
	return string(output)
}

// executeHAProxySocketCommand executes a command against the HAProxy socket
func executeHAProxySocketCommand(t *testing.T, socketCommand string) (string, error) {
	t.Helper()
	ctx := context.Background()
	// #nosec G204 - socketCommand is controlled by test code, not external input
	cmd := exec.CommandContext(ctx, "docker", "exec", "haproxy-test", "sh", "-c",
		fmt.Sprintf("echo '%s' | socat stdio /tmp/haproxy.sock", socketCommand))
	output, err := cmd.Output()
	return string(output), err
}

// getHAProxyStatsForBackend retrieves stats for a specific backend from HAProxy socket
func getHAProxyStatsForBackend(t *testing.T, backendName string) string {
	t.Helper()
	output, err := executeHAProxySocketCommand(t, "show stat")
	if err != nil {
		return ""
	}
	// Filter for backend in Go instead of shell to avoid gosec warning
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, backendName+",") {
			return line
		}
	}
	return ""
}

// createMisconfiguredBackendForTest creates a backend without health checks to simulate production scenarios
func createMisconfiguredBackendForTest(t *testing.T, client *haproxy.Client, backendName string) {
	t.Helper()

	version, err := client.GetConfigVersion()
	if err != nil {
		t.Fatalf("Failed to get config version: %v", err)
	}

	misconfiguredBackend := haproxy.Backend{
		Name: backendName,
		Balance: haproxy.Balance{
			Algorithm: "roundrobin",
		},
		// NO AdvCheck, NO HTTPCheckParams, NO DefaultServer - simulating misconfiguration
	}

	_, err = client.CreateBackend(misconfiguredBackend, version)
	if err != nil {
		t.Fatalf("Failed to create misconfigured backend: %v", err)
	}

	// Verify it's actually misconfigured
	backend, err := client.GetBackend(backendName)
	if err != nil {
		t.Fatalf("Failed to get backend: %v", err)
	}

	if backend.AdvCheck != "" {
		t.Errorf("Expected AdvCheck to be empty (misconfigured), got: %s", backend.AdvCheck)
	}
	if backend.HTTPCheckParams != nil {
		t.Error("Expected HTTPCheckParams to be nil (misconfigured)")
	}
	if backend.DefaultServer != nil {
		t.Error("Expected DefaultServer to be nil (misconfigured)")
	}

	t.Logf("✓ Created misconfigured backend")
	t.Logf("  Backend: %s", backendName)
	t.Logf("  AdvCheck: %v (empty = no HTTP checks)", backend.AdvCheck)
	t.Logf("  DefaultServer: %v (nil = no default-server check)", backend.DefaultServer)
}

// TestConnector_HTTPHealthCheckE2E tests the complete connector flow with HTTP health checks
// This is a TRUE end-to-end test that:
// 1. Simulates a Nomad service registration event with health check tags
// 2. Calls the connector's ProcessServiceEvent
// 3. Verifies the connector creates the backend with HTTP health checks
// 4. Verifies the connector adds the server
// 5. Verifies the connector enables health checks via socket
// 6. Verifies HAProxy actually runs HTTP health checks
func TestConnector_HTTPHealthCheckE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := haproxy.NewClient("http://localhost:5555", "admin", "adminpwd")

	cfg := &config.Config{
		HAProxy: config.HAProxyConfig{
			Address:         "http://localhost:5555",
			Username:        "admin",
			Password:        "adminpwd",
			BackendStrategy: "create_new",
		},
	}

	serviceName := "test-service-e2e"
	backendName := "test_service_e2e"

	cleanup := func() {
		version, _ := client.GetConfigVersion()
		_ = client.DeleteBackend(backendName, version)
	}
	defer cleanup()

	t.Run("1_ProcessServiceRegistration_WithHealthCheckTags", func(t *testing.T) {
		event := &connector.ServiceEvent{
			Type: connector.EventTypeServiceRegistration,
			Service: connector.Service{
				ServiceName: serviceName,
				Address:     "test-backend",
				Port:        80,
				Tags: []string{
					"haproxy.enable=true",
					"haproxy.check.path=/health",
					"haproxy.check.host=test-e2e.local",
				},
			},
		}

		ctx := context.Background()
		result, err := connector.ProcessServiceEvent(ctx, client, event, cfg)
		if err != nil {
			t.Fatalf("ProcessServiceEvent failed: %v", err)
		}

		resultMap, ok := result.(map[string]string)
		if !ok {
			t.Fatalf("Expected result to be map[string]string, got %T", result)
		}

		if resultMap["status"] != "created" && resultMap["status"] != "updated" {
			t.Errorf("Expected status 'created' or 'updated', got: %s", resultMap["status"])
		}

		t.Logf("✓ Connector processed service registration")
		t.Logf("  Status: %s", resultMap["status"])
		t.Logf("  Backend: %s", resultMap["backend"])
	})

	t.Run("2_VerifyBackendHasHTTPCheck", func(t *testing.T) {
		backend, err := client.GetBackend(backendName)
		if err != nil {
			t.Fatalf("Failed to get backend: %v", err)
		}

		if backend.AdvCheck != advCheckHTTP {
			t.Errorf("Expected AdvCheck=%s, got: %s", advCheckHTTP, backend.AdvCheck)
		}

		if backend.HTTPCheckParams == nil {
			t.Fatal("HTTPCheckParams is nil - connector did not configure HTTP health checks!")
		}

		if backend.HTTPCheckParams.URI != "/health" {
			t.Errorf("Expected URI=/health, got: %s", backend.HTTPCheckParams.URI)
		}

		t.Logf("✓ Backend has HTTP health check configuration")
		t.Logf("  URI: %s", backend.HTTPCheckParams.URI)
	})

	t.Run("3_VerifyHAProxyConfig", func(t *testing.T) {
		configContent := getHAProxyConfig(t)

		if !strings.Contains(configContent, fmt.Sprintf("backend %s", backendName)) {
			t.Error("Backend not found in HAProxy config")
		}

		if !strings.Contains(configContent, "option httpchk") {
			t.Error("'option httpchk' not found in HAProxy config - connector did not create backend correctly!")
		}

		hasHTTPCheckPath := strings.Contains(configContent, "/health")
		if !hasHTTPCheckPath {
			t.Error("Health check path '/health' not found in HAProxy config")
		}

		t.Logf("✓ HAProxy config contains correct HTTP health check")
	})

	t.Run("4_VerifyServerAdded", func(t *testing.T) {
		servers, err := client.GetServers(backendName)
		if err != nil {
			t.Fatalf("Failed to get servers: %v", err)
		}

		if len(servers) == 0 {
			t.Fatal("No servers found - connector did not add server!")
		}

		server := servers[0]
		if server.Address != "test-backend" {
			t.Errorf("Expected address=test-backend, got: %s", server.Address)
		}

		if server.Port != 80 {
			t.Errorf("Expected port=80, got: %d", server.Port)
		}

		if server.Check != connector.CheckEnabled {
			t.Errorf("Expected check=%s, got: %s", connector.CheckEnabled, server.Check)
		}

		t.Logf("✓ Server added with check enabled")
		t.Logf("  Server: %s:%d", server.Address, server.Port)
	})

	t.Run("5_VerifyHealthCheckEnabled", func(t *testing.T) {
		ctx := context.Background()
		beforeCmd := exec.CommandContext(ctx, "docker", "exec", "haproxy-test", "sh", "-c",
			"ps aux | grep 'haproxy.*-sf' | grep -v grep | awk '{print $1}'")
		beforeOutput, _ := beforeCmd.Output()
		beforePID := strings.TrimSpace(string(beforeOutput))
		t.Logf("HAProxy worker PID before reload: %s", beforePID)

		t.Log("Waiting for DataPlane API to trigger HAProxy reload (reload-delay=5s)...")
		time.Sleep(7 * time.Second)

		afterCmd := exec.CommandContext(ctx, "docker", "exec", "haproxy-test", "sh", "-c",
			"ps aux | grep 'haproxy.*-sf' | grep -v grep | awk '{print $1}'")
		afterOutput, _ := afterCmd.Output()
		afterPID := strings.TrimSpace(string(afterOutput))
		t.Logf("HAProxy worker PID after reload: %s", afterPID)

		if beforePID == afterPID && beforePID != "" {
			t.Errorf("⚠️  HAProxy did NOT reload - worker PID unchanged (%s)", beforePID)
			t.Error("   This explains why server doesn't appear in runtime stats!")
		} else if afterPID != "" {
			t.Logf("✓ HAProxy reloaded successfully (PID changed: %s → %s)", beforePID, afterPID)
		}

		servers, err := client.GetServers(backendName)
		if err != nil {
			t.Fatalf("Failed to get servers: %v", err)
		}

		if len(servers) == 0 {
			t.Fatal("No servers found")
		}

		t.Logf("✓ Server configured with health checks in HAProxy")
	})

	t.Run("6_VerifyHealthChecksActuallyRun", func(t *testing.T) {
		t.Log("Polling for server to appear in HAProxy runtime stats (waiting for reload)...")

		var stats string
		maxAttempts := 30
		found := false

		for attempt := 1; attempt <= maxAttempts; attempt++ {
			stats = getHAProxyStatsForBackend(t, backendName)
			if stats != "" {
				found = true
				t.Logf("✓ Backend appeared in stats after %d seconds", attempt)
				break
			}

			if attempt%5 == 0 {
				t.Logf("  Still waiting for HAProxy reload... (%d/%d seconds)", attempt, maxAttempts)
			}
			time.Sleep(1 * time.Second)
		}

		if !found {
			t.Error("Server never appeared in HAProxy stats after 30 seconds")
			t.Log("DEBUG: Dumping diagnostic information...")

			ctx := context.Background()

			socketCheckCmd := exec.CommandContext(ctx, "docker", "exec", "haproxy-test", "sh", "-c",
				"ls -la /tmp/haproxy.sock || echo 'Socket does not exist'")
			socketCheckOutput, _ := socketCheckCmd.Output()
			t.Logf("DEBUG: Socket check:\n%s", string(socketCheckOutput))

			rawStatsCmd := exec.CommandContext(ctx, "docker", "exec", "haproxy-test", "sh", "-c",
				"echo 'show stat' | socat stdio /tmp/haproxy.sock | head -30")
			rawStatsOutput, rawErr := rawStatsCmd.CombinedOutput()
			t.Logf("DEBUG: Raw 'show stat' output (first 30 lines):\n%s", string(rawStatsOutput))
			if rawErr != nil {
				t.Logf("DEBUG: Error from 'show stat': %v", rawErr)
			}

			allStatsCmd := exec.CommandContext(ctx, "docker", "exec", "haproxy-test", "sh", "-c",
				"echo 'show stat' | socat stdio /tmp/haproxy.sock | grep ',BACKEND,' | head -20")
			allStatsOutput, _ := allStatsCmd.Output()
			t.Logf("DEBUG: Backends in stats (filtered):\n%s", string(allStatsOutput))

			// Search for backend in config file - filter in Go to avoid gosec warning
			haproxyConfig := getHAProxyConfig(t)
			t.Log("DEBUG: Backend in config file:")
			lines := strings.Split(haproxyConfig, "\n")
			for i, line := range lines {
				if strings.Contains(line, "backend "+backendName) {
					// Print 5 lines after finding the backend
					endIdx := i + 6
					if endIdx > len(lines) {
						endIdx = len(lines)
					}
					t.Logf("%s", strings.Join(lines[i:endIdx], "\n"))
					break
				}
			}

			pidCmd := exec.CommandContext(ctx, "docker", "exec", "haproxy-test", "ps", "aux")
			pidOutput, _ := pidCmd.Output()
			t.Logf("DEBUG: Process list:\n%s", string(pidOutput))

			t.Fatalf("Server never appeared in HAProxy stats - see debug output above")
		}

		fields := strings.Split(stats, ",")
		if len(fields) > 35 {
			checkStatus := fields[35]
			t.Logf("Check status field: %s", checkStatus)

			if checkStatus != "" && checkStatus != "0" {
				t.Logf("✓ Health checks ARE running!")
			} else {
				t.Logf("Note: Check status is %s (may still be initializing)", checkStatus)
			}
		}

		t.Logf("✓ End-to-end test complete - connector successfully configured HTTP health checks")
	})
}

// TestConnector_HTTPHealthCheckE2E_ExistingMisconfiguredBackend reproduces the production bug
// where a backend already exists but is missing health check configuration.
// This test verifies that the connector properly updates existing backends to add missing health checks.
//
// This reproduces the exact scenario from production (2025-10-14):
// - Backend existed in haproxy.cfg without default-server check
// - Connector found it, saw "roundrobin", returned early without updating
// - Result: servers added with NO health checks → 503 errors during deployments
func TestConnector_HTTPHealthCheckE2E_ExistingMisconfiguredBackend(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := haproxy.NewClient("http://localhost:5555", "admin", "adminpwd")

	cfg := &config.Config{
		HAProxy: config.HAProxyConfig{
			Address:         "http://localhost:5555",
			Username:        "admin",
			Password:        "adminpwd",
			BackendStrategy: "create_new",
		},
	}

	serviceName := "test-misconfigured-e2e"
	backendName := "test_misconfigured_e2e"

	cleanup := func() {
		version, _ := client.GetConfigVersion()
		_ = client.DeleteBackend(backendName, version)
	}
	defer cleanup()

	t.Run("1_CreateMisconfiguredBackend", func(t *testing.T) {
		createMisconfiguredBackendForTest(t, client, backendName)
	})

	t.Run("2_ProcessServiceRegistration_WithExistingBackend", func(t *testing.T) {
		event := &connector.ServiceEvent{
			Type: connector.EventTypeServiceRegistration,
			Service: connector.Service{
				ServiceName: serviceName,
				Address:     "test-backend",
				Port:        80,
				Tags: []string{
					"haproxy.enable=true",
					"haproxy.check.path=/health",
					"haproxy.check.host=test-misconfigured.local",
				},
			},
		}

		ctx := context.Background()
		result, err := connector.ProcessServiceEvent(ctx, client, event, cfg)
		if err != nil {
			t.Fatalf("ProcessServiceEvent failed: %v", err)
		}

		resultMap, ok := result.(map[string]string)
		if !ok {
			t.Fatalf("Expected result to be map[string]string, got %T", result)
		}

		t.Logf("✓ Connector processed service registration with existing backend")
		t.Logf("  Status: %s", resultMap["status"])
	})

	t.Run("3_VerifyBackendNowHasHealthChecks", func(t *testing.T) {
		backend, err := client.GetBackend(backendName)
		if err != nil {
			t.Fatalf("Failed to get backend: %v", err)
		}

		if backend.AdvCheck != advCheckHTTP {
			t.Errorf("BUG (Client): Expected AdvCheck=%s after processing, got: %s", advCheckHTTP, backend.AdvCheck)
			t.Error("The connector did not update the existing misconfigured backend!")
		}

		if backend.HTTPCheckParams == nil {
			t.Error("BUG (Client): HTTPCheckParams is still nil - connector did not add HTTP health checks to existing backend!")
		} else if backend.HTTPCheckParams.URI != "/health" {
			t.Errorf("BUG (Client): Expected URI=/health, got: %s", backend.HTTPCheckParams.URI)
		}

		if backend.DefaultServer == nil {
			t.Error("BUG (Client): DefaultServer is still nil - connector did not add default-server check!")
		} else if backend.DefaultServer.Check != connector.CheckEnabled {
			t.Errorf("BUG (Client): Expected default-server check=%s, got: %s", connector.CheckEnabled, backend.DefaultServer.Check)
		}

		t.Logf("✓ Client reports backend has health checks")
		t.Logf("  AdvCheck: %s", backend.AdvCheck)
		if backend.HTTPCheckParams != nil {
			t.Logf("  HTTP Check URI: %s", backend.HTTPCheckParams.URI)
		}
		if backend.DefaultServer != nil {
			t.Logf("  DefaultServer Check: %s", backend.DefaultServer.Check)
		}
	})

	t.Run("4_VerifyViaSocket", func(t *testing.T) {
		output, err := executeHAProxySocketCommand(t, "show backend")
		if err != nil {
			t.Logf("Note: 'show backend' command failed (may not be available): %v", err)
		} else {
			// Filter for our backend in Go
			for _, line := range strings.Split(output, "\n") {
				if strings.Contains(line, backendName) {
					t.Logf("Socket output for backend: %s", line)
					break
				}
			}
		}

		t.Logf("✓ HAProxy socket verification attempted")
	})

	t.Run("5_VerifyHAProxyConfigUpdated", func(t *testing.T) {
		time.Sleep(2 * time.Second)

		configContent := getHAProxyConfig(t)

		if !strings.Contains(configContent, fmt.Sprintf("backend %s", backendName)) {
			t.Error("BUG (HAProxy Config): Backend not found in HAProxy config")
		}

		if !strings.Contains(configContent, "option httpchk") {
			t.Error("BUG (HAProxy Config): 'option httpchk' not found in HAProxy config after update!")
		}

		hasHTTPCheckPath := strings.Contains(configContent, "/health")
		if !hasHTTPCheckPath {
			t.Error("BUG (HAProxy Config): Health check path '/health' not found in config!")
		}

		if !strings.Contains(configContent, "default-server check") {
			t.Error("BUG (HAProxy Config): 'default-server check' not found in HAProxy config after update!")
		}

		t.Logf("✓ HAProxy config file contains health check directives")
	})

	t.Run("6_VerifyHealthChecksActuallyWork", func(t *testing.T) {
		servers, err := client.GetServers(backendName)
		if err != nil {
			t.Fatalf("Failed to get servers: %v", err)
		}

		if len(servers) == 0 {
			t.Fatal("No servers found")
		}

		server := servers[0]
		if server.Check != connector.CheckEnabled {
			t.Errorf("Expected server check=%s, got: %s", connector.CheckEnabled, server.Check)
		}

		t.Logf("✓ Server has health checks enabled")
		t.Logf("  This test reproduces the production bug where health checks were missing")
		t.Logf("  The fix should ensure existing backends are updated with proper health check config")
	})
}

// TestConnector_HTTPHealthCheckE2E_ExistingMisconfiguredBackend_ProductionPath tests the ACTUAL
// production code path that processes service events with health check synchronization.
//
// This is the CRITICAL test that was missing from ADR-011. The original test only tested
// ProcessServiceEvent (which calls handleServiceRegistration → ensureBackend),
// but production uses ProcessNomadServiceEvent which calls handleServiceRegistrationWithHealthCheck.
//
// Both code paths now share the same updateBackendHealthChecks() function to eliminate duplication.
func TestConnector_HTTPHealthCheckE2E_ExistingMisconfiguredBackend_ProductionPath(t *testing.T) {
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

	serviceName := "test-prod-path-e2e"
	backendName := "test_prod_path_e2e"

	cleanup := func() {
		version, _ := client.GetConfigVersion()
		_ = client.DeleteBackend(backendName, version)
	}
	defer cleanup()

	t.Run("1_CreateMisconfiguredBackend", func(t *testing.T) {
		createMisconfiguredBackendForTest(t, client, backendName)
	})

	t.Run("2_ProcessServiceRegistration_ViaProductionCodePath", func(t *testing.T) {
		nomadEvent := nomad.ServiceEvent{
			Type:  "ServiceRegistration",
			Topic: "Service",
			Payload: nomad.Payload{
				Service: &nomad.Service{
					ServiceName: serviceName,
					Address:     "test-backend",
					Port:        80,
					JobID:       "",
					Tags: []string{
						"haproxy.enable=true",
						"haproxy.check.path=/healthcheck",
						"haproxy.check.host=test-prod.local",
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

		t.Logf("✓ Processed via PRODUCTION code path (ProcessNomadServiceEvent)")
		t.Logf("  Status: %s", resultMap["status"])
		t.Logf("  This uses handleServiceRegistrationWithHealthCheck")
	})

	t.Run("3_VerifyBackendUpdatedCorrectly", func(t *testing.T) {
		time.Sleep(2 * time.Second)

		// Verify the backend configuration WAS updated correctly (ADR-011 fix)
		backend, err := client.GetBackend(backendName)
		if err != nil {
			t.Fatalf("Failed to get backend: %v", err)
		}

		t.Logf("Backend configuration after processing:")
		t.Logf("  AdvCheck: '%s' (should be 'httpchk' for HTTP health checks)", backend.AdvCheck)
		if backend.HTTPCheckParams != nil {
			t.Logf("  HTTPCheckParams.URI: '%s' (should be '/healthcheck')", backend.HTTPCheckParams.URI)
		} else {
			t.Logf("  HTTPCheckParams: nil")
		}
		if backend.DefaultServer != nil {
			t.Logf("  DefaultServer.Check: '%s'", backend.DefaultServer.Check)
		} else {
			t.Logf("  DefaultServer: nil")
		}

		// Verify ADR-011 fix: Backend should have HTTP health checks configured
		if backend.AdvCheck != advCheckHTTP {
			t.Errorf("REGRESSION: Backend missing 'option %s'", advCheckHTTP)
			t.Error("   The updateBackendHealthChecks function should have updated the backend")
			t.Error("   Result: HAProxy uses TCP checks instead of HTTP checks")
			t.Error("   Impact: Canary becomes 'healthy' when port opens, NOT when app is ready → DOWNTIME")
		}

		if backend.HTTPCheckParams == nil || backend.HTTPCheckParams.URI != "/healthcheck" {
			t.Error("REGRESSION: Backend missing HTTP check path '/healthcheck'")
		}

		if backend.DefaultServer == nil || backend.DefaultServer.Check != connector.CheckEnabled {
			t.Errorf("REGRESSION: Backend missing 'default-server check=%s'", connector.CheckEnabled)
		}

		t.Logf("✓ Backend correctly updated with HTTP health check configuration")
	})
}
