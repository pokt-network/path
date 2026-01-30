// Package cosmos provides a DataExtractor implementation for Cosmos SDK-based blockchains.
//
// The CosmosDataExtractor knows how to extract quality data from multiple response formats:
//   - CometBFT JSON-RPC responses (status endpoint)
//   - Cosmos SDK REST responses (status endpoint)
//
// Cosmos chains can return data in different formats depending on the endpoint:
//   - /status (CometBFT): JSON-RPC with node_info, sync_info
//   - /cosmos/base/node/v1beta1/status: REST with height field
package cosmos

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/pokt-network/path/qos/jsonrpc"
	qostypes "github.com/pokt-network/path/qos/types"
)

// Verify CosmosDataExtractor implements the DataExtractor interface at compile time.
var _ qostypes.DataExtractor = (*CosmosDataExtractor)(nil)

// CosmosDataExtractor extracts quality data from Cosmos SDK and CometBFT responses.
// It handles multiple response formats common in the Cosmos ecosystem.
type CosmosDataExtractor struct{}

// NewCosmosDataExtractor creates a new Cosmos data extractor.
func NewCosmosDataExtractor() *CosmosDataExtractor {
	return &CosmosDataExtractor{}
}

// ExtractBlockHeight extracts the block height from a Cosmos response.
// Supports both CometBFT status (JSON-RPC) and Cosmos SDK REST status responses.
//
// CometBFT format:
//
//	{"jsonrpc":"2.0","id":1,"result":{"sync_info":{"latest_block_height":"12345"}}}
//
// Cosmos SDK REST format:
//
//	{"height":"12345"}
//
// Returns:
//   - Block height as int64
//   - Error if extraction fails or response doesn't contain block height
func (e *CosmosDataExtractor) ExtractBlockHeight(request []byte, response []byte) (int64, error) {
	if len(response) == 0 {
		return 0, fmt.Errorf("empty response")
	}

	// Try CometBFT JSON-RPC format first
	if height, err := e.extractCometBFTBlockHeight(response); err == nil {
		return height, nil
	}

	// Try Cosmos SDK REST format
	if height, err := e.extractCosmosRESTBlockHeight(response); err == nil {
		return height, nil
	}

	return 0, fmt.Errorf("could not extract block height from response")
}

// ExtractChainID extracts the chain identifier from a Cosmos response.
// The chain ID comes from the CometBFT status response's node_info.network field.
//
// Expected response format (CometBFT):
//
//	{"jsonrpc":"2.0","id":1,"result":{"node_info":{"network":"cosmoshub-4"}}}
//
// Returns:
//   - Chain ID as string (e.g., "cosmoshub-4", "osmosis-1")
//   - Error if extraction fails or response doesn't contain chain ID
func (e *CosmosDataExtractor) ExtractChainID(request []byte, response []byte) (string, error) {
	if len(response) == 0 {
		return "", fmt.Errorf("empty response")
	}

	// CometBFT JSON-RPC format
	var jsonrpcResp jsonrpc.Response
	if err := json.Unmarshal(response, &jsonrpcResp); err == nil && jsonrpcResp.Result != nil {
		var result ResultStatus
		resultBytes, err := json.Marshal(jsonrpcResp.Result)
		if err != nil {
			return "", fmt.Errorf("marshal result: %w", err)
		}
		if err := json.Unmarshal(resultBytes, &result); err == nil {
			if result.NodeInfo.Network != "" {
				return result.NodeInfo.Network, nil
			}
		}
	}

	return "", fmt.Errorf("could not extract chain ID from response")
}

// IsSyncing determines if the endpoint is currently syncing.
// Uses the CometBFT status response's sync_info.catching_up field.
//
// Expected response format:
//
//	{"jsonrpc":"2.0","id":1,"result":{"sync_info":{"catching_up":false}}}
//
// Returns:
//   - true if endpoint is syncing (catching_up = true)
//   - false if endpoint is synced (catching_up = false)
//   - Error if sync status cannot be determined
func (e *CosmosDataExtractor) IsSyncing(request []byte, response []byte) (bool, error) {
	if len(response) == 0 {
		return false, fmt.Errorf("empty response")
	}

	// CometBFT JSON-RPC format
	var jsonrpcResp jsonrpc.Response
	if err := json.Unmarshal(response, &jsonrpcResp); err != nil {
		return false, fmt.Errorf("parse JSON-RPC response: %w", err)
	}

	if jsonrpcResp.Error != nil {
		return false, fmt.Errorf("status returned error: code=%d, message=%s",
			jsonrpcResp.Error.Code, jsonrpcResp.Error.Message)
	}

	if jsonrpcResp.Result == nil {
		return false, fmt.Errorf("response missing result field")
	}

	var result ResultStatus
	resultBytes, err := json.Marshal(jsonrpcResp.Result)
	if err != nil {
		return false, fmt.Errorf("marshal result: %w", err)
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		return false, fmt.Errorf("parse status result: %w", err)
	}

	return result.SyncInfo.CatchingUp, nil
}

// IsArchival determines if the endpoint supports archival queries.
// For Cosmos chains, this is typically checked by querying historical block data.
//
// An archival node will return data for historical queries.
// A non-archival (pruned) node will return an error for old blocks.
//
// Returns:
//   - true if endpoint is archival (query succeeded)
//   - false if endpoint is not archival (query failed with pruning error)
//   - Error if archival status cannot be determined
func (e *CosmosDataExtractor) IsArchival(request []byte, response []byte) (bool, error) {
	if len(response) == 0 {
		return false, fmt.Errorf("empty response")
	}

	// Try to parse as JSON
	var parsed map[string]interface{}
	if err := json.Unmarshal(response, &parsed); err != nil {
		return false, fmt.Errorf("parse response: %w", err)
	}

	// Check for error field (common in REST responses)
	if errMsg, ok := parsed["error"].(string); ok {
		errMsgLower := strings.ToLower(errMsg)
		pruningIndicators := []string{
			"pruned",
			"not available",
			"height is not available",
			"block not found",
			"could not retrieve",
		}
		for _, indicator := range pruningIndicators {
			if strings.Contains(errMsgLower, indicator) {
				return false, nil // Not archival
			}
		}
		return false, fmt.Errorf("query returned error: %s", errMsg)
	}

	// Try JSON-RPC error format
	var jsonrpcResp jsonrpc.Response
	if err := json.Unmarshal(response, &jsonrpcResp); err == nil && jsonrpcResp.Error != nil {
		errMsgLower := strings.ToLower(jsonrpcResp.Error.Message)
		pruningIndicators := []string{
			"pruned",
			"not available",
			"height is not available",
			"block not found",
		}
		for _, indicator := range pruningIndicators {
			if strings.Contains(errMsgLower, indicator) {
				return false, nil // Not archival
			}
		}
		return false, fmt.Errorf("query returned error: code=%d, message=%s",
			jsonrpcResp.Error.Code, jsonrpcResp.Error.Message)
	}

	// No error in response - assume archival
	return true, nil
}

// IsValidResponse checks if the response is valid.
// Supports both JSON-RPC and REST response formats.
//
// Returns:
//   - true if response is valid (correct format, no errors)
//   - false if response is invalid (malformed, contains error)
//   - Error if validation fails unexpectedly
func (e *CosmosDataExtractor) IsValidResponse(request []byte, response []byte) (bool, error) {
	if len(response) == 0 {
		return false, nil
	}

	// Try to parse as generic JSON first
	var parsed map[string]interface{}
	if err := json.Unmarshal(response, &parsed); err != nil {
		return false, nil // Invalid JSON
	}

	// Check for explicit error field (REST)
	if _, hasError := parsed["error"]; hasError {
		return false, nil
	}

	// Check for JSON-RPC format
	var jsonrpcResp jsonrpc.Response
	if err := json.Unmarshal(response, &jsonrpcResp); err == nil {
		// Valid JSON-RPC response
		if jsonrpcResp.Version == jsonrpc.Version2 {
			// Has error - not valid for QoS
			if jsonrpcResp.Error != nil {
				return false, nil
			}
			// Has result - valid
			if jsonrpcResp.Result != nil {
				return true, nil
			}
			// Neither result nor error - invalid JSON-RPC
			return false, nil
		}
	}

	// Valid JSON but not JSON-RPC - assume REST format is valid
	return true, nil
}

// extractCometBFTBlockHeight extracts block height from CometBFT JSON-RPC status response.
func (e *CosmosDataExtractor) extractCometBFTBlockHeight(response []byte) (int64, error) {
	var jsonrpcResp jsonrpc.Response
	if err := json.Unmarshal(response, &jsonrpcResp); err != nil {
		return 0, fmt.Errorf("parse JSON-RPC response: %w", err)
	}

	if jsonrpcResp.Error != nil {
		return 0, fmt.Errorf("JSON-RPC error: code=%d, message=%s",
			jsonrpcResp.Error.Code, jsonrpcResp.Error.Message)
	}

	if jsonrpcResp.Result == nil {
		return 0, fmt.Errorf("missing result field")
	}

	var result ResultStatus
	resultBytes, err := json.Marshal(jsonrpcResp.Result)
	if err != nil {
		return 0, fmt.Errorf("marshal result: %w", err)
	}
	if err := json.Unmarshal(resultBytes, &result); err != nil {
		return 0, fmt.Errorf("parse status result: %w", err)
	}

	if result.SyncInfo.LatestBlockHeight == "" {
		return 0, fmt.Errorf("no block height in result")
	}

	height, err := strconv.ParseInt(result.SyncInfo.LatestBlockHeight, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse block height %q: %w", result.SyncInfo.LatestBlockHeight, err)
	}

	return height, nil
}

// extractCosmosRESTBlockHeight extracts block height from Cosmos SDK REST status response.
func (e *CosmosDataExtractor) extractCosmosRESTBlockHeight(response []byte) (int64, error) {
	var result cosmosStatusResponse
	if err := json.Unmarshal(response, &result); err != nil {
		return 0, fmt.Errorf("parse REST response: %w", err)
	}

	if result.Height == "" {
		return 0, fmt.Errorf("no height in response")
	}

	height, err := strconv.ParseInt(result.Height, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse height %q: %w", result.Height, err)
	}

	return height, nil
}
