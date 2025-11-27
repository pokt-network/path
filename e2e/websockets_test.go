//go:build e2e

// Package e2e provides Websocket testing functionality for PATH E2E tests.
// This file contains the Websocket test client and related functions for testing
// JSON-RPC over Websocket connections, with full integration into the existing
// E2E test framework and validation logic.
//
// WEBSOCKET ARCHITECTURE:
// - Uses a single persistent Websocket connection per service to test all EVM methods
// - Sends the configured number of requests per method sequentially over the same connection
// - Provides progress bars and metrics collection similar to HTTP tests
// - Only tests EVM JSON-RPC methods (not CometBFT or REST endpoints)
//
// This separation ensures clean boundaries between different transport protocols
// while maintaining shared validation logic in assertions_test.go.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/pokt-network/path/qos/jsonrpc"
)

// websocketTestClient handles Websocket connections and JSON-RPC requests.
//
// This client provides a complete Websocket testing solution that:
// - Converts HTTP/HTTPS gateway URLs to Websocket (WS/WSS) equivalents
// - Establishes persistent Websocket connections with proper headers
// - Sends JSON-RPC requests over Websocket and validates responses
// - Integrates with the existing E2E test validation framework
// - Supports all EVM JSON-RPC methods defined in service_evm_test.go
//
// Usage:
//
//	client := newWebSocketTestClient(gatewayURL, serviceID)
//	err := client.connect(ctx)
//	results, err := client.sendEVMRequestsFromServiceParams(ctx, serviceParams)
//	client.close()
type websocketTestClient struct {
	conn       *websocket.Conn
	serviceID  string
	gatewayURL string
	mutex      sync.RWMutex
	closed     bool
}

// newWebSocketTestClient creates a new Websocket test client
func newWebSocketTestClient(gatewayURL, serviceID string) *websocketTestClient {
	return &websocketTestClient{
		serviceID:  serviceID,
		gatewayURL: gatewayURL,
	}
}

// connect establishes a Websocket connection to the PATH gateway
func (c *websocketTestClient) connect(ctx context.Context) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	// Convert HTTP/HTTPS URL to WS/WSS URL
	wsURL, err := convertToWebSocketURL(c.gatewayURL)
	if err != nil {
		return fmt.Errorf("failed to convert gateway URL to Websocket URL: %w", err)
	}

	// Set up headers including the Target-Service-Id
	headers := http.Header{
		"Target-Service-Id": []string{c.serviceID},
	}

	// Create Websocket dialer with timeout
	dialer := websocket.Dialer{
		HandshakeTimeout: 30 * time.Second,
	}

	// Establish Websocket connection
	conn, resp, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("Websocket dial failed with status %d: %w", resp.StatusCode, err)
		}
		return fmt.Errorf("Websocket dial failed: %w", err)
	}

	c.conn = conn
	c.closed = false

	return nil
}

// sendJSONRPCRequest sends a JSON-RPC request over the Websocket connection
func (c *websocketTestClient) sendJSONRPCRequest(ctx context.Context, req jsonrpc.Request) (*jsonrpc.Response, error) {
	c.mutex.RLock()
	if c.closed || c.conn == nil {
		c.mutex.RUnlock()
		return nil, fmt.Errorf("Websocket connection is not open")
	}
	conn := c.conn
	c.mutex.RUnlock()

	// Marshal the JSON-RPC request
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON-RPC request: %w", err)
	}

	// Set write deadline
	if err := conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return nil, fmt.Errorf("failed to set write deadline: %w", err)
	}

	// Send the request
	if err := conn.WriteMessage(websocket.TextMessage, reqBytes); err != nil {
		return nil, fmt.Errorf("failed to send Websocket message: %w", err)
	}

	// Set read deadline
	if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return nil, fmt.Errorf("failed to set read deadline: %w", err)
	}

	// Read the response
	_, respBytes, err := conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("failed to read Websocket response: %w", err)
	}

	// Parse the JSON-RPC response
	var resp jsonrpc.Response
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON-RPC response: %w", err)
	}

	return &resp, nil
}

// close closes the Websocket connection
func (c *websocketTestClient) close() error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.closed || c.conn == nil {
		return nil
	}

	c.closed = true

	// Send close message
	err := c.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	if err != nil {
		// If we can't send close message, just close the connection
		return c.conn.Close()
	}

	// Wait for close confirmation or timeout
	select {
	case <-time.After(time.Second):
		// Timeout waiting for close confirmation
	default:
		// Try to read close message
		c.conn.SetReadDeadline(time.Now().Add(time.Second))
		_, _, _ = c.conn.ReadMessage()
	}

	return c.conn.Close()
}

// convertToWebSocketURL converts an HTTP/HTTPS URL to WS/WSS URL
func convertToWebSocketURL(httpURL string) (string, error) {
	u, err := url.Parse(httpURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse URL: %w", err)
	}

	// Convert scheme
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported URL scheme: %s", u.Scheme)
	}

	return u.String(), nil
}

// websocketTestResult represents the result of a Websocket test
type websocketTestResult struct {
	Method   string
	Request  jsonrpc.Request
	Response *jsonrpc.Response
	Error    error
	Duration time.Duration
}

// validateWebSocketResults validates Websocket test results using the existing validation logic
func validateWebSocketResults(results []*websocketTestResult) map[string]*methodMetrics {
	metrics := make(map[string]*methodMetrics)

	for _, result := range results {
		// Initialize method metrics if not exists
		if metrics[result.Method] == nil {
			metrics[result.Method] = &methodMetrics{
				method:                  result.Method,
				statusCodes:             make(map[int]int),
				errors:                  make(map[string]int),
				results:                 make([]*VegetaResult, 0),
				jsonrpcParseErrors:      make(map[string]int),
				jsonrpcValidationErrors: make(map[string]int),
			}
		}

		methodMetrics := metrics[result.Method]

		// Create a VegetaResult for compatibility with existing metrics collection
		vegetaResult := &VegetaResult{
			Latency: result.Duration,
		}

		if result.Error != nil {
			methodMetrics.failed++
			methodMetrics.errors[result.Error.Error()]++
			vegetaResult.Code = 0 // No HTTP status for Websocket errors
			vegetaResult.Error = result.Error.Error()
			// For Websocket errors, we don't have a valid response body
			vegetaResult.Body = []byte{}
		} else {
			methodMetrics.success++
			vegetaResult.Code = 200 // Simulate successful Websocket connection

			// Validate JSON-RPC response if we have one
			if result.Response != nil {
				respBytes, _ := json.Marshal(result.Response)
				vegetaResult.Body = respBytes

				// Use the existing transport-agnostic JSON-RPC validation function
				_ = validateJSONRPCResponse(respBytes, jsonrpc.IDFromInt(1), methodMetrics)
			} else {
				vegetaResult.Body = []byte{}
			}
		}

		methodMetrics.results = append(methodMetrics.results, vegetaResult)
		methodMetrics.statusCodes[int(vegetaResult.Code)]++
	}

	// Calculate success rates and percentiles for all methods using existing functions
	for _, methodMetrics := range metrics {
		calculateAllSuccessRates(methodMetrics)
		calculatePercentiles(methodMetrics)
	}

	return metrics
}

// runWebSocketServiceTest runs Websocket-based E2E tests for a single service.
// This function uses a single Websocket connection to test all EVM methods sequentially.
func runWebSocketServiceTest(
	t *testing.T,
	ctx context.Context,
	ts *TestService,
	results map[string]*methodMetrics,
	resultsMutex *sync.Mutex,
) (serviceTestFailed bool) {
	fmt.Printf("\n%s🔌 Starting Websocket tests for %s%s\n", BOLD_CYAN, ts.ServiceID, RESET)

	// Get only EVM methods for Websocket testing
	evmMethods := getEVMTestMethodsForWebSocket()

	// Get the service config from any method (they all share the same config)
	var serviceConfig testMethodConfig
	for _, config := range ts.testMethodsMap {
		serviceConfig = config
		break
	}

	// Create a simplified method map for progress bars (EVM methods only)
	evmMethodsMap := make(map[string]testMethodConfig)
	for _, method := range evmMethods {
		evmMethodsMap[method] = serviceConfig
	}

	// Create progress bars for Websocket tests (EVM methods only)
	progBars, err := newProgressBars(evmMethodsMap)
	if err != nil {
		t.Fatalf("Failed to create progress bars for Websocket tests: %v", err)
	}
	defer func() {
		if err := progBars.finish(); err != nil {
			fmt.Printf("Error stopping Websocket progress bars: %v", err)
		}
	}()

	// Create a single Websocket client for all methods
	client := newWebSocketTestClient(serviceConfig.target.URL, string(ts.ServiceID))

	// Connect to Websocket once
	if err := client.connect(ctx); err != nil {
		t.Errorf("❌ Failed to connect Websocket for service %s: %v", ts.ServiceID, err)
		return true
	}
	defer client.close()

	fmt.Printf("✅ Websocket connected successfully for service %s\n", ts.ServiceID)

	// Run all method tests sequentially using the single connection
	allResults := runAllMethodsOnSingleConnection(ctx, client, evmMethods, ts, progBars)

	// Finish progress bars
	if err := progBars.finish(); err != nil {
		fmt.Printf("Error stopping Websocket progress bars: %v", err)
	}

	// Convert Websocket results to method metrics and validate
	websocketMetrics := validateWebSocketResults(allResults)

	// Use the service configuration we retrieved earlier
	wsServiceConfig := serviceConfig.serviceConfig

	// Add Websocket results to the results map (no labeling needed since tests are separate)
	resultsMutex.Lock()
	for method, metrics := range websocketMetrics {
		results[method] = metrics

		// Validate individual Websocket method results using existing assertion logic
		if !validateMethodResults(t, ts.ServiceID, metrics, wsServiceConfig, ts.ServiceParams) {
			serviceTestFailed = true
		}
	}
	resultsMutex.Unlock()

	if serviceTestFailed {
		fmt.Printf("%s❌ Websocket tests failed for service %s%s\n", RED, ts.ServiceID, RESET)
	} else {
		fmt.Printf("%s✅ Websocket tests passed for service %s%s\n", GREEN, ts.ServiceID, RESET)
	}

	return serviceTestFailed
}

// runAllMethodsOnSingleConnection runs load tests for all EVM methods using a single Websocket connection.
// This function sends the configured number of requests per method sequentially over the same connection.
func runAllMethodsOnSingleConnection(
	ctx context.Context,
	client *websocketTestClient,
	evmMethods []string,
	ts *TestService,
	progBars *progressBars,
) []*websocketTestResult {
	var allResults []*websocketTestResult

	// Get service configuration
	var methodConfig testMethodConfig
	for _, config := range ts.testMethodsMap {
		methodConfig = config
		break
	}

	// Test each method sequentially using the same connection
	for _, method := range evmMethods {
		// Send the configured number of requests for this method
		for i := 0; i < methodConfig.serviceConfig.RequestsPerMethod; i++ {
			select {
			case <-ctx.Done():
				return allResults
			default:
			}

			start := time.Now()

			params := createEVMJsonRPCParams(jsonrpc.Method(method), ts.ServiceParams)
			req := jsonrpc.Request{
				JSONRPC: jsonrpc.Version2,
				ID:      jsonrpc.IDFromInt(1), // Always use ID 1 for consistency
				Method:  jsonrpc.Method(method),
				Params:  params,
			}

			resp, err := client.sendJSONRPCRequest(ctx, req)
			duration := time.Since(start)

			result := &websocketTestResult{
				Method:   method,
				Request:  req,
				Response: resp,
				Error:    err,
				Duration: duration,
			}

			allResults = append(allResults, result)

			// Update progress bar for this method
			if progBar := progBars.get(method); progBar != nil {
				if progBar.Current() < int64(methodConfig.serviceConfig.RequestsPerMethod) {
					progBar.Increment()
				}
			}

			// Add small delay between requests to avoid overwhelming the connection
			time.Sleep(10 * time.Millisecond)
		}

		// Ensure progress bar is complete for this method
		if progBar := progBars.get(method); progBar != nil {
			if progBar.Current() < int64(methodConfig.serviceConfig.RequestsPerMethod) {
				remaining := int64(methodConfig.serviceConfig.RequestsPerMethod) - progBar.Current()
				progBar.Add64(remaining)
			}
		}
	}

	return allResults
}
