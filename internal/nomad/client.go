package nomad

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	nomadapi "github.com/hashicorp/nomad/api"
)

type Client struct {
	client  *nomadapi.Client
	address string
	token   string
	region  string
	logger  *log.Logger
}

// ServiceEvent represents a Nomad service registration/deregistration event
type ServiceEvent struct {
	Type    string  `json:"Type"`
	Topic   string  `json:"Topic"`
	Key     string  `json:"Key"`
	Index   uint64  `json:"Index"`
	Payload Payload `json:"Payload"`
}

type Payload struct {
	Service *Service `json:"Service"`
}

type Service struct {
	ID          string            `json:"ID"`
	ServiceName string            `json:"ServiceName"`
	Namespace   string            `json:"Namespace"`
	NodeID      string            `json:"NodeID"`
	Datacenter  string            `json:"Datacenter"`
	JobID       string            `json:"JobID"`
	AllocID     string            `json:"AllocID"`
	Tags        []string          `json:"Tags"`
	Address     string            `json:"Address"`
	Port        int               `json:"Port"`
	Meta        map[string]string `json:"Meta"`
	CreateIndex uint64            `json:"CreateIndex"`
	ModifyIndex uint64            `json:"ModifyIndex"`
}

// ServiceCheck represents a Nomad service health check configuration
type ServiceCheck struct {
	Type     string        // "http", "tcp", "script", "grpc"
	Path     string        // HTTP path for http checks
	Method   string        // HTTP method for http checks
	Interval time.Duration // Check interval
	Timeout  time.Duration // Check timeout
}

// NewClient creates a new Nomad client
func NewClient(address, token, region string, logger *log.Logger) (*Client, error) {
	config := nomadapi.DefaultConfig()
	config.Address = address

	if token != "" {
		config.SecretID = token
	}

	if region != "" {
		config.Region = region
	}

	client, err := nomadapi.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Nomad client: %w", err)
	}

	return &Client{
		client:  client,
		address: address,
		token:   token,
		region:  region,
		logger:  logger,
	}, nil
}

// StreamServiceEvents streams Nomad service events
func (c *Client) StreamServiceEvents(ctx context.Context, eventChan chan<- ServiceEvent) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if err := c.streamEvents(ctx, eventChan); err != nil {
				c.logger.Printf("Event stream error: %v", err)
				c.logger.Printf("Reconnecting in 5 seconds...")

				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(5 * time.Second):
					continue
				}
			}
		}
	}
}

func (c *Client) streamEvents(ctx context.Context, eventChan chan<- ServiceEvent) error {
	// Create HTTP request for event stream
	url := fmt.Sprintf("%s/v1/event/stream?topic=Service", c.address)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Add authentication if token provided
	if c.token != "" {
		req.Header.Set("X-Nomad-Token", c.token)
	}

	// Add headers for streaming
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")

	client := &http.Client{
		Timeout: 0, // No timeout for streaming
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to event stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("event stream returned status %d", resp.StatusCode)
	}

	c.logger.Printf("Connected to Nomad event stream: %s", url)

	// Process streaming JSON lines
	decoder := json.NewDecoder(resp.Body)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			var eventWrapper struct {
				Events []ServiceEvent `json:"Events"`
			}

			if err := decoder.Decode(&eventWrapper); err != nil {
				if err == io.EOF {
					return fmt.Errorf("event stream ended")
				}
				c.logger.Printf("Failed to decode event: %v", err)
				continue
			}

			// Process each event
			for _, event := range eventWrapper.Events {
				if event.Topic == "Service" && event.Payload.Service != nil {
					select {
					case eventChan <- event:
						c.logger.Printf("Processed %s event for service %s",
							event.Type, event.Payload.Service.ServiceName)
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
		}
	}
}

// GetServices gets all registered services (for initial sync)
func (c *Client) GetServices() ([]*Service, error) {
	// For now, return empty slice - we'll rely on event stream for service discovery
	// This can be improved later when we sort out the exact API structure
	c.logger.Printf("Initial sync disabled - relying on event stream for service discovery")
	return []*Service{}, nil
}

// GetJobSpec retrieves the job specification for a given job ID
func (c *Client) GetJobSpec(jobID string) (*nomadapi.Job, error) {
	job, _, err := c.client.Jobs().Info(jobID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get job spec for %s: %w", jobID, err)
	}
	return job, nil
}

// GetServiceCheckFromJob extracts the health check configuration for a specific service from a job
func (c *Client) GetServiceCheckFromJob(jobID, serviceName string) (*ServiceCheck, error) {
	job, err := c.GetJobSpec(jobID)
	if err != nil {
		return nil, err
	}

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

	return nil, fmt.Errorf("service %s not found in job %s", serviceName, jobID)
}
