package connector

import (
	"context"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"github.com/pscheit/haproxy-nomad-connector/internal/config"
	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
	"github.com/pscheit/haproxy-nomad-connector/internal/nomad"
)

const expectedServerName = "web_app_10_0_0_1_8080"

// mockNomadClient implements nomad.NomadClient for cleanup tests
type mockNomadClient struct {
	services       []*nomad.Service
	getServicesErr error
}

func (m *mockNomadClient) GetServices() ([]*nomad.Service, error) {
	return m.services, m.getServicesErr
}

func (m *mockNomadClient) StreamServiceEvents(
	ctx context.Context, eventChan chan<- nomad.ServiceEvent,
) error {
	<-ctx.Done()
	return ctx.Err()
}

func (m *mockNomadClient) GetServiceCheckFromJob(
	jobID, serviceName string,
) (*nomad.ServiceCheck, error) {
	return nil, nil
}

func testLogger() *log.Logger {
	return log.New(os.Stdout, "test: ", log.LstdFlags)
}

func TestPeriodicCleanup_RemovesStaleServers(t *testing.T) {
	haproxyClient := NewMockHAProxyClient()

	// Backend has 2 servers: one expected, one stale
	haproxyClient.servers["web_app"] = []haproxy.Server{
		{Name: expectedServerName, Address: "10.0.0.1", Port: 8080},
		{Name: "web_app_10_0_0_2_8080", Address: "10.0.0.2", Port: 8080},
	}
	haproxyClient.backends["web_app"] = &haproxy.Backend{Name: "web_app"}

	// Nomad only knows about one server
	nomadClient := &mockNomadClient{
		services: []*nomad.Service{
			{
				ServiceName: "web-app",
				Address:     "10.0.0.1",
				Port:        8080,
				Tags:        []string{"haproxy.enable=true"},
			},
		},
	}

	connector := NewForTesting(
		&config.Config{HAProxy: config.HAProxyConfig{
			Frontend: "https", CleanupDelaySec: 30,
		}},
		nomadClient, haproxyClient, testLogger(),
	)

	connector.periodicCleanup()

	servers, _ := haproxyClient.GetServers("web_app")
	if len(servers) != 1 {
		t.Fatalf("Expected 1 server after cleanup, got %d", len(servers))
	}
	if servers[0].Name != expectedServerName {
		t.Errorf("Expected server %s to remain, got %s",
			expectedServerName, servers[0].Name)
	}
}

func TestPeriodicCleanup_NoOpWhenClean(t *testing.T) {
	haproxyClient := NewMockHAProxyClient()

	haproxyClient.servers["web_app"] = []haproxy.Server{
		{Name: expectedServerName, Address: "10.0.0.1", Port: 8080},
	}
	haproxyClient.backends["web_app"] = &haproxy.Backend{Name: "web_app"}

	nomadClient := &mockNomadClient{
		services: []*nomad.Service{
			{
				ServiceName: "web-app",
				Address:     "10.0.0.1",
				Port:        8080,
				Tags:        []string{"haproxy.enable=true"},
			},
		},
	}

	initialVersion := haproxyClient.version

	connector := NewForTesting(
		&config.Config{HAProxy: config.HAProxyConfig{
			Frontend: "https", CleanupDelaySec: 30,
		}},
		nomadClient, haproxyClient, testLogger(),
	)

	connector.periodicCleanup()

	if haproxyClient.version != initialVersion {
		t.Error("Version should not change when no stale servers exist")
	}
}

func TestPeriodicCleanup_HandlesNomadError(t *testing.T) {
	haproxyClient := NewMockHAProxyClient()
	haproxyClient.servers["web_app"] = []haproxy.Server{
		{Name: expectedServerName, Address: "10.0.0.1", Port: 8080},
	}

	nomadClient := &mockNomadClient{
		getServicesErr: fmt.Errorf("nomad unavailable"),
	}

	connector := NewForTesting(
		&config.Config{HAProxy: config.HAProxyConfig{
			Frontend: "https", CleanupDelaySec: 30,
		}},
		nomadClient, haproxyClient, testLogger(),
	)

	connector.periodicCleanup()

	servers, _ := haproxyClient.GetServers("web_app")
	if len(servers) != 1 {
		t.Error("Servers should not be modified when Nomad is unavailable")
	}
}

func TestStart_DebouncedCleanupTriggersAfterDeregistration(t *testing.T) {
	haproxyClient := NewMockHAProxyClient()

	// Pre-seed a backend with a stale server
	haproxyClient.backends["web_app"] = &haproxy.Backend{Name: "web_app"}
	haproxyClient.servers["web_app"] = []haproxy.Server{
		{Name: expectedServerName, Address: "10.0.0.1", Port: 8080},
		{Name: "web_app_10_0_0_2_8080", Address: "10.0.0.2", Port: 8080},
	}

	nomadClient := &mockNomadClient{
		services: []*nomad.Service{
			{
				ServiceName: "web-app",
				Address:     "10.0.0.1",
				Port:        8080,
				Tags: []string{
					"haproxy.enable=true", "haproxy.backend=dynamic",
				},
			},
		},
	}

	cfg := &config.Config{HAProxy: config.HAProxyConfig{
		Frontend:        "https",
		CleanupDelaySec: 1,
		DrainTimeoutSec: 0,
	}}

	connector := NewForTesting(
		cfg, nomadClient, haproxyClient, testLogger(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- connector.Start(ctx)
	}()

	// Wait for initial sync to complete and clean up the stale server
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	servers, _ := haproxyClient.GetServers("web_app")
	if len(servers) != 1 {
		t.Fatalf("Expected initial sync to remove stale server, got %d servers",
			len(servers))
	}
	if servers[0].Name != expectedServerName {
		t.Errorf("Expected %s to remain, got %s",
			expectedServerName, servers[0].Name)
	}
}

func TestStart_DebouncedCleanupAfterEventProcessing(t *testing.T) {
	haproxyClient := NewMockHAProxyClient()

	// Pre-seed backend with expected server
	haproxyClient.backends["web_app"] = &haproxy.Backend{Name: "web_app"}
	haproxyClient.servers["web_app"] = []haproxy.Server{
		{Name: expectedServerName, Address: "10.0.0.1", Port: 8080},
	}

	nomadClient := &mockNomadClient{
		services: []*nomad.Service{
			{
				ServiceName: "web-app",
				Address:     "10.0.0.1",
				Port:        8080,
				Tags: []string{
					"haproxy.enable=true", "haproxy.backend=dynamic",
				},
			},
		},
	}

	eventChan := make(chan nomad.ServiceEvent, 10)

	streamingNomadClient := &streamingMockNomadClient{
		mockNomadClient: *nomadClient,
		eventChan:       eventChan,
	}

	cfg := &config.Config{HAProxy: config.HAProxyConfig{
		Frontend:        "https",
		CleanupDelaySec: 1,
		DrainTimeoutSec: 0,
	}}

	connector := NewForTesting(
		cfg, streamingNomadClient, haproxyClient, testLogger(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- connector.Start(ctx)
	}()

	// Wait for startup sync
	time.Sleep(100 * time.Millisecond)

	// Inject a stale server using thread-safe helper
	haproxyClient.AddServers("web_app", []haproxy.Server{
		{Name: "web_app_10_0_0_2_8080", Address: "10.0.0.2", Port: 8080},
	})

	// Send a deregistration event to trigger the debounced cleanup
	eventChan <- nomad.ServiceEvent{
		Type:  "ServiceDeregistration",
		Topic: "Service",
		Payload: nomad.Payload{
			Service: &nomad.Service{
				ServiceName: "web-app",
				Address:     "10.0.0.2",
				Port:        8080,
				Tags: []string{
					"haproxy.enable=true", "haproxy.backend=dynamic",
				},
			},
		},
	}

	// Wait for the cleanup delay (1s) + buffer
	time.Sleep(1500 * time.Millisecond)

	servers, _ := haproxyClient.GetServers("web_app")
	if len(servers) != 1 {
		t.Fatalf("Expected cleanup to remove stale server, got %d servers",
			len(servers))
	}
	if servers[0].Name != expectedServerName {
		t.Errorf("Expected %s to remain, got %s",
			expectedServerName, servers[0].Name)
	}

	cancel()
	<-done
}

// streamingMockNomadClient allows injecting events via a channel
type streamingMockNomadClient struct {
	mockNomadClient
	eventChan <-chan nomad.ServiceEvent
}

func (m *streamingMockNomadClient) StreamServiceEvents(
	ctx context.Context, outChan chan<- nomad.ServiceEvent,
) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-m.eventChan:
			outChan <- event
		}
	}
}
