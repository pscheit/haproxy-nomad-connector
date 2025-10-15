package nomad

import "context"

// NomadClient defines the interface for interacting with Nomad
// This allows mocking in tests while the production code uses the concrete Client
type NomadClient interface {
	// StreamServiceEvents streams Nomad service registration/deregistration events
	StreamServiceEvents(ctx context.Context, eventChan chan<- ServiceEvent) error

	// GetServices retrieves all registered services (used for initial sync)
	GetServices() ([]*Service, error)

	// GetServiceCheckFromJob extracts health check configuration for a service from a job
	GetServiceCheckFromJob(jobID, serviceName string) (*ServiceCheck, error)
}

// Ensure Client implements NomadClient interface
var _ NomadClient = (*Client)(nil)
