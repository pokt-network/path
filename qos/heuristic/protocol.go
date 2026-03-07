package heuristic

import (
	"bytes"
	"fmt"
	"strings"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
)

// restEmptyObjectValidPathPrefixes is the whitelist of REST path prefixes where a {} response
// is valid. Tron's HTTP API returns {} for query endpoints when the entity doesn't exist
// (e.g., /wallet/getaccount for unactivated accounts, /wallet/gettransactionbyid for
// unknown tx hashes). Without this whitelist, the heuristic flags these as errors and the
// circuit breaker locks out the domain.
var restEmptyObjectValidPathPrefixes = []string{
	// Tron full node HTTP API — query endpoints return {} when entity not found
	"/wallet/",
	// Tron solidity node HTTP API — same behavior as full node
	"/walletsolidity/",
	// Cosmos SDK gRPC-gateway REST API — many query endpoints return {} when the
	// entity doesn't exist (e.g., /cosmos/slashing/v1beta1/signing_infos/{addr}
	// returns {} for validators with no slashing record). This is valid behavior.
	"/cosmos/",
}

// emptyArrayValidMethods is the whitelist of JSON-RPC methods where "result":[] is valid.
// For all other methods, an empty array result indicates a broken/misconfigured supplier.
var emptyArrayValidMethods = map[string]bool{
	// === EVM ===
	// Core — high-volume methods that legitimately return empty arrays
	"eth_getLogs":          true,
	"eth_getFilterChanges": true,
	"eth_getFilterLogs":    true,
	"eth_accounts":         true,
	"eth_getBlockReceipts": true,
	// Trace (Parity/Erigon)
	"trace_filter":                  true,
	"trace_block":                   true,
	"trace_replayBlockTransactions": true,
	"trace_callMany":                true,
	// Debug (Geth/Reth)
	"debug_getBadBlocks":                true,
	"debug_traceBlockByNumber":          true,
	"debug_traceBlockByHash":            true,
	"debug_traceBlock":                  true,
	"debug_getModifiedAccountsByNumber": true,
	"debug_getModifiedAccountsByHash":   true,
	"debug_traceCallMany":               true,

	// === Sei (EVM-compatible aliases) ===
	// Sei exposes eth_* methods with a sei_ prefix that behave identically
	"sei_getLogs":          true,
	"sei_getFilterChanges": true,
	"sei_getFilterLogs":    true,
	"sei_getBlockReceipts": true,

	// === Solana ===
	// Methods that return raw arrays (not wrapped in {context, value} objects)
	"getBlocks":                         true, // array of slot numbers in range
	"getBlocksWithLimit":                true, // array of slot numbers
	"getConfirmedBlocks":                true, // deprecated, same as getBlocks
	"getSignaturesForAddress":           true, // array of signature info objects
	"getConfirmedSignaturesForAddress2": true, // deprecated, same as above
	"getRecentPerformanceSamples":       true, // array of performance samples
	"getClusterNodes":                   true, // array of node info
	"getRecentPrioritizationFees":       true, // array of fee objects
}

// Tier 2: Protocol-Specific Success Checks
//
// These checks understand what a VALID SUCCESS response looks like
// for each protocol. Instead of hunting for error patterns (infinite),
// we check for the presence of success indicators.
//
// Protocol success indicators:
//   - JSON-RPC (EVM, Solana): Must have "result" field for success
//   - REST (Cosmos): Varies by endpoint, but errors have "error" field
//   - CometBFT: Returns JSON-RPC style responses

// Common byte patterns for protocol detection
var (
	// JSON-RPC success indicator - response MUST have "result" for success
	jsonrpcResultField = []byte(`"result"`)

	// JSON-RPC error indicator
	jsonrpcErrorField = []byte(`"error"`)

	// JSON-RPC version field (validates it's a JSON-RPC response)
	jsonrpcVersionField = []byte(`"jsonrpc"`)

	// REST/generic error indicators
	restErrorField   = []byte(`"error"`)
	restMessageField = []byte(`"message"`)
	restCodeField    = []byte(`"code"`)
)

// ProtocolAnalysis performs Tier 2 protocol-specific analysis.
// It checks whether the response contains expected success indicators
// for the given RPC type.
//
// Parameters:
//   - prefix: First N bytes of the response (typically 512)
//   - fullLength: Total length of the response
//   - rpcType: The protocol type for specific validation
//
// Cost: O(n) where n is prefix length
// Time: ~1-3μs for 512 byte prefix
func ProtocolAnalysis(prefix []byte, fullLength int, rpcType sharedtypes.RPCType, jsonrpcMethod string) AnalysisResult {
	switch rpcType {
	case sharedtypes.RPCType_JSON_RPC:
		return analyzeJSONRPC(prefix, fullLength, rpcType, jsonrpcMethod)

	case sharedtypes.RPCType_REST:
		return analyzeREST(prefix, fullLength, jsonrpcMethod)

	case sharedtypes.RPCType_COMET_BFT:
		// CometBFT uses JSON-RPC style responses
		return analyzeJSONRPC(prefix, fullLength, rpcType, jsonrpcMethod)

	case sharedtypes.RPCType_WEBSOCKET:
		// WebSocket messages are typically JSON-RPC
		return analyzeJSONRPC(prefix, fullLength, rpcType, jsonrpcMethod)

	default:
		// Unknown protocol - can't make protocol-specific assertions
		return AnalysisResult{
			ShouldRetry: false,
			Confidence:  0.0,
			Reason:      "unknown_protocol",
			Structure:   StructureValid,
			Details:     "Cannot perform protocol-specific analysis for unknown RPC type",
		}
	}
}

// analyzeJSONRPC checks JSON-RPC response structure.
// Per JSON-RPC 2.0 spec, a valid response MUST have either:
//   - "result" field (success)
//   - "error" field (error - but still a VALID response)
//
// Having neither or both indicates a malformed response.
func analyzeJSONRPC(prefix []byte, fullLength int, rpcType sharedtypes.RPCType, jsonrpcMethod string) AnalysisResult {
	hasResult := bytes.Contains(prefix, jsonrpcResultField)
	hasError := bytes.Contains(prefix, jsonrpcErrorField)
	hasVersion := bytes.Contains(prefix, jsonrpcVersionField)

	// Case 1: Has "result" without "error"
	if hasResult && !hasError {
		// Check for empty containers in the result.
		// For EVM/Solana (JSON_RPC): "result":{} is never valid — every method that returns
		// an object has mandatory fields. This is a strong signal of a broken/lazy supplier.
		// For CometBFT: "result":{} IS valid — the "health" method returns an empty object
		// when the node is healthy. Skip the empty object check for CometBFT.
		emptyType := emptyResultType(prefix)
		if emptyType == emptyObject && rpcType != sharedtypes.RPCType_COMET_BFT {
			return AnalysisResult{
				ShouldRetry: true,
				Confidence:  0.95,
				Reason:      "jsonrpc_empty_object_result",
				Structure:   StructureValid,
				Details:     "JSON-RPC result is an empty object — never valid for EVM/Solana methods",
			}
		}

		// CometBFT: "result":[] is NEVER valid. All CometBFT methods return objects:
		// status → {node_info, sync_info}, block → {block_id, block}, health → {}.
		// An empty array result is a strong signal of a gaming supplier returning canned responses.
		if emptyType == emptyArray && rpcType == sharedtypes.RPCType_COMET_BFT {
			return AnalysisResult{
				ShouldRetry: true,
				Confidence:  0.95,
				Reason:      "cometbft_invalid_empty_array",
				Structure:   StructureValid,
				Details:     "CometBFT result is [] — no CometBFT method ever returns an array",
			}
		}

		// Method-aware empty array detection:
		// "result":[] is valid for some methods (eth_getLogs, eth_accounts, etc.) but broken
		// for most others (eth_blockNumber, eth_getBalance return scalars/objects, never arrays).
		// Only flag when we KNOW the method AND it's not in the whitelist.
		// When method is unknown (empty string), we conservatively do NOT flag.
		if emptyType == emptyArray && jsonrpcMethod != "" && !emptyArrayValidMethods[jsonrpcMethod] {
			return AnalysisResult{
				ShouldRetry: true,
				Confidence:  0.95,
				Reason:      "jsonrpc_invalid_empty_array",
				Structure:   StructureValid,
				Details:     fmt.Sprintf("JSON-RPC result is [] for method %q which should never return an array", jsonrpcMethod),
			}
		}

		return AnalysisResult{
			ShouldRetry: false,
			Confidence:  0.0,
			Reason:      "jsonrpc_success",
			Structure:   StructureValid,
			Details:     "JSON-RPC response contains result field",
		}
	}

	// Case 2: Has "error" without "result" AND has proper structure
	// This is a VALID JSON-RPC error response - should NOT retry
	// Examples: smart contract errors, middleware errors, method not found, etc.
	if hasError && !hasResult && hasVersion {
		return AnalysisResult{
			ShouldRetry: false, // FIXED: Don't retry valid error responses
			Confidence:  0.0,
			Reason:      "jsonrpc_valid_error",
			Structure:   StructureValid,
			Details:     "JSON-RPC response contains valid error field",
		}
	}

	// Case 3: Has BOTH "result" and "error" - malformed (violates JSON-RPC spec)
	if hasResult && hasError {
		return AnalysisResult{
			ShouldRetry: true,
			Confidence:  0.95,
			Reason:      "jsonrpc_both_result_and_error",
			Structure:   StructureValid,
			Details:     "JSON-RPC response has both result and error (malformed)",
		}
	}

	// Case 4: Has "error" but no "jsonrpc" version field - suspicious
	// Might be a non-JSON-RPC error like {"error":"Bad Gateway"} from proxy
	if hasError && !hasVersion {
		return AnalysisResult{
			ShouldRetry: true,
			Confidence:  0.80,
			Reason:      "error_without_jsonrpc_version",
			Structure:   StructureValid,
			Details:     "Response has error field but missing jsonrpc version",
		}
	}

	// Case 5: Has neither "result" nor "error"
	if !hasResult && !hasError {
		// If it looks like JSON-RPC (has version) but no result/error
		if hasVersion && fullLength < 500 {
			return AnalysisResult{
				ShouldRetry: true,
				Confidence:  0.85,
				Reason:      "jsonrpc_missing_result_and_error",
				Structure:   StructureValid,
				Details:     "JSON-RPC response missing both result and error fields",
			}
		}

		// Small response without result is suspicious
		if fullLength < 100 {
			return AnalysisResult{
				ShouldRetry: true,
				Confidence:  0.70,
				Reason:      "small_no_result",
				Structure:   StructureValid,
				Details:     "Small response without result field",
			}
		}

		// Larger response - result might be beyond prefix
		return AnalysisResult{
			ShouldRetry: false,
			Confidence:  0.3,
			Reason:      "large_no_result_in_prefix",
			Structure:   StructureValid,
			Details:     "Large response, result field may be beyond inspected prefix",
		}
	}

	// Default - no strong signal
	return AnalysisResult{
		ShouldRetry: false,
		Confidence:  0.0,
		Reason:      "jsonrpc_indeterminate",
		Structure:   StructureValid,
		Details:     "Could not determine JSON-RPC response status",
	}
}

// analyzeREST checks REST API response structure.
// REST is more varied, but common error patterns include:
//   - {"error": "..."} or {"error": {...}}
//   - {"code": N, "message": "..."}
//   - {"status": "error", ...}
//
// The requestPath parameter carries the original HTTP request path (e.g., "/wallet/getaccount")
// for path-aware validation. It may be empty if not available.
func analyzeREST(prefix []byte, fullLength int, requestPath string) AnalysisResult {
	// Empty JSON object ({} or { }) is usually a broken supplier returning canned responses.
	// However, some APIs legitimately return {} (e.g., Tron's /wallet/getaccount for
	// non-existent accounts). Check the path whitelist before flagging.
	if fullLength <= 10 {
		stripped := bytes.Map(func(r rune) rune {
			if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
				return -1 // drop whitespace
			}
			return r
		}, prefix[:min(len(prefix), fullLength)])
		if len(stripped) == 2 && stripped[0] == '{' && stripped[1] == '}' {
			if !isRESTEmptyObjectValid(requestPath) {
				return AnalysisResult{
					ShouldRetry: true,
					Confidence:  0.80,
					Reason:      "rest_empty_object",
					Structure:   StructureValid,
					Details:     "REST response is an empty JSON object",
				}
			}
		}
	}

	// Protocol mismatch: a JSON-RPC response to a REST request.
	// Real Cosmos SDK REST endpoints never return JSON-RPC envelopes.
	// Gaming suppliers (e.g., spacebelt.xyz) return canned {"jsonrpc":"2.0","id":1,"result":[]}
	// for ALL requests regardless of protocol type.
	if bytes.Contains(prefix, jsonrpcVersionField) {
		return AnalysisResult{
			ShouldRetry: true,
			Confidence:  0.95,
			Reason:      "rest_protocol_mismatch",
			Structure:   StructureValid,
			Details:     "REST request received a JSON-RPC formatted response — supplier is likely gaming",
		}
	}

	hasError := bytes.Contains(prefix, restErrorField)
	hasMessage := bytes.Contains(prefix, restMessageField)
	hasCode := bytes.Contains(prefix, restCodeField)

	// Common REST error pattern: {"error": ...}
	if hasError {
		// Check if it's a top-level error (not nested in data)
		// Simple heuristic: "error" appears early in response
		errorIdx := bytes.Index(prefix, restErrorField)
		if errorIdx < 50 {
			return AnalysisResult{
				ShouldRetry: true,
				Confidence:  0.85,
				Reason:      "rest_error_field",
				Structure:   StructureValid,
				Details:     "REST response contains error field near start",
			}
		}
	}

	// Error pattern: {"code": N, "message": "..."} without data
	// This is common in Cosmos SDK and other APIs
	if hasCode && hasMessage && !bytes.Contains(prefix, []byte(`"data"`)) {
		// Small response with code+message is likely an error
		if fullLength < 200 {
			return AnalysisResult{
				ShouldRetry: true,
				Confidence:  0.75,
				Reason:      "rest_code_message_error",
				Structure:   StructureValid,
				Details:     "REST response has code and message fields (likely error)",
			}
		}
	}

	// No clear error indicators
	return AnalysisResult{
		ShouldRetry: false,
		Confidence:  0.0,
		Reason:      "rest_no_error_indicator",
		Structure:   StructureValid,
		Details:     "REST response has no obvious error indicators",
	}
}

// isRESTEmptyObjectValid checks if the request path is whitelisted for empty object responses.
// Returns true if {} is a valid response for this path (e.g., Tron /wallet/* query endpoints).
func isRESTEmptyObjectValid(requestPath string) bool {
	if requestPath == "" {
		return false
	}
	lowerPath := strings.ToLower(requestPath)
	for _, prefix := range restEmptyObjectValidPathPrefixes {
		if strings.HasPrefix(lowerPath, prefix) {
			return true
		}
	}
	return false
}

// emptyResultKind represents the type of empty result detected.
type emptyResultKind int

const (
	notEmpty    emptyResultKind = iota
	emptyArray                  // "result":[]
	emptyObject                 // "result":{}
)

// emptyResultType checks if a JSON-RPC response has "result":[] or "result":{}.
// Returns the specific kind to allow different confidence levels:
//   - emptyObject: NEVER valid for any JSON-RPC method (0.95 confidence)
//   - emptyArray: valid for some methods like eth_getLogs (0.75 confidence)
func emptyResultType(prefix []byte) emptyResultKind {
	idx := bytes.Index(prefix, jsonrpcResultField)
	if idx < 0 {
		return notEmpty
	}

	// Skip past "result" and find the colon
	after := prefix[idx+len(jsonrpcResultField):]
	after = bytes.TrimLeft(after, " \t\n\r")
	if len(after) == 0 || after[0] != ':' {
		return notEmpty
	}
	after = bytes.TrimLeft(after[1:], " \t\n\r")

	if len(after) < 2 {
		return notEmpty
	}

	if after[0] == '[' && after[1] == ']' {
		return emptyArray
	}
	if after[0] == '{' && after[1] == '}' {
		return emptyObject
	}
	return notEmpty
}

// IsJSONRPCLikeSuccess provides a quick check for JSON-RPC success.
// This is a simplified version for callers who just need a boolean.
func IsJSONRPCLikeSuccess(prefix []byte) bool {
	hasResult := bytes.Contains(prefix, jsonrpcResultField)
	hasError := bytes.Contains(prefix, jsonrpcErrorField)
	return hasResult && !hasError
}

// HasErrorField checks if the response prefix contains an error field.
// Works for both JSON-RPC and REST responses.
func HasErrorField(prefix []byte) bool {
	return bytes.Contains(prefix, jsonrpcErrorField)
}
