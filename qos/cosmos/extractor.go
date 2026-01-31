// Package cosmos provides a DataExtractor implementation for Cosmos SDK-based blockchains.
//
// The CosmosDataExtractor knows how to extract quality data from multiple response formats:
//   - CometBFT JSON-RPC responses (status endpoint)
//   - Cosmos SDK REST responses (status endpoint)
//
// Cosmos chains can return data in different formats depending on the endpoint:
//   - /status (CometBFT): JSON-RPC with node_info, sync_info
//   - /cosmos/base/node/v1beta1/status: REST with height field
//
// Uses gjson for efficient field extraction without full unmarshalling.
package cosmos

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/pokt-network/path/qos/jsonrpc"
	qostypes "github.com/pokt-network/path/qos/types"
)

// Verify CosmosDataExtractor implements the DataExtractor interface at compile time.
var _ qostypes.DataExtractor = (*CosmosDataExtractor)(nil)

// CosmosDataExtractor extracts quality data from Cosmos SDK and CometBFT responses.
// It handles multiple response formats common in the Cosmos ecosystem.
// Uses gjson for efficient field extraction without full unmarshalling.
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

	// Try CometBFT JSON-RPC format - network is in result.node_info.network
	network := gjson.GetBytes(response, "result.node_info.network")
	if network.Exists() && network.String() != "" {
		return network.String(), nil
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

	// Check for JSON-RPC error
	errorResult := gjson.GetBytes(response, "error")
	if errorResult.Exists() && errorResult.Type != gjson.Null {
		code := gjson.GetBytes(response, "error.code").Int()
		msg := gjson.GetBytes(response, "error.message").String()
		return false, fmt.Errorf("status returned error: code=%d, message=%s", code, msg)
	}

	// Check for result
	resultField := gjson.GetBytes(response, "result")
	if !resultField.Exists() {
		return false, fmt.Errorf("response missing result field")
	}

	// Get catching_up from sync_info
	catchingUp := gjson.GetBytes(response, "result.sync_info.catching_up")
	if !catchingUp.Exists() {
		return false, fmt.Errorf("sync_info.catching_up not found in response")
	}

	return catchingUp.Bool(), nil
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

	// Validate JSON
	if !gjson.ValidBytes(response) {
		return false, fmt.Errorf("invalid JSON response")
	}

	// Check for error field (common in REST responses)
	errorField := gjson.GetBytes(response, "error")
	if errorField.Exists() && errorField.Type == gjson.String {
		errMsg := strings.ToLower(errorField.String())
		pruningIndicators := []string{
			"pruned",
			"not available",
			"height is not available",
			"block not found",
			"could not retrieve",
		}
		for _, indicator := range pruningIndicators {
			if strings.Contains(errMsg, indicator) {
				return false, nil // Not archival
			}
		}
		return false, fmt.Errorf("query returned error: %s", errorField.String())
	}

	// Try JSON-RPC error format (error is an object)
	if errorField.Exists() && errorField.IsObject() {
		errMsg := strings.ToLower(gjson.GetBytes(response, "error.message").String())
		pruningIndicators := []string{
			"pruned",
			"not available",
			"height is not available",
			"block not found",
		}
		for _, indicator := range pruningIndicators {
			if strings.Contains(errMsg, indicator) {
				return false, nil // Not archival
			}
		}
		code := gjson.GetBytes(response, "error.code").Int()
		return false, fmt.Errorf("query returned error: code=%d, message=%s", code, errMsg)
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

	// Validate JSON
	if !gjson.ValidBytes(response) {
		return false, nil // Invalid JSON
	}

	// Check for explicit error field (REST - string error)
	errorField := gjson.GetBytes(response, "error")
	if errorField.Exists() && errorField.Type != gjson.Null {
		return false, nil
	}

	// Check for JSON-RPC format
	version := gjson.GetBytes(response, "jsonrpc")
	if version.Exists() && version.String() == string(jsonrpc.Version2) {
		// Has result - valid
		resultField := gjson.GetBytes(response, "result")
		if resultField.Exists() && resultField.Type != gjson.Null {
			return true, nil
		}
		// Neither result nor error - invalid JSON-RPC
		return false, nil
	}

	// Valid JSON but not JSON-RPC - assume REST format is valid
	return true, nil
}

// extractCometBFTBlockHeight extracts block height from CometBFT JSON-RPC status response.
func (e *CosmosDataExtractor) extractCometBFTBlockHeight(response []byte) (int64, error) {
	// Check for JSON-RPC error
	errorResult := gjson.GetBytes(response, "error")
	if errorResult.Exists() && errorResult.Type != gjson.Null {
		code := gjson.GetBytes(response, "error.code").Int()
		msg := gjson.GetBytes(response, "error.message").String()
		return 0, fmt.Errorf("JSON-RPC error: code=%d, message=%s", code, msg)
	}

	// Get block height from sync_info
	heightStr := gjson.GetBytes(response, "result.sync_info.latest_block_height")
	if !heightStr.Exists() || heightStr.String() == "" {
		return 0, fmt.Errorf("no block height in result")
	}

	height, err := strconv.ParseInt(heightStr.String(), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse block height %q: %w", heightStr.String(), err)
	}

	return height, nil
}

// extractCosmosRESTBlockHeight extracts block height from Cosmos SDK REST status response.
func (e *CosmosDataExtractor) extractCosmosRESTBlockHeight(response []byte) (int64, error) {
	// Try direct height field
	heightStr := gjson.GetBytes(response, "height")
	if !heightStr.Exists() || heightStr.String() == "" {
		return 0, fmt.Errorf("no height in response")
	}

	height, err := strconv.ParseInt(heightStr.String(), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse height %q: %w", heightStr.String(), err)
	}

	return height, nil
}
