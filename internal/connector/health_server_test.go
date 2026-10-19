package connector

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/pscheit/haproxy-nomad-connector/internal/config"
)

// On lb1 nginx owns :8080, so the health server never ran there
func TestHealthServerListensOnConfiguredAddress(t *testing.T) {
	address := freeLocalAddress(t)

	connector := NewForTesting(
		&config.Config{
			HAProxy: config.HAProxyConfig{Frontend: "https"},
			Health:  config.HealthConfig{Address: address},
		},
		&mockNomadClient{}, NewMockHAProxyClient(), testLogger(),
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

	httpClient := &http.Client{Timeout: time.Second}
	deadline := time.After(2 * time.Second)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/health", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := httpClient.Do(req)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200 from /health, got %d: %s", resp.StatusCode, body)
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("health server does not answer on %s: %v", address, err)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func freeLocalAddress(t *testing.T) string {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}
