// Package solana provides a DataExtractor implementation for Solana blockchain.
//
// The SolanaDataExtractor knows how to extract quality data from Solana JSON-RPC responses:
//   - Block height from getEpochInfo responses
//   - Health status from getHealth responses
//   - Cluster (chain) info from getClusterNodes or getVersion responses
//
// Solana uses JSON-RPC 2.0 for all RPC calls, similar to EVM chains.
// Uses gjson for efficient field extraction without full unmarshalling.
package solana

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/pokt-network/path/qos/jsonrpc"
	qostypes "github.com/pokt-network/path/qos/types"
)

// Verify SolanaDataExtractor implements the DataExtractor interface at compile time.
var _ qostypes.DataExtractor = (*SolanaDataExtractor)(nil)

// SolanaDataExtractor extracts quality data from Solana JSON-RPC responses.
// Uses gjson for efficient field extraction without full unmarshalling.
type SolanaDataExtractor struct{}

// NewSolanaDataExtractor creates a new Solana data extractor.
func NewSolanaDataExtractor() *SolanaDataExtractor {
	return &SolanaDataExtractor{}
}

// ExtractBlockHeight extracts the block height from a getEpochInfo response.
// Solana's block height comes from the epochInfo result's blockHeight field.
//
// Expected response format:
//
//	{"jsonrpc":"2.0","id":1,"result":{"blockHeight":123456789,"epoch":100,...}}
//
// Returns:
//   - Block height as int64
//   - Error if extraction fails or response doesn't contain block height
func (e *SolanaDataExtractor) ExtractBlockHeight(request []byte, response []byte) (int64, error) {
	// Check for error first
	errorResult := gjson.GetBytes(response, "error")
	if errorResult.Exists() && errorResult.Type != gjson.Null {
		code := gjson.GetBytes(response, "error.code").Int()
		msg := gjson.GetBytes(response, "error.message").String()
		return 0, fmt.Errorf("getEpochInfo returned error: code=%d, message=%s", code, msg)
	}

	// Check for result field
	resultField := gjson.GetBytes(response, "result")
	if !resultField.Exists() || resultField.Type == gjson.Null {
		return 0, fmt.Errorf("response missing result field")
	}

	// Get blockHeight from result
	// Note: Solana returns blockHeight as a number, or we might also check absoluteSlot
	blockHeight := gjson.GetBytes(response, "result.blockHeight")
	if blockHeight.Exists() && blockHeight.Type == gjson.Number {
		return blockHeight.Int(), nil
	}

	// Try absoluteSlot as fallback (some responses use this)
	absoluteSlot := gjson.GetBytes(response, "result.absoluteSlot")
	if absoluteSlot.Exists() && absoluteSlot.Type == gjson.Number {
		return absoluteSlot.Int(), nil
	}

	return 0, fmt.Errorf("could not extract block height from response")
}

// ExtractChainID extracts the cluster identifier from a Solana response.
// Solana doesn't have a traditional chain ID like EVM chains. Instead, it uses
// cluster names (mainnet-beta, devnet, testnet) or genesis hash.
//
// This method attempts to extract cluster info from getClusterNodes or getVersion responses,
// or from the feature set in getEpochInfo.
//
// Note: For Solana, chain identification is typically done via the genesis hash
// or by querying getClusterNodes. This returns an empty string with an error
// for responses that don't contain cluster information.
//
// Returns:
//   - Cluster identifier as string (e.g., solana-core version)
//   - Error if extraction fails
func (e *SolanaDataExtractor) ExtractChainID(request []byte, response []byte) (string, error) {
	// Check for error first
	errorResult := gjson.GetBytes(response, "error")
	if errorResult.Exists() && errorResult.Type != gjson.Null {
		code := gjson.GetBytes(response, "error.code").Int()
		msg := gjson.GetBytes(response, "error.message").String()
		return "", fmt.Errorf("response returned error: code=%d, message=%s", code, msg)
	}

	// Check for result field
	resultField := gjson.GetBytes(response, "result")
	if !resultField.Exists() || resultField.Type == gjson.Null {
		return "", fmt.Errorf("response missing result field")
	}

	// Try to extract version info (from getVersion) - solana-core field
	solanaCore := gjson.GetBytes(response, "result.solana-core")
	if solanaCore.Exists() && solanaCore.String() != "" {
		return solanaCore.String(), nil
	}

	// For Solana, chain ID extraction is not straightforward like EVM
	// Return error indicating this response doesn't contain chain ID
	return "", fmt.Errorf("response doesn't contain cluster/chain identifier")
}

// IsSyncing determines if the endpoint is currently syncing.
// Uses the getHealth response to determine health status.
//
// getHealth returns:
//   - "ok" when the node is healthy and not syncing
//   - An error response when the node is unhealthy or syncing
//
// Expected response format (healthy):
//
//	{"jsonrpc":"2.0","id":1,"result":"ok"}
//
// Expected response format (unhealthy/syncing):
//
//	{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"Node is behind by 42 slots"}}
//
// Returns:
//   - true if endpoint is syncing/unhealthy
//   - false if endpoint is healthy (not syncing)
//   - Error if sync status cannot be determined
func (e *SolanaDataExtractor) IsSyncing(request []byte, response []byte) (bool, error) {
	// If getHealth returns an error, the node is unhealthy (possibly syncing)
	errorResult := gjson.GetBytes(response, "error")
	if errorResult.Exists() && errorResult.Type != gjson.Null {
		// Check if it's a "behind" error which indicates syncing
		errMsg := strings.ToLower(gjson.GetBytes(response, "error.message").String())
		if strings.Contains(errMsg, "behind") || strings.Contains(errMsg, "unhealthy") {
			return true, nil // Node is syncing/behind
		}
		// Other errors - return error to caller
		code := gjson.GetBytes(response, "error.code").Int()
		return false, fmt.Errorf("getHealth returned error: code=%d, message=%s", code, errMsg)
	}

	// Check for result field
	resultField := gjson.GetBytes(response, "result")
	if !resultField.Exists() || resultField.Type == gjson.Null {
		return false, fmt.Errorf("response missing result field")
	}

	// Result should be "ok" for healthy node
	healthResult := resultField.String()

	// "ok" means healthy (not syncing)
	// Anything else means unhealthy/syncing
	return healthResult != "ok", nil
}

// IsArchival determines if the endpoint supports archival queries.
// For Solana, archival nodes store all transaction and block data.
// Non-archival nodes only keep recent data (typically ~2 epochs).
//
// This is checked by querying historical slot data. An archival node
// will return data for old slots, while a non-archival node will return an error.
//
// Returns:
//   - true if endpoint is archival (historical query succeeded)
//   - false if endpoint is not archival (historical query failed)
//   - Error if archival status cannot be determined
func (e *SolanaDataExtractor) IsArchival(request []byte, response []byte) (bool, error) {
	// Check for error in the response
	errorResult := gjson.GetBytes(response, "error")
	if errorResult.Exists() && errorResult.Type != gjson.Null {
		errMsg := strings.ToLower(gjson.GetBytes(response, "error.message").String())
		archivalErrorIndicators := []string{
			"slot was skipped",
			"block not available",
			"slot is not available",
			"long-term storage query",
			"first available block",
			"slot too old",
		}

		for _, indicator := range archivalErrorIndicators {
			if strings.Contains(errMsg, indicator) {
				return false, nil // Not archival
			}
		}

		// Some other error - can't determine archival status
		code := gjson.GetBytes(response, "error.code").Int()
		return false, fmt.Errorf("archival check returned error: code=%d, message=%s", code, errMsg)
	}

	// No error and has result - this is an archival node
	resultField := gjson.GetBytes(response, "result")
	if resultField.Exists() && resultField.Type != gjson.Null {
		return true, nil
	}

	return false, fmt.Errorf("archival check response missing both result and error")
}

// IsValidResponse checks if the response is a valid JSON-RPC 2.0 response.
// This performs basic structural validation without extracting specific data.
//
// Returns:
//   - true if response is valid JSON-RPC with result
//   - false if response is malformed or contains error
//   - Error if validation fails unexpectedly
func (e *SolanaDataExtractor) IsValidResponse(request []byte, response []byte) (bool, error) {
	if len(response) == 0 {
		return false, nil
	}

	// Validate JSON
	if !gjson.ValidBytes(response) {
		return false, nil // Invalid JSON or structure
	}

	// Check JSON-RPC version
	version := gjson.GetBytes(response, "jsonrpc")
	if !version.Exists() || version.String() != string(jsonrpc.Version2) {
		return false, nil
	}

	// Check for valid result/error combination
	resultField := gjson.GetBytes(response, "result")
	errorField := gjson.GetBytes(response, "error")

	hasResult := resultField.Exists() && resultField.Type != gjson.Null
	hasError := errorField.Exists() && errorField.Type != gjson.Null

	// Must have exactly one of result or error
	if !hasResult && !hasError {
		return false, nil
	}
	if hasResult && hasError {
		return false, nil
	}

	// Error responses are not considered valid for QoS purposes
	if hasError {
		return false, nil
	}

	return true, nil
}
