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

// EventTypeStreamReconnected is sent on the event channel after the stream reconnected.
// Events may have been missed while it was down, so the receiver has to resync.
const EventTypeStreamReconnected = "StreamReconnected"

// variables, so tests do not have to wait
var (
	streamReconnectDelay = 5 * time.Second
	apiRequestTimeout    = 30 * time.Second
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
	// only for the plain API calls, the event stream builds its own client without a timeout
	config.HttpClient = &http.Client{Timeout: apiRequestTimeout}

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

// StreamServiceEvents streams Nomad service events. It reconnects after every stream error and
// sends an EventTypeStreamReconnected event on every connect after a failed attempt.
func (c *Client) StreamServiceEvents(ctx context.Context, eventChan chan<- ServiceEvent) error {
	// also true when the very first connect failed: then the initial sync most likely failed too
	failedBefore := false
	for {
		err := c.streamEvents(ctx, eventChan, failedBefore)
		failedBefore = true
		if ctx.Err() != nil {
			return ctx.Err()
		}

		c.logger.Printf("Event stream error: %v, reconnecting in %s", err, streamReconnectDelay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(streamReconnectDelay):
		}
	}
}

// streamEvents reads one connection until it fails, it never returns nil
func (c *Client) streamEvents(ctx context.Context, eventChan chan<- ServiceEvent, isReconnect bool) error {
	body, err := c.openEventStream(ctx)
	if err != nil {
		return err
	}
	defer body.Close()

	if isReconnect {
		select {
		case eventChan <- ServiceEvent{Type: EventTypeStreamReconnected}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return c.readEvents(ctx, json.NewDecoder(body), eventChan)
}

func (c *Client) openEventStream(ctx context.Context) (io.ReadCloser, error) {
	url := fmt.Sprintf("%s/v1/event/stream?topic=Service", c.address)

	req, err := http.NewRequestWithContext(ctx, "GET", url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if c.token != "" {
		req.Header.Set("X-Nomad-Token", c.token)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")

	client := &http.Client{
		Timeout: 0, // No timeout for streaming
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to event stream: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("event stream returned status %d", resp.StatusCode)
	}

	c.logger.Printf("Connected to Nomad event stream: %s", url)
	return resp.Body, nil
}

// readEvents forwards service events until the stream fails, it never returns nil
func (c *Client) readEvents(ctx context.Context, decoder *json.Decoder, eventChan chan<- ServiceEvent) error {
	for {
		var eventWrapper struct {
			Events []ServiceEvent `json:"Events"`
		}

		// A json.Decoder stays broken after any error (a syntax error included), so every
		// error ends this connection. Continuing here once looped forever and filled the disk.
		if err := decoder.Decode(&eventWrapper); err != nil {
			if err == io.EOF {
				return fmt.Errorf("event stream ended")
			}
			return fmt.Errorf("failed to read event stream: %w", err)
		}

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

// GetServices gets all registered services (for initial sync)
func (c *Client) GetServices() ([]*Service, error) {
	c.logger.Printf("Fetching existing services from Nomad for initial sync...")

	// First, list all service names grouped by namespace
	serviceListStubs, _, err := c.client.Services().List(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list services from Nomad: %w", err)
	}

	var services []*Service

	// Iterate through each namespace and service name to get full registrations
	for _, listStub := range serviceListStubs {
		for _, serviceStub := range listStub.Services {
			// Get full service registration details for this service name
			serviceRegistrations, _, err := c.client.Services().Get(serviceStub.ServiceName, nil)
			if err != nil {
				c.logger.Printf("Warning: failed to get details for service %s: %v", serviceStub.ServiceName, err)
				continue
			}

			// Convert each full service registration to our internal Service struct
			for _, registration := range serviceRegistrations {
				service := &Service{
					ID:          registration.ID,
					ServiceName: registration.ServiceName,
					Namespace:   registration.Namespace,
					NodeID:      registration.NodeID,
					Datacenter:  registration.Datacenter,
					JobID:       registration.JobID,
					AllocID:     registration.AllocID,
					Tags:        registration.Tags,
					Address:     registration.Address,
					Port:        registration.Port,
					CreateIndex: registration.CreateIndex,
					ModifyIndex: registration.ModifyIndex,
				}
				services = append(services, service)
			}
		}
	}

	c.logger.Printf("Found %d existing services in Nomad", len(services))
	return services, nil
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

	return extractServiceCheckFromJob(job, serviceName)
}

// extractServiceCheckFromJob is a helper function to extract service check from job spec
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
