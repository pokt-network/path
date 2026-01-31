// Package evm provides a DataExtractor implementation for EVM-based blockchains.
//
// The EVMDataExtractor knows how to extract quality data from EVM JSON-RPC responses:
//   - Block height from eth_blockNumber responses
//   - Chain ID from eth_chainId responses
//   - Sync status from eth_syncing responses
//   - Archival status from historical query responses (e.g., eth_getBalance)
//   - Response validity from JSON-RPC structure
package evm

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/pokt-network/path/qos/jsonrpc"
	qostypes "github.com/pokt-network/path/qos/types"
)

// Verify EVMDataExtractor implements the DataExtractor interface at compile time.
var _ qostypes.DataExtractor = (*EVMDataExtractor)(nil)

// EVMDataExtractor extracts quality data from EVM JSON-RPC responses.
// It knows how to parse responses from eth_blockNumber, eth_chainId, eth_syncing, etc.
// Uses gjson for efficient field extraction without full unmarshalling.
type EVMDataExtractor struct{}

// NewEVMDataExtractor creates a new EVM data extractor.
func NewEVMDataExtractor() *EVMDataExtractor {
	return &EVMDataExtractor{}
}

// ExtractBlockHeight extracts the block height from an eth_blockNumber response.
// The response result is a hex string (e.g., "0x10d4f") which is converted to int64.
//
// Expected response format:
//
//	{"jsonrpc":"2.0","id":1,"result":"0x10d4f"}
//
// Returns:
//   - Block height as int64
//   - Error if response is invalid or doesn't contain block height
func (e *EVMDataExtractor) ExtractBlockHeight(request []byte, response []byte) (int64, error) {
	// Only extract block height from eth_blockNumber requests
	if !isMethod(request, "eth_blockNumber") {
		return 0, fmt.Errorf("not an eth_blockNumber request")
	}

	result, err := e.extractStringResult(response)
	if err != nil {
		return 0, fmt.Errorf("extract block height: %w", err)
	}

	blockHeight, err := parseHexToInt64(result)
	if err != nil {
		return 0, fmt.Errorf("parse block height hex %q: %w", result, err)
	}

	return blockHeight, nil
}

// ExtractChainID extracts the chain identifier from an eth_chainId response.
// The chain ID is returned as a hex string (e.g., "0x1" for Ethereum mainnet).
//
// Expected response format:
//
//	{"jsonrpc":"2.0","id":1,"result":"0x1"}
//
// Returns:
//   - Chain ID as hex string
//   - Error if response is invalid or doesn't contain chain ID
func (e *EVMDataExtractor) ExtractChainID(request []byte, response []byte) (string, error) {
	// Only extract chain ID from eth_chainId requests
	if !isMethod(request, "eth_chainId") {
		return "", fmt.Errorf("not an eth_chainId request")
	}

	result, err := e.extractStringResult(response)
	if err != nil {
		return "", fmt.Errorf("extract chain ID: %w", err)
	}

	return result, nil
}

// IsSyncing determines if the endpoint is currently syncing from an eth_syncing response.
//
// eth_syncing returns:
//   - false: when not syncing (node is fully synced)
//   - object: when syncing (contains startingBlock, currentBlock, highestBlock)
//
// Expected response formats:
//
//	{"jsonrpc":"2.0","id":1,"result":false}                    // Not syncing
//	{"jsonrpc":"2.0","id":1,"result":{"startingBlock":"0x0",...}} // Syncing
//
// Returns:
//   - true if endpoint is syncing
//   - false if endpoint is synced
//   - Error if sync status cannot be determined
func (e *EVMDataExtractor) IsSyncing(request []byte, response []byte) (bool, error) {
	// Only check sync status from eth_syncing requests
	if !isMethod(request, "eth_syncing") {
		return false, fmt.Errorf("not an eth_syncing request")
	}

	// Check for error first
	errorResult := gjson.GetBytes(response, "error")
	if errorResult.Exists() && errorResult.Type != gjson.Null {
		code := gjson.GetBytes(response, "error.code").Int()
		msg := gjson.GetBytes(response, "error.message").String()
		return false, fmt.Errorf("eth_syncing returned error: code=%d, message=%s", code, msg)
	}

	// Get the result field
	resultField := gjson.GetBytes(response, "result")
	if !resultField.Exists() {
		return false, fmt.Errorf("eth_syncing response missing result field")
	}

	// If result is a boolean false, the node is not syncing
	if resultField.Type == gjson.False {
		return false, nil
	}

	// If result is a boolean true (shouldn't happen per spec, but handle it)
	if resultField.Type == gjson.True {
		return true, nil
	}

	// If result is an object, the node is syncing
	if resultField.IsObject() {
		return true, nil
	}

	return false, fmt.Errorf("unexpected eth_syncing result format")
}

// IsArchival determines if the endpoint supports archival queries.
// This is typically checked by querying historical data (e.g., eth_getBalance at block 1).
//
// An archival node will return a valid result for historical queries.
// A non-archival node will return an error indicating the block is too old.
//
// Expected response format (archival):
//
//	{"jsonrpc":"2.0","id":1,"result":"0x0"}  // Balance at historical block
//
// Expected response format (non-archival):
//
//	{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"missing trie node..."}}
//
// Returns:
//   - true if endpoint is archival (query succeeded)
//   - false if endpoint is not archival (query failed with specific error)
//   - Error if archival status cannot be determined
func (e *EVMDataExtractor) IsArchival(request []byte, response []byte) (bool, error) {
	// This method should ONLY return true/false when the request is an archival-related query.
	// For non-archival queries (eth_blockNumber, eth_chainId, etc.), return an error to indicate
	// that archival status cannot be determined from this request.
	//
	// Archival-related queries are those that access historical state:
	// - eth_getBalance with a block parameter (not "latest")
	// - eth_getStorageAt with a historical block
	// - eth_call with a historical block
	// - etc.

	if len(request) == 0 {
		return false, fmt.Errorf("empty request")
	}

	// Check if this is an archival-related request by examining the method
	method := gjson.GetBytes(request, "method").String()

	// Only these methods with historical block parameters can determine archival status
	archivalMethods := map[string]bool{
		"eth_getBalance":          true,
		"eth_getStorageAt":        true,
		"eth_getTransactionCount": true,
		"eth_getCode":             true,
		"eth_call":                true,
	}

	if !archivalMethods[method] {
		// This request cannot determine archival status
		return false, fmt.Errorf("not an archival-related request (method: %s)", method)
	}

	// Check for error in the response
	errorResult := gjson.GetBytes(response, "error")
	if errorResult.Exists() && errorResult.Type != gjson.Null {
		// Check if it's an archival-related error
		errMsg := strings.ToLower(gjson.GetBytes(response, "error.message").String())
		archivalErrorIndicators := []string{
			"missing trie node",
			"pruned",
			"ancient block",
			"block not found",
			"header not found",
			"state not available",
			"state histories",      // "state histories haven't been fully indexed yet"
			"not fully indexed",    // catch variations
			"historical data",      // "historical data not available"
		}

		for _, indicator := range archivalErrorIndicators {
			if strings.Contains(errMsg, indicator) {
				// This is a non-archival node
				return false, nil
			}
		}

		// Some other error - can't determine archival status
		code := gjson.GetBytes(response, "error.code").Int()
		return false, fmt.Errorf("archival check returned error: code=%d, message=%s", code, errMsg)
	}

	// Check for result field
	resultField := gjson.GetBytes(response, "result")
	if resultField.Exists() && resultField.Type != gjson.Null {
		// The archival query succeeded - endpoint is archival-capable
		return true, nil
	}

	return false, fmt.Errorf("archival check response missing both result and error")
}

// IsValidResponse checks if the response is a valid JSON-RPC 2.0 response.
// This performs basic structural validation without extracting specific data.
//
// Checks performed:
//   - Valid JSON structure
//   - Has "jsonrpc": "2.0"
//   - Has either "result" or "error" (not both, not neither)
//
// Returns:
//   - true if response is valid JSON-RPC
//   - false if response is malformed or contains JSON-RPC error
//   - Error if validation fails unexpectedly
func (e *EVMDataExtractor) IsValidResponse(request []byte, response []byte) (bool, error) {
	if len(response) == 0 {
		return false, nil
	}

	// Validate JSON
	if !gjson.ValidBytes(response) {
		return false, nil
	}

	// Check JSON-RPC version
	version := gjson.GetBytes(response, "jsonrpc").String()
	if version != string(jsonrpc.Version2) {
		return false, nil
	}

	// Check for valid result/error combination
	resultField := gjson.GetBytes(response, "result")
	errorField := gjson.GetBytes(response, "error")

	// hasResult is true if result field exists (including null, which is a valid result)
	hasResult := resultField.Exists()
	hasError := errorField.Exists() && errorField.Type != gjson.Null

	// Must have exactly one of result or error
	if !hasResult && !hasError {
		return false, nil
	}
	if hasResult && hasError {
		return false, nil
	}

	// If it's an error response, it's technically valid JSON-RPC but indicates an issue
	if hasError {
		return false, nil
	}

	return true, nil
}

// isMethod checks if the JSON-RPC request calls the specified method.
// Uses gjson for efficient parsing.
func isMethod(request []byte, method string) bool {
	return gjson.GetBytes(request, "method").String() == method
}

// extractStringResult extracts the result field as a string from a JSON-RPC response.
// Used for responses where the result is a simple string (e.g., hex values).
func (e *EVMDataExtractor) extractStringResult(response []byte) (string, error) {
	// Check for error first
	errorResult := gjson.GetBytes(response, "error")
	if errorResult.Exists() && errorResult.Type != gjson.Null {
		code := gjson.GetBytes(response, "error.code").Int()
		msg := gjson.GetBytes(response, "error.message").String()
		return "", fmt.Errorf("JSON-RPC error: code=%d, message=%s", code, msg)
	}

	// Get the result field
	resultField := gjson.GetBytes(response, "result")
	if !resultField.Exists() || resultField.Type == gjson.Null {
		return "", fmt.Errorf("response missing result field")
	}

	// Result should be a string
	if resultField.Type != gjson.String {
		return "", fmt.Errorf("result is not a string")
	}

	return resultField.String(), nil
}

// parseHexToInt64 converts a hex string (with or without "0x" prefix) to int64.
// Examples: "0x10d4f" -> 68943, "10d4f" -> 68943
func parseHexToInt64(hexStr string) (int64, error) {
	// Remove 0x prefix if present
	cleanHex := strings.TrimPrefix(hexStr, "0x")
	cleanHex = strings.TrimPrefix(cleanHex, "0X")

	if cleanHex == "" {
		return 0, fmt.Errorf("empty hex string")
	}

	value, err := strconv.ParseInt(cleanHex, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("parse hex %q: %w", hexStr, err)
	}

	return value, nil
}
