// Integration test for frontend rule management with real HAProxy
// Run with: go run test_frontend_rules.go
// Requires: docker-compose -f docker-compose.dev.yml up -d
package main

import (
	"fmt"
	"os"
	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
)

func main() {
	fmt.Println("🧪 Frontend Rule Management Integration Test")
	fmt.Println("============================================")
	
	// Check if HAProxy is accessible
	client := haproxy.NewClient("http://localhost:5555", "admin", "adminpwd")
	
	// Test 1: Basic rule addition
	fmt.Println("\n1️⃣ Testing rule addition...")
	testDomain := "integration-test.local"
	err := client.AddFrontendRule("https", testDomain, "test_backend")
	if err != nil {
		fmt.Printf("❌ Failed to add rule: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Added rule: %s -> test_backend\n", testDomain)
	
	// Test 2: Rule retrieval and verification
	fmt.Println("\n2️⃣ Testing rule retrieval...")
	rules, err := client.GetFrontendRules("https")
	if err != nil {
		fmt.Printf("❌ Failed to get rules: %v\n", err)
		os.Exit(1)
	}
	
	found := false
	fmt.Printf("📋 Current rules (%d total):\n", len(rules))
	for _, rule := range rules {
		fmt.Printf("   %s -> %s\n", rule.Domain, rule.Backend)
		if rule.Domain == testDomain && rule.Backend == "test_backend" {
			found = true
		}
	}
	
	if !found {
		fmt.Printf("❌ Added rule not found in results!\n")
		os.Exit(1)
	}
	fmt.Printf("✅ Rule verified in frontend configuration\n")
	
	// Test 3: Rule update (same domain, different backend)
	fmt.Println("\n3️⃣ Testing rule update...")
	err = client.AddFrontendRule("https", testDomain, "dynamic_backend")
	if err != nil {
		fmt.Printf("❌ Failed to update rule: %v\n", err)
		os.Exit(1)
	}
	
	rules, err = client.GetFrontendRules("https")
	if err != nil {
		fmt.Printf("❌ Failed to get rules after update: %v\n", err)
		os.Exit(1)
	}
	
	updated := false
	for _, rule := range rules {
		if rule.Domain == testDomain && rule.Backend == "dynamic_backend" {
			updated = true
			break
		}
	}
	
	if !updated {
		fmt.Printf("❌ Rule update failed!\n")
		os.Exit(1)
	}
	fmt.Printf("✅ Rule updated: %s -> dynamic_backend\n", testDomain)
	
	// Test 4: Rule removal
	fmt.Println("\n4️⃣ Testing rule removal...")
	err = client.RemoveFrontendRule("https", testDomain)
	if err != nil {
		fmt.Printf("❌ Failed to remove rule: %v\n", err)
		os.Exit(1)
	}
	
	rules, err = client.GetFrontendRules("https")
	if err != nil {
		fmt.Printf("❌ Failed to get rules after removal: %v\n", err)
		os.Exit(1)
	}
	
	stillExists := false
	for _, rule := range rules {
		if rule.Domain == testDomain {
			stillExists = true
			break
		}
	}
	
	if stillExists {
		fmt.Printf("❌ Rule removal failed - still exists!\n")
		os.Exit(1)
	}
	fmt.Printf("✅ Rule removed successfully\n")
	
	// Test 5: Multiple rules
	fmt.Println("\n5️⃣ Testing multiple rules...")
	testRules := []struct{domain, backend string}{
		{"test1.local", "test_backend"},
		{"test2.local", "dynamic_backend"},
		{"test3.local", "test_backend"},
	}
	
	for _, tr := range testRules {
		err := client.AddFrontendRule("https", tr.domain, tr.backend)
		if err != nil {
			fmt.Printf("❌ Failed to add rule %s: %v\n", tr.domain, err)
			os.Exit(1)
		}
	}
	
	rules, err = client.GetFrontendRules("https")
	if err != nil {
		fmt.Printf("❌ Failed to get rules: %v\n", err)
		os.Exit(1)
	}
	
	addedCount := 0
	for _, tr := range testRules {
		for _, rule := range rules {
			if rule.Domain == tr.domain && rule.Backend == tr.backend {
				addedCount++
				break
			}
		}
	}
	
	if addedCount != len(testRules) {
		fmt.Printf("❌ Multiple rules test failed: expected %d, found %d\n", len(testRules), addedCount)
		os.Exit(1)
	}
	fmt.Printf("✅ Multiple rules added successfully (%d rules)\n", len(testRules))
	
	// Cleanup
	fmt.Println("\n🧹 Cleaning up test rules...")
	for _, tr := range testRules {
		err := client.RemoveFrontendRule("https", tr.domain)
		if err != nil {
			fmt.Printf("⚠️  Failed to cleanup rule %s: %v\n", tr.domain, err)
		}
	}
	
	fmt.Println("\n🎉 All integration tests passed!")
	fmt.Println("✅ Frontend rule management is working correctly with real HAProxy")
}