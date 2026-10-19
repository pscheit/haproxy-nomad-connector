package connector

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pscheit/haproxy-nomad-connector/internal/config"
	"github.com/pscheit/haproxy-nomad-connector/internal/nomad"
)

// reconnectingNomadClient knows no service at the initial sync. The service appears while the
// stream is down, so only a resync after the reconnect can add it.
type reconnectingNomadClient struct {
	mu               sync.Mutex
	getServicesCalls int
	service          *nomad.Service
}

func (m *reconnectingNomadClient) GetServices() ([]*nomad.Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getServicesCalls++
	if m.getServicesCalls == 1 {
		return nil, nil
	}
	return []*nomad.Service{m.service}, nil
}

func (m *reconnectingNomadClient) StreamServiceEvents(
	ctx context.Context, eventChan chan<- nomad.ServiceEvent,
) error {
	eventChan <- nomad.ServiceEvent{Type: nomad.EventTypeStreamReconnected}
	<-ctx.Done()
	return ctx.Err()
}

func (m *reconnectingNomadClient) GetServiceCheckFromJob(
	jobID, serviceName string,
) (*nomad.ServiceCheck, error) {
	return nil, nil
}

func TestStreamReconnectTriggersFullResync(t *testing.T) {
	haproxyClient := NewMockHAProxyClient()

	nomadClient := &reconnectingNomadClient{service: &nomad.Service{
		ServiceName: "web-app",
		Address:     "10.0.0.1",
		Port:        8080,
		Tags:        []string{"haproxy.enable=true", "haproxy.backend=dynamic"},
	}}

	connector := NewForTesting(
		&config.Config{HAProxy: config.HAProxyConfig{Frontend: "https"}},
		nomadClient, haproxyClient, testLogger(),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = connector.Start(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()

	deadline := time.After(2 * time.Second)
	for {
		servers, _ := haproxyClient.GetServers("web_app")
		if len(servers) == 1 && servers[0].Name == expectedServerName {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("expected server %s after the resync, got %v", expectedServerName, servers)
		case <-time.After(20 * time.Millisecond):
		}
	}
}
