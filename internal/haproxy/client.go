package haproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	HTTPMethodGET    = "GET"
	HTTPMethodPOST   = "POST"
	HTTPMethodPUT    = "PUT"
	HTTPMethodDELETE = "DELETE"
)

// Client configuration constants
const (
	DefaultClientTimeoutSec  = 10
	HTTPStatusClientErrorMin = 400
)

type Client struct {
	baseURL    string
	username   string
	password   string
	httpClient *http.Client
}

// NewClient creates a new HAProxy Data Plane API client
func NewClient(baseURL, username, password string) *Client {
	return &Client{
		baseURL:  baseURL,
		username: username,
		password: password,
		httpClient: &http.Client{
			Timeout: DefaultClientTimeoutSec * time.Second,
		},
	}
}

// GetInfo gets Data Plane API information
func (c *Client) GetInfo() (*APIInfo, error) {
	var info APIInfo
	err := c.makeRequest(HTTPMethodGET, "/v3/info", nil, &info, 0)
	return &info, err
}

// GetConfigVersion gets the current configuration version
func (c *Client) GetConfigVersion() (int, error) {
	resp, err := c.makeRawRequest(HTTPMethodGET, "/v3/services/haproxy/configuration/version", nil, 0)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	version, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil {
		return 0, fmt.Errorf("failed to parse version: %w", err)
	}

	return version, nil
}

func (c *Client) GetBackends() ([]Backend, error) {
	var backends []Backend
	err := c.makeRequest(HTTPMethodGET, "/v3/services/haproxy/configuration/backends", nil, &backends, 0)
	return backends, err
}

func (c *Client) GetBackend(name string) (*Backend, error) {
	var backend Backend
	path := fmt.Sprintf("/v3/services/haproxy/configuration/backends/%s", name)
	err := c.makeRequest(HTTPMethodGET, path, nil, &backend, 0)
	if err != nil {
		return nil, err
	}
	return &backend, nil
}

// CreateBackend creates a new backend
//
//nolint:gocritic // Backend struct matches API interface requirements
func (c *Client) CreateBackend(backend Backend, version int) (*Backend, error) {
	var created Backend
	err := c.makeRequest(HTTPMethodPOST, "/v3/services/haproxy/configuration/backends", backend, &created, version)
	return &created, err
}

// DeleteBackend deletes a backend
func (c *Client) DeleteBackend(name string, version int) error {
	path := fmt.Sprintf("/v3/services/haproxy/configuration/backends/%s", name)
	return c.makeRequest(HTTPMethodDELETE, path, nil, nil, version)
}

// CreateServer adds a server to a backend
func (c *Client) CreateServer(backendName string, server *Server, version int) (*Server, error) {
	var created Server
	path := fmt.Sprintf("/v3/services/haproxy/configuration/backends/%s/servers", backendName)
	err := c.makeRequest(HTTPMethodPOST, path, server, &created, version)
	return &created, err
}

// DeleteServer removes a server from a backend
func (c *Client) DeleteServer(backendName, serverName string, version int) error {
	path := fmt.Sprintf("/v3/services/haproxy/configuration/backends/%s/servers/%s", backendName, serverName)
	return c.makeRequest(HTTPMethodDELETE, path, nil, nil, version)
}

// GetRuntimeServer gets runtime server information
func (c *Client) GetRuntimeServer(backendName, serverName string) (*RuntimeServer, error) {
	var server RuntimeServer
	path := fmt.Sprintf("/v3/services/haproxy/runtime/backends/%s/servers/%s", backendName, serverName)
	err := c.makeRequest(HTTPMethodGET, path, nil, &server, 0)
	return &server, err
}

// SetServerState sets the administrative state of a server (ready, drain, maint)
func (c *Client) SetServerState(ctx context.Context, backendName, serverName, adminState string) error {
	path := fmt.Sprintf("/v3/services/haproxy/runtime/backends/%s/servers/%s", backendName, serverName)

	// Create the runtime server object with the new admin state
	server := RuntimeServer{
		AdminState: adminState,
	}

	return c.makeRequest(HTTPMethodPUT, path, server, nil, 0)
}

// DrainServer puts a server into drain mode (completes existing connections, no new ones)
func (c *Client) DrainServer(backendName, serverName string) error {
	return c.SetServerState(context.Background(), backendName, serverName, "drain")
}

// ReadyServer puts a server into ready mode (accepts new connections)
func (c *Client) ReadyServer(backendName, serverName string) error {
	return c.SetServerState(context.Background(), backendName, serverName, "ready")
}

// MaintainServer puts a server into maintenance mode (no connections)
func (c *Client) MaintainServer(backendName, serverName string) error {
	return c.SetServerState(context.Background(), backendName, serverName, "maint")
}

// makeRequest is a helper for making authenticated HTTP requests
func (c *Client) makeRequest(method, path string, body, result interface{}, version int) error {
	resp, err := c.makeRawRequest(method, path, body, version)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= HTTPStatusClientErrorMin {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	if result != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
	}

	return nil
}

// makeRawRequest makes the actual HTTP request
func (c *Client) makeRawRequest(method, path string, body interface{}, version int) (*http.Response, error) {
	url := c.baseURL + path

	// Add version parameter for operations that require it
	if version > 0 && (method == HTTPMethodPOST || method == HTTPMethodPUT || method == HTTPMethodDELETE) {
		separator := "?"
		if strings.Contains(url, "?") {
			separator = "&"
		}
		url += fmt.Sprintf("%sversion=%d", separator, version)
	}

	var bodyReader io.Reader = http.NoBody
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add authentication
	req.SetBasicAuth(c.username, c.password)

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	return c.httpClient.Do(req)
}

func (c *Client) GetServers(backendName string) ([]Server, error) {
	var servers []Server
	path := fmt.Sprintf("/v3/services/haproxy/configuration/backends/%s/servers", backendName)
	err := c.makeRequest(HTTPMethodGET, path, nil, &servers, 0)
	return servers, err
}

func IsBackendCompatibleForDynamicService(backend *Backend) bool {
	return backend.Balance.Algorithm == "roundrobin"
}

// AddFrontendRule adds a domain-to-backend routing rule to the specified frontend
func (c *Client) AddFrontendRule(frontend, domain, backend string) error {
	return c.AddFrontendRuleWithType(frontend, domain, backend, DomainTypeExact)
}

// AddFrontendRuleWithType adds a domain-to-backend routing rule with specific domain type
func (c *Client) AddFrontendRuleWithType(frontend, domain, backend string, domainType DomainType) error {
	// Create transaction
	transactionID, err := c.createTransaction()
	if err != nil {
		return fmt.Errorf("failed to create transaction: %w", err)
	}

	// Get current rules to append to
	currentRules, err := c.getFrontendRulesInTransaction(frontend, transactionID)
	if err != nil {
		return fmt.Errorf("failed to get current rules: %w", err)
	}

	// Add new rule (avoid duplicates)
	newRule := FrontendRule{Domain: domain, Backend: backend, Type: domainType}
	exists := false
	for i, rule := range currentRules {
		if rule.Domain == domain {
			// Update existing rule
			currentRules[i].Backend = backend
			currentRules[i].Type = domainType
			exists = true
			break
		}
	}
	if !exists {
		currentRules = append(currentRules, newRule)
	}

	// Update ACLs and backend switching rules
	if err := c.setFrontendRulesInTransaction(frontend, currentRules, transactionID); err != nil {
		return fmt.Errorf("failed to update rules: %w", err)
	}

	// Commit transaction
	if err := c.commitTransaction(transactionID); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// RemoveFrontendRule removes a domain routing rule from the specified frontend
// ResetFrontendRules clears all ACLs and backend switching rules for a frontend
func (c *Client) ResetFrontendRules(frontendName string) error {
	// Create transaction
	transactionID, err := c.createTransaction()
	if err != nil {
		return fmt.Errorf("failed to create transaction: %w", err)
	}

	// Clear all ACLs
	emptyACLs := []interface{}{}
	err = c.makeRequest(HTTPMethodPUT,
		fmt.Sprintf("/v3/services/haproxy/configuration/frontends/%s/acls?transaction_id=%s", frontendName, transactionID),
		emptyACLs, nil, 0)
	if err != nil {
		return fmt.Errorf("failed to clear ACLs: %w", err)
	}

	// Clear all backend switching rules
	emptyRules := []interface{}{}
	err = c.makeRequest(HTTPMethodPUT,
		fmt.Sprintf("/v3/services/haproxy/configuration/frontends/%s/backend_switching_rules?transaction_id=%s", frontendName, transactionID),
		emptyRules, nil, 0)
	if err != nil {
		return fmt.Errorf("failed to clear backend switching rules: %w", err)
	}

	// Commit transaction
	err = c.commitTransaction(transactionID)
	if err != nil {
		return fmt.Errorf("failed to commit reset transaction: %w", err)
	}

	return nil
}

func (c *Client) RemoveFrontendRule(frontend, domain string) error {
	// Create transaction
	transactionID, err := c.createTransaction()
	if err != nil {
		return fmt.Errorf("failed to create transaction: %w", err)
	}

	// Get current rules
	currentRules, err := c.getFrontendRulesInTransaction(frontend, transactionID)
	if err != nil {
		return fmt.Errorf("failed to get current rules: %w", err)
	}

	// Remove rule for domain
	var updatedRules []FrontendRule
	for _, rule := range currentRules {
		if rule.Domain != domain {
			updatedRules = append(updatedRules, rule)
		}
	}

	// Update ACLs and backend switching rules
	if err := c.setFrontendRulesInTransaction(frontend, updatedRules, transactionID); err != nil {
		return fmt.Errorf("failed to update rules: %w", err)
	}

	// Commit transaction
	if err := c.commitTransaction(transactionID); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// GetFrontendRules returns all domain-to-backend routing rules for the specified frontend
func (c *Client) GetFrontendRules(frontend string) ([]FrontendRule, error) {
	return c.getFrontendRulesInTransaction(frontend, "")
}

// Helper methods for transaction management and rule manipulation

func (c *Client) createTransaction() (string, error) {
	// Get current version
	version, err := c.GetConfigVersion()
	if err != nil {
		return "", fmt.Errorf("failed to get config version: %w", err)
	}

	// Create transaction
	var response map[string]interface{}
	path := fmt.Sprintf("/v3/services/haproxy/transactions?version=%d", version)
	err = c.makeRequest(HTTPMethodPOST, path, nil, &response, 0)
	if err != nil {
		return "", err
	}

	transactionID, ok := response["id"].(string)
	if !ok {
		return "", fmt.Errorf("invalid transaction ID in response")
	}

	return transactionID, nil
}

func (c *Client) commitTransaction(transactionID string) error {
	path := fmt.Sprintf("/v3/services/haproxy/transactions/%s", transactionID)
	var response map[string]interface{}
	return c.makeRequest(HTTPMethodPUT, path, nil, &response, 0)
}

func (c *Client) getFrontendRulesInTransaction(frontend, transactionID string) ([]FrontendRule, error) {
	// Get ACLs
	var acls []map[string]interface{}
	aclPath := fmt.Sprintf("/v3/services/haproxy/configuration/frontends/%s/acls", frontend)
	if transactionID != "" {
		aclPath += "?transaction_id=" + transactionID
	}
	if err := c.makeRequest(HTTPMethodGET, aclPath, nil, &acls, 0); err != nil {
		return nil, fmt.Errorf("failed to get ACLs: %w", err)
	}

	// Get backend switching rules
	var rules []map[string]interface{}
	rulePath := fmt.Sprintf("/v3/services/haproxy/configuration/frontends/%s/backend_switching_rules", frontend)
	if transactionID != "" {
		rulePath += "?transaction_id=" + transactionID
	}
	if err := c.makeRequest(HTTPMethodGET, rulePath, nil, &rules, 0); err != nil {
		return nil, fmt.Errorf("failed to get backend switching rules: %w", err)
	}

	// Match ACLs to backend switching rules
	var frontendRules []FrontendRule
	for _, rule := range rules {
		condTest, _ := rule["cond_test"].(string)
		backendName, _ := rule["name"].(string)

		// Find matching ACL
		for _, acl := range acls {
			aclName, _ := acl["acl_name"].(string)
			if aclName == condTest {
				value, _ := acl["value"].(string)

				// Strip -m reg prefix if present
				domain := value
				domainType := DomainTypeExact
				if strings.HasPrefix(value, "-m reg ") {
					domain = strings.TrimPrefix(value, "-m reg ")
					domainType = DomainTypeRegex
				}

				frontendRules = append(frontendRules, FrontendRule{
					Domain:  domain,
					Backend: backendName,
					Type:    domainType,
				})
				break
			}
		}
	}

	return frontendRules, nil
}

// hashDomain creates a short hash of the domain for use in ACL names
func hashDomain(domain string) string {
	hash := sha256.Sum256([]byte(domain))
	return fmt.Sprintf("%x", hash[:4]) // Use first 8 hex chars (4 bytes)
}

func (c *Client) setFrontendRulesInTransaction(frontend string, rules []FrontendRule, transactionID string) error {
	// Convert rules to ACLs and backend switching rules
	var acls []map[string]interface{}
	var backendRules []map[string]interface{}

	for _, rule := range rules {
		// Generate ACL name: backend + domain hash (safe for HAProxy, unique per domain+backend)
		aclName := fmt.Sprintf("is_%s_%s",
			strings.ReplaceAll(rule.Backend, "-", "_"),
			hashDomain(rule.Domain))

		value := rule.Domain
		if rule.Type == DomainTypeRegex {
			value = "-m reg " + rule.Domain
		}

		acl := map[string]interface{}{
			"acl_name":  aclName,
			"criterion": "hdr(host)",
			"value":     value,
		}

		if rule.Type == DomainTypeRegex {
			fmt.Printf("DEBUG: Adding regex ACL: %+v\n", acl)
		}

		acls = append(acls, acl)

		// Add backend switching rule
		backendRules = append(backendRules, map[string]interface{}{
			"cond":      "if",
			"cond_test": aclName,
			"name":      rule.Backend,
		})
	}

	// Update ACLs
	aclPath := fmt.Sprintf("/v3/services/haproxy/configuration/frontends/%s/acls?transaction_id=%s", frontend, transactionID)
	if err := c.makeRequest(HTTPMethodPUT, aclPath, acls, nil, 0); err != nil {
		return fmt.Errorf("failed to update ACLs: %w", err)
	}

	// Update backend switching rules
	rulePath := fmt.Sprintf("/v3/services/haproxy/configuration/frontends/%s/backend_switching_rules?transaction_id=%s",
		frontend, transactionID)
	if err := c.makeRequest(HTTPMethodPUT, rulePath, backendRules, nil, 0); err != nil {
		return fmt.Errorf("failed to update backend switching rules: %w", err)
	}

	return nil
}
