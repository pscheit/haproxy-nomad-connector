package haproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
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
			Timeout: 10 * time.Second,
		},
	}
}

// GetInfo gets Data Plane API information
func (c *Client) GetInfo() (*APIInfo, error) {
	var info APIInfo
	err := c.makeRequest("GET", "/v3/info", nil, &info, 0)
	return &info, err
}

// GetConfigVersion gets the current configuration version
func (c *Client) GetConfigVersion() (int, error) {
	resp, err := c.makeRawRequest("GET", "/v3/services/haproxy/configuration/version", nil, 0)
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

// GetBackends lists all backends
func (c *Client) GetBackends() ([]Backend, error) {
	var backends []Backend
	err := c.makeRequest("GET", "/v3/services/haproxy/configuration/backends", nil, &backends, 0)
	return backends, err
}

// CreateBackend creates a new backend
func (c *Client) CreateBackend(backend Backend, version int) (*Backend, error) {
	var created Backend
	err := c.makeRequest("POST", "/v3/services/haproxy/configuration/backends", backend, &created, version)
	return &created, err
}

// DeleteBackend deletes a backend
func (c *Client) DeleteBackend(name string, version int) error {
	path := fmt.Sprintf("/v3/services/haproxy/configuration/backends/%s", name)
	return c.makeRequest("DELETE", path, nil, nil, version)
}

// CreateServer adds a server to a backend
func (c *Client) CreateServer(backendName string, server Server, version int) (*Server, error) {
	var created Server
	path := fmt.Sprintf("/v3/services/haproxy/configuration/backends/%s/servers", backendName)
	err := c.makeRequest("POST", path, server, &created, version)
	return &created, err
}

// DeleteServer removes a server from a backend
func (c *Client) DeleteServer(backendName, serverName string, version int) error {
	path := fmt.Sprintf("/v3/services/haproxy/configuration/backends/%s/servers/%s", backendName, serverName)
	return c.makeRequest("DELETE", path, nil, nil, version)
}

// makeRequest is a helper for making authenticated HTTP requests
func (c *Client) makeRequest(method, path string, body interface{}, result interface{}, version int) error {
	resp, err := c.makeRawRequest(method, path, body, version)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
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
	if version > 0 && (method == "POST" || method == "PUT" || method == "DELETE") {
		separator := "?"
		if strings.Contains(url, "?") {
			separator = "&"
		}
		url += fmt.Sprintf("%sversion=%d", separator, version)
	}

	var bodyReader io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequest(method, url, bodyReader)
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