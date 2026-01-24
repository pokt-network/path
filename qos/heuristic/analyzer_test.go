package heuristic

import (
	"testing"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStructuralAnalysis(t *testing.T) {
	tests := []struct {
		name           string
		response       []byte
		expectedRetry  bool
		expectedStruct ResponseStructure
		expectedReason string
		minConfidence  float64
	}{
		{
			name:           "empty response",
			response:       []byte{},
			expectedRetry:  true,
			expectedStruct: StructureEmpty,
			expectedReason: "empty_response",
			minConfidence:  1.0,
		},
		{
			name:           "whitespace only response",
			response:       []byte("   \n\t  "),
			expectedRetry:  true,
			expectedStruct: StructureEmpty,
			expectedReason: "empty_response",
			minConfidence:  1.0,
		},
		{
			name:           "HTML error page",
			response:       []byte("<html><body>502 Bad Gateway</body></html>"),
			expectedRetry:  true,
			expectedStruct: StructureHTML,
			expectedReason: "html_error_page",
			minConfidence:  0.95,
		},
		{
			name:           "HTML doctype",
			response:       []byte("<!DOCTYPE html><html><body>Error</body></html>"),
			expectedRetry:  true,
			expectedStruct: StructureHTML,
			expectedReason: "html_error_page",
			minConfidence:  0.95,
		},
		{
			name:           "XML response",
			response:       []byte("<?xml version=\"1.0\"?><error>Something went wrong</error>"),
			expectedRetry:  true,
			expectedStruct: StructureXML,
			expectedReason: "xml_response",
			minConfidence:  0.90,
		},
		{
			name:           "plain text error - Bad Gateway",
			response:       []byte("Bad Gateway"),
			expectedRetry:  true,
			expectedStruct: StructureNonJSON,
			expectedReason: "non_json_response",
			minConfidence:  0.90,
		},
		{
			name:           "plain text error - Internal Server Error",
			response:       []byte("Internal Server Error"),
			expectedRetry:  true,
			expectedStruct: StructureNonJSON,
			expectedReason: "non_json_response",
			minConfidence:  0.90,
		},
		{
			name:           "malformed JSON - unclosed object",
			response:       []byte("{\"error\": \"test"),
			expectedRetry:  true,
			expectedStruct: StructureMalformed,
			expectedReason: "malformed_json",
			minConfidence:  0.85,
		},
		{
			name:           "valid JSON object",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":"0x123"}`),
			expectedRetry:  false,
			expectedStruct: StructureValid,
			expectedReason: "valid_structure",
			minConfidence:  0.0,
		},
		{
			name:           "valid JSON array",
			response:       []byte(`[{"id":1},{"id":2}]`),
			expectedRetry:  false,
			expectedStruct: StructureValid,
			expectedReason: "valid_structure",
			minConfidence:  0.0,
		},
		{
			name:           "JSON with leading whitespace",
			response:       []byte(`  {"result": "ok"}`),
			expectedRetry:  false,
			expectedStruct: StructureValid,
			expectedReason: "valid_structure",
			minConfidence:  0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StructuralAnalysis(tt.response)

			assert.Equal(t, tt.expectedRetry, result.ShouldRetry, "ShouldRetry mismatch")
			assert.Equal(t, tt.expectedStruct, result.Structure, "Structure mismatch")
			assert.Equal(t, tt.expectedReason, result.Reason, "Reason mismatch")
			assert.GreaterOrEqual(t, result.Confidence, tt.minConfidence, "Confidence too low")
		})
	}
}

func TestProtocolAnalysis_JSONRPC(t *testing.T) {
	tests := []struct {
		name           string
		response       []byte
		expectedRetry  bool
		expectedReason string
		minConfidence  float64
	}{
		{
			name:           "JSON-RPC success with result",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":"0x47f5d16"}`),
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "JSON-RPC success with null result",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":null}`),
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "JSON-RPC success with object result",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":{"blockNumber":"0x123"}}`),
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "JSON-RPC error response",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"server error"}}`),
			expectedRetry:  true,
			expectedReason: "jsonrpc_error_field",
			minConfidence:  0.85,
		},
		{
			name:           "JSON-RPC error with details",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-31002,"message":"invalid response","data":{"endpoint_response":"Bad Gateway"}}}`),
			expectedRetry:  true,
			expectedReason: "jsonrpc_error_field",
			minConfidence:  0.85,
		},
		{
			name:           "JSON-RPC missing both result and error (small)",
			response:       []byte(`{"jsonrpc":"2.0","id":1}`),
			expectedRetry:  true,
			expectedReason: "jsonrpc_missing_result",
			minConfidence:  0.80,
		},
		{
			name:           "Large response without result in prefix",
			response:       append([]byte(`{"jsonrpc":"2.0","id":1,"data":`), make([]byte, 1000)...),
			expectedRetry:  false,
			expectedReason: "large_no_result_in_prefix",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefixLen := min(512, len(tt.response))
			result := ProtocolAnalysis(tt.response[:prefixLen], len(tt.response), sharedtypes.RPCType_JSON_RPC)

			assert.Equal(t, tt.expectedRetry, result.ShouldRetry, "ShouldRetry mismatch")
			assert.Equal(t, tt.expectedReason, result.Reason, "Reason mismatch")
			if tt.minConfidence > 0 {
				assert.GreaterOrEqual(t, result.Confidence, tt.minConfidence, "Confidence too low")
			}
		})
	}
}

func TestProtocolAnalysis_REST(t *testing.T) {
	tests := []struct {
		name           string
		response       []byte
		expectedRetry  bool
		expectedReason string
	}{
		{
			name:           "REST success response",
			response:       []byte(`{"data":{"height":"12345","time":"2024-01-01"}}`),
			expectedRetry:  false,
			expectedReason: "rest_no_error_indicator",
		},
		{
			name:           "REST error at start",
			response:       []byte(`{"error":"block has been pruned"}`),
			expectedRetry:  true,
			expectedReason: "rest_error_field",
		},
		{
			name:           "REST code+message error",
			response:       []byte(`{"code":5,"message":"height not available"}`),
			expectedRetry:  true,
			expectedReason: "rest_code_message_error",
		},
		{
			name:           "REST error nested in data (not top-level)",
			response:       []byte(`{"data":{"error_count":5,"status":"ok"}}`),
			expectedRetry:  false,
			expectedReason: "rest_no_error_indicator",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefixLen := min(512, len(tt.response))
			result := ProtocolAnalysis(tt.response[:prefixLen], len(tt.response), sharedtypes.RPCType_REST)

			assert.Equal(t, tt.expectedRetry, result.ShouldRetry, "ShouldRetry mismatch")
			assert.Equal(t, tt.expectedReason, result.Reason, "Reason mismatch")
		})
	}
}

func TestIndicatorAnalysis(t *testing.T) {
	tests := []struct {
		name             string
		content          string
		expectedFound    bool
		expectedCategory ErrorCategory
		minConfidence    float64
	}{
		{
			name:             "Bad Gateway text",
			content:          "bad gateway error occurred",
			expectedFound:    true,
			expectedCategory: CategoryHTTPError,
			minConfidence:    0.90,
		},
		{
			name:             "Connection refused",
			content:          "dial tcp 127.0.0.1:8545: connection refused",
			expectedFound:    true,
			expectedCategory: CategoryConnectionError,
			minConfidence:    0.90,
		},
		{
			name:             "Rate limit error",
			content:          "you have exceeded your rate limit quota",
			expectedFound:    true,
			expectedCategory: CategoryRateLimit,
			minConfidence:    0.90,
		},
		{
			name:             "Unauthorized",
			content:          "unauthorized: invalid api key provided",
			expectedFound:    true,
			expectedCategory: CategoryAuthError,
			minConfidence:    0.85,
		},
		{
			name:             "EVM pruned state",
			content:          "missing trie node abc123 (path)",
			expectedFound:    true,
			expectedCategory: CategoryBlockchainError,
			minConfidence:    0.90,
		},
		{
			name:             "Solana unhealthy",
			content:          "node is unhealthy: behind by 100 slots",
			expectedFound:    true,
			expectedCategory: CategoryBlockchainError,
			minConfidence:    0.90,
		},
		{
			name:             "Cosmos pruned block",
			content:          "block has been pruned at height 1000000",
			expectedFound:    true,
			expectedCategory: CategoryBlockchainError,
			minConfidence:    0.90,
		},
		{
			name:             "Clean success response",
			content:          `{"jsonrpc":"2.0","id":1,"result":"0x123"}`,
			expectedFound:    false,
			expectedCategory: CategoryNone,
		},
		{
			name:             "Case insensitive - uppercase",
			content:          "CONNECTION REFUSED BY SERVER",
			expectedFound:    true,
			expectedCategory: CategoryConnectionError,
			minConfidence:    0.90,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IndicatorAnalysis([]byte(tt.content), false)

			assert.Equal(t, tt.expectedFound, result.Found, "Found mismatch")
			assert.Equal(t, tt.expectedCategory, result.Category, "Category mismatch")
			if tt.minConfidence > 0 {
				assert.GreaterOrEqual(t, result.Confidence, tt.minConfidence, "Confidence too low")
			}
		})
	}
}

func TestFullAnalyzer(t *testing.T) {
	analyzer := NewDefaultAnalyzer()

	tests := []struct {
		name           string
		response       []byte
		httpStatus     int
		rpcType        sharedtypes.RPCType
		expectedRetry  bool
		expectedReason string
	}{
		// HTTP Status checks (highest priority)
		{
			name:           "HTTP 502 Bad Gateway",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":"0x123"}`),
			httpStatus:     502,
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			expectedRetry:  true,
			expectedReason: "http_5xx",
		},
		{
			name:           "HTTP 429 Rate Limited",
			response:       []byte(`{"error":"rate limit"}`),
			httpStatus:     429,
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			expectedRetry:  true,
			expectedReason: "http_4xx",
		},

		// Structural checks
		{
			name:           "Empty response with HTTP 200",
			response:       []byte{},
			httpStatus:     200,
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			expectedRetry:  true,
			expectedReason: "empty_response",
		},
		{
			name:           "HTML error page with HTTP 200",
			response:       []byte("<html><body>Bad Gateway</body></html>"),
			httpStatus:     200,
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			expectedRetry:  true,
			expectedReason: "html_error_page",
		},
		{
			name:           "Raw text error with HTTP 200",
			response:       []byte("Bad Gateway"),
			httpStatus:     200,
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			expectedRetry:  true,
			expectedReason: "non_json_response",
		},

		// Protocol-specific checks
		{
			name:           "JSON-RPC success - should NOT retry",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":"0x47f5d16"}`),
			httpStatus:     200,
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			expectedRetry:  false,
			expectedReason: "likely_valid",
		},
		{
			name:           "JSON-RPC error in body with HTTP 200",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"server error"}}`),
			httpStatus:     200,
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			expectedRetry:  true,
			expectedReason: "jsonrpc_error_field",
		},

		// Error indicator checks
		{
			name:           "Connection refused in JSON",
			response:       []byte(`{"error":"connection refused to backend"}`),
			httpStatus:     200,
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			expectedRetry:  true,
			expectedReason: "jsonrpc_error_field",
		},

		// Edge cases
		{
			name:           "Large valid response",
			response:       append([]byte(`{"jsonrpc":"2.0","id":1,"result":`), make([]byte, 10000)...),
			httpStatus:     200,
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			expectedRetry:  false,
			expectedReason: "likely_valid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := analyzer.Analyze(tt.response, tt.httpStatus, tt.rpcType)

			assert.Equal(t, tt.expectedRetry, result.ShouldRetry, "ShouldRetry mismatch for: %s", tt.name)
			assert.Equal(t, tt.expectedReason, result.Reason, "Reason mismatch for: %s", tt.name)
		})
	}
}

func TestAnalyzeQuick(t *testing.T) {
	analyzer := NewDefaultAnalyzer()

	tests := []struct {
		name          string
		response      []byte
		httpStatus    int
		expectedRetry bool
	}{
		{
			name:          "HTTP error",
			response:      []byte(`{"result":"ok"}`),
			httpStatus:    500,
			expectedRetry: true,
		},
		{
			name:          "Empty",
			response:      []byte{},
			httpStatus:    200,
			expectedRetry: true,
		},
		{
			name:          "Non-JSON",
			response:      []byte("Bad Gateway"),
			httpStatus:    200,
			expectedRetry: true,
		},
		{
			name:          "Quick error pattern",
			response:      []byte(`{"error":"bad gateway occurred"}`),
			httpStatus:    200,
			expectedRetry: true,
		},
		{
			name:          "Valid JSON",
			response:      []byte(`{"jsonrpc":"2.0","id":1,"result":"0x123"}`),
			httpStatus:    200,
			expectedRetry: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := analyzer.AnalyzeQuick(tt.response, tt.httpStatus)
			assert.Equal(t, tt.expectedRetry, result.ShouldRetry, "ShouldRetry mismatch")
		})
	}
}

func TestPackageLevelFunctions(t *testing.T) {
	// Test that package-level functions work with default analyzer
	response := []byte(`{"jsonrpc":"2.0","id":1,"result":"0x123"}`)

	result := Analyze(response, 200, sharedtypes.RPCType_JSON_RPC)
	assert.False(t, result.ShouldRetry)

	shouldRetry := ShouldRetry(response, 200, sharedtypes.RPCType_JSON_RPC)
	assert.False(t, shouldRetry)

	quickResult := AnalyzeQuick(response, 200)
	assert.False(t, quickResult.ShouldRetry)
}

func TestRealWorldScenarios(t *testing.T) {
	analyzer := NewDefaultAnalyzer()

	// These are based on real error scenarios from production
	realWorldCases := []struct {
		name          string
		response      string
		httpStatus    int
		rpcType       sharedtypes.RPCType
		expectedRetry bool
		description   string
	}{
		{
			name:          "PATH wrapped error from screenshot",
			response:      `{"id":1,"jsonrpc":"2.0","error":{"code":-31002,"message":"the response returned by the endpoint is not a valid JSONRPC response","data":{"endpoint_response":"Bad Gateway","unmarshaling_error":"invalid character 'B' looking for beginning of value"}}}`,
			httpStatus:    502,
			rpcType:       sharedtypes.RPCType_JSON_RPC,
			expectedRetry: true,
			description:   "PATH-wrapped error with HTTP 502",
		},
		{
			name:          "EVM archival error",
			response:      `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"missing trie node 1234567890abcdef (path )"}}`,
			httpStatus:    200,
			rpcType:       sharedtypes.RPCType_JSON_RPC,
			expectedRetry: true,
			description:   "Archival node missing state",
		},
		{
			name:          "Cosmos pruned block",
			response:      `{"error":"block has been pruned"}`,
			httpStatus:    200,
			rpcType:       sharedtypes.RPCType_REST,
			expectedRetry: true,
			description:   "Cosmos REST pruned block error",
		},
		{
			name:          "Solana unhealthy node",
			response:      `{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"Node is unhealthy","data":{"numSlotsBehind":100}}}`,
			httpStatus:    200,
			rpcType:       sharedtypes.RPCType_JSON_RPC,
			expectedRetry: true,
			description:   "Solana node behind",
		},
		{
			name:          "Valid eth_blockNumber response",
			response:      `{"jsonrpc":"2.0","id":1,"result":"0x47f5d16"}`,
			httpStatus:    200,
			rpcType:       sharedtypes.RPCType_JSON_RPC,
			expectedRetry: false,
			description:   "Normal successful response",
		},
		{
			name:          "Nginx 502 page",
			response:      `<html><head><title>502 Bad Gateway</title></head><body><center><h1>502 Bad Gateway</h1></center><hr><center>nginx/1.18.0</center></body></html>`,
			httpStatus:    502,
			rpcType:       sharedtypes.RPCType_JSON_RPC,
			expectedRetry: true,
			description:   "Nginx HTML error page",
		},
		{
			name:          "Rate limited",
			response:      `{"error":{"code":429,"message":"Rate limit exceeded. Please slow down."}}`,
			httpStatus:    429,
			rpcType:       sharedtypes.RPCType_JSON_RPC,
			expectedRetry: true,
			description:   "Rate limited response",
		},
	}

	for _, tc := range realWorldCases {
		t.Run(tc.name, func(t *testing.T) {
			result := analyzer.Analyze([]byte(tc.response), tc.httpStatus, tc.rpcType)
			assert.Equal(t, tc.expectedRetry, result.ShouldRetry,
				"Failed for %s: %s (got reason: %s)", tc.name, tc.description, result.Reason)
		})
	}
}

func TestCustomConfig(t *testing.T) {
	// Test with high confidence threshold
	config := AnalyzerConfig{
		MaxPrefixBytes:      256,
		ConfidenceThreshold: 0.95, // Very high threshold
		EnableTier3:         false,
	}
	analyzer := NewAnalyzer(config)

	// This should NOT trigger retry because confidence is too low
	response := []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"test"}}`)
	result := analyzer.Analyze(response, 200, sharedtypes.RPCType_JSON_RPC)

	// The error field detection has 0.90 confidence, below 0.95 threshold
	require.False(t, result.ShouldRetry, "Should not retry with high confidence threshold")
}

func BenchmarkAnalyze(b *testing.B) {
	analyzer := NewDefaultAnalyzer()
	response := []byte(`{"jsonrpc":"2.0","id":1,"result":"0x47f5d16"}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		analyzer.Analyze(response, 200, sharedtypes.RPCType_JSON_RPC)
	}
}

func BenchmarkAnalyzeQuick(b *testing.B) {
	analyzer := NewDefaultAnalyzer()
	response := []byte(`{"jsonrpc":"2.0","id":1,"result":"0x47f5d16"}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		analyzer.AnalyzeQuick(response, 200)
	}
}

func BenchmarkAnalyze_LargeResponse(b *testing.B) {
	analyzer := NewDefaultAnalyzer()
	// Simulate a large block response
	response := append([]byte(`{"jsonrpc":"2.0","id":1,"result":`), make([]byte, 50000)...)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		analyzer.Analyze(response, 200, sharedtypes.RPCType_JSON_RPC)
	}
}
