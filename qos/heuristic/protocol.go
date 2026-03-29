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

// cometBFTPathPrefixes are REST-style paths that correspond to CometBFT RPC endpoints.
// CometBFT nodes ALWAYS return JSON-RPC formatted responses (e.g., {"jsonrpc":"2.0","id":-1,"result":{}})
// even for GET requests. This is NOT a protocol mismatch — it's the expected CometBFT behavior.
// Without this list, analyzeREST flags these valid responses as "rest_protocol_mismatch".
var cometBFTPathPrefixes = []string{
	"/health",
	"/status",
	"/block",
	"/commit",
	"/validators",
	"/genesis",
	"/net_info",
	"/consensus_state",
	"/dump_consensus_state",
	"/num_unconfirmed_txs",
	"/tx",
	"/tx_search",
	"/block_search",
	"/block_results",
	"/header",
	"/abci_info",
	"/abci_query",
	"/broadcast_tx",
	"/subscribe",
	"/unsubscribe",
}

// cometBFTMethods are JSON-RPC method names used by CometBFT.
// When the heuristic sees one of these methods, it knows the response will be
// CometBFT-formatted (JSON-RPC envelope) regardless of the detected rpcType.
var cometBFTMethods = map[string]bool{
	"health":               true,
	"status":               true,
	"block":                true,
	"commit":               true,
	"validators":           true,
	"genesis":              true,
	"net_info":             true,
	"consensus_state":      true,
	"dump_consensus_state": true,
	"num_unconfirmed_txs":  true,
	"tx":                   true,
	"tx_search":            true,
	"block_search":         true,
	"block_results":        true,
	"header":               true,
	"header_by_hash":       true,
	"abci_info":            true,
	"abci_query":           true,
	"broadcast_tx_sync":    true,
	"broadcast_tx_async":   true,
	"broadcast_tx_commit":  true,
	"subscribe":            true,
	"unsubscribe":          true,
	"unsubscribe_all":      true,
}

// isCometBFTPath checks if a request path corresponds to a CometBFT RPC endpoint.
func isCometBFTPath(path string) bool {
	for _, prefix := range cometBFTPathPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// isCometBFTMethod checks if a JSON-RPC method name or path is a CometBFT method.
// Handles both direct method names ("health") and path-based methods ("/health").
func isCometBFTMethod(method string) bool {
	if cometBFTMethods[method] {
		return true
	}
	// Also check path-style (e.g., "/health" → "health")
	if strings.HasPrefix(method, "/") {
		return cometBFTMethods[strings.TrimPrefix(method, "/")]
	}
	return false
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

	// JSON-RPC null result - many Ethereum clients (Geth, Bor, Erigon) include
	// "result":null alongside error responses. This is technically a JSON-RPC spec
	// violation but is standard behavior. We treat it as error-only (no result).
	jsonrpcResultNull = []byte(`"result":null`)

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

	// Many Ethereum clients (Geth, Bor/Polygon, Erigon) include "result":null alongside
	// error responses. This is a JSON-RPC spec quirk, not a malformed response.
	// A null result adds no information — treat it as error-only.
	// We still catch genuinely contradictory responses like "result":"0x123" + "error":{...}
	// which indicate a fake or broken supplier.
	if hasResult && hasError && bytes.Contains(prefix, jsonrpcResultNull) {
		hasResult = false
	}

	// Case 1: Has "result" without "error"
	if hasResult && !hasError {
		// Check for empty containers in the result.
		// For EVM/Solana (JSON_RPC): "result":{} is never valid — every method that returns
		// an object has mandatory fields. This is a strong signal of a broken/lazy supplier.
		// For CometBFT: "result":{} IS valid — the "health" method returns an empty object
		// when the node is healthy. Skip the empty object check for CometBFT.
		// Also skip if the method is a known CometBFT method — CometBFT can be routed
		// through JSON_RPC endpoints (via rpc_type_fallbacks), so rpcType may be JSON_RPC
		// even though the response is a valid CometBFT response.
		emptyType := emptyResultType(prefix)
		if emptyType == emptyObject && rpcType != sharedtypes.RPCType_COMET_BFT && !isCometBFTMethod(jsonrpcMethod) {
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
	if hasError && !hasResult && hasVersion {
		// Detect fabricated parse errors (-32700).
		// PATH validates all JSON-RPC requests before relay — invalid requests are
		// rejected at the gateway and never reach suppliers. A -32700 from a supplier
		// means their relay miner or backend failed to forward a valid request
		// (overloaded node, broken relay miner, rate-limited free-tier RPC).
		if bytes.Contains(prefix, []byte(`"code":-32700`)) {
			return AnalysisResult{
				ShouldRetry: true,
				Confidence:  0.98,
				Reason:      "jsonrpc_fabricated_parse_error",
				Structure:   StructureValid,
				Details:     "Supplier returned -32700 parse error for valid JSON-RPC request",
			}
		}

		// Detect suppliers wrapping backend 5xx errors as JSON-RPC errors.
		// Some suppliers intercept HTTP 5xx from their backend and return
		// HTTP 200 + {"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"service unavailable"}}
		// to bypass PATH's 5xx retry logic. The supplier gets paid for a relay
		// that returned garbage to the user.
		if bytes.Contains(prefix, []byte(`"code":-32603`)) && containsServiceErrorMessage(prefix) {
			return AnalysisResult{
				ShouldRetry: true,
				Confidence:  0.95,
				Reason:      "jsonrpc_wrapped_service_error",
				Structure:   StructureValid,
				Details:     "Supplier wrapped a backend service error as JSON-RPC -32603",
			}
		}

		// This is a VALID JSON-RPC error response - should NOT retry
		// Examples: smart contract errors, method not found, invalid params, etc.
		return AnalysisResult{
			ShouldRetry: false, // Don't retry valid error responses
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
	//
	// EXCEPTION: CometBFT endpoints (e.g., /health, /status, /block) ALWAYS return
	// JSON-RPC formatted responses even for GET requests. This is expected CometBFT
	// behavior, not a protocol mismatch. Skip the check for CometBFT paths.
	if bytes.Contains(prefix, jsonrpcVersionField) && !isCometBFTPath(requestPath) {
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

// serviceErrorMessages are messages that indicate a supplier is wrapping backend
// transport/infrastructure errors as JSON-RPC -32603 responses to bypass 5xx retry logic.
// A real node's -32603 ("Internal error") contains execution-specific messages,
// not generic service availability messages.
var serviceErrorMessages = [][]byte{
	[]byte(`"service unavailable"`),
	[]byte(`"bad gateway"`),
	[]byte(`"gateway timeout"`),
	[]byte(`"upstream connect error"`),
	[]byte(`"connection refused"`),
	[]byte(`"connection reset"`),
	[]byte(`"no healthy upstream"`),
	[]byte(`"backend unavailable"`),
	[]byte(`"proxy error"`),
}

// containsServiceErrorMessage checks if the response contains a message
// indicating the supplier wrapped a backend infrastructure error.
func containsServiceErrorMessage(prefix []byte) bool {
	lower := bytes.ToLower(prefix)
	for _, msg := range serviceErrorMessages {
		if bytes.Contains(lower, msg) {
			return true
		}
	}
	return false
}

// CheckRequestIDMismatch detects fabricated responses by comparing the response ID
// to the original request ID. JSON-RPC 2.0 spec requires the response ID to match
// the request ID. A response with "id":null when the request had a real ID proves
// the response was fabricated by the supplier's relay miner, not the actual node.
//
// Returns an AnalysisResult if a mismatch is detected, nil otherwise.
func CheckRequestIDMismatch(responsePrefix []byte, requestID string) *AnalysisResult {
	if requestID == "" || requestID == "null" {
		return nil // Can't validate if request had no ID or null ID
	}

	responseID := extractResponseID(responsePrefix)
	if responseID == "" {
		return nil // Couldn't extract response ID, don't flag
	}

	// Response has "id":null but request had a real ID — fabricated response
	if responseID == "null" {
		return &AnalysisResult{
			ShouldRetry: true,
			Confidence:  0.99,
			Reason:      "jsonrpc_id_mismatch",
			Structure:   StructureValid,
			Details:     fmt.Sprintf("Response id is null but request id was %s — supplier fabricated the response", requestID),
		}
	}

	return nil
}

// extractResponseID extracts the value of the "id" field from a JSON-RPC response
// using byte scanning (no full JSON parse). Returns the raw value as a string,
// e.g. "1", "null", `"abc"`, or "" if not found.
func extractResponseID(prefix []byte) string {
	idField := []byte(`"id"`)
	idx := bytes.Index(prefix, idField)
	if idx < 0 {
		return ""
	}

	// Skip past "id" and find the colon
	after := prefix[idx+len(idField):]
	after = bytes.TrimLeft(after, " \t\n\r")
	if len(after) == 0 || after[0] != ':' {
		return ""
	}
	after = bytes.TrimLeft(after[1:], " \t\n\r")

	if len(after) == 0 {
		return ""
	}

	// Extract the value
	switch {
	case bytes.HasPrefix(after, []byte("null")):
		return "null"
	case after[0] == '"':
		// String ID — find closing quote
		end := bytes.IndexByte(after[1:], '"')
		if end < 0 {
			return ""
		}
		return string(after[:end+2]) // include quotes
	default:
		// Numeric ID — read until non-digit
		end := 0
		for end < len(after) && (after[end] >= '0' && after[end] <= '9' || after[end] == '-') {
			end++
		}
		if end == 0 {
			return ""
		}
		return string(after[:end])
	}
}
