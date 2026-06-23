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

// TestConnector_InPlaceRestart_SamePortReregister_SurvivesDrain reproduces the
// 2026-06-23 outage (lb1 ADR-011): an in-place task restart (Vault secret change /
// `template { change_mode = "restart" }`) re-registers the SAME address:port while a
// deregistration's graceful-drain timer is still pending. The drain then deletes the
// freshly re-registered server, leaving the backend EMPTY → site down.
//
// Expected after the fix: a re-registration cancels the pending drain/delete for that
// server AND brings it back to "ready", so the same-port dereg→rereg is a no-op.
func TestConnector_InPlaceRestart_SamePortReregister_SurvivesDrain(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := haproxy.NewClient("http://localhost:5555", "admin", "adminpwd")
	logger := log.New(os.Stderr, "[test] ", log.LstdFlags)

	// Short drain so the test runs fast; production default is 10s.
	cfg := &config.Config{
		HAProxy: config.HAProxyConfig{
			Address:         "http://localhost:5555",
			Username:        "admin",
			Password:        "adminpwd",
			BackendStrategy: "create_new",
			Frontend:        "https",
			DrainTimeoutSec: 2,
		},
	}

	const (
		serviceName = "reregister-race"
		backendName = "reregister_race"
	)

	// A real, reachable backend (the docker-compose test-backend) so the surviving
	// server can actually be ready/serving, not just present in config.
	svc := &nomad.Service{
		ServiceName: serviceName,
		Address:     "test-backend",
		Port:        80,
		Tags:        []string{"haproxy.enable=true"},
	}
	reg := nomad.ServiceEvent{Type: "ServiceRegistration", Topic: "Service", Payload: nomad.Payload{Service: svc}}
	dereg := nomad.ServiceEvent{Type: "ServiceDeregistration", Topic: "Service", Payload: nomad.Payload{Service: svc}}

	ctx := context.Background()

	setupCleanSlate(t, client)

	// 1. Initial registration — server is added and serving.
	if _, err := connector.ProcessNomadServiceEvent(ctx, client, nil, reg, logger, cfg); err != nil {
		t.Fatalf("initial registration failed: %v", err)
	}
	servers, err := client.GetServers(backendName)
	if err != nil {
		t.Fatalf("GetServers after initial registration failed: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server after initial registration, got %d", len(servers))
	}
	serverName := servers[0].Name

	// CreateServer is a config change → DataPlane reloads; the runtime server only
	// exists a moment later. Wait for it so the deregistration takes the graceful-drain
	// path (DrainServer needs a runtime server), matching the production scenario of an
	// established, long-running allocation.
	waitForRuntimeServer(t, client, backendName, serverName)

	// 2. Deregistration arms the graceful-drain timer (delete in DrainTimeoutSec).
	//    Assert we actually hit the drain path — otherwise the race isn't reproduced.
	deregResult, err := connector.ProcessNomadServiceEvent(ctx, client, nil, dereg, logger, cfg)
	if err != nil {
		t.Fatalf("deregistration failed: %v", err)
	}
	if status := statusOf(deregResult); status != connector.StatusDraining {
		t.Fatalf("deregistration status = %q, want %q — server was not gracefully drained, "+
			"so the delayed-delete race is not being exercised", status, connector.StatusDraining)
	}

	// 3. ~1s later the in-place restart re-registers the SAME address:port,
	//    while the drain timer from step 2 is still pending.
	time.Sleep(1 * time.Second)
	if _, err = connector.ProcessNomadServiceEvent(ctx, client, nil, reg, logger, cfg); err != nil {
		t.Fatalf("re-registration failed: %v", err)
	}

	// 4. Wait past the drain window so the (now hopefully canceled) delete would fire.
	time.Sleep(time.Duration(cfg.HAProxy.DrainTimeoutSec+2) * time.Second)

	// 5a. The server must still exist — today the drain deletes it → empty backend.
	servers, err = client.GetServers(backendName)
	if err != nil {
		t.Fatalf("GetServers after drain window failed: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("REGRESSION (lb1 ADR-011): backend %q has %d servers after same-port dereg→rereg, "+
			"want 1 — the drain deleted the re-registered server (empty backend = outage)", backendName, len(servers))
	}

	// 5b. ...and it must be serving, not stuck in drain from step 2.
	rt, err := client.GetRuntimeServer(backendName, serverName)
	if err != nil {
		t.Fatalf("GetRuntimeServer for surviving server %q failed: %v", serverName, err)
	}
	if rt.AdminState != "ready" {
		t.Fatalf("REGRESSION (lb1 ADR-011): surviving server %q admin_state=%q, want \"ready\" — "+
			"re-registration must take it out of drain or the backend serves nothing", serverName, rt.AdminState)
	}
}

// waitForRuntimeServer blocks until the runtime server is present (the DataPlane reload
// after CreateServer has applied), so a subsequent DrainServer takes effect.
func waitForRuntimeServer(t *testing.T, client *haproxy.Client, backendName, serverName string) {
	t.Helper()
	for attempt := 1; attempt <= 30; attempt++ {
		if _, err := client.GetRuntimeServer(backendName, serverName); err == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("runtime server %s/%s never became available", backendName, serverName)
}

// statusOf extracts the "status" field from a ProcessNomadServiceEvent result.
func statusOf(result interface{}) string {
	if m, ok := result.(map[string]string); ok {
		return m["status"]
	}
	return ""
}
