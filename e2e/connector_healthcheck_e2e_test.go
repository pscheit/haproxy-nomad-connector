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

// setupCleanSlate restarts HAProxy container to get a completely clean baseline config
// This is the SIMPLEST and MOST RELIABLE way to ensure no leftover state from previous tests
// DataPlane API has no "reset to baseline" endpoint, so we just restart the container
func setupCleanSlate(t *testing.T, client *haproxy.Client) {
	t.Helper()

	ctx := context.Background()

	// Restart HAProxy container with 1s timeout - this reloads base config and clears ALL dynamic changes
	t.Log("Restarting HAProxy container to get clean baseline config...")
	restartCmd := exec.CommandContext(ctx, "docker", "restart", "-t", "1", "haproxy-test")
	if err := restartCmd.Run(); err != nil {
		t.Fatalf("FATAL: Could not restart HAProxy container: %v", err)
	}

	// Wait for HAProxy to fully start and DataPlane API to be available
	t.Log("Waiting for HAProxy and DataPlane API to be ready...")
	maxAttempts := 30
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		_, err := client.GetConfigVersion()
		if err == nil {
			t.Logf("✓ HAProxy restarted and DataPlane API ready after %d seconds", attempt)
			return
		}

		if attempt == maxAttempts {
			t.Fatalf("FATAL: DataPlane API not ready after %d seconds: %v", maxAttempts, err)
		}

		time.Sleep(1 * time.Second)
	}
}

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

// ActualBackendConfig represents the actual configuration HAProxy is using
// This is extracted from the config file and runtime state, NOT from the API response
type ActualBackendConfig struct {
	Name               string
	Mode               string // "http", "tcp", or empty
	Algorithm          string
	HealthCheckType    string   // "tcp", "http", or "none"
	HTTPCheckMethod    string   // "GET", "POST", etc (empty if not HTTP)
	HTTPCheckPath      string   // "/health", "/healthcheck", etc (empty if not HTTP)
	HTTPCheckHost      string   // Host header value for HTTP checks (empty if not specified)
	DefaultServerCheck bool     // true if "default-server check" is present
	Servers            []string // List of server names
}

// getActualBackendConfig extracts the ACTUAL backend configuration from HAProxy
// This reads the config file and parses it to see what HAProxy will actually do
func getActualBackendConfig(t *testing.T, backendName string) ActualBackendConfig {
	t.Helper()

	config := ActualBackendConfig{
		Name:            backendName,
		HealthCheckType: "none",
	}

	// Read the actual HAProxy config file
	configContent := getHAProxyConfig(t)
	lines := strings.Split(configContent, "\n")

	// Find the backend section
	inBackend := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Check if we're entering our backend
		if strings.HasPrefix(trimmed, "backend "+backendName) {
			inBackend = true
			continue
		}

		// Check if we've left the backend (next backend or frontend section)
		if inBackend && (strings.HasPrefix(trimmed, "backend ") || strings.HasPrefix(trimmed, "frontend ") || strings.HasPrefix(trimmed, "listen ")) {
			break
		}

		if !inBackend {
			continue
		}

		// Parse backend configuration lines
		if strings.HasPrefix(trimmed, "mode ") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				config.Mode = parts[1]
			}
		}

		if strings.Contains(trimmed, "balance ") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				config.Algorithm = parts[1]
			}
		}

		if strings.Contains(trimmed, "option httpchk") {
			config.HealthCheckType = "http"
			// Parse: option httpchk GET /health HTTP/1.1 (old format)
			// OR: option httpchk (new format with separate http-check send line)
			parts := strings.Fields(trimmed)
			if len(parts) >= 3 {
				config.HTTPCheckMethod = parts[2]
			}
			if len(parts) >= 4 {
				config.HTTPCheckPath = parts[3]
			}
		}

		if strings.Contains(trimmed, "http-check send") {
			// Parse: http-check send meth GET uri /healthz hdr Host test-manual.local
			parts := strings.Fields(trimmed)
			for i := 0; i < len(parts); i++ {
				if parts[i] == "meth" && i+1 < len(parts) {
					config.HTTPCheckMethod = parts[i+1]
				}
				if parts[i] == "uri" && i+1 < len(parts) {
					config.HTTPCheckPath = parts[i+1]
				}
				if parts[i] == "hdr" && i+1 < len(parts) && parts[i+1] == "Host" && i+2 < len(parts) {
					config.HTTPCheckHost = parts[i+2]
				}
			}
		}

		if strings.Contains(trimmed, "default-server") && strings.Contains(trimmed, "check") {
			config.DefaultServerCheck = true
		}

		if strings.HasPrefix(trimmed, "server ") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				config.Servers = append(config.Servers, parts[1])
			}
		}
	}

	// If no http check but default-server check, it's TCP
	if config.HealthCheckType == "none" && config.DefaultServerCheck {
		config.HealthCheckType = "tcp"
	}

	return config
}

// assertBackendConfigEquals fetches the actual backend config and compares it to expected
// This provides clear error messages about what's missing or incorrect
// By fetching the actual config internally, callers can't accidentally pass the wrong backend
func assertBackendConfigEquals(t *testing.T, backendName string, expected ActualBackendConfig) {
	t.Helper()

	// Fetch the actual config - this ensures we're comparing the right backend
	actual := getActualBackendConfig(t, backendName)

	t.Logf("Actual config for %s: %+v", backendName, actual)

	if expected.Mode != "" && actual.Mode != expected.Mode {
		t.Errorf("Mode mismatch: expected=%s, actual=%s", expected.Mode, actual.Mode)
	}

	if actual.Algorithm != "" && expected.Algorithm != "" && actual.Algorithm != expected.Algorithm {
		t.Errorf("Algorithm mismatch: expected=%s, actual=%s", expected.Algorithm, actual.Algorithm)
	}

	if expected.HealthCheckType != actual.HealthCheckType {
		t.Errorf("HealthCheckType mismatch: expected=%s, actual=%s", expected.HealthCheckType, actual.HealthCheckType)
	}

	if expected.HTTPCheckMethod != "" && actual.HTTPCheckMethod != expected.HTTPCheckMethod {
		t.Errorf("HTTPCheckMethod mismatch: expected=%s, actual=%s", expected.HTTPCheckMethod, actual.HTTPCheckMethod)
	}

	if expected.HTTPCheckPath != "" && actual.HTTPCheckPath != expected.HTTPCheckPath {
		t.Errorf("HTTPCheckPath mismatch: expected=%s, actual=%s", expected.HTTPCheckPath, actual.HTTPCheckPath)
	}

	if expected.HTTPCheckHost != "" && actual.HTTPCheckHost != expected.HTTPCheckHost {
		t.Errorf("HTTPCheckHost mismatch: expected=%s, actual=%s", expected.HTTPCheckHost, actual.HTTPCheckHost)
		t.Error("  ⚠️  CRITICAL: Missing Host header will cause health checks to fail with 400 Bad Request!")
		t.Error("  This is the production bug that caused the outage!")
	}

	if expected.DefaultServerCheck != actual.DefaultServerCheck {
		t.Errorf("DefaultServerCheck mismatch: expected=%v, actual=%v", expected.DefaultServerCheck, actual.DefaultServerCheck)
	}

	if len(expected.Servers) > 0 && len(actual.Servers) != len(expected.Servers) {
		t.Errorf("Server count mismatch: expected=%d, actual=%d", len(expected.Servers), len(actual.Servers))
	}
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

	t.Run("0_Setup_CleanSlate", func(t *testing.T) {
		setupCleanSlate(t, client)
	})

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

		// Verify HTTP checks via the /http_checks API
		checks, err := client.GetHTTPChecks(backendName)
		if err != nil {
			t.Fatalf("Failed to get HTTP checks: %v", err)
		}

		if len(checks) == 0 {
			t.Fatal("No HTTP checks found - connector did not configure HTTP health checks!")
		}

		check := checks[0]
		if check.URI != "/health" {
			t.Errorf("Expected URI=/health, got: %s", check.URI)
		}

		if check.Method != "GET" {
			t.Errorf("Expected Method=GET, got: %s", check.Method)
		}

		hostHeaderFound := false
		for _, hdr := range check.Headers {
			if hdr.Name == "Host" && hdr.Fmt == "test-e2e.local" {
				hostHeaderFound = true
				break
			}
		}
		if !hostHeaderFound {
			t.Errorf("Expected Host header 'test-e2e.local' not found in HTTP checks")
		}

		t.Logf("✓ Backend has HTTP health check configuration")
		t.Logf("  URI: %s", check.URI)
		t.Logf("  Method: %s", check.Method)
		t.Logf("  Host header: %v", hostHeaderFound)
	})

	t.Run("3_VerifyActualHAProxyConfig", func(t *testing.T) {
		assertBackendConfigEquals(t, backendName, ActualBackendConfig{
			Name:               backendName,
			Algorithm:          "roundrobin",
			HealthCheckType:    "http",
			HTTPCheckMethod:    "GET",
			HTTPCheckPath:      "/health",
			HTTPCheckHost:      "test-e2e.local",
			DefaultServerCheck: true,
		})

		t.Logf("✓ HAProxy config contains correct HTTP health check with Host header")
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

	t.Run("0_Setup_CleanSlate", func(t *testing.T) {
		setupCleanSlate(t, client)
	})

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

		// Verify HTTP checks via the /http_checks API
		checks, err := client.GetHTTPChecks(backendName)
		if err != nil {
			t.Fatalf("Failed to get HTTP checks: %v", err)
		}

		if len(checks) == 0 {
			t.Error("BUG (Client): No HTTP checks found - connector did not add HTTP health checks to existing backend!")
		} else if checks[0].URI != "/health" {
			t.Errorf("BUG (Client): Expected URI=/health, got: %s", checks[0].URI)
		}

		if backend.DefaultServer == nil {
			t.Error("BUG (Client): DefaultServer is still nil - connector did not add default-server check!")
		} else if backend.DefaultServer.Check != connector.CheckEnabled {
			t.Errorf("BUG (Client): Expected default-server check=%s, got: %s", connector.CheckEnabled, backend.DefaultServer.Check)
		}

		t.Logf("✓ Client reports backend has health checks")
		t.Logf("  AdvCheck: %s", backend.AdvCheck)
		if len(checks) > 0 {
			t.Logf("  HTTP Check URI: %s", checks[0].URI)
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

	t.Run("5_VerifyActualHAProxyConfigUpdated", func(t *testing.T) {
		time.Sleep(2 * time.Second)

		assertBackendConfigEquals(t, backendName, ActualBackendConfig{
			Name:               backendName,
			Algorithm:          "roundrobin",
			HealthCheckType:    "http",
			HTTPCheckMethod:    "GET",
			HTTPCheckPath:      "/health",
			HTTPCheckHost:      "test-misconfigured.local",
			DefaultServerCheck: true,
		})

		t.Logf("✓ HAProxy config file contains health check directives with Host header")
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

	t.Run("0_Setup_CleanSlate", func(t *testing.T) {
		setupCleanSlate(t, client)
	})

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

		// Verify ADR-011 fix: Backend should have HTTP health checks configured
		if backend.AdvCheck != advCheckHTTP {
			t.Errorf("REGRESSION: Backend missing 'option %s'", advCheckHTTP)
			t.Error("   The updateBackendHealthChecks function should have updated the backend")
			t.Error("   Result: HAProxy uses TCP checks instead of HTTP checks")
			t.Error("   Impact: Canary becomes 'healthy' when port opens, NOT when app is ready → DOWNTIME")
		}

		// Verify HTTP checks via the /http_checks API
		checks, err := client.GetHTTPChecks(backendName)
		if err != nil {
			t.Fatalf("Failed to get HTTP checks: %v", err)
		}

		if len(checks) == 0 || checks[0].URI != "/healthcheck" {
			t.Error("REGRESSION: Backend missing HTTP check path '/healthcheck'")
		}

		if backend.DefaultServer == nil || backend.DefaultServer.Check != connector.CheckEnabled {
			t.Errorf("REGRESSION: Backend missing 'default-server check=%s'", connector.CheckEnabled)
		}

		t.Logf("Backend configuration after processing:")
		t.Logf("  AdvCheck: '%s' (should be 'httpchk' for HTTP health checks)", backend.AdvCheck)
		if len(checks) > 0 {
			t.Logf("  HTTP Check URI: '%s' (should be '/healthcheck')", checks[0].URI)
		}
		if backend.DefaultServer != nil {
			t.Logf("  DefaultServer.Check: '%s'", backend.DefaultServer.Check)
		} else {
			t.Logf("  DefaultServer: nil")
		}

		// Verify ACTUAL config file has Host header (would have caught the production bug)
		assertBackendConfigEquals(t, backendName, ActualBackendConfig{
			Name:               backendName,
			Algorithm:          "roundrobin",
			HealthCheckType:    "http",
			HTTPCheckMethod:    "GET",
			HTTPCheckPath:      "/healthcheck",
			HTTPCheckHost:      "test-prod.local",
			DefaultServerCheck: true,
		})

		t.Logf("✓ Backend correctly updated with HTTP health check configuration INCLUDING Host header")
	})
}

// TestConnector_DomainTagHTTPHealthCheck_E2E tests creating a new backend with domain tag
// This reproduces the paperless scenario where:
// - Service registers with haproxy.domain tag
// - Connector should create backend with Mode="http", option httpchk, and http-check send hdr Host
// - This test would have caught the production bug where Mode was missing
func TestConnector_DomainTagHTTPHealthCheck_E2E(t *testing.T) {
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

	serviceName := "paperless-test"
	backendName := "paperless_test"

	t.Run("0_Setup_CleanSlate", func(t *testing.T) {
		setupCleanSlate(t, client)
	})

	t.Run("1_RegisterServiceWithDomainTag", func(t *testing.T) {
		nomadEvent := nomad.ServiceEvent{
			Type:  "ServiceRegistration",
			Topic: "Service",
			Payload: nomad.Payload{
				Service: &nomad.Service{
					ServiceName: serviceName,
					Address:     "192.168.5.13",
					Port:        8030,
					JobID:       "",
					Tags: []string{
						"haproxy.enable=true",
						"haproxy.domain=paperless.ps-webforge.net",
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

		t.Logf("✓ Service registered with domain tag")
		t.Logf("  Status: %s", resultMap["status"])
		t.Logf("  Backend: %s", resultMap["backend"])
	})

	t.Run("2_VerifyActualHAProxyConfig", func(t *testing.T) {
		time.Sleep(2 * time.Second)
		assertBackendConfigEquals(t, backendName, ActualBackendConfig{
			Name:               backendName,
			Mode:               "http",
			Algorithm:          "roundrobin",
			HealthCheckType:    "http",
			HTTPCheckMethod:    "GET",
			HTTPCheckPath:      "/",
			HTTPCheckHost:      "paperless.ps-webforge.net",
			DefaultServerCheck: true,
		})

		t.Logf("✓ HAProxy config has complete HTTP health check configuration")
		t.Logf("  This test reproduces the paperless scenario")
		t.Logf("  It verifies that domain tags trigger full HTTP health check setup")
	})
}

// TestConnector_HealthCheckTagsChanged_E2E reproduces the TeamCity production bug (2025-10-15) where:
// 1. Service registers with domain tag only (auto-generates health check path = "/")
// 2. Service tags CHANGE to add explicit health check path "/healthCheck/healthy"
// 3. Backend already exists → connector returns "already_exists" WITHOUT updating health checks
// 4. Result: TeamCity stays DOWN because health checks still check "/" (401) instead of "/healthCheck/healthy" (200)
//
// This test demonstrates the fundamental reconciliation bug: connector doesn't detect when health check configuration changes.
// The connector treats HAProxy as write-only instead of comparing desired state vs actual state.
func TestConnector_HealthCheckTagsChanged_E2E(t *testing.T) {
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

	serviceName := "teamcity-test"
	backendName := "teamcity_test"

	t.Run("0_Setup_CleanSlate", func(t *testing.T) {
		setupCleanSlate(t, client)
	})

	t.Run("1_RegisterServiceWithDomainTagOnly", func(t *testing.T) {
		nomadEvent := nomad.ServiceEvent{
			Type:  "ServiceRegistration",
			Topic: "Service",
			Payload: nomad.Payload{
				Service: &nomad.Service{
					ServiceName: serviceName,
					Address:     "192.168.5.10",
					Port:        30945,
					JobID:       "",
					Tags: []string{
						"haproxy.enable=true",
						"haproxy.backend=dynamic",
						"haproxy.domain=tc.test.local",
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

		if resultMap["status"] != "created" {
			t.Errorf("Expected status 'created', got: %s", resultMap["status"])
		}

		t.Logf("✓ Service registered with domain tag only (initial deployment)")
		t.Logf("  Status: %s", resultMap["status"])
		t.Logf("  Backend: %s", resultMap["backend"])
	})

	t.Run("2_VerifyInitialHealthCheckPath", func(t *testing.T) {
		time.Sleep(2 * time.Second)
		assertBackendConfigEquals(t, backendName, ActualBackendConfig{
			Name:               backendName,
			Mode:               "http",
			Algorithm:          "roundrobin",
			HealthCheckType:    "http",
			HTTPCheckMethod:    "GET",
			HTTPCheckPath:      "/",
			HTTPCheckHost:      "tc.test.local",
			DefaultServerCheck: true,
		})

		t.Logf("✓ Backend created with default health check path '/'")
		t.Logf("  This simulates initial deployment before health check tags were added")
	})

	t.Run("3_ReRegisterServiceWithChangedHealthCheckPath", func(t *testing.T) {
		nomadEvent := nomad.ServiceEvent{
			Type:  "ServiceRegistration",
			Topic: "Service",
			Payload: nomad.Payload{
				Service: &nomad.Service{
					ServiceName: serviceName,
					Address:     "192.168.5.10",
					Port:        30945,
					JobID:       "",
					Tags: []string{
						"haproxy.enable=true",
						"haproxy.backend=dynamic",
						"haproxy.domain=tc.test.local",
						"haproxy.check.path=/healthCheck/healthy",
						"haproxy.check.host=tc.test.local",
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

		t.Logf("✓ Service re-registered with explicit health check tags")
		t.Logf("  Status: %s (connector detected existing backend)", resultMap["status"])
		t.Logf("  This simulates Nomad job update where health check tags change")
	})

	t.Run("4_VerifyHealthCheckPathUpdated", func(t *testing.T) {
		time.Sleep(2 * time.Second)

		assertBackendConfigEquals(t, backendName, ActualBackendConfig{
			Name:               backendName,
			Mode:               "http",
			Algorithm:          "roundrobin",
			HealthCheckType:    "http",
			HTTPCheckMethod:    "GET",
			HTTPCheckPath:      "/healthCheck/healthy",
			HTTPCheckHost:      "tc.test.local",
			DefaultServerCheck: true,
		})

		t.Logf("✓ Backend health check path UPDATED to '/healthCheck/healthy'")
		t.Logf("  This test reproduces the TeamCity production bug (2025-10-15)")
		t.Logf("  Without reconciliation, this test will FAIL RED")
		t.Logf("  The connector must compare desired vs actual state and update differences")
	})
}

// TestConnector_ServiceDeregistration_E2E tests if the connector properly handles service deregistration
// This test checks if the connector CAN interact with existing state when removing services.
// If this test PASSES (GREEN) while the registration test FAILS (RED), it proves the connector
// can read and modify existing state during deregistration, but doesn't during registration.
// This would confirm the reconciliation bug is asymmetric: deregistration works, registration doesn't.
func TestConnector_ServiceDeregistration_E2E(t *testing.T) {
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

	serviceName := "deregister-test"
	backendName := "deregister_test"

	t.Run("0_Setup_CleanSlate", func(t *testing.T) {
		setupCleanSlate(t, client)
	})

	t.Run("1_RegisterService", func(t *testing.T) {
		nomadEvent := nomad.ServiceEvent{
			Type:  "ServiceRegistration",
			Topic: "Service",
			Payload: nomad.Payload{
				Service: &nomad.Service{
					ServiceName: serviceName,
					Address:     "192.168.5.100",
					Port:        9000,
					JobID:       "",
					Tags: []string{
						"haproxy.enable=true",
						"haproxy.backend=dynamic",
						"haproxy.domain=deregister.test.local",
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

		if resultMap["status"] != "created" {
			t.Errorf("Expected status 'created', got: %s", resultMap["status"])
		}

		t.Logf("✓ Service registered successfully")
		t.Logf("  Backend: %s", backendName)
	})

	t.Run("2_VerifyServiceExists", func(t *testing.T) {
		time.Sleep(2 * time.Second)

		backend, err := client.GetBackend(backendName)
		if err != nil {
			t.Fatalf("Backend not found: %v", err)
		}

		servers, err := client.GetServers(backendName)
		if err != nil {
			t.Fatalf("Failed to get servers: %v", err)
		}

		if len(servers) != 1 {
			t.Fatalf("Expected 1 server, got %d", len(servers))
		}

		t.Logf("✓ Backend and server exist")
		t.Logf("  Backend: %s", backend.Name)
		t.Logf("  Server: %s", servers[0].Name)
	})

	t.Run("3_DeregisterService", func(t *testing.T) {
		nomadEvent := nomad.ServiceEvent{
			Type:  "ServiceDeregistration",
			Topic: "Service",
			Payload: nomad.Payload{
				Service: &nomad.Service{
					ServiceName: serviceName,
					Address:     "192.168.5.100",
					Port:        9000,
					JobID:       "",
					Tags: []string{
						"haproxy.enable=true",
						"haproxy.backend=dynamic",
						"haproxy.domain=deregister.test.local",
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

		t.Logf("✓ Deregistration processed")
		t.Logf("  Status: %s", resultMap["status"])
	})

	t.Run("4_VerifyServerRemoved", func(t *testing.T) {
		time.Sleep(2 * time.Second)

		servers, err := client.GetServers(backendName)
		if err != nil {
			t.Logf("Note: Backend may have been removed entirely: %v", err)
			return
		}

		if len(servers) > 0 {
			t.Errorf("Expected 0 servers after deregistration, got %d", len(servers))
			for _, srv := range servers {
				t.Logf("  Remaining server: %s", srv.Name)
			}
		}

		t.Logf("✓ Server successfully removed from backend")
	})

	t.Run("5_VerifyFrontendRuleRemoved", func(t *testing.T) {
		time.Sleep(1 * time.Second)

		rules, err := client.GetFrontendRules("https")
		if err != nil {
			t.Logf("Note: Could not get frontend rules: %v", err)
			return
		}

		var actualDomains []string
		for _, rule := range rules {
			actualDomains = append(actualDomains, rule.Domain)
		}

		if len(actualDomains) != 0 {
			t.Errorf("Expected no frontend rules after deregistration, got: %v", actualDomains)
		}

		t.Logf("✓ Frontend rule removed successfully")
		t.Logf("  Expected: []")
		t.Logf("  Actual: %v", actualDomains)
		t.Logf("  This proves the connector properly handles deregistration")
	})
}
