//go:build integration
// +build integration

// Integration test for frontend rule management with real HAProxy
// Run with: go test -tags=integration -v ./e2e/ -run TestFrontendRules
// Requires: docker-compose -f docker-compose.dev.yml up -d
package e2e

import (
	"testing"

	"github.com/pscheit/haproxy-nomad-connector/internal/haproxy"
)

func TestFrontendRules(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client := haproxy.NewClient("http://localhost:5555", "admin", "adminpwd")

	// Reset HAProxy frontend rules before starting the test
	t.Log("🧹 Resetting HAProxy frontend rules to clean state...")
	err := client.ResetFrontendRules("https")
	if err != nil {
		t.Fatalf("❌ Failed to reset frontend rules: %v", err)
	}
	t.Log("✅ HAProxy frontend rules reset successfully")

	testDomain := "integration-test.local"

	t.Run("RuleAddition", func(t *testing.T) {
		t.Log("1️⃣ Testing rule addition...")
		err := client.AddFrontendRule("https", testDomain, "test_backend")
		if err != nil {
			t.Fatalf("❌ Failed to add rule: %v", err)
		}
		t.Logf("✅ Added rule: %s -> test_backend", testDomain)
	})

	t.Run("RuleRetrieval", func(t *testing.T) {
		t.Log("2️⃣ Testing rule retrieval...")
		rules, err := client.GetFrontendRules("https")
		if err != nil {
			t.Fatalf("❌ Failed to get rules: %v", err)
		}

		found := false
		t.Logf("📋 Current rules (%d total):", len(rules))
		for _, rule := range rules {
			t.Logf("   %s -> %s", rule.Domain, rule.Backend)
			if rule.Domain == testDomain && rule.Backend == "test_backend" {
				found = true
			}
		}

		if !found {
			t.Fatalf("❌ Added rule not found in results!")
		}
		t.Logf("✅ Rule verified in frontend configuration")
	})

	t.Run("RuleUpdate", func(t *testing.T) {
		t.Log("3️⃣ Testing rule update...")
		err := client.AddFrontendRule("https", testDomain, "dynamic_backend")
		if err != nil {
			t.Fatalf("❌ Failed to update rule: %v", err)
		}

		rules, err := client.GetFrontendRules("https")
		if err != nil {
			t.Fatalf("❌ Failed to get rules after update: %v", err)
		}

		updated := false
		for _, rule := range rules {
			if rule.Domain == testDomain && rule.Backend == "dynamic_backend" {
				updated = true
				break
			}
		}

		if !updated {
			t.Fatalf("❌ Rule update failed!")
		}
		t.Logf("✅ Rule updated: %s -> dynamic_backend", testDomain)
	})

	t.Run("RuleRemoval", func(t *testing.T) {
		t.Log("4️⃣ Testing rule removal...")
		err := client.RemoveFrontendRule("https", testDomain)
		if err != nil {
			t.Fatalf("❌ Failed to remove rule: %v", err)
		}

		rules, err := client.GetFrontendRules("https")
		if err != nil {
			t.Fatalf("❌ Failed to get rules after removal: %v", err)
		}

		stillExists := false
		for _, rule := range rules {
			if rule.Domain == testDomain {
				stillExists = true
				break
			}
		}

		if stillExists {
			t.Fatalf("❌ Rule removal failed - still exists!")
		}
		t.Logf("✅ Rule removed successfully")
	})

	t.Run("MultipleRules", func(t *testing.T) {
		t.Log("5️⃣ Testing multiple rules...")
		testRules := []struct{ domain, backend string }{
			{"test1.local", "test_backend"},
			{"test2.local", "dynamic_backend"},
			{"test3.local", "test_backend"},
		}

		for _, tr := range testRules {
			err := client.AddFrontendRule("https", tr.domain, tr.backend)
			if err != nil {
				t.Fatalf("❌ Failed to add rule %s: %v", tr.domain, err)
			}
		}

		rules, err := client.GetFrontendRules("https")
		if err != nil {
			t.Fatalf("❌ Failed to get rules: %v", err)
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
			t.Fatalf("❌ Multiple rules test failed: expected %d, found %d", len(testRules), addedCount)
		}
		t.Logf("✅ Multiple rules added successfully (%d rules)", len(testRules))

		// Cleanup
		t.Log("🧹 Cleaning up test rules...")
		for _, tr := range testRules {
			err := client.RemoveFrontendRule("https", tr.domain)
			if err != nil {
				t.Logf("⚠️  Failed to cleanup rule %s: %v", tr.domain, err)
			}
		}
	})

	t.Log("🎉 All integration tests passed!")
	t.Log("✅ Frontend rule management is working correctly with real HAProxy")
}
