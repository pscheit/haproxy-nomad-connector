package connector

import (
	"log"
	"testing"

	"github.com/pscheit/haproxy-nomad-connector/internal/nomad"
	"github.com/stretchr/testify/assert"
)

func TestParseHealthCheckFromTags(t *testing.T) {
	tests := []struct {
		name     string
		tags     []string
		expected *HealthCheckConfig
	}{
		{
			name: "HTTP health check from tags",
			tags: []string{
				"haproxy.enable=true",
				"haproxy.check.path=/health",
				"haproxy.check.method=GET",
				"haproxy.check.host=api.internal",
			},
			expected: &HealthCheckConfig{
				Type:   "http",
				Path:   "/health",
				Method: "GET",
				Host:   "api.internal",
			},
		},
		{
			name: "TCP health check from tags",
			tags: []string{
				"haproxy.enable=true",
				"haproxy.check.type=tcp",
			},
			expected: &HealthCheckConfig{
				Type: "tcp",
			},
		},
		{
			name: "Disabled health check",
			tags: []string{
				"haproxy.enable=true",
				"haproxy.check.disabled",
			},
			expected: &HealthCheckConfig{
				Type:     "disabled",
				Disabled: true,
			},
		},
		{
			name: "No health check tags",
			tags: []string{
				"haproxy.enable=true",
				"haproxy.backend=dynamic",
			},
			expected: nil,
		},
		{
			name: "Implicit HTTP from path",
			tags: []string{
				"haproxy.enable=true",
				"haproxy.check.path=/api/health",
			},
			expected: &HealthCheckConfig{
				Type: "http",
				Path: "/api/health",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseHealthCheckFromTags(tt.tags)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConvertNomadToHAProxyCheck(t *testing.T) {
	tests := []struct {
		name     string
		nomad    *nomad.ServiceCheck
		expected *HealthCheckConfig
	}{
		{
			name: "HTTP check conversion",
			nomad: &nomad.ServiceCheck{
				Type:   "http",
				Path:   "/health",
				Method: "GET",
			},
			expected: &HealthCheckConfig{
				Type:   "http",
				Path:   "/health",
				Method: "GET",
			},
		},
		{
			name: "HTTP check without method defaults to GET",
			nomad: &nomad.ServiceCheck{
				Type: "http",
				Path: "/status",
			},
			expected: &HealthCheckConfig{
				Type:   "http",
				Path:   "/status",
				Method: "GET",
			},
		},
		{
			name: "TCP check conversion",
			nomad: &nomad.ServiceCheck{
				Type: "tcp",
			},
			expected: &HealthCheckConfig{
				Type: "tcp",
			},
		},
		{
			name: "gRPC check maps to TCP",
			nomad: &nomad.ServiceCheck{
				Type: "grpc",
			},
			expected: &HealthCheckConfig{
				Type: "tcp",
			},
		},
		{
			name: "HTTPS check maps to HTTP",
			nomad: &nomad.ServiceCheck{
				Type:   "https",
				Path:   "/secure-health",
				Method: "POST",
			},
			expected: &HealthCheckConfig{
				Type:   "http",
				Path:   "/secure-health",
				Method: "POST",
			},
		},
		{
			name: "Unknown check type defaults to TCP",
			nomad: &nomad.ServiceCheck{
				Type: "script",
			},
			expected: &HealthCheckConfig{
				Type: "tcp",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertNomadToHAProxyCheck(tt.nomad)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCreateServerWithHealthCheck(t *testing.T) {
	logger := log.New(&testWriter{}, "", 0)

	tests := []struct {
		name       string
		service    Service
		serverName string
		nomadCheck *nomad.ServiceCheck
		tags       []string
		expected   string // Expected CheckType
	}{
		{
			name: "Tag overrides Nomad check",
			service: Service{
				ServiceName: "web-app",
				Address:     "192.168.1.10",
				Port:        8080,
			},
			serverName: "web_app_192_168_1_10_8080",
			nomadCheck: &nomad.ServiceCheck{
				Type: "tcp",
			},
			tags: []string{
				"haproxy.enable=true",
				"haproxy.check.path=/health",
			},
			expected: "http",
		},
		{
			name: "Use Nomad check when no tags",
			service: Service{
				ServiceName: "api",
				Address:     "192.168.1.20",
				Port:        3000,
			},
			serverName: "api_192_168_1_20_3000",
			nomadCheck: &nomad.ServiceCheck{
				Type:   "http",
				Path:   "/api/health",
				Method: "GET",
			},
			tags: []string{
				"haproxy.enable=true",
			},
			expected: "http",
		},
		{
			name: "Default to TCP when no checks",
			service: Service{
				ServiceName: "database",
				Address:     "192.168.1.30",
				Port:        5432,
			},
			serverName: "database_192_168_1_30_5432",
			nomadCheck: nil,
			tags: []string{
				"haproxy.enable=true",
			},
			expected: "tcp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createServerWithHealthCheck(&tt.service, tt.serverName, tt.nomadCheck, tt.tags, logger)

			assert.Equal(t, tt.serverName, server.Name)
			assert.Equal(t, tt.service.Address, server.Address)
			assert.Equal(t, tt.service.Port, server.Port)
			assert.Equal(t, tt.expected, server.CheckType)

			if tt.expected != "disabled" {
				assert.Equal(t, "enabled", server.Check)
			} else {
				assert.Equal(t, "disabled", server.Check)
			}
		})
	}
}

// testWriter implements io.Writer for silent test logging
type testWriter struct{}

func (w *testWriter) Write(p []byte) (n int, err error) {
	return len(p), nil
}
