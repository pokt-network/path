package gateway

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/qos/heuristic"
)

func TestAddRelayMetadataHeaders_AlwaysPresent(t *testing.T) {
	tests := []struct {
		name                    string
		suppliersTried          []string
		archivalRequestDetected bool
		retryCount              int
		expectedHeaders         map[string]string
	}{
		{
			name:                    "early error - no suppliers, not archival",
			suppliersTried:          nil,
			archivalRequestDetected: false,
			retryCount:              0,
			expectedHeaders: map[string]string{
				"X-Archival-Request": "false",
				"X-Suppliers-Tried":  "",
				"X-Retry-Count":      "0",
			},
		},
		{
			name:                    "archival error - detected but no suppliers",
			suppliersTried:          nil,
			archivalRequestDetected: true,
			retryCount:              0,
			expectedHeaders: map[string]string{
				"X-Archival-Request": "true",
				"X-Suppliers-Tried":  "",
				"X-Retry-Count":      "0",
			},
		},
		{
			name:                    "retry with suppliers",
			suppliersTried:          []string{"supplier1", "supplier2"},
			archivalRequestDetected: true,
			retryCount:              1,
			expectedHeaders: map[string]string{
				"X-Archival-Request": "true",
				"X-Suppliers-Tried":  "supplier1,supplier2",
				"X-Retry-Count":      "1",
			},
		},
		{
			name:                    "multiple retries with many suppliers",
			suppliersTried:          []string{"pokt1abc", "pokt1def", "pokt1ghi"},
			archivalRequestDetected: false,
			retryCount:              2,
			expectedHeaders: map[string]string{
				"X-Archival-Request": "false",
				"X-Suppliers-Tried":  "pokt1abc,pokt1def,pokt1ghi",
				"X-Retry-Count":      "2",
			},
		},
		{
			name:                    "archival request with single supplier",
			suppliersTried:          []string{"pokt1supplier"},
			archivalRequestDetected: true,
			retryCount:              0,
			expectedHeaders: map[string]string{
				"X-Archival-Request": "true",
				"X-Suppliers-Tried":  "pokt1supplier",
				"X-Retry-Count":      "0",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create minimal requestContext with test logger
			logger := polylog.Ctx(context.Background())
			rc := &requestContext{
				suppliersTried:          tt.suppliersTried,
				archivalRequestDetected: tt.archivalRequestDetected,
				retryCount:              tt.retryCount,
				logger:                  logger,
			}

			// Create response recorder
			w := httptest.NewRecorder()

			// Call addRelayMetadataHeaders
			rc.addRelayMetadataHeaders(w)

			// Verify all expected headers present
			for header, expected := range tt.expectedHeaders {
				actual := w.Header().Get(header)
				require.Equal(t, expected, actual, "header %s mismatch", header)
			}
		})
	}
}

func TestIsDeceptiveResponsePattern(t *testing.T) {
	tests := []struct {
		reason   string
		expected bool
	}{
		{"jsonrpc_invalid_empty_array", true},
		{"jsonrpc_empty_object_result", true},
		{"rest_protocol_mismatch", true},
		{"cometbft_invalid_empty_array", true},
		{"http_5xx", false},
		{"jsonrpc_success", false},
		{"rest_error_field", false},
		{"empty_response", false},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			assert.Equal(t, tt.expected, isDeceptiveResponsePattern(tt.reason))
		})
	}
}

func TestShouldCircuitBreak(t *testing.T) {
	tests := []struct {
		name           string
		result         *heuristic.AnalysisResult
		httpStatusCode int
		expected       bool
	}{
		// Nil heuristic result (transport errors, etc.) should circuit break
		{"nil_result", nil, 500, true},

		// HTTP 4xx (client errors) should NOT circuit break — domain is healthy
		{"400_bad_request", nil, 400, false},
		{"400_with_heuristic", &heuristic.AnalysisResult{
			ShouldRetry: true, MatchedPattern: "parse error",
		}, 400, false},
		{"401_unauthorized", nil, 401, false},
		{"404_not_found", nil, 404, false},
		{"429_rate_limited", nil, 429, false},

		// HTTP 5xx should circuit break
		{"500_server_error", nil, 500, true},
		{"502_bad_gateway", nil, 502, true},
		{"503_unavailable", nil, 503, true},

		// Transport error (status 0) should circuit break
		{"transport_error", nil, 0, true},

		// Archival-related errors should NOT circuit break
		{"historical_state", &heuristic.AnalysisResult{
			ShouldRetry: true, MatchedPattern: "historical state",
		}, 200, false},
		{"state_pruned", &heuristic.AnalysisResult{
			ShouldRetry: true, MatchedPattern: "state has been pruned",
		}, 200, false},
		{"is_pruned", &heuristic.AnalysisResult{
			ShouldRetry: true, MatchedPattern: "is pruned",
		}, 200, false},
		{"state_not_available", &heuristic.AnalysisResult{
			ShouldRetry: true, MatchedPattern: "state not available",
		}, 200, false},
		{"not_indexed", &heuristic.AnalysisResult{
			ShouldRetry: true, MatchedPattern: "haven't been fully indexed",
		}, 200, false},
		{"missing_trie_node", &heuristic.AnalysisResult{
			ShouldRetry: true, MatchedPattern: "missing trie node",
		}, 200, false},
		{"block_pruned", &heuristic.AnalysisResult{
			ShouldRetry: true, MatchedPattern: "block has been pruned",
		}, 200, false},
		{"height_not_available", &heuristic.AnalysisResult{
			ShouldRetry: true, MatchedPattern: "height is not available",
		}, 200, false},

		// Capability limitation errors should NOT circuit break (node works for other requests)
		{"capability_limitation", &heuristic.AnalysisResult{
			ShouldRetry: true, MatchedPattern: "capability_limitation",
		}, 200, false},

		// Non-archival errors SHOULD circuit break
		{"failed_fallback", &heuristic.AnalysisResult{
			ShouldRetry: true, MatchedPattern: "failed to call fallback",
		}, 200, true},
		{"mdbx_panic", &heuristic.AnalysisResult{
			ShouldRetry: true, MatchedPattern: "mdbx_panic",
		}, 200, true},
		{"bad_gateway", &heuristic.AnalysisResult{
			ShouldRetry: true, MatchedPattern: "bad gateway",
		}, 200, true},
		{"connection_refused", &heuristic.AnalysisResult{
			ShouldRetry: true, MatchedPattern: "connection refused",
		}, 200, true},
		{"empty_pattern", &heuristic.AnalysisResult{
			ShouldRetry: true, Reason: "html_error_page",
		}, 200, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, shouldCircuitBreak(tt.result, tt.httpStatusCode, nil))
		})
	}

	// Test error string fallback for archival/capability patterns (hedge_failed path where heuristicResult is nil)
	archivalErrorTests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil_result_with_archival_error", fmt.Errorf("raw_payload: historical state is not available"), false},
		{"nil_result_with_missing_trie_node", fmt.Errorf("hedge_failed: missing trie node abc123"), false},
		{"nil_result_with_state_pruned", fmt.Errorf("relay error: state has been pruned"), false},
		{"nil_result_with_lite_fullnode", fmt.Errorf("raw_payload: this API is closed because this node is a lite fullnode"), false},
		{"nil_result_with_api_not_supported", fmt.Errorf("raw_payload: api is not supported on this node"), false},
		{"nil_result_with_non_archival_error", fmt.Errorf("connection refused"), true},
		{"nil_result_with_nil_error", nil, true},
	}

	for _, tt := range archivalErrorTests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, shouldCircuitBreak(nil, 200, tt.err))
		})
	}
}
