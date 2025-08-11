package connector

import (
	"testing"

	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
)

func TestParseDomainMapping(t *testing.T) {
	tests := []struct {
		name        string
		serviceName string
		tags        []string
		expected    *haproxy.DomainMapping
	}{
		{
			name:        "basic domain mapping",
			serviceName: "api-service",
			tags:        []string{"haproxy.enable=true", "haproxy.domain=api.example.com"},
			expected: &haproxy.DomainMapping{
				Domain:      "api.example.com",
				BackendName: "api_service",
				Type:        haproxy.DomainTypeExact,
			},
		},
		{
			name:        "domain with explicit exact type",
			serviceName: "web-app",
			tags:        []string{"haproxy.domain=web.example.com", "haproxy.domain.type=exact"},
			expected: &haproxy.DomainMapping{
				Domain:      "web.example.com",
				BackendName: "web_app",
				Type:        haproxy.DomainTypeExact,
			},
		},
		{
			name:        "domain with prefix type",
			serviceName: "api",
			tags:        []string{"haproxy.domain=api.example.com", "haproxy.domain.type=prefix"},
			expected: &haproxy.DomainMapping{
				Domain:      "api.example.com",
				BackendName: "api",
				Type:        haproxy.DomainTypePrefix,
			},
		},
		{
			name:        "domain with regex type",
			serviceName: "assets",
			tags:        []string{"haproxy.domain=.*\\.assets\\.example\\.com", "haproxy.domain.type=regex"},
			expected: &haproxy.DomainMapping{
				Domain:      ".*\\.assets\\.example\\.com",
				BackendName: "assets",
				Type:        haproxy.DomainTypeRegex,
			},
		},
		{
			name:        "no domain tag",
			serviceName: "database",
			tags:        []string{"haproxy.enable=true", "haproxy.backend=dynamic"},
			expected:    nil,
		},
		{
			name:        "empty domain value",
			serviceName: "cache",
			tags:        []string{"haproxy.domain="},
			expected:    nil,
		},
		{
			name:        "invalid domain type falls back to exact",
			serviceName: "logger",
			tags:        []string{"haproxy.domain=log.example.com", "haproxy.domain.type=invalid"},
			expected: &haproxy.DomainMapping{
				Domain:      "log.example.com",
				BackendName: "logger",
				Type:        haproxy.DomainTypeExact,
			},
		},
		{
			name:        "complex service name with dashes",
			serviceName: "photo-book-service",
			tags:        []string{"haproxy.domain=photobooks.example.com"},
			expected: &haproxy.DomainMapping{
				Domain:      "photobooks.example.com",
				BackendName: "photo_book_service",
				Type:        haproxy.DomainTypeExact,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseDomainMapping(tt.serviceName, tt.tags)
			
			if tt.expected == nil {
				if result != nil {
					t.Errorf("parseDomainMapping() = %+v, expected nil", result)
				}
				return
			}

			if result == nil {
				t.Errorf("parseDomainMapping() = nil, expected %+v", tt.expected)
				return
			}

			if result.Domain != tt.expected.Domain {
				t.Errorf("parseDomainMapping().Domain = %q, expected %q", result.Domain, tt.expected.Domain)
			}

			if result.BackendName != tt.expected.BackendName {
				t.Errorf("parseDomainMapping().BackendName = %q, expected %q", result.BackendName, tt.expected.BackendName)
			}

			if result.Type != tt.expected.Type {
				t.Errorf("parseDomainMapping().Type = %q, expected %q", result.Type, tt.expected.Type)
			}
		})
	}
}

func TestHasDomainMapping(t *testing.T) {
	tests := []struct {
		name     string
		tags     []string
		expected bool
	}{
		{
			name:     "has domain tag",
			tags:     []string{"haproxy.enable=true", "haproxy.domain=api.example.com"},
			expected: true,
		},
		{
			name:     "no domain tag",
			tags:     []string{"haproxy.enable=true", "haproxy.backend=dynamic"},
			expected: false,
		},
		{
			name:     "empty domain value",
			tags:     []string{"haproxy.domain="},
			expected: true, // tag exists but value is empty
		},
		{
			name:     "domain tag with other tags",
			tags:     []string{"web", "api", "haproxy.domain=web.example.com", "version=1.0"},
			expected: true,
		},
		{
			name:     "no tags",
			tags:     []string{},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasDomainMapping(tt.tags)
			if result != tt.expected {
				t.Errorf("hasDomainMapping() = %v, expected %v", result, tt.expected)
			}
		})
	}
}