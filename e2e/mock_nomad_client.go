package e2e

import (
	"context"
	"fmt"

	"github.com/pscheit/haproxy-nomad-connector/internal/nomad"
)

// MockNomadClient is a test double for nomad.NomadClient interface
// It allows us to inject predefined health check responses in E2E tests
type MockNomadClient struct {
	// ChecksByService maps service names to their health check configurations
	ChecksByService map[string]*nomad.ServiceCheck

	// ServicesByName stores all registered services
	ServicesByName map[string][]*nomad.Service

	// StreamFunc can be set to customize event streaming behavior
	StreamFunc func(ctx context.Context, eventChan chan<- nomad.ServiceEvent) error
}

// NewMockNomadClient creates a new mock Nomad client
func NewMockNomadClient() *MockNomadClient {
	return &MockNomadClient{
		ChecksByService: make(map[string]*nomad.ServiceCheck),
		ServicesByName:  make(map[string][]*nomad.Service),
	}
}

// SetServiceCheck configures the mock to return a specific health check for a service
func (m *MockNomadClient) SetServiceCheck(serviceName string, check *nomad.ServiceCheck) {
	m.ChecksByService[serviceName] = check
}

// GetServiceCheckFromJob returns the mocked health check for the service
func (m *MockNomadClient) GetServiceCheckFromJob(jobID, serviceName string) (*nomad.ServiceCheck, error) {
	check, exists := m.ChecksByService[serviceName]
	if !exists {
		return nil, fmt.Errorf("service %s not found in job", serviceName)
	}
	return check, nil
}

// GetServices returns all mocked services
func (m *MockNomadClient) GetServices() ([]*nomad.Service, error) {
	var allServices []*nomad.Service
	for _, services := range m.ServicesByName {
		allServices = append(allServices, services...)
	}
	return allServices, nil
}

// StreamServiceEvents uses the configured StreamFunc or returns an error
func (m *MockNomadClient) StreamServiceEvents(ctx context.Context, eventChan chan<- nomad.ServiceEvent) error {
	if m.StreamFunc != nil {
		return m.StreamFunc(ctx, eventChan)
	}
	return fmt.Errorf("mock StreamServiceEvents not implemented")
}

// Ensure MockNomadClient implements nomad.NomadClient interface
var _ nomad.NomadClient = (*MockNomadClient)(nil)
