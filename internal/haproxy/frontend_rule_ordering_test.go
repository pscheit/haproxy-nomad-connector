package haproxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const (
	webappRegexDomain = `^[-a-zA-Z0-9._]+\.yaymemories\.com$`
	pictorDomain      = "pictor.yaymemories.com"
)

// fakeDataplane serves the DataPlane API endpoints used by the frontend rule code
// and records what the client writes.
type fakeDataplane struct {
	acls  []map[string]interface{}
	rules []map[string]interface{}

	putACLs      []map[string]interface{}
	putRules     []map[string]interface{}
	transactions int
	committed    bool
}

func (f *fakeDataplane) start(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == HTTPMethodGET && strings.Contains(r.URL.Path, "/configuration/version"):
			_, _ = w.Write([]byte("12"))

		case r.Method == HTTPMethodPOST && strings.HasSuffix(r.URL.Path, "/transactions"):
			f.transactions++
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "test-tx", "status": "in_progress"})

		case r.Method == HTTPMethodPUT && strings.Contains(r.URL.Path, "/transactions/test-tx"):
			f.committed = true
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"id": "test-tx", "status": "success"})

		case r.Method == HTTPMethodGET && strings.HasSuffix(r.URL.Path, "/acls"):
			_ = json.NewEncoder(w).Encode(f.acls)

		case r.Method == HTTPMethodGET && strings.HasSuffix(r.URL.Path, "/backend_switching_rules"):
			_ = json.NewEncoder(w).Encode(f.rules)

		case r.Method == HTTPMethodPUT && strings.HasSuffix(r.URL.Path, "/acls"):
			if err := json.NewDecoder(r.Body).Decode(&f.putACLs); err != nil {
				t.Errorf("Failed to decode ACL request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(f.putACLs)

		case r.Method == HTTPMethodPUT && strings.HasSuffix(r.URL.Path, "/backend_switching_rules"):
			if err := json.NewDecoder(r.Body).Decode(&f.putRules); err != nil {
				t.Errorf("Failed to decode backend switching rule request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(f.putRules)

		default:
			t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func acl(name, value string) map[string]interface{} {
	return map[string]interface{}{"acl_name": name, "criterion": "hdr(host)", "value": value}
}

func switchingRule(aclName, backend string) map[string]interface{} {
	return map[string]interface{}{"cond": "if", "cond_test": aclName, "name": backend}
}

func aclValues(acls []map[string]interface{}) []string {
	values := make([]string, 0, len(acls))
	for _, a := range acls {
		values = append(values, a["value"].(string))
	}
	return values
}

func ruleBackends(rules []map[string]interface{}) []string {
	backends := make([]string, 0, len(rules))
	for _, r := range rules {
		backends = append(backends, r["name"].(string))
	}
	return backends
}

func assertACLsMatchRules(t *testing.T, acls, rules []map[string]interface{}) {
	t.Helper()

	if len(acls) != len(rules) {
		t.Fatalf("Expected %d ACLs for %d switching rules", len(rules), len(acls))
	}
	for i := range rules {
		if acls[i]["acl_name"] != rules[i]["cond_test"] {
			t.Errorf("ACL/rule order diverges at position %d: acl %v, cond_test %v",
				i, acls[i]["acl_name"], rules[i]["cond_test"])
		}
	}
}

func TestSortFrontendRulesBySpecificity(t *testing.T) {
	rules := []FrontendRule{
		{Domain: webappRegexDomain, Backend: "webapp_prod", Type: DomainTypeRegex},
		{Domain: "portal.yayphotobooks.com", Backend: "photobooks_portal_prod", Type: DomainTypeExact},
		{Domain: `^(www\.)?ps-webforge\.com$`, Backend: "ps_webforge", Type: DomainTypeRegex},
		{Domain: pictorDomain, Backend: "pictor_prod", Type: DomainTypeExact},
	}

	sorted := sortFrontendRulesBySpecificity(rules)

	expected := []FrontendRule{
		{Domain: "portal.yayphotobooks.com", Backend: "photobooks_portal_prod", Type: DomainTypeExact},
		{Domain: pictorDomain, Backend: "pictor_prod", Type: DomainTypeExact},
		{Domain: webappRegexDomain, Backend: "webapp_prod", Type: DomainTypeRegex},
		{Domain: `^(www\.)?ps-webforge\.com$`, Backend: "ps_webforge", Type: DomainTypeRegex},
	}
	if !reflect.DeepEqual(sorted, expected) {
		t.Errorf("Expected sorted rules\n%+v\ngot\n%+v", expected, sorted)
	}
}

func TestSortFrontendRulesBySpecificity_KeepsInputUnchanged(t *testing.T) {
	rules := []FrontendRule{
		{Domain: webappRegexDomain, Backend: "webapp_prod", Type: DomainTypeRegex},
		{Domain: pictorDomain, Backend: "pictor_prod", Type: DomainTypeExact},
	}
	original := append([]FrontendRule(nil), rules...)

	sortFrontendRulesBySpecificity(rules)

	if !reflect.DeepEqual(rules, original) {
		t.Errorf("Expected input to stay unchanged\n%+v\ngot\n%+v", original, rules)
	}
}

func TestClient_AddFrontendRuleWithType_PlacesExactRuleBeforeRegexRule(t *testing.T) {
	fake := &fakeDataplane{
		acls:  []map[string]interface{}{acl("is_webapp_prod_69a242d1", "-m reg "+webappRegexDomain)},
		rules: []map[string]interface{}{switchingRule("is_webapp_prod_69a242d1", "webapp_prod")},
	}
	server := fake.start(t)
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")

	err := client.AddFrontendRuleWithType("https", pictorDomain, "pictor_prod", DomainTypeExact)
	if err != nil {
		t.Fatalf("AddFrontendRuleWithType failed: %v", err)
	}

	expectedBackends := []string{"pictor_prod", "webapp_prod"}
	if !reflect.DeepEqual(ruleBackends(fake.putRules), expectedBackends) {
		t.Errorf("Expected backend order %v, got %v", expectedBackends, ruleBackends(fake.putRules))
	}

	expectedValues := []string{pictorDomain, "-m reg " + webappRegexDomain}
	if !reflect.DeepEqual(aclValues(fake.putACLs), expectedValues) {
		t.Errorf("Expected ACL order %v, got %v", expectedValues, aclValues(fake.putACLs))
	}

	assertACLsMatchRules(t, fake.putACLs, fake.putRules)
}

func TestClient_ReorderFrontendRules_SortsExistingRules(t *testing.T) {
	fake := &fakeDataplane{
		acls: []map[string]interface{}{
			acl("is_webapp_prod_69a242d1", "-m reg "+webappRegexDomain),
			acl("is_pictor_prod_109776fd", pictorDomain),
		},
		rules: []map[string]interface{}{
			switchingRule("is_webapp_prod_69a242d1", "webapp_prod"),
			switchingRule("is_pictor_prod_109776fd", "pictor_prod"),
		},
	}
	server := fake.start(t)
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")

	if err := client.ReorderFrontendRules("https"); err != nil {
		t.Fatalf("ReorderFrontendRules failed: %v", err)
	}

	expectedBackends := []string{"pictor_prod", "webapp_prod"}
	if !reflect.DeepEqual(ruleBackends(fake.putRules), expectedBackends) {
		t.Errorf("Expected backend order %v, got %v", expectedBackends, ruleBackends(fake.putRules))
	}

	assertACLsMatchRules(t, fake.putACLs, fake.putRules)

	if !fake.committed {
		t.Error("Expected the reorder transaction to be committed")
	}
}

func TestClient_ReorderFrontendRules_DoesNotWriteWhenAlreadySorted(t *testing.T) {
	fake := &fakeDataplane{
		acls: []map[string]interface{}{
			acl("is_pictor_prod_109776fd", pictorDomain),
			acl("is_webapp_prod_69a242d1", "-m reg "+webappRegexDomain),
		},
		rules: []map[string]interface{}{
			switchingRule("is_pictor_prod_109776fd", "pictor_prod"),
			switchingRule("is_webapp_prod_69a242d1", "webapp_prod"),
		},
	}
	server := fake.start(t)
	defer server.Close()

	client := NewClient(server.URL, "admin", "password")

	if err := client.ReorderFrontendRules("https"); err != nil {
		t.Fatalf("ReorderFrontendRules failed: %v", err)
	}

	if fake.transactions != 0 {
		t.Errorf("Expected no transaction when rules are already sorted, got %d", fake.transactions)
	}
	if fake.putRules != nil || fake.putACLs != nil {
		t.Error("Expected no write when rules are already sorted")
	}
}
