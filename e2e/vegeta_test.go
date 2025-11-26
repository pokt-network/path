//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cheggaaa/pb/v3"
	vegeta "github.com/tsenart/vegeta/lib"

	"github.com/pokt-network/path/log"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// This file contains HTTP-specific test configuration, execution, and metrics collection.
// It uses the Vegeta load testing library (https://github.com/tsenart/vegeta) to perform
// high-performance HTTP requests against PATH gateway endpoints.
//
// VEGETA INTEGRATION:
// - Vegeta is an HTTP load testing tool and library written in Go
// - Repository: https://github.com/tsenart/vegeta
// - Used for generating HTTP traffic at configurable rates and durations
// - Provides detailed latency metrics and response validation
//
// TEST FLOW:
// 1. Service configuration defines test parameters (RPS, request count, latency thresholds)
// 2. Each JSON-RPC method gets converted to Vegeta HTTP targets with proper headers/body
// 3. Vegeta attackers execute parallel HTTP requests at specified rates
// 4. Results are collected and validated for HTTP status codes and JSON-RPC responses
// 5. Metrics are aggregated and compared against configured thresholds
// 6. Service summaries provide overall test results and failure analysis

// ===== Type Aliases for Vegeta =====
// These aliases allow us to use Vegeta types in our exported structs
// while maintaining clean separation between packages
type VegetaResult = vegeta.Result

// ===== Metrics Types (Exported for use across test files) =====

// methodMetrics stores metrics for each method
// Tracks HTTP and JSON-RPC results and derived rates
// Used for assertion and reporting
type methodMetrics struct {
	method       string          // RPC method name
	success      int             // Number of successful requests
	failed       int             // Number of failed requests
	statusCodes  map[int]int     // Count of each status code
	errors       map[string]int  // Count of each error type
	results      []*VegetaResult // All raw results for this method
	requestCount int             // Total number of requests
	successRate  float64         // Success rate as a ratio (0-1)
	p50          time.Duration   // 50th percentile latency
	p95          time.Duration   // 95th percentile latency
	p99          time.Duration   // 99th percentile latency

	// JSON-RPC specific validation metrics
	jsonrpcResponses       int // Count of responses we could unmarshal as JSON-RPC
	jsonrpcUnmarshalErrors int // Count of responses we couldn't unmarshal
	jsonrpcErrorField      int // Count of responses with non-nil Error field
	jsonrpcNilResult       int // Count of responses with nil Result field
	jsonrpcValidateErrors  int // Count of responses that fail validation

	// Error tracking with response previews
	jsonrpcParseErrors      map[string]int // Parse errors with response previews
	jsonrpcValidationErrors map[string]int // Validation errors with response previews

	// Success rates for specific checks
	jsonrpcSuccessRate    float64 // Success rate for JSON-RPC unmarshaling
	jsonrpcErrorFieldRate float64 // Error field absent rate (success = no error)
	jsonrpcResultRate     float64 // Non-nil result rate
	jsonrpcValidateRate   float64 // Validation success rate
}

// serviceSummary holds aggregated metrics for a service
// Used for service-level reporting
type serviceSummary struct {
	ServiceID protocol.ServiceID

	AvgP50Latency  time.Duration
	AvgP90Latency  time.Duration
	AvgLatency     time.Duration
	AvgSuccessRate float64

	TotalRequests int
	TotalSuccess  int
	TotalFailure  int

	ServiceConfig ServiceConfig
	MethodErrors  map[string]map[string]int
	MethodCount   int
	TotalErrors   int
}

// NewServiceSummary creates a new service summary
func newServiceSummary(
	serviceID protocol.ServiceID,
	serviceConfig ServiceConfig,
	testMethodsMap map[string]testMethodConfig,
) *serviceSummary {
	return &serviceSummary{
		ServiceID:     serviceID,
		ServiceConfig: serviceConfig,
		MethodErrors:  make(map[string]map[string]int),
		MethodCount:   len(testMethodsMap),
	}
}

// ===== Vegeta HTTP Test Functions =====
//
// The following functions implement the HTTP testing workflow using Vegeta:
// 1. runServiceTest() - Entry point for standalone HTTP testing
// 2. runHTTPServiceTest() - Core HTTP test execution for a service
// 3. runMethodAttack() - Individual method attack coordination
// 4. runAttack() - Single method load test execution
// 5. Progress bar management and result processing

// runHTTPServiceTest runs the HTTP-based E2E test for a single service using Vegeta.
// This function focuses exclusively on HTTP request testing and metrics collection.
// Results are populated into the provided results map for further validation.
func runHTTPServiceTest(
	t *testing.T,
	ctx context.Context,
	ts *TestService,
	results map[string]*methodMetrics,
	resultsMutex *sync.Mutex,
) {
	progBars, err := newProgressBars(ts.testMethodsMap)
	if err != nil {
		t.Fatalf("Failed to create progress bars: %v", err)
	}
	defer func() {
		if err := progBars.finish(); err != nil {
			fmt.Printf("Error stopping progress bars: %v", err)
		}
	}()

	var methodWg sync.WaitGroup
	for method := range ts.testMethodsMap {
		methodWg.Add(1)

		go func(ctx context.Context, method string, methodConfig testMethodConfig) {
			defer methodWg.Done()

			metrics := runMethodAttack(ctx, method, ts, progBars.get(method))

			resultsMutex.Lock()
			results[method] = metrics
			resultsMutex.Unlock()

		}(ctx, method, ts.testMethodsMap[method])
	}
	methodWg.Wait()

	if err := progBars.finish(); err != nil {
		fmt.Printf("Error stopping progress bars: %v", err)
	}
}

// runMethodAttack executes the attack for a single JSON-RPC method and returns metrics.
func runMethodAttack(
	ctx context.Context,
	method string,
	ts *TestService,
	progBar *pb.ProgressBar,
) *methodMetrics {
	select {
	case <-ctx.Done():
		fmt.Printf("Method %s canceled", method)
		return nil
	default:
	}

	// We don't need to extract or modify the target anymore, just pass it through
	metrics := runAttack(ctx, method, ts, progBar)

	return metrics
}

// runAttack executes a Vegeta load test attack for a single JSON-RPC method.
//
// VEGETA TERMINOLOGY:
// • "Attack" = Vegeta's term for executing load tests against targets
// • "Target" = HTTP request configuration (URL, method, headers, body)
// • "Attacker" = Vegeta component that generates HTTP requests
// • "Rate" = Requests per second (RPS) for the attack
//
// This function sends `serviceConfig.RequestsPerMethod` requests at calculated RPS.
// See Vegeta documentation: https://github.com/tsenart/vegeta
func runAttack(
	ctx context.Context,
	method string,
	ts *TestService,
	progressBar *pb.ProgressBar,
) *methodMetrics {
	methodConfig := ts.testMethodsMap[method]

	// Calculate RPS per method, rounding up and ensuring at least 1 RPS
	attackRPS := max((methodConfig.serviceConfig.GlobalRPS+len(ts.testMethodsMap)-1)/len(ts.testMethodsMap), 1)

	// Initialize the method metrics
	metrics := initMethodMetrics(method, methodConfig.serviceConfig.RequestsPerMethod)

	// Use the target directly, no need to recreate it
	targeter := func(tgt *vegeta.Target) error {
		*tgt = methodConfig.target
		return nil
	}

	maxDuration := time.Duration(2*methodConfig.serviceConfig.RequestsPerMethod/attackRPS)*time.Second + 5*time.Second

	// Vegeta timeout is set to the 99th percentile latency of the method + 5 seconds
	// This is because the P99 latency is the highest latency band for test assertions.
	// We add 5 seconds to account for any unexpected delays.
	attacker := createVegetaAttacker(attackRPS, methodConfig.serviceConfig.MaxP99LatencyMS+5*time.Second)

	if progressBar == nil {
		fmt.Printf("Starting test for method %s (%d requests at %d GlobalRPS)...\n",
			method, methodConfig.serviceConfig.RequestsPerMethod, attackRPS,
		)
	}

	// Create a channel to collect results
	resultsChan := make(chan *vegeta.Result, methodConfig.serviceConfig.RequestsPerMethod)

	// Start a goroutine to process results
	var resultsWg sync.WaitGroup
	startResultsCollector(
		ts,
		method,
		methodConfig,
		metrics,
		resultsChan,
		&resultsWg,
		progressBar,
	)

	// Run the Vegeta attack
	attackCh := attacker.Attack(
		makeTargeter(methodConfig, targeter),
		vegeta.Rate{Freq: attackRPS, Per: time.Second},
		maxDuration,
		method,
	)

	// Run the attack loop, sending results to the channel and handling cancellation
	runVegetaAttackLoop(ctx, attackCh, resultsChan)

	close(resultsChan)

	// Wait for results processing with a timeout to prevent hanging
	done := make(chan struct{})
	go func() {
		resultsWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Results processing completed normally
	case <-ctx.Done():
		// Context was canceled, results processing may not be complete
		// This is acceptable as we're shutting down
	}

	calculateAllSuccessRates(metrics)
	calculatePercentiles(metrics)
	return metrics
}

// ===== Vegeta Configuration and Setup =====

// initMethodMetrics
// • Initializes methodMetrics struct for a method
func initMethodMetrics(method string, totalRequests int) *methodMetrics {
	return &methodMetrics{
		method:      method,
		statusCodes: make(map[int]int),
		errors:      make(map[string]int),
		results:     make([]*vegeta.Result, 0, totalRequests),
		// Initialize the error tracking fields
		jsonrpcParseErrors:      make(map[string]int),
		jsonrpcValidationErrors: make(map[string]int),
	}
}

// createVegetaAttacker
// • Sets up a vegeta attacker with fixed options
func createVegetaAttacker(rps int, timeout time.Duration) *vegeta.Attacker {
	return vegeta.NewAttacker(
		vegeta.Timeout(timeout),
		vegeta.KeepAlive(true),
		vegeta.Workers(uint64(max(rps/2, 1))),  // Ensure at least 1 worker
		vegeta.MaxWorkers(uint64(max(rps, 1))), // Ensure at least 1 max worker
	)
}

// ===== Vegeta Attack Execution =====

// startResultsCollector
// • Launches a goroutine to process results, update progress bar, print status
func startResultsCollector(
	ts *TestService,
	method string,
	methodConfig testMethodConfig,
	metrics *methodMetrics,
	resultsChan <-chan *vegeta.Result,
	resultsWg *sync.WaitGroup,
	progressBar *pb.ProgressBar,
) {
	processedCount := 0
	resultsWg.Add(1)
	go func() {
		defer resultsWg.Done()
		for res := range resultsChan {
			if res.Error == "no targets to attack" {
				continue
			}
			if processedCount < methodConfig.serviceConfig.RequestsPerMethod {
				processResult(metrics, res, ts.serviceType, methodConfig.target)
				processedCount++
				if progressBar != nil && progressBar.Current() < int64(methodConfig.serviceConfig.RequestsPerMethod) {
					progressBar.Increment()
				}
				if progressBar == nil && processedCount%50 == 0 {
					percent := float64(processedCount) / float64(methodConfig.serviceConfig.RequestsPerMethod) * 100
					fmt.Printf("  %s: %d/%d requests completed (%.1f%%)\n",
						method, processedCount, methodConfig.serviceConfig.RequestsPerMethod, percent)
				}
			}
		}
		if progressBar != nil && progressBar.Current() < int64(methodConfig.serviceConfig.RequestsPerMethod) {
			remaining := int64(methodConfig.serviceConfig.RequestsPerMethod) - progressBar.Current()
			progressBar.Add64(remaining)
		}
		if progressBar == nil {
			fmt.Printf("  %s: test completed (%d/%d requests)\n",
				method, processedCount, methodConfig.serviceConfig.RequestsPerMethod,
			)
		}
	}()
}

// makeTargeter
// • Returns a vegeta.Targeter that enforces the request limit
func makeTargeter(
	methodConfig testMethodConfig,
	target vegeta.Targeter,
) vegeta.Targeter {
	requestSlots := methodConfig.serviceConfig.RequestsPerMethod

	return func(tgt *vegeta.Target) error {
		if requestSlots <= 0 {
			return vegeta.ErrNoTargets
		}
		requestSlots--
		return target(tgt)
	}
}

// runVegetaAttackLoop
// • Runs the attack loop, sending results to the channel and handling cancellation
func runVegetaAttackLoop(
	ctx context.Context,
	attackCh <-chan *vegeta.Result,
	resultsChan chan<- *vegeta.Result,
) {
attackLoop:
	for {
		select {
		case <-ctx.Done():
			break attackLoop
		case res, ok := <-attackCh:
			if !ok {
				break attackLoop
			}
			// Use a select to avoid blocking on resultsChan send
			select {
			case resultsChan <- res:
				// Successfully sent result
			case <-ctx.Done():
				// Context canceled while trying to send
				break attackLoop
			}
		}
	}
}

// ===== HTTP Response Processing =====

// processJSONRPCResponse handles both single and batch JSON-RPC responses
func processJSONRPCResponse(body []byte, m *methodMetrics, serviceType serviceType) error {
	// Check if response is a batch (array) by looking at the first non-whitespace character
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) == 0 {
		return fmt.Errorf("empty response body")
	}

	if trimmed[0] == '[' {
		// Batch response - unmarshal as array
		var batchResponse []jsonrpc.Response
		if err := json.Unmarshal(body, &batchResponse); err != nil {
			return err
		}

		// For batch responses, validate that all expected IDs are present
		expectedIDs := []int{1, 2, 3} // Expected IDs for our batch request (eth_blockNumber, eth_chainId, eth_gasPrice)
		processBatchJSONRPCResponse(batchResponse, m, expectedIDs)
	} else {
		// Single response
		var rpcResponse jsonrpc.Response
		if err := json.Unmarshal(body, &rpcResponse); err != nil {
			return err
		}

		m.jsonrpcResponses++
		expectedID := getExpectedID(serviceType)
		processSingleJSONRPCResponseWithID(&rpcResponse, m, expectedID, "single")
	}

	return nil
}

// processBatchJSONRPCResponse validates and processes a batch of JSON-RPC responses
// It validates that all expected IDs are present regardless of order
func processBatchJSONRPCResponse(batchResponse []jsonrpc.Response, m *methodMetrics, expectedIDs []int) {
	// Create maps for tracking
	responsesByID := make(map[int]*jsonrpc.Response)
	expectedIDsMap := make(map[int]bool)

	// Build expected IDs map
	for _, id := range expectedIDs {
		expectedIDsMap[id] = true
	}

	// Parse responses and group by ID
	for i := range batchResponse {
		response := &batchResponse[i]
		m.jsonrpcResponses++

		// Extract ID as int - check if it's an integer ID
		if !response.ID.IsEmpty() {
			// Access the private intID field via the String() method and parse
			idStr := response.ID.String()
			if idStr != "null" {
				// Try to parse as integer for our batch validation
				var responseID int
				if n, err := fmt.Sscanf(idStr, "%d", &responseID); err == nil && n == 1 {
					responsesByID[responseID] = response
				} else {
					// Handle non-integer IDs as validation errors
					m.jsonrpcValidateErrors++
					errorMsg := fmt.Sprintf("[batch] Invalid ID type: expected integer, got %s", idStr)
					m.jsonrpcValidationErrors[errorMsg]++
					m.errors[errorMsg]++
					continue
				}
			} else {
				// Handle null ID
				m.jsonrpcValidateErrors++
				errorMsg := "[batch] Null ID in batch response"
				m.jsonrpcValidationErrors[errorMsg]++
				m.errors[errorMsg]++
				continue
			}
		} else {
			// Handle missing/empty ID
			m.jsonrpcValidateErrors++
			errorMsg := "[batch] Missing ID in response"
			m.jsonrpcValidationErrors[errorMsg]++
			m.errors[errorMsg]++
			continue
		}
	}

	// Validate that all expected IDs are present
	for _, expectedID := range expectedIDs {
		if response, found := responsesByID[expectedID]; found {
			// Process the response with the correct expected ID
			expectedIDObj := jsonrpc.IDFromInt(expectedID)
			processSingleJSONRPCResponseWithID(response, m, expectedIDObj, fmt.Sprintf("batch[id=%d]", expectedID))
		} else {
			// Missing expected ID
			m.jsonrpcValidateErrors++
			errorMsg := fmt.Sprintf("[batch] Missing expected ID %d in batch response", expectedID)
			m.jsonrpcValidationErrors[errorMsg]++
			m.errors[errorMsg]++
		}
	}

	// Check for unexpected IDs (extra responses)
	for responseID := range responsesByID {
		if !expectedIDsMap[responseID] {
			m.jsonrpcValidateErrors++
			errorMsg := fmt.Sprintf("[batch] Unexpected ID %d in batch response", responseID)
			m.jsonrpcValidationErrors[errorMsg]++
			m.errors[errorMsg]++
		}
	}
}

// processSingleJSONRPCResponseWithID validates and processes a single JSON-RPC response with explicit expected ID
func processSingleJSONRPCResponseWithID(rpcResponse *jsonrpc.Response, m *methodMetrics, expectedID jsonrpc.ID, context string) {
	// Validate the response first
	validationErr := rpcResponse.Validate(expectedID)

	// Check if Error field is nil (good)
	if rpcResponse.Error != nil {
		m.jsonrpcErrorField++
		// Only track the error field message if there's no validation error
		// (to avoid duplicate tracking when validation fails due to error field)
		if validationErr == nil {
			errorMsg := fmt.Sprintf("[%s] RPC Error: %s", context, rpcResponse.Error.Message)
			m.errors[errorMsg]++
		}
	}

	// Check if Result field is not nil (good)
	if rpcResponse.Result == nil {
		m.jsonrpcNilResult++
	}

	// Process validation error
	if validationErr != nil {
		m.jsonrpcValidateErrors++

		errorMsg := fmt.Sprintf("[%s] JSON-RPC validation error: %v", context, validationErr)
		m.jsonrpcValidationErrors[errorMsg]++
		m.errors[errorMsg]++
	}
}

// processResult
// • Updates metrics based on a single result
func processResult(
	m *methodMetrics,
	result *vegeta.Result,
	serviceType serviceType,
	target vegeta.Target,
) {
	// Skip "no targets to attack" errors (not actual requests)
	if result.Error == "no targets to attack" {
		return
	}
	// Store the raw result
	m.results = append(m.results, result)

	// Update status code counts
	m.statusCodes[int(result.Code)]++

	// Track if this request should be considered successful
	httpSuccess := result.Code >= 200 && result.Code < 300 && result.Error == ""

	// Determine if this is a REST request by checking HTTP method
	// This is more reliable than checking body length:
	// - REST requests use GET method (Cosmos SDK REST endpoints)
	// - JSON-RPC requests use POST method (EVM, Solana, CometBFT, etc.)
	isJSONRPCRequest := isJSONRPCRequest(target.Method, target.Body)

	// Track validation error counts before processing
	preValidationErrors := m.jsonrpcUnmarshalErrors + m.jsonrpcValidateErrors + m.jsonrpcErrorField

	// Process JSON-RPC validation only for JSON-RPC requests (not REST)
	if httpSuccess && isJSONRPCRequest {
		if err := processJSONRPCResponse(result.Body, m, serviceType); err != nil {
			m.jsonrpcUnmarshalErrors++

			// Create response preview for parse errors
			preview := log.Preview(string(result.Body))
			errorMsg := fmt.Sprintf("JSON parse error: %v (response preview: %s)", err, preview)
			m.jsonrpcParseErrors[errorMsg]++
			m.errors[errorMsg]++
		}
	}

	// Check if any validation errors occurred during processing
	postValidationErrors := m.jsonrpcUnmarshalErrors + m.jsonrpcValidateErrors + m.jsonrpcErrorField
	jsonrpcSuccess := httpSuccess && (postValidationErrors == preValidationErrors)

	// For REST requests, count success based only on HTTP status
	// For JSON-RPC requests, count success only if both HTTP and JSON-RPC validation succeed
	if isJSONRPCRequest {
		if jsonrpcSuccess {
			m.success++
		} else {
			m.failed++
		}
	} else {
		if httpSuccess {
			m.success++
		} else {
			m.failed++
		}
	}
}

// isJSONRPCRequest determines if the incoming HTTP request is a JSONRPC request
// Uses simple heuristics: POST method and specific content.
func isJSONRPCRequest(httpMethod string, httpRequestBody []byte) bool {
	// Stage 1: Non-POST requests are always REST
	if httpMethod != http.MethodPost {
		return false
	}

	// Stage 2: POST requests - check for JSONRPC payload
	if strings.Contains(string(httpRequestBody), "jsonrpc") {
		return true
	}

	// Stage 3: POST without jsonrpc field is REST
	return false
}

// calculateServiceSummary validates method results, aggregates summary metrics, and updates the service summary.
func calculateServiceSummary(
	t *testing.T,
	ts *TestService,
	results map[string]*methodMetrics,
) bool {
	var serviceTestFailed bool = false

	methodConfigs := ts.testMethodsMap
	summary := ts.summary
	serviceId := ts.ServiceID

	// Validate results for each method
	for method := range methodConfigs {
		metrics := results[method]

		// Skip methods with no data
		if metrics == nil || len(metrics.results) == 0 {
			continue
		}

		// Convert ServiceConfig to methodTestConfig for validation
		methodDef := methodConfigs[method]
		methodTestConfig := ServiceConfig{
			RequestsPerMethod: methodDef.serviceConfig.RequestsPerMethod,
			GlobalRPS:         methodDef.serviceConfig.GlobalRPS,
			SuccessRate:       methodDef.serviceConfig.SuccessRate,
			MaxP50LatencyMS:   methodDef.serviceConfig.MaxP50LatencyMS,
			MaxP95LatencyMS:   methodDef.serviceConfig.MaxP95LatencyMS,
			MaxP99LatencyMS:   methodDef.serviceConfig.MaxP99LatencyMS,
		}

		// Use the decoupled validation function
		if !validateMethodResults(t, serviceId, metrics, methodTestConfig, ts.ServiceParams) {
			serviceTestFailed = true
		}
	}

	// Calculate service averages using the decoupled calculation functions
	calculateServiceAverages(summary, results)
	collectServiceErrors(summary, results)

	return serviceTestFailed
}
