package connector

import (
	"strings"

	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
)

// parseDomainMapping extracts domain mapping configuration from service tags
func parseDomainMapping(serviceName string, tags []string) *haproxy.DomainMapping {
	var domain string
	domainType := haproxy.DomainTypeExact // default

	for _, tag := range tags {
		if strings.HasPrefix(tag, "haproxy.domain=") {
			domain = strings.TrimPrefix(tag, "haproxy.domain=")
		}
		if strings.HasPrefix(tag, "haproxy.domain.type=") {
			typeStr := strings.TrimPrefix(tag, "haproxy.domain.type=")
			switch typeStr {
			case "exact":
				domainType = haproxy.DomainTypeExact
			case "prefix":
				domainType = haproxy.DomainTypePrefix  
			case "regex":
				domainType = haproxy.DomainTypeRegex
			}
		}
	}

	// Return nil if no domain tag found
	if domain == "" {
		return nil
	}

	return &haproxy.DomainMapping{
		Domain:      domain,
		BackendName: sanitizeServiceName(serviceName),
		Type:        domainType,
	}
}

// hasDomainMapping checks if service has domain mapping tags
func hasDomainMapping(tags []string) bool {
	for _, tag := range tags {
		if strings.HasPrefix(tag, "haproxy.domain=") {
			return true
		}
	}
	return false
}