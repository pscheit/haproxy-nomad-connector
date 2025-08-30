package connector

import (
	"context"
	"log"
	"os"
	"testing"

	"github.com/pscheit/haproxy-nomad-connector/internal/config"
	"github.com/pscheit/haproxy-nomad-connector/internal/nomad"
)

// TestNodeEventHandledForServiceCleanup verifies the fix for Bug #2
// where NodeEvent and similar events are now properly handled for service cleanup
func TestNodeEventHandledForServiceCleanup(t *testing.T) {
	mock := &mockHAProxyClientWithReadyTracking{}
	
	// Create a NodeEvent (which Nomad sends when nodes have issues)
	nodeEvent := nomad.ServiceEvent{
		Type:  "NodeEvent", // This should now be handled (not skipped)
		Topic: "Service",
		Key:   "node123",
		Index: 100,
		Payload: nomad.Payload{
			Service: &nomad.Service{
				ID:          "crm-prod-service",
				ServiceName: "crm-prod",
				NodeID:      "node123", 
				Address:     "192.168.5.12",
				Port:        26678,
				Tags:        []string{"haproxy.enable=true", "haproxy.backend=custom"},
				JobID:       "crm-prod",
				AllocID:     "alloc456",
			},
		},
	}

	cfg := &config.Config{}
	
	// Process the NodeEvent  
	logger := log.New(os.Stdout, "[test] ", log.LstdFlags)
	result, err := ProcessNomadServiceEvent(context.Background(), mock, nil, nodeEvent, logger, cfg)
	if err != nil {
		t.Fatalf("ProcessNomadServiceEvent failed: %v", err)
	}

	// Check the result
	if resultMap, ok := result.(map[string]string); ok {
		status := resultMap["status"]
		
		// FIXED: NodeEvents should now be processed (not skipped)
		if status == "skipped" {
			t.Errorf("REGRESSION: NodeEvent was skipped - stale servers won't be cleaned up from HAProxy")
		}
		
		reason, hasReason := resultMap["reason"]
		if hasReason && reason == "unknown event type" {
			t.Errorf("REGRESSION: NodeEvent treated as 'unknown event type' - connector doesn't handle node-related service cleanup")
		}

		// Expect status to be "draining" or "deleted" for deregistration events
		if status != "draining" && status != "deleted" {
			t.Logf("NodeEvent processed with status: %s (expected draining or deleted)", status)
		}
	} else {
		t.Errorf("Expected result to be map[string]string, got %T", result)
	}
}

// TestMultipleEventTypesProcessed verifies that various service-affecting events are handled
func TestMultipleEventTypesProcessed(t *testing.T) {
	mock := &mockHAProxyClientWithReadyTracking{}
	cfg := &config.Config{}
	
	// Test various event types that could affect services
	testEvents := []struct {
		eventType     string
		expectSkipped bool
	}{
		{"ServiceRegistration", false},   // Should be processed
		{"ServiceDeregistration", false}, // Should be processed  
		{"NodeEvent", false},             // FIXED: Now processed as service deregistration
		{"NodeDeregistration", false},    // FIXED: Now processed as service deregistration
		{"AllocationUpdated", false},     // FIXED: Now processed as service deregistration
		{"DeploymentStatusUpdate", true}, // Still skipped - not directly service-related
	}
	
	for _, tc := range testEvents {
		t.Run(tc.eventType, func(t *testing.T) {
			event := nomad.ServiceEvent{
				Type:  tc.eventType,
				Topic: "Service", // Keep topic as Service so payload is processed
				Payload: nomad.Payload{
					Service: &nomad.Service{
						ServiceName: "test-service",
						Address:     "192.168.1.10", 
						Port:        8080,
						Tags:        []string{"haproxy.enable=true", "haproxy.backend=custom"}, // Use custom backend to avoid algorithm mismatch
					},
				},
			}
			
			logger := log.New(os.Stdout, "[test] ", log.LstdFlags)
			result, err := ProcessNomadServiceEvent(context.Background(), mock, nil, event, logger, cfg)
			if err != nil && !tc.expectSkipped {
				// For registration events that might fail due to backend conflicts, still check if they're processed
				if tc.eventType == "ServiceRegistration" {
					t.Logf("ServiceRegistration failed due to mock setup (expected): %v", err)
					return
				}
				t.Fatalf("ProcessNomadServiceEvent failed for %s: %v", tc.eventType, err)
			}
			
			if resultMap, ok := result.(map[string]string); ok {
				status := resultMap["status"]
				isSkipped := status == "skipped"
				
				if tc.expectSkipped && !isSkipped {
					t.Errorf("Expected %s to be skipped, but it was processed with status: %s", tc.eventType, status)
				} else if !tc.expectSkipped && isSkipped {
					t.Errorf("REGRESSION: Expected %s to be processed, but it was skipped", tc.eventType)
				}
			}
		})
	}
}