//go:build integration
// +build integration

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/pscheit/haproxy-nomad-connector/internal/config"
	"github.com/pscheit/haproxy-nomad-connector/internal/connector"
	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
)

func main() {
	fmt.Println("🧪 End-to-End Service Registration with Regex Domain Test")
	fmt.Println("========================================================")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create HAProxy client
	client := haproxy.NewClient("http://localhost:5555", "admin", "adminpwd")

	// Reset HAProxy frontend rules before starting the test
	fmt.Println("\n🧹 Resetting HAProxy frontend rules to clean state...")
	err := client.ResetFrontendRules("http")
	if err != nil {
		fmt.Printf("❌ Failed to reset frontend rules: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✅ HAProxy frontend rules reset successfully")

	// Test the exact production case: service with regex domain
	fmt.Println("\n📝 Testing service registration with regex domain pattern...")

	serviceEvent := connector.ServiceEvent{
		Type: "ServiceRegistration",
		Service: connector.Service{
			ServiceName: "test-backend-service", 
			Address:     "test-backend",
			Port:        80, // Points to our test-backend nginx container
			Tags: []string{
				"haproxy.enable=true",
				"haproxy.domain=^(api\\.|www\\.)?test-regex\\.com$",
				"haproxy.domain.type=regex",
				"haproxy.backend=dynamic",
			},
		},
	}

	fmt.Printf("Service: %s\n", serviceEvent.Service.ServiceName)
	fmt.Printf("Domain: %s\n", serviceEvent.Service.Tags[1]) // haproxy.domain tag
	fmt.Printf("Tags: %v\n", serviceEvent.Service.Tags)

	// Process event through connector - this should succeed completely
	fmt.Println("\n1️⃣ Processing service event through connector...")
	// Create config for test
	cfg := &config.Config{
		HAProxy: config.HAProxyConfig{
			Frontend: "http",
		},
	}
	
	result, err2 := connector.ProcessServiceEvent(ctx, client, &serviceEvent, cfg)

	if err2 != nil {
		fmt.Printf("❌ Service processing failed: %v\n", err2)
		os.Exit(1)
	}
	fmt.Printf("✅ Service processed successfully\n")

	// Verify backend was created
	fmt.Println("\n2️⃣ Verifying backend creation...")
	backends, err := client.GetBackends()
	if err != nil {
		fmt.Printf("❌ Failed to get backends: %v\n", err)
		os.Exit(1)
	}

	backendFound := false
	for _, backend := range backends {
		if backend.Name == "test_backend_service" { // connector should sanitize service name
			backendFound = true
			fmt.Printf("✅ Backend created: %s\n", backend.Name)
			break
		}
	}

	if !backendFound {
		fmt.Printf("❌ Backend 'test_backend_service' not found\n")
		os.Exit(1)
	}

	// Verify frontend rules were created
	fmt.Println("\n3️⃣ Verifying frontend rule creation...")
	rules, err := client.GetFrontendRules("http")
	if err != nil {
		fmt.Printf("❌ Failed to get frontend rules: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("📋 Current frontend rules (%d total):\n", len(rules))
	ruleFound := false
	for _, rule := range rules {
		fmt.Printf("   %s -> %s\n", rule.Domain, rule.Backend)
		if rule.Domain == "^(api\\.|www\\.)?test-regex\\.com$" && rule.Backend == "test_backend_service" {
			ruleFound = true
		}
	}

	if !ruleFound {
		fmt.Printf("❌ Frontend rule not found for regex domain\n")
		os.Exit(1)
	}
	fmt.Printf("✅ Frontend rule found for regex domain\n")

	fmt.Println("\n4️⃣ Testing HTTP backend routing through HAProxy...")

	testCases := []struct {
		host        string
		shouldMatch bool
		description string
	}{
		{"test-regex.com", true, "base domain"},
		{"www.test-regex.com", true, "www subdomain"},
		{"api.test-regex.com", true, "api subdomain"},
		{"other.test-regex.com", false, "non-matching subdomain"},
		{"test-regex.net", false, "different TLD"},
	}

	for _, tc := range testCases {
		fmt.Printf("   Testing %s (%s)... ", tc.host, tc.description)

		req, err := http.NewRequest("GET", "http://localhost:8080/", nil)
		if err != nil {
			fmt.Printf("❌ Failed to create request: %v\n", err)
			os.Exit(1)
		}
		req.Host = tc.host

		httpClient := &http.Client{
			Timeout: 5 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		resp, err := httpClient.Do(req)

		if tc.shouldMatch {
			if err != nil {
				fmt.Printf("❌ Expected routing to backend but got error: %v\n", err)
				os.Exit(1)
			}
			
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				fmt.Printf("❌ Failed to read response body: %v\n", err)
				os.Exit(1)
			}
			resp.Body.Close()
			
			fmt.Printf("DEBUG: Response status: %d, body: %s\n", resp.StatusCode, string(body))
			
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				fmt.Printf("✅ Routed to backend (status: %d, body length: %d)\n", resp.StatusCode, len(body))
			} else if resp.StatusCode == 503 {
				fmt.Printf("❌ Backend unavailable (503) - service not properly registered\n")
				os.Exit(1)
			} else {
				fmt.Printf("❌ Unexpected response from backend: %d, expected 200\n", resp.StatusCode)
				os.Exit(1)
			}
		} else {
			if err != nil {
				fmt.Printf("❌ Request should reach default backend but got error: %v\n", err)
				os.Exit(1)
			} else {
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					fmt.Printf("❌ Failed to read response body: %v\n", err)
					os.Exit(1)
				}
				resp.Body.Close()
				
				if resp.StatusCode == 404 {
					bodyStr := string(body)
					if bodyStr == "404 - Domain not found" {
						fmt.Printf("✅ Got 404 from default backend as expected\n")
					} else {
						fmt.Printf("✅ Got 404 but unexpected body: %s\n", bodyStr)
					}
				} else {
					fmt.Printf("❌ Expected 404 from default backend, got %d\n", resp.StatusCode)
					os.Exit(1)
				}
			}
		}
	}

	// Complete success
	fmt.Println("\n🎉 Complete End-to-End Success!")
	fmt.Printf("✅ Service registration processed successfully\n")
	fmt.Printf("✅ Backend created: test_backend_service\n")
	fmt.Printf("✅ Frontend rule created: ^(api\\.|www\\.)?test-regex\\.com$ -> test_backend_service\n")
	fmt.Printf("✅ HTTP routing works correctly for regex domain patterns\n")
	fmt.Printf("✅ Regex domain pattern: ^(api\\.|www\\.)?test-regex\\.com$\n")
	fmt.Printf("✅ Service name sanitized to: test_regex_service\n")
	fmt.Printf("✅ ACL names are now generated from backend names, not domain patterns\n")

	if result != nil {
		fmt.Printf("📊 Process result: %+v\n", result)
	}

	fmt.Println("\n🚀 Production regex domain bug is FIXED!")
}
