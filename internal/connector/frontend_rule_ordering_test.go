package connector

import (
	"context"
	"reflect"
	"testing"

	"github.com/pscheit/haproxy-nomad-connector/internal/config"
	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
	"github.com/pscheit/haproxy-nomad-connector/internal/nomad"
)

// Existing rules are only rewritten when a domain is added or removed, so a startup
// sync must reorder them explicitly. Otherwise a catch-all regex registered before an
// exact host keeps shadowing that host forever.
func TestSyncExistingServices_ReordersFrontendRulesOnce(t *testing.T) {
	haproxyClient := NewMockHAProxyClient()
	haproxyClient.backends["web_app"] = &haproxy.Backend{
		Name:    "web_app",
		Balance: haproxy.Balance{Algorithm: "roundrobin"},
	}

	nomadClient := &mockNomadClient{
		services: []*nomad.Service{
			{
				ServiceName: "web-app",
				Address:     "10.0.0.1",
				Port:        8080,
				Tags:        []string{"haproxy.enable=true", "haproxy.domain=web.example.com"},
			},
		},
	}

	connector := NewForTesting(
		&config.Config{HAProxy: config.HAProxyConfig{Frontend: "https"}},
		nomadClient, haproxyClient, testLogger(),
	)

	if err := connector.syncExistingServices(context.Background()); err != nil {
		t.Fatalf("syncExistingServices failed: %v", err)
	}

	expected := []string{"https"}
	if !reflect.DeepEqual(haproxyClient.reorderedFrontends, expected) {
		t.Errorf("Expected reorder calls %v, got %v", expected, haproxyClient.reorderedFrontends)
	}
}
