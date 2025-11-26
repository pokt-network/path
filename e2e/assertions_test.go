//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// ===== JSON-RPC Response Validation =====

// validateJSONRPCResponse validates a JSON-RPC response and updates metrics
// This is decoupled from HTTP/Websocket transport and can be used for both
func validateJSONRPCResponse(
	responseBody []byte,
	expectedID jsonrpc.ID,
	metrics *methodMetrics,
) error {
	var rpcResponse jsonrpc.Response

	// Try to unmarshal the response
	if err := json.Unmarshal(responseBody, &rpcResponse); err != nil {
		metrics.jsonrpcUnmarshalErrors++

		// Create response preview for parse errors
		preview := createResponsePreview(responseBody, 100)
		errorMsg := fmt.Sprintf("JSON parse error: %v (response preview: %s)", err, preview)
		metrics.jsonrpcParseErrors[errorMsg]++
		metrics.errors[errorMsg]++
		return fmt.Errorf("failed to unmarshal JSON-RPC response: %w", err)
	}

	metrics.jsonrpcResponses++

	// Validate the response structure
	validationErr := rpcResponse.Validate(expectedID)

	// Check if Error field is nil (good)
	if rpcResponse.Error != nil {
		metrics.jsonrpcErrorField++
		// Only track the error field message if there's no validation error
		// (to avoid duplicate tracking when validation fails due to error field)
		if validationErr == nil {
			metrics.errors[rpcResponse.Error.Message]++
		}
	}

	// Check if Result field is not nil (good)
	if rpcResponse.Result == nil {
		metrics.jsonrpcNilResult++
	}

	// Process validation error
	if validationErr != nil {
		metrics.jsonrpcValidateErrors++

		// Create response preview for validation errors
		preview := createResponsePreview(responseBody, 100)
		errorMsg := fmt.Sprintf("JSON-RPC validation error: %v (response preview: %s)", validationErr, preview)
		metrics.jsonrpcValidationErrors[errorMsg]++
		metrics.errors[errorMsg]++
		return validationErr
	}

	return nil
}

// createResponsePreview creates a sanitized preview of the response body for error logging
func createResponsePreview(body []byte, maxLen int) string {
	if len(body) == 0 {
		return "(empty)"
	}

	// Convert to string and normalize whitespace
	bodyStr := string(body)

	// Replace all whitespace characters with single spaces
	bodyStr = strings.ReplaceAll(bodyStr, "\n", " ")
	bodyStr = strings.ReplaceAll(bodyStr, "\r", " ")
	bodyStr = strings.ReplaceAll(bodyStr, "\t", " ")

	// Collapse multiple spaces into single spaces
	bodyStr = strings.Join(strings.Fields(bodyStr), " ")

	// Truncate if needed
	if len(bodyStr) <= maxLen {
		return bodyStr
	}
	if maxLen <= 3 {
		return bodyStr[:maxLen]
	}
	return bodyStr[:maxLen-3] + "..."
}

// ===== Test Assertions =====

// validateMethodResults performs all assertions on test metrics for a single method
func validateMethodResults(
	t *testing.T,
	serviceID protocol.ServiceID,
	metrics *methodMetrics,
	config ServiceConfig,
	serviceParams ServiceParams,
) bool {
	// Create a slice to collect all assertion failures
	var failures []string

	// Print metrics with formatting
	printMethodMetrics(serviceID, metrics, config, serviceParams)

	// Collect all assertion failures
	failures = append(failures, collectHTTPSuccessRateFailures(metrics, config.SuccessRate)...)
	failures = append(failures, collectJSONRPCRatesFailures(metrics, config.SuccessRate)...)
	failures = append(failures, collectLatencyFailures(metrics, config)...)

	// Report failures if any
	if len(failures) > 0 {
		fmt.Printf("\n%s❌ Assertion failures for %s:%s\n", RED, metrics.method, RESET)
		for i, failure := range failures {
			fmt.Printf("   %s%d. %s%s\n", RED, i+1, failure, RESET)
		}
		t.Fail()
		return false
	}

	fmt.Printf("\n%s✅ Method %s passed all assertions%s\n", GREEN, metrics.method, RESET)
	return true
}

// ===== Failure Collection Functions =====

// collectHTTPSuccessRateFailures checks HTTP success rate and returns failure messages
func collectHTTPSuccessRateFailures(m *methodMetrics, requiredRate float64) []string {
	var failures []string

	if m.successRate < requiredRate {
		msg := fmt.Sprintf("HTTP success rate %.2f%% is below required %.2f%%",
			m.successRate*100, requiredRate*100)
		failures = append(failures, msg)
	}

	return failures
}

// collectJSONRPCRatesFailures checks all JSON-RPC success rates and returns failure messages
func collectJSONRPCRatesFailures(m *methodMetrics, requiredRate float64) []string {
	var failures []string

	// Skip if we don't have any JSON-RPC responses
	if m.jsonrpcResponses+m.jsonrpcUnmarshalErrors == 0 {
		return failures
	}

	// Check JSON-RPC unmarshal success rate
	if m.jsonrpcSuccessRate < requiredRate {
		msg := fmt.Sprintf("JSON-RPC unmarshal success rate %.2f%% is below required %.2f%%",
			m.jsonrpcSuccessRate*100, requiredRate*100)
		failures = append(failures, msg)
	}

	// Skip the rest if we don't have valid JSON-RPC responses
	if m.jsonrpcResponses == 0 {
		return failures
	}

	// Check Error field absence rate
	if m.jsonrpcErrorFieldRate < requiredRate {
		msg := fmt.Sprintf("JSON-RPC error field absence rate %.2f%% is below required %.2f%%",
			m.jsonrpcErrorFieldRate*100, requiredRate*100)
		failures = append(failures, msg)
	}

	// Check non-nil result rate
	if m.jsonrpcResultRate < requiredRate {
		msg := fmt.Sprintf("JSON-RPC non-nil result rate %.2f%% is below required %.2f%%",
			m.jsonrpcResultRate*100, requiredRate*100)
		failures = append(failures, msg)
	}

	// Check validation success rate
	if m.jsonrpcValidateRate < requiredRate {
		msg := fmt.Sprintf("JSON-RPC validation success rate %.2f%% is below required %.2f%%",
			m.jsonrpcValidateRate*100, requiredRate*100)
		failures = append(failures, msg)
	}

	return failures
}

// collectLatencyFailures checks latency metrics and returns failure messages
func collectLatencyFailures(m *methodMetrics, config ServiceConfig) []string {
	var failures []string

	// P50 latency check
	if m.p50 > config.MaxP50LatencyMS {
		msg := fmt.Sprintf("P50 latency %s exceeds maximum allowed %s",
			formatLatency(m.p50), formatLatency(config.MaxP50LatencyMS))
		failures = append(failures, msg)
	}

	// P95 latency check
	if m.p95 > config.MaxP95LatencyMS {
		msg := fmt.Sprintf("P95 latency %s exceeds maximum allowed %s",
			formatLatency(m.p95), formatLatency(config.MaxP95LatencyMS))
		failures = append(failures, msg)
	}

	// P99 latency check
	if m.p99 > config.MaxP99LatencyMS {
		msg := fmt.Sprintf("P99 latency %s exceeds maximum allowed %s",
			formatLatency(m.p99), formatLatency(config.MaxP99LatencyMS))
		failures = append(failures, msg)
	}

	return failures
}
