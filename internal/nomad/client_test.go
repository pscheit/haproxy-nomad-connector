package nomad

import (
	"fmt"
	"testing"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetServiceCheckFromJob(t *testing.T) {
	tests := []struct {
		name          string
		job           *nomadapi.Job
		serviceName   string
		expectedCheck *ServiceCheck
		expectedError bool
	}{
		{
			name: "HTTP health check in task service",
			job: &nomadapi.Job{
				TaskGroups: []*nomadapi.TaskGroup{
					{
						Name: stringPtr("web"),
						Tasks: []*nomadapi.Task{
							{
								Name: "web-server",
								Services: []*nomadapi.Service{
									{
										Name: "web-app",
										Checks: []nomadapi.ServiceCheck{
											{
												Type:     "http",
												Path:     "/health",
												Method:   "GET",
												Interval: 10 * time.Second,
												Timeout:  2 * time.Second,
											},
										},
									},
								},
							},
						},
					},
				},
			},
			serviceName: "web-app",
			expectedCheck: &ServiceCheck{
				Type:     "http",
				Path:     "/health",
				Method:   "GET",
				Interval: 10 * time.Second,
				Timeout:  2 * time.Second,
			},
			expectedError: false,
		},
		{
			name: "TCP health check at task group level",
			job: &nomadapi.Job{
				TaskGroups: []*nomadapi.TaskGroup{
					{
						Name: stringPtr("database"),
						Services: []*nomadapi.Service{
							{
								Name: "postgres",
								Checks: []nomadapi.ServiceCheck{
									{
										Type:     "tcp",
										Interval: 5 * time.Second,
										Timeout:  1 * time.Second,
									},
								},
							},
						},
					},
				},
			},
			serviceName: "postgres",
			expectedCheck: &ServiceCheck{
				Type:     "tcp",
				Path:     "",
				Method:   "",
				Interval: 5 * time.Second,
				Timeout:  1 * time.Second,
			},
			expectedError: false,
		},
		{
			name: "Service with no health checks",
			job: &nomadapi.Job{
				TaskGroups: []*nomadapi.TaskGroup{
					{
						Name: stringPtr("worker"),
						Tasks: []*nomadapi.Task{
							{
								Name: "worker",
								Services: []*nomadapi.Service{
									{
										Name:   "worker-service",
										Checks: []nomadapi.ServiceCheck{},
									},
								},
							},
						},
					},
				},
			},
			serviceName:   "worker-service",
			expectedCheck: nil,
			expectedError: false,
		},
		{
			name: "Service not found in job",
			job: &nomadapi.Job{
				TaskGroups: []*nomadapi.TaskGroup{
					{
						Name: stringPtr("web"),
						Tasks: []*nomadapi.Task{
							{
								Name: "web-server",
								Services: []*nomadapi.Service{
									{
										Name: "web-app",
									},
								},
							},
						},
					},
				},
			},
			serviceName:   "non-existent",
			expectedCheck: nil,
			expectedError: true,
		},
		{
			name: "Multiple health checks - use first one",
			job: &nomadapi.Job{
				TaskGroups: []*nomadapi.TaskGroup{
					{
						Name: stringPtr("api"),
						Tasks: []*nomadapi.Task{
							{
								Name: "api-server",
								Services: []*nomadapi.Service{
									{
										Name: "api",
										Checks: []nomadapi.ServiceCheck{
											{
												Type:     "http",
												Path:     "/health",
												Method:   "GET",
												Interval: 10 * time.Second,
												Timeout:  2 * time.Second,
											},
											{
												Type:     "http",
												Path:     "/ready",
												Method:   "GET",
												Interval: 5 * time.Second,
												Timeout:  1 * time.Second,
											},
										},
									},
								},
							},
						},
					},
				},
			},
			serviceName: "api",
			expectedCheck: &ServiceCheck{
				Type:     "http",
				Path:     "/health",
				Method:   "GET",
				Interval: 10 * time.Second,
				Timeout:  2 * time.Second,
			},
			expectedError: false,
		},
		{
			name: "gRPC health check",
			job: &nomadapi.Job{
				TaskGroups: []*nomadapi.TaskGroup{
					{
						Name: stringPtr("grpc"),
						Tasks: []*nomadapi.Task{
							{
								Name: "grpc-server",
								Services: []*nomadapi.Service{
									{
										Name: "grpc-service",
										Checks: []nomadapi.ServiceCheck{
											{
												Type:     "grpc",
												Interval: 15 * time.Second,
												Timeout:  3 * time.Second,
											},
										},
									},
								},
							},
						},
					},
				},
			},
			serviceName: "grpc-service",
			expectedCheck: &ServiceCheck{
				Type:     "grpc",
				Path:     "",
				Method:   "",
				Interval: 15 * time.Second,
				Timeout:  3 * time.Second,
			},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test the extraction logic directly
			check, err := extractServiceCheckFromJob(tt.job, tt.serviceName)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedCheck, check)
			}
		})
	}
}

// Helper function to extract service check from job (testable version)
func extractServiceCheckFromJob(job *nomadapi.Job, serviceName string) (*ServiceCheck, error) {
	// Search through all task groups
	for _, taskGroup := range job.TaskGroups {
		// Search through all tasks
		for _, task := range taskGroup.Tasks {
			// Search through all services
			for _, service := range task.Services {
				if service.Name == serviceName {
					// If there are checks defined, use the first one
					if len(service.Checks) > 0 {
						check := service.Checks[0]
						return &ServiceCheck{
							Type:     check.Type,
							Path:     check.Path,
							Method:   check.Method,
							Interval: check.Interval,
							Timeout:  check.Timeout,
						}, nil
					}
					// Service found but no checks defined
					return nil, nil
				}
			}
		}

		// Also check services defined at task group level
		for _, service := range taskGroup.Services {
			if service.Name == serviceName {
				if len(service.Checks) > 0 {
					check := service.Checks[0]
					return &ServiceCheck{
						Type:     check.Type,
						Path:     check.Path,
						Method:   check.Method,
						Interval: check.Interval,
						Timeout:  check.Timeout,
					}, nil
				}
				return nil, nil
			}
		}
	}

	return nil, fmt.Errorf("service %s not found in job", serviceName)
}

func stringPtr(s string) *string {
	return &s
}
