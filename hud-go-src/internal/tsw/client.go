package tsw

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client is an HTTP client for the TSW CommAPI.
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	connected  bool
	mu         sync.RWMutex
	lastData   map[string]any
}

// NewClient creates a new TSW CommAPI client.
func NewClient() *Client {
	return &Client{
		baseURL:    "http://127.0.0.1:31270",
		httpClient: &http.Client{Timeout: 5 * time.Second},
		lastData:   make(map[string]any),
	}
}

// SetAPIKey sets the DTGCommKey used for authentication.
func (c *Client) SetAPIKey(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apiKey = key
}

// IsConnected returns whether the client has an active connection to the TSW API.
func (c *Client) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

// setConnected updates the connection state.
func (c *Client) setConnected(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connected = v
}

// Do makes an HTTP request to the TSW CommAPI with the DTGCommKey header.
func (c *Client) Do(method, path string, body string) ([]byte, error) {
	c.mu.RLock()
	key := c.apiKey
	c.mu.RUnlock()

	if key == "" {
		return nil, fmt.Errorf("no API key available")
	}

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}

	req, err := http.NewRequest(method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("DTGCommKey", key)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return data, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(data))
	}

	return data, nil
}

// TestConnection attempts a GET to /subscription/?Subscription=1 to verify connectivity.
func (c *Client) TestConnection() error {
	_, err := c.Do("GET", "/subscription/?Subscription=1", "")
	if err != nil {
		c.setConnected(false)
		return err
	}
	c.setConnected(true)
	return nil
}

// GetLastData returns a copy of the latest subscription data.
func (c *Client) GetLastData() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	// Return a shallow copy
	cp := make(map[string]any, len(c.lastData))
	for k, v := range c.lastData {
		cp[k] = v
	}
	return cp
}

// setLastData stores the latest subscription data.
func (c *Client) setLastData(data map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastData = data
}
