// Package evm provides archival request detection heuristics.
//
// This file contains logic to detect whether an incoming EVM JSON-RPC request
// requires an archival node (historical state access) WITHOUT fully unmarshalling
// the JSON payload. This is critical for hot-path performance.
//
// The detection is based on analyzing:
//  1. The JSON-RPC method name
//  2. The block parameter value (if applicable)
//  3. Comparison against the perceived current block number
package evm

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// ArchivalThreshold is the default number of blocks behind the perceived block
// that triggers archival classification. Set to 128 which is a common pruning depth.
const DefaultArchivalThreshold uint64 = 128

// BlockTag represents special block identifier tags in EVM.
type BlockTag string

const (
	BlockTagLatest    BlockTag = "latest"
	BlockTagPending   BlockTag = "pending"
	BlockTagEarliest  BlockTag = "earliest"
	BlockTagSafe      BlockTag = "safe"
	BlockTagFinalized BlockTag = "finalized"
)

// methodBlockParamInfo describes where to find the block parameter for each method.
type methodBlockParamInfo struct {
	// paramIndex is the index in the params array where the block parameter is located.
	// -1 means the method doesn't accept a block parameter.
	paramIndex int
	// isObject indicates if the block param is inside an object (like eth_getLogs).
	isObject bool
	// objectKey is the key to extract from the object (e.g., "fromBlock" for eth_getLogs).
	objectKey string
}

// evmMethodBlockParams maps EVM methods to their block parameter positions.
// Methods not in this map don't accept block parameters.
var evmMethodBlockParams = map[string]methodBlockParamInfo{
	// State query methods - block param at index 1
	"eth_getBalance":          {paramIndex: 1, isObject: false},
	"eth_getTransactionCount": {paramIndex: 1, isObject: false},
	"eth_getCode":             {paramIndex: 1, isObject: false},
	"eth_call":                {paramIndex: 1, isObject: false},
	"eth_estimateGas":         {paramIndex: 1, isObject: false}, // Optional block param

	// Storage query - block param at index 2
	"eth_getStorageAt": {paramIndex: 2, isObject: false},

	// Proof query - block param at index 2
	"eth_getProof": {paramIndex: 2, isObject: false},

	// Block query methods - block param at index 0
	"eth_getBlockByNumber":                    {paramIndex: 0, isObject: false},
	"eth_getBlockTransactionCountByNumber":    {paramIndex: 0, isObject: false},
	"eth_getUncleCountByBlockNumber":          {paramIndex: 0, isObject: false},
	"eth_getUncleByBlockNumberAndIndex":       {paramIndex: 0, isObject: false},
	"eth_getTransactionByBlockNumberAndIndex": {paramIndex: 0, isObject: false},

	// Log query - uses object with fromBlock/toBlock
	"eth_getLogs": {paramIndex: 0, isObject: true, objectKey: "fromBlock"},

	// Filter methods - these create filters that may query historical data
	"eth_newFilter": {paramIndex: 0, isObject: true, objectKey: "fromBlock"},
}

// ArchivalHeuristicResult contains the result of archival detection.
type ArchivalHeuristicResult struct {
	// RequiresArchival indicates if the request likely needs an archival node.
	RequiresArchival bool
	// Reason provides context for the decision.
	Reason string
	// RequestedBlock is the parsed block number (0 if tag or not applicable).
	RequestedBlock uint64
	// BlockTag is the block tag if one was used (empty if numeric).
	BlockTag BlockTag
	// Method is the JSON-RPC method that was analyzed.
	Method string
}

// ArchivalHeuristic provides archival request detection with configurable threshold.
type ArchivalHeuristic struct {
	// archivalThreshold is the number of blocks behind perceived that triggers archival.
	archivalThreshold uint64
}

// NewArchivalHeuristic creates a new archival heuristic detector with the given threshold.
// If threshold is 0, DefaultArchivalThreshold is used.
func NewArchivalHeuristic(threshold uint64) *ArchivalHeuristic {
	if threshold == 0 {
		threshold = DefaultArchivalThreshold
	}
	return &ArchivalHeuristic{
		archivalThreshold: threshold,
	}
}

// IsArchivalRequest determines if a JSON-RPC request requires archival data.
// This function is optimized for the hot path - it uses gjson to query the JSON
// without full unmarshalling.
//
// Parameters:
//   - requestBody: Raw JSON-RPC request bytes
//   - perceivedBlock: The current perceived block number from QoS state
//
// Returns ArchivalHeuristicResult with detection details.
func (h *ArchivalHeuristic) IsArchivalRequest(requestBody []byte, perceivedBlock uint64) ArchivalHeuristicResult {
	// Fast path: empty or too short to be valid JSON-RPC
	if len(requestBody) < 10 {
		return ArchivalHeuristicResult{
			RequiresArchival: false,
			Reason:           "request_too_short",
		}
	}

	// Use gjson to extract method without full unmarshalling
	methodResult := gjson.GetBytes(requestBody, "method")
	if !methodResult.Exists() {
		return ArchivalHeuristicResult{
			RequiresArchival: false,
			Reason:           "no_method_field",
		}
	}

	method := methodResult.String()

	// Check if this method accepts a block parameter
	blockInfo, hasBlockParam := evmMethodBlockParams[method]
	if !hasBlockParam {
		return ArchivalHeuristicResult{
			RequiresArchival: false,
			Reason:           "method_no_block_param",
			Method:           method,
		}
	}

	// Extract the block parameter based on method configuration
	blockParam := h.extractBlockParam(requestBody, blockInfo)
	if blockParam == "" {
		// No block param provided - defaults to "latest" in most EVM implementations
		return ArchivalHeuristicResult{
			RequiresArchival: false,
			Reason:           "no_block_param_defaults_latest",
			Method:           method,
			BlockTag:         BlockTagLatest,
		}
	}

	// Analyze the block parameter
	return h.analyzeBlockParam(blockParam, perceivedBlock, method)
}

// extractBlockParam extracts the block parameter from the request body.
// Uses gjson for efficient JSON querying without full unmarshalling.
func (h *ArchivalHeuristic) extractBlockParam(requestBody []byte, info methodBlockParamInfo) string {
	// Build the gjson path based on param configuration
	var path string
	if info.isObject {
		// For object params like eth_getLogs: params.0.fromBlock
		path = "params." + strconv.Itoa(info.paramIndex) + "." + info.objectKey
	} else {
		// For simple params: params.1
		path = "params." + strconv.Itoa(info.paramIndex)
	}

	result := gjson.GetBytes(requestBody, path)
	if !result.Exists() {
		return ""
	}

	return result.String()
}

// analyzeBlockParam determines if the given block parameter indicates archival access.
func (h *ArchivalHeuristic) analyzeBlockParam(blockParam string, perceivedBlock uint64, method string) ArchivalHeuristicResult {
	// Normalize to lowercase for tag comparison
	blockParamLower := strings.ToLower(blockParam)

	// Check for special block tags
	switch BlockTag(blockParamLower) {
	case BlockTagLatest, BlockTagPending, BlockTagSafe, BlockTagFinalized:
		return ArchivalHeuristicResult{
			RequiresArchival: false,
			Reason:           "block_tag_current",
			Method:           method,
			BlockTag:         BlockTag(blockParamLower),
		}
	case BlockTagEarliest:
		// "earliest" always refers to block 0 - definitely archival
		return ArchivalHeuristicResult{
			RequiresArchival: true,
			Reason:           "block_tag_earliest",
			Method:           method,
			BlockTag:         BlockTagEarliest,
			RequestedBlock:   0,
		}
	}

	// Parse as hex block number
	requestedBlock, err := parseBlockNumber(blockParam)
	if err != nil {
		// Could be a block hash or invalid - can't determine, assume not archival
		return ArchivalHeuristicResult{
			RequiresArchival: false,
			Reason:           "block_param_parse_error",
			Method:           method,
		}
	}

	// Compare against perceived block
	if perceivedBlock == 0 {
		// No perceived block yet - can't determine, assume not archival
		return ArchivalHeuristicResult{
			RequiresArchival: false,
			Reason:           "no_perceived_block",
			Method:           method,
			RequestedBlock:   requestedBlock,
		}
	}

	// Check if requested block is behind the threshold
	if requestedBlock < perceivedBlock {
		blocksBehind := perceivedBlock - requestedBlock
		if blocksBehind > h.archivalThreshold {
			return ArchivalHeuristicResult{
				RequiresArchival: true,
				Reason:           "block_behind_threshold",
				Method:           method,
				RequestedBlock:   requestedBlock,
			}
		}
	}

	return ArchivalHeuristicResult{
		RequiresArchival: false,
		Reason:           "block_within_threshold",
		Method:           method,
		RequestedBlock:   requestedBlock,
	}
}

// parseBlockNumber parses a hex block number string (e.g., "0x10d4f") to uint64.
func parseBlockNumber(blockParam string) (uint64, error) {
	// Remove 0x prefix if present
	cleanHex := strings.TrimPrefix(blockParam, "0x")
	cleanHex = strings.TrimPrefix(cleanHex, "0X")

	if cleanHex == "" {
		return 0, strconv.ErrSyntax
	}

	return strconv.ParseUint(cleanHex, 16, 64)
}

// IsArchivalRequestQuick is a faster variant that only checks method and basic patterns.
// Use this when you need maximum speed and can tolerate some false negatives.
// Does not require perceived block number.
func IsArchivalRequestQuick(requestBody []byte) bool {
	// Super fast check: look for "earliest" in the request
	if bytes.Contains(requestBody, []byte(`"earliest"`)) {
		return true
	}

	// Check for very old block numbers (heuristic: short hex like "0x1" to "0xff")
	// This catches queries to genesis or very early blocks
	// Pattern: ,"0x followed by 1-2 hex chars followed by " or ]
	if bytes.Contains(requestBody, []byte(`,"0x0"`)) ||
		bytes.Contains(requestBody, []byte(`,"0x1"`)) ||
		bytes.Contains(requestBody, []byte(`["0x0"`)) ||
		bytes.Contains(requestBody, []byte(`["0x1"`)) {
		return true
	}

	return false
}

// GetArchivalMethods returns the list of methods that can potentially require archival access.
func GetArchivalMethods() []string {
	methods := make([]string, 0, len(evmMethodBlockParams))
	for method := range evmMethodBlockParams {
		methods = append(methods, method)
	}
	return methods
}
