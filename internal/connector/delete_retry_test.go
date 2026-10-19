package connector

import (
	"context"
	"testing"
	"time"

	"github.com/pscheit/haproxy-nomad-connector/internal/config"
	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
	"github.com/pscheit/haproxy-nomad-connector/internal/nomad"
)

const staleServerName = "web_app_10_0_0_2_8080"

// concurrentWriterClient simulates another config write (e.g. a parallel delayed
// delete) between GetConfigVersion and DeleteServer, as on lb1 2026-10-19.
type concurrentWriterClient struct {
	*MockHAProxyClient
	foreignWrites int
}

func (c *concurrentWriterClient) DeleteServer(backendName, serverName string, version int) error {
	c.mu.Lock()
	if c.foreignWrites > 0 {
		c.foreignWrites--
		c.version++
	}
	c.mu.Unlock()
	return c.MockHAProxyClient.DeleteServer(backendName, serverName, version)
}

func (c *concurrentWriterClient) remainingForeignWrites() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.foreignWrites
}

func newConcurrentWriterClient(foreignWrites int) *concurrentWriterClient {
	client := &concurrentWriterClient{MockHAProxyClient: NewMockHAProxyClient(), foreignWrites: foreignWrites}
	client.backends["web_app"] = &haproxy.Backend{Name: "web_app"}
	client.servers["web_app"] = []haproxy.Server{
		{Name: staleServerName, Address: "10.0.0.2", Port: 8080},
	}
	return client
}

func webAppServices() []*nomad.Service {
	return []*nomad.Service{
		{ServiceName: "web-app", Address: "10.0.0.1", Port: 8080, Tags: []string{"haproxy.enable=true"}},
	}
}

func waitForServerCount(t *testing.T, client haproxy.ClientInterface, backendName string, expected int) []haproxy.Server {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		servers, _ := client.GetServers(backendName)
		if len(servers) == expected || time.Now().After(deadline) {
			return servers
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDelayedServerRemoval_RetriesAfterVersionConflict(t *testing.T) {
	client := newConcurrentWriterClient(1)

	err := drainAndRemoveServer(client, "web_app", staleServerName, 0, testLogger(), map[string]string{})
	if err != nil {
		t.Fatalf("Expected drain to start, got: %v", err)
	}

	if servers := waitForServerCount(t, client, "web_app", 0); len(servers) != 0 {
		t.Fatalf("Expected server to be removed after retry, still present: %v", servers)
	}
}

func TestDelayedServerRemoval_GivesUpAfterMaxAttempts(t *testing.T) {
	client := newConcurrentWriterClient(100)

	err := drainAndRemoveServer(client, "web_app", staleServerName, 0, testLogger(), map[string]string{})
	if err != nil {
		t.Fatalf("Expected drain to start, got: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for client.remainingForeignWrites() > 100-maxDeleteAttempts && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)

	if remaining := client.remainingForeignWrites(); remaining != 100-maxDeleteAttempts {
		t.Errorf("Expected exactly %d delete attempts, got %d", maxDeleteAttempts, 100-remaining)
	}
}

func TestDrainAndRemoveServer_FallbackDeleteOfMissingServerSucceeds(t *testing.T) {
	client := &mockHAProxyClient{
		drainError:  &haproxy.APIError{StatusCode: 404},
		deleteError: &haproxy.APIError{StatusCode: 404},
	}
	result := map[string]string{}

	err := drainAndRemoveServer(client, "web_app", staleServerName, 0, testLogger(), result)

	if err != nil {
		t.Fatalf("A server that is already gone is removed, expected no error, got: %v", err)
	}
	if result["status"] != StatusDeleted {
		t.Errorf("Expected status %s, got %s", StatusDeleted, result["status"])
	}
}

func TestPeriodicCleanup_RetriesAfterVersionConflict(t *testing.T) {
	client := newConcurrentWriterClient(1)
	client.servers["web_app"] = append(client.servers["web_app"],
		haproxy.Server{Name: expectedServerName, Address: "10.0.0.1", Port: 8080})

	nomadClient := &mockNomadClient{services: webAppServices()}
	connector := NewForTesting(
		&config.Config{HAProxy: config.HAProxyConfig{Frontend: "https", CleanupDelaySec: 30}},
		nomadClient, client, testLogger(),
	)

	connector.periodicCleanup()

	servers, _ := client.GetServers("web_app")
	if len(servers) != 1 || servers[0].Name != expectedServerName {
		t.Fatalf("Expected only %s to remain after retry, got %v", expectedServerName, servers)
	}
}

func TestSyncAndCleanup_RetriesAfterVersionConflict(t *testing.T) {
	client := newConcurrentWriterClient(1)

	_, removed, err := SyncAndCleanupStaleServers(
		context.Background(), client, &mockNomadClient{services: webAppServices()}, testLogger(), testConfig(),
	)

	if err != nil || removed != 1 {
		t.Fatalf("Expected 1 stale server removed without error, got removed=%d err=%v", removed, err)
	}
}
