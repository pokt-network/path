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
			name:           "plain text - Tron lite fullnode capability limitation",
			response:       []byte("this API is closed because this node is a lite fullnode"),
			expectedRetry:  true,
			expectedStruct: StructureNonJSON,
			expectedReason: "non_json_capability_limitation",
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
			name:           "JSON-RPC valid error response - should NOT retry",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"server error"}}`),
			expectedRetry:  false,
			expectedReason: "jsonrpc_valid_error",
		},
		{
			name:           "Middleware custom error - valid response",
			response:       []byte(`{"jsonrpc":"2.0","error":{"code":3200,"message":"ho ho ho merry christmas"},"id":1}`),
			expectedRetry:  false,
			expectedReason: "jsonrpc_valid_error",
		},
		{
			name:           "JSON-RPC with both result and error - malformed",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":"0x123","error":{"code":-32000,"message":"error"}}`),
			expectedRetry:  true,
			expectedReason: "jsonrpc_both_result_and_error",
		},
		{
			name:           "JSON-RPC with result:null and error - valid error (Geth/Bor quirk)",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":null,"error":{"code":-32000,"message":"block #25721481 not found"}}`),
			expectedRetry:  false,
			expectedReason: "jsonrpc_valid_error",
		},
		{
			name:           "JSON-RPC with result:null and error - execution reverted (Polygon Bor)",
			response:       []byte(`{"jsonrpc":"2.0","id":2471943,"error":{"code":3,"message":"execution reverted: Amount exceeds available interest","data":"0x08c379a0"},"result":null}`),
			expectedRetry:  false,
			expectedReason: "jsonrpc_valid_error",
		},
		{
			name:           "JSON-RPC with result:null and error - fabricated parse error still caught",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":null,"error":{"code":-32700,"message":"parse error"}}`),
			expectedRetry:  true,
			expectedReason: "jsonrpc_fabricated_parse_error",
		},
		{
			name:           "JSON-RPC with result:null and error - wrapped service error still caught",
			response:       []byte(`{"jsonrpc":"2.0","id":null,"result":null,"error":{"code":-32603,"message":"service unavailable"}}`),
			expectedRetry:  true,
			expectedReason: "jsonrpc_wrapped_service_error",
		},
		{
			name:           "JSON-RPC with non-null result and error - still malformed",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":-32000,"message":"error"}}`),
			expectedRetry:  true,
			expectedReason: "jsonrpc_both_result_and_error",
		},
		{
			name:           "JSON-RPC with result:[] and error - still malformed",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":[],"error":{"code":-32000,"message":"error"}}`),
			expectedRetry:  true,
			expectedReason: "jsonrpc_both_result_and_error",
		},
		{
			name:           "NEAR result with nested error string - valid success (not malformed)",
			response:       []byte(`{"jsonrpc":"2.0","result":{"block_hash":"kkJU2quYwNQ","block_height":192983668,"error":"wasm execution failed with error: HostError(GuestPanic { panic_msg: \"panicked at E102\" })","logs":[]},"id":20}`),
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "NEAR result with nested error string - simple",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":{"error":"some error message","logs":[]}}`),
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "Error without jsonrpc version - suspicious",
			response:       []byte(`{"error":"Bad Gateway"}`),
			expectedRetry:  true,
			expectedReason: "error_without_jsonrpc_version",
		},
		{
			name:           "JSON-RPC missing both result and error (small)",
			response:       []byte(`{"jsonrpc":"2.0","id":1}`),
			expectedRetry:  true,
			expectedReason: "jsonrpc_missing_result_and_error",
			minConfidence:  0.80,
		},
		{
			name:           "JSON-RPC empty array result — valid for eth_getLogs etc",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":[]}`),
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "JSON-RPC empty object result — major penalty (never valid for EVM/Solana)",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`),
			expectedRetry:  true,
			expectedReason: "jsonrpc_empty_object_result",
			minConfidence:  0.95,
		},
		{
			name:           "JSON-RPC non-empty array result",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":["0x123"]}`),
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "Large response without result in prefix",
			response:       append([]byte(`{"jsonrpc":"2.0","id":1,"data":`), make([]byte, 1000)...),
			expectedRetry:  false,
			expectedReason: "large_no_result_in_prefix",
		},
		// Fabricated response detection
		{
			name:           "Fabricated parse error (-32700) — supplier returned parse error for valid request",
			response:       []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`),
			expectedRetry:  true,
			expectedReason: "jsonrpc_fabricated_parse_error",
			minConfidence:  0.95,
		},
		{
			name:           "Fabricated parse error — real Bor format (still fabricated, PATH sends valid JSON)",
			response:       []byte(`{"jsonrpc":"2.0","error":{"code":-32700,"message":"Parse error","data":"Failed to parse the request body as JSON"},"id":-1}`),
			expectedRetry:  true,
			expectedReason: "jsonrpc_fabricated_parse_error",
			minConfidence:  0.95,
		},
		{
			name:           "Wrapped service error (-32603 with service unavailable)",
			response:       []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"service unavailable"}}`),
			expectedRetry:  true,
			expectedReason: "jsonrpc_wrapped_service_error",
			minConfidence:  0.95,
		},
		{
			name:           "Wrapped service error (-32603 with bad gateway)",
			response:       []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"Bad Gateway"}}`),
			expectedRetry:  true,
			expectedReason: "jsonrpc_wrapped_service_error",
			minConfidence:  0.95,
		},
		{
			name:           "Legitimate -32603 internal error — should NOT retry",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"execution reverted: insufficient balance"}}`),
			expectedRetry:  false,
			expectedReason: "jsonrpc_valid_error",
		},
		{
			name:           "Legitimate -32601 method not found — should NOT retry",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"the method eth_foo does not exist"}}`),
			expectedRetry:  false,
			expectedReason: "jsonrpc_valid_error",
		},
		{
			name:           "Legitimate -32602 invalid params — should NOT retry",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"missing value for required argument 0"}}`),
			expectedRetry:  false,
			expectedReason: "jsonrpc_valid_error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefixLen := min(512, len(tt.response))
			result := ProtocolAnalysis(tt.response[:prefixLen], len(tt.response), sharedtypes.RPCType_JSON_RPC, "")

			assert.Equal(t, tt.expectedRetry, result.ShouldRetry, "ShouldRetry mismatch")
			assert.Equal(t, tt.expectedReason, result.Reason, "Reason mismatch")
			if tt.minConfidence > 0 {
				assert.GreaterOrEqual(t, result.Confidence, tt.minConfidence, "Confidence too low")
			}
		})
	}
}

func TestProtocolAnalysis_CometBFT(t *testing.T) {
	tests := []struct {
		name           string
		response       []byte
		expectedRetry  bool
		expectedReason string
		minConfidence  float64
	}{
		{
			name:           "CometBFT health — empty object result is valid",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`),
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "CometBFT status — non-empty result is valid",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":{"sync_info":{"latest_block_height":"123"}}}`),
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "CometBFT valid error — should NOT retry",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"method not found"}}`),
			expectedRetry:  false,
			expectedReason: "jsonrpc_valid_error",
		},
		{
			name:           "CometBFT empty array result — always invalid (gaming supplier)",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":[]}`),
			expectedRetry:  true,
			expectedReason: "cometbft_invalid_empty_array",
			minConfidence:  0.95,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefixLen := min(512, len(tt.response))
			result := ProtocolAnalysis(tt.response[:prefixLen], len(tt.response), sharedtypes.RPCType_COMET_BFT, "")

			assert.Equal(t, tt.expectedRetry, result.ShouldRetry, "ShouldRetry mismatch")
			assert.Equal(t, tt.expectedReason, result.Reason, "Reason mismatch")
			if tt.minConfidence > 0 {
				assert.GreaterOrEqual(t, result.Confidence, tt.minConfidence, "Confidence too low")
			}
		})
	}
}

// TestProtocolAnalysis_CometBFTViaJSONRPC tests that CometBFT responses are correctly
// identified even when rpcType is JSON_RPC (due to rpc_type_fallbacks routing).
// CometBFT's "health" method returns {"result":{}} which is valid — but when routed
// through JSON_RPC endpoints, the rpcType may arrive as JSON_RPC instead of COMET_BFT.
// The method name must be used as a safety net.
func TestProtocolAnalysis_CometBFTViaJSONRPC(t *testing.T) {
	emptyObjectResponse := []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)

	tests := []struct {
		name           string
		rpcType        sharedtypes.RPCType
		method         string
		expectedRetry  bool
		expectedReason string
	}{
		{
			name:           "health via COMET_BFT type — valid (existing behavior)",
			rpcType:        sharedtypes.RPCType_COMET_BFT,
			method:         "health",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "health via JSON_RPC type — valid (method-based detection)",
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			method:         "health",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "health via path /health — valid (path-based detection)",
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			method:         "/health",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "status via JSON_RPC type — valid (CometBFT method)",
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			method:         "status",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "eth_blockNumber via JSON_RPC — still invalid (not CometBFT)",
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			method:         "eth_blockNumber",
			expectedRetry:  true,
			expectedReason: "jsonrpc_empty_object_result",
		},
		{
			name:           "unknown method via JSON_RPC — still invalid",
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			method:         "",
			expectedRetry:  true,
			expectedReason: "jsonrpc_empty_object_result",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ProtocolAnalysis(emptyObjectResponse, len(emptyObjectResponse), tt.rpcType, tt.method)
			assert.Equal(t, tt.expectedRetry, result.ShouldRetry, "ShouldRetry mismatch")
			assert.Equal(t, tt.expectedReason, result.Reason, "Reason mismatch")
		})
	}
}

// TestProtocolAnalysis_RESTCometBFTPath tests that CometBFT GET responses
// are not flagged as rest_protocol_mismatch. CometBFT endpoints always return
// JSON-RPC formatted responses even for REST-style GET requests.
func TestProtocolAnalysis_RESTCometBFTPath(t *testing.T) {
	healthResponse := []byte(`{"jsonrpc":"2.0","id":-1,"result":{}}`)
	statusResponse := []byte(`{"jsonrpc":"2.0","id":-1,"result":{"sync_info":{"latest_block_height":"123"}}}`)
	gamingResponse := []byte(`{"jsonrpc":"2.0","id":1,"result":[]}`)

	tests := []struct {
		name           string
		response       []byte
		path           string
		expectedRetry  bool
		expectedReason string
	}{
		{
			name:           "GET /health — JSON-RPC response is expected",
			response:       healthResponse,
			path:           "/health",
			expectedRetry:  false,
			expectedReason: "rest_no_error_indicator",
		},
		{
			name:           "GET /status — JSON-RPC response is expected",
			response:       statusResponse,
			path:           "/status",
			expectedRetry:  false,
			expectedReason: "rest_no_error_indicator",
		},
		{
			name:           "GET /block — JSON-RPC response is expected",
			response:       gamingResponse,
			path:           "/block",
			expectedRetry:  false,
			expectedReason: "rest_no_error_indicator",
		},
		{
			name:           "GET /cosmos/staking — JSON-RPC response IS protocol mismatch",
			response:       healthResponse,
			path:           "/cosmos/staking/v1beta1/validators",
			expectedRetry:  true,
			expectedReason: "rest_protocol_mismatch",
		},
		{
			name:           "GET / — JSON-RPC response IS protocol mismatch",
			response:       healthResponse,
			path:           "/",
			expectedRetry:  true,
			expectedReason: "rest_protocol_mismatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ProtocolAnalysis(tt.response, len(tt.response), sharedtypes.RPCType_REST, tt.path)
			assert.Equal(t, tt.expectedRetry, result.ShouldRetry, "ShouldRetry mismatch")
			assert.Equal(t, tt.expectedReason, result.Reason, "Reason mismatch")
		})
	}
}

func TestProtocolAnalysis_MethodAwareEmptyArray(t *testing.T) {
	emptyArrayResponse := []byte(`{"jsonrpc":"2.0","id":1,"result":[]}`)

	tests := []struct {
		name           string
		method         string
		expectedRetry  bool
		expectedReason string
	}{
		{
			name:           "eth_blockNumber + empty array = broken supplier",
			method:         "eth_blockNumber",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "eth_getBalance + empty array = broken supplier",
			method:         "eth_getBalance",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "eth_getLogs + empty array = valid (no matching events)",
			method:         "eth_getLogs",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "eth_getFilterChanges + empty array = valid",
			method:         "eth_getFilterChanges",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "eth_accounts + empty array = valid (public RPC)",
			method:         "eth_accounts",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "trace_filter + empty array = valid",
			method:         "trace_filter",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "debug_getBadBlocks + empty array = valid",
			method:         "debug_getBadBlocks",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "unknown method (empty string) + empty array = conservative pass",
			method:         "",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "eth_call + empty array = broken supplier",
			method:         "eth_call",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "eth_getTransactionReceipt + empty array = broken supplier",
			method:         "eth_getTransactionReceipt",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "eth_getBlockReceipts + empty array = valid (empty block)",
			method:         "eth_getBlockReceipts",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},

		// Solana — scalar-returning methods (NEVER return arrays)
		{
			name:           "getSlot + empty array = broken supplier",
			method:         "getSlot",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "getBlockHeight + empty array = broken supplier",
			method:         "getBlockHeight",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "getBalance + empty array = broken supplier",
			method:         "getBalance",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "getTransaction + empty array = broken supplier",
			method:         "getTransaction",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "getEpochInfo + empty array = broken supplier",
			method:         "getEpochInfo",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "getHealth + empty array = broken supplier",
			method:         "getHealth",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "getVersion + empty array = broken supplier",
			method:         "getVersion",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "getBlock + empty array = broken supplier",
			method:         "getBlock",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "getIdentity + empty array = broken supplier",
			method:         "getIdentity",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "getRecentBlockhash + empty array = broken supplier",
			method:         "getRecentBlockhash",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "getLatestBlockhash + empty array = broken supplier",
			method:         "getLatestBlockhash",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},

		// Solana — array-returning methods (empty array IS valid)
		{
			name:           "getBlocks + empty array = valid (no blocks in range)",
			method:         "getBlocks",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "getBlocksWithLimit + empty array = valid",
			method:         "getBlocksWithLimit",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "getSignaturesForAddress + empty array = valid (no signatures)",
			method:         "getSignaturesForAddress",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "getConfirmedSignaturesForAddress2 + empty array = valid",
			method:         "getConfirmedSignaturesForAddress2",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "getRecentPerformanceSamples + empty array = valid",
			method:         "getRecentPerformanceSamples",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "getClusterNodes + empty array = valid",
			method:         "getClusterNodes",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "getRecentPrioritizationFees + empty array = valid",
			method:         "getRecentPrioritizationFees",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ProtocolAnalysis(emptyArrayResponse, len(emptyArrayResponse), sharedtypes.RPCType_JSON_RPC, tt.method)

			assert.Equal(t, tt.expectedRetry, result.ShouldRetry, "ShouldRetry mismatch")
			assert.Equal(t, tt.expectedReason, result.Reason, "Reason mismatch")
			if tt.expectedRetry {
				assert.GreaterOrEqual(t, result.Confidence, 0.95, "Confidence should be >= 0.95 for flagged empty arrays")
				assert.Contains(t, result.Details, tt.method, "Details should mention the method name")
			}
		})
	}
}

func TestFullAnalyzer_MethodAwareEmptyArray(t *testing.T) {
	analyzer := NewDefaultAnalyzer()
	emptyArrayResponse := []byte(`{"jsonrpc":"2.0","id":1,"result":[]}`)

	tests := []struct {
		name           string
		method         string
		expectedRetry  bool
		expectedReason string
	}{
		{
			name:           "eth_blockNumber with empty array via full analyzer",
			method:         "eth_blockNumber",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "eth_getLogs with empty array via full analyzer",
			method:         "eth_getLogs",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "unknown method with empty array via full analyzer",
			method:         "",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := analyzer.Analyze(emptyArrayResponse, 200, sharedtypes.RPCType_JSON_RPC, tt.method)

			assert.Equal(t, tt.expectedRetry, result.ShouldRetry, "ShouldRetry mismatch")
			assert.Equal(t, tt.expectedReason, result.Reason, "Reason mismatch")
		})
	}
}

func TestFullAnalyzer_SolanaEmptyArrayDetection(t *testing.T) {
	analyzer := NewDefaultAnalyzer()
	emptyArrayResponse := []byte(`{"jsonrpc":"2.0","id":1,"result":[]}`)

	tests := []struct {
		name           string
		method         string
		expectedRetry  bool
		expectedReason string
	}{
		{
			name:           "getSlot with empty array = invalid response",
			method:         "getSlot",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "getBlockHeight with empty array = invalid response",
			method:         "getBlockHeight",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "getBalance with empty array = invalid response",
			method:         "getBalance",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "getEpochInfo with empty array = invalid response",
			method:         "getEpochInfo",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "getVersion with empty array = invalid response",
			method:         "getVersion",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "getIdentity with empty array = invalid response",
			method:         "getIdentity",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "getRecentBlockhash with empty array = invalid response",
			method:         "getRecentBlockhash",
			expectedRetry:  true,
			expectedReason: "jsonrpc_invalid_empty_array",
		},
		{
			name:           "getSignaturesForAddress with empty array = valid (no sigs)",
			method:         "getSignaturesForAddress",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "getBlocks with empty array = valid (empty range)",
			method:         "getBlocks",
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := analyzer.Analyze(emptyArrayResponse, 200, sharedtypes.RPCType_JSON_RPC, tt.method)

			assert.Equal(t, tt.expectedRetry, result.ShouldRetry, "ShouldRetry mismatch")
			assert.Equal(t, tt.expectedReason, result.Reason, "Reason mismatch")
			if tt.expectedRetry {
				assert.GreaterOrEqual(t, result.Confidence, 0.95, "Invalid response detection should have high confidence")
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
		{
			name:           "REST empty JSON object",
			response:       []byte(`{}`),
			expectedRetry:  true,
			expectedReason: "rest_empty_object",
		},
		{
			name:           "REST empty JSON object with whitespace",
			response:       []byte(`{ }`),
			expectedRetry:  true,
			expectedReason: "rest_empty_object",
		},
		{
			name:           "REST receives JSON-RPC response — protocol mismatch (gaming supplier)",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":[]}`),
			expectedRetry:  true,
			expectedReason: "rest_protocol_mismatch",
		},
		{
			name:           "REST receives JSON-RPC success — still protocol mismatch",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":"0x123"}`),
			expectedRetry:  true,
			expectedReason: "rest_protocol_mismatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefixLen := min(512, len(tt.response))
			result := ProtocolAnalysis(tt.response[:prefixLen], len(tt.response), sharedtypes.RPCType_REST, "")

			assert.Equal(t, tt.expectedRetry, result.ShouldRetry, "ShouldRetry mismatch")
			assert.Equal(t, tt.expectedReason, result.Reason, "Reason mismatch")
		})
	}
}

func TestRESTEmptyObjectPathWhitelist(t *testing.T) {
	emptyObject := []byte(`{}`)

	tests := []struct {
		name          string
		path          string
		expectedRetry bool
		expectedReason string
	}{
		{
			name:          "No path — still flagged",
			path:          "",
			expectedRetry: true,
			expectedReason: "rest_empty_object",
		},
		{
			name:          "Tron /wallet/getaccount — whitelisted",
			path:          "/wallet/getaccount",
			expectedRetry: false,
			expectedReason: "rest_no_error_indicator",
		},
		{
			name:          "Tron /wallet/gettransactionbyid — whitelisted",
			path:          "/wallet/gettransactionbyid",
			expectedRetry: false,
			expectedReason: "rest_no_error_indicator",
		},
		{
			name:          "Tron /walletsolidity/getaccount — whitelisted",
			path:          "/walletsolidity/getaccount",
			expectedRetry: false,
			expectedReason: "rest_no_error_indicator",
		},
		{
			name:          "Cosmos REST path — whitelisted",
			path:          "/cosmos/base/tendermint/v1beta1/blocks/latest",
			expectedRetry: false,
			expectedReason: "rest_no_error_indicator",
		},
		{
			name:          "Root path — not whitelisted",
			path:          "/",
			expectedRetry: true,
			expectedReason: "rest_empty_object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ProtocolAnalysis(emptyObject, len(emptyObject), sharedtypes.RPCType_REST, tt.path)

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
			name:             "EVM fallback API failure",
			content:          `Failed to call fallback API`,
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
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "JSON-RPC error in body with HTTP 200",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"server error"}}`),
			httpStatus:     200,
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			expectedRetry:  false,
			expectedReason: "jsonrpc_valid_error",
		},
		{
			name:           "JSON-RPC with both result and error - malformed",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":"0x123","error":{"code":-32000,"message":"error"}}`),
			httpStatus:     200,
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			expectedRetry:  true,
			expectedReason: "jsonrpc_both_result_and_error",
		},
		{
			name:           "JSON-RPC result:null with error - valid error (Geth/Bor quirk)",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":null,"error":{"code":-32000,"message":"block not found"}}`),
			httpStatus:     200,
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			expectedRetry:  false,
			expectedReason: "jsonrpc_valid_error",
		},
		{
			name:           "JSON-RPC result:null with error - non-null result still malformed",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":[],"error":{"code":-32000,"message":"error"}}`),
			httpStatus:     200,
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			expectedRetry:  true,
			expectedReason: "jsonrpc_both_result_and_error",
		},
		{
			name:           "Error without jsonrpc version - suspicious",
			response:       []byte(`{"error":"Bad Gateway"}`),
			httpStatus:     200,
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			expectedRetry:  true,
			expectedReason: "error_without_jsonrpc_version",
		},

		// Error indicator checks
		{
			name:           "Connection refused in JSON",
			response:       []byte(`{"error":"connection refused to backend"}`),
			httpStatus:     200,
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			expectedRetry:  true,
			expectedReason: "error_without_jsonrpc_version",
		},

		// Edge cases
		{
			name:           "Large valid response",
			response:       append([]byte(`{"jsonrpc":"2.0","id":1,"result":`), make([]byte, 10000)...),
			httpStatus:     200,
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
		{
			name:           "JSON-RPC with 403 in result - should NOT retry",
			response:       []byte(`{"jsonrpc":"2.0","id":1,"result":"address contains 403 somewhere"}`),
			httpStatus:     200,
			rpcType:        sharedtypes.RPCType_JSON_RPC,
			expectedRetry:  false,
			expectedReason: "jsonrpc_success",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := analyzer.Analyze(tt.response, tt.httpStatus, tt.rpcType, "")

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

	result := Analyze(response, 200, sharedtypes.RPCType_JSON_RPC, "")
	assert.False(t, result.ShouldRetry)

	shouldRetry := ShouldRetry(response, 200, sharedtypes.RPCType_JSON_RPC, "")
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
			name:          "Erigon MDBX database corruption",
			response:      `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"mdbx_txn_begin: MDBX_PANIC(-30795): Maybe free space is over on disk. Otherwise it's hardware failure."}}`,
			httpStatus:    200,
			rpcType:       sharedtypes.RPCType_JSON_RPC,
			expectedRetry: true,
			description:   "Erigon node with corrupted MDBX database",
		},
		{
			name:          "EVM fallback API failure for archival request",
			response:      `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"Failed to call fallback API"}}`,
			httpStatus:    200,
			rpcType:       sharedtypes.RPCType_JSON_RPC,
			expectedRetry: true,
			description:   "Node's internal fallback for archival data failed",
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
			name:          "REST empty JSON object",
			response:      `{}`,
			httpStatus:    200,
			rpcType:       sharedtypes.RPCType_REST,
			expectedRetry: true,
			description:   "Broken supplier returning empty REST response",
		},
		{
			name:          "JSON-RPC empty array result — valid for eth_getLogs",
			response:      `{"jsonrpc":"2.0","id":1,"result":[]}`,
			httpStatus:    200,
			rpcType:       sharedtypes.RPCType_JSON_RPC,
			expectedRetry: false,
			description:   "Valid response for eth_getLogs with no matching events",
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
		// Gaming supplier scenarios — spacebelt.xyz returns canned {"jsonrpc":"2.0","id":1,"result":[]}
		// for ALL requests regardless of protocol type
		{
			name:          "Gaming supplier: JSON-RPC response to REST request",
			response:      `{"jsonrpc":"2.0","id":1,"result":[]}`,
			httpStatus:    200,
			rpcType:       sharedtypes.RPCType_REST,
			expectedRetry: true,
			description:   "Gaming supplier returning canned JSON-RPC to Cosmos REST /cosmos/base/tendermint/v1beta1/syncing",
		},
		{
			name:          "Gaming supplier: empty array to CometBFT status",
			response:      `{"jsonrpc":"2.0","id":1,"result":[]}`,
			httpStatus:    200,
			rpcType:       sharedtypes.RPCType_COMET_BFT,
			expectedRetry: true,
			description:   "Gaming supplier returning canned empty array to CometBFT /status request",
		},
	}

	for _, tc := range realWorldCases {
		t.Run(tc.name, func(t *testing.T) {
			result := analyzer.Analyze([]byte(tc.response), tc.httpStatus, tc.rpcType, "")
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
	result := analyzer.Analyze(response, 200, sharedtypes.RPCType_JSON_RPC, "")

	// The error field detection has 0.90 confidence, below 0.95 threshold
	require.False(t, result.ShouldRetry, "Should not retry with high confidence threshold")
}

func BenchmarkAnalyze(b *testing.B) {
	analyzer := NewDefaultAnalyzer()
	response := []byte(`{"jsonrpc":"2.0","id":1,"result":"0x47f5d16"}`)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		analyzer.Analyze(response, 200, sharedtypes.RPCType_JSON_RPC, "eth_blockNumber")
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
		analyzer.Analyze(response, 200, sharedtypes.RPCType_JSON_RPC, "eth_getBlockByNumber")
	}
}

func TestCheckRequestIDMismatch(t *testing.T) {
	tests := []struct {
		name        string
		response    []byte
		requestID   string
		expectFlag  bool
		expectReason string
	}{
		{
			name:        "ID mismatch — response null, request had integer ID",
			response:    []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`),
			requestID:   "1",
			expectFlag:  true,
			expectReason: "jsonrpc_id_mismatch",
		},
		{
			name:        "ID mismatch — response null, request had string ID",
			response:    []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"service unavailable"}}`),
			requestID:   `"abc"`,
			expectFlag:  true,
			expectReason: "jsonrpc_id_mismatch",
		},
		{
			name:       "ID match — both integer 1",
			response:   []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"server error"}}`),
			requestID:  "1",
			expectFlag: false,
		},
		{
			name:       "ID match — both null (request had null ID)",
			response:   []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`),
			requestID:  "null",
			expectFlag: false,
		},
		{
			name:       "No request ID — cannot validate, should not flag",
			response:   []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`),
			requestID:  "",
			expectFlag: false,
		},
		{
			name:       "Response has integer ID matching request",
			response:   []byte(`{"jsonrpc":"2.0","id":42,"result":"0x123"}`),
			requestID:  "42",
			expectFlag: false,
		},
		{
			name:       "Response has string ID, request had integer — not null mismatch",
			response:   []byte(`{"jsonrpc":"2.0","id":"1","error":{"code":-32000,"message":"error"}}`),
			requestID:  "1",
			expectFlag: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckRequestIDMismatch(tt.response, tt.requestID)
			if tt.expectFlag {
				require.NotNil(t, result, "Expected mismatch to be flagged")
				assert.True(t, result.ShouldRetry)
				assert.Equal(t, tt.expectReason, result.Reason)
				assert.GreaterOrEqual(t, result.Confidence, 0.95)
			} else {
				assert.Nil(t, result, "Expected no mismatch flag")
			}
		})
	}
}

func TestExtractResponseID(t *testing.T) {
	tests := []struct {
		name     string
		response []byte
		expected string
	}{
		{
			name:     "null ID",
			response: []byte(`{"jsonrpc":"2.0","id":null,"error":{}}`),
			expected: "null",
		},
		{
			name:     "integer ID",
			response: []byte(`{"jsonrpc":"2.0","id":1,"result":"0x123"}`),
			expected: "1",
		},
		{
			name:     "negative integer ID",
			response: []byte(`{"jsonrpc":"2.0","id":-1,"error":{}}`),
			expected: "-1",
		},
		{
			name:     "string ID",
			response: []byte(`{"jsonrpc":"2.0","id":"abc","result":"0x123"}`),
			expected: `"abc"`,
		},
		{
			name:     "ID with spaces around colon",
			response: []byte(`{"jsonrpc":"2.0", "id" : null, "error":{}}`),
			expected: "null",
		},
		{
			name:     "no ID field",
			response: []byte(`{"jsonrpc":"2.0","result":"0x123"}`),
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractResponseID(tt.response)
			assert.Equal(t, tt.expected, result)
		})
	}
}
