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
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/pokt-network/path/qos/jsonrpc"
	qostypes "github.com/pokt-network/path/qos/types"
)

// Verify EVMDataExtractor implements the DataExtractor interface at compile time.
var _ qostypes.DataExtractor = (*EVMDataExtractor)(nil)

// EVMDataExtractor extracts quality data from EVM JSON-RPC responses.
// It knows how to parse responses from eth_blockNumber, eth_chainId, eth_syncing, etc.
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
func (e *EVMDataExtractor) ExtractBlockHeight(response []byte) (int64, error) {
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
func (e *EVMDataExtractor) ExtractChainID(response []byte) (string, error) {
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
func (e *EVMDataExtractor) IsSyncing(response []byte) (bool, error) {
	jsonrpcResp, err := e.parseJSONRPCResponse(response)
	if err != nil {
		return false, fmt.Errorf("parse syncing response: %w", err)
	}

	if jsonrpcResp.Error != nil {
		return false, fmt.Errorf("eth_syncing returned error: code=%d, message=%s",
			jsonrpcResp.Error.Code, jsonrpcResp.Error.Message)
	}

	if jsonrpcResp.Result == nil {
		return false, fmt.Errorf("eth_syncing response missing result field")
	}

	// Try to unmarshal as boolean first (false = not syncing)
	var syncResult bool
	if err := json.Unmarshal(*jsonrpcResp.Result, &syncResult); err == nil {
		// eth_syncing returns `false` when NOT syncing (node is fully synced)
		// It returns an object when syncing. So if we get a bool, it's `false`.
		// We return `false` to indicate "not syncing".
		return syncResult, nil
	}

	// If not a boolean, it's an object which means the node IS syncing
	// The object contains fields like startingBlock, currentBlock, highestBlock
	var syncStatus map[string]interface{}
	if err := json.Unmarshal(*jsonrpcResp.Result, &syncStatus); err == nil {
		// Successfully parsed as object - node is syncing
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
func (e *EVMDataExtractor) IsArchival(response []byte) (bool, error) {
	jsonrpcResp, err := e.parseJSONRPCResponse(response)
	if err != nil {
		return false, fmt.Errorf("parse archival response: %w", err)
	}

	// If there's an error in the response, check if it's an archival-related error
	if jsonrpcResp.Error != nil {
		// Common error messages for non-archival nodes
		errMsg := strings.ToLower(jsonrpcResp.Error.Message)
		archivalErrorIndicators := []string{
			"missing trie node",
			"pruned",
			"ancient block",
			"block not found",
			"header not found",
			"state not available",
		}

		for _, indicator := range archivalErrorIndicators {
			if strings.Contains(errMsg, indicator) {
				// This is a non-archival node
				return false, nil
			}
		}

		// Some other error - can't determine archival status
		return false, fmt.Errorf("archival check returned error: code=%d, message=%s",
			jsonrpcResp.Error.Code, jsonrpcResp.Error.Message)
	}

	// No error and has result - this is an archival node
	if jsonrpcResp.Result != nil {
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
func (e *EVMDataExtractor) IsValidResponse(response []byte) (bool, error) {
	if len(response) == 0 {
		return false, nil
	}

	jsonrpcResp, err := e.parseJSONRPCResponse(response)
	if err != nil {
		return false, nil // Invalid JSON or structure
	}

	// Check JSON-RPC version
	if jsonrpcResp.Version != jsonrpc.Version2 {
		return false, nil
	}

	// Check for valid result/error combination
	hasResult := jsonrpcResp.Result != nil
	hasError := jsonrpcResp.Error != nil

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

// extractStringResult extracts the result field as a string from a JSON-RPC response.
// Used for responses where the result is a simple string (e.g., hex values).
func (e *EVMDataExtractor) extractStringResult(response []byte) (string, error) {
	jsonrpcResp, err := e.parseJSONRPCResponse(response)
	if err != nil {
		return "", err
	}

	if jsonrpcResp.Error != nil {
		return "", fmt.Errorf("JSON-RPC error: code=%d, message=%s",
			jsonrpcResp.Error.Code, jsonrpcResp.Error.Message)
	}

	if jsonrpcResp.Result == nil {
		return "", fmt.Errorf("response missing result field")
	}

	var result string
	if err := json.Unmarshal(*jsonrpcResp.Result, &result); err != nil {
		return "", fmt.Errorf("unmarshal result as string: %w", err)
	}

	return result, nil
}

// parseJSONRPCResponse parses raw bytes into a JSON-RPC response struct.
func (e *EVMDataExtractor) parseJSONRPCResponse(response []byte) (*jsonrpc.Response, error) {
	var jsonrpcResp jsonrpc.Response
	if err := json.Unmarshal(response, &jsonrpcResp); err != nil {
		return nil, fmt.Errorf("unmarshal JSON-RPC response: %w", err)
	}
	return &jsonrpcResp, nil
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
