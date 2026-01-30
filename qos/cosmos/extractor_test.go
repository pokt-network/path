package cosmos

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCosmosDataExtractor_ExtractBlockHeight(t *testing.T) {
	extractor := NewCosmosDataExtractor()

	tests := []struct {
		name          string
		response      string
		expectedBlock int64
		expectError   bool
	}{
		{
			name: "CometBFT status response",
			response: `{
				"jsonrpc": "2.0",
				"id": 1,
				"result": {
					"node_info": {"network": "cosmoshub-4"},
					"sync_info": {"latest_block_height": "12345678", "catching_up": false}
				}
			}`,
			expectedBlock: 12345678,
			expectError:   false,
		},
		{
			name:          "Cosmos SDK REST status response",
			response:      `{"height": "9876543"}`,
			expectedBlock: 9876543,
			expectError:   false,
		},
		{
			name:        "JSON-RPC error response",
			response:    `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Invalid Request"}}`,
			expectError: true,
		},
		{
			name:        "empty response",
			response:    ``,
			expectError: true,
		},
		{
			name:        "invalid json",
			response:    `{invalid}`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blockHeight, err := extractor.ExtractBlockHeight(nil, []byte(tt.response))
			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedBlock, blockHeight)
			}
		})
	}
}

func TestCosmosDataExtractor_ExtractChainID(t *testing.T) {
	extractor := NewCosmosDataExtractor()

	tests := []struct {
		name            string
		response        string
		expectedChainID string
		expectError     bool
	}{
		{
			name: "Cosmos Hub mainnet",
			response: `{
				"jsonrpc": "2.0",
				"id": 1,
				"result": {
					"node_info": {"network": "cosmoshub-4"},
					"sync_info": {"latest_block_height": "12345", "catching_up": false}
				}
			}`,
			expectedChainID: "cosmoshub-4",
			expectError:     false,
		},
		{
			name: "Osmosis mainnet",
			response: `{
				"jsonrpc": "2.0",
				"id": 1,
				"result": {
					"node_info": {"network": "osmosis-1"},
					"sync_info": {"latest_block_height": "54321", "catching_up": false}
				}
			}`,
			expectedChainID: "osmosis-1",
			expectError:     false,
		},
		{
			name:        "REST response (no chain ID)",
			response:    `{"height": "12345"}`,
			expectError: true,
		},
		{
			name:        "error response",
			response:    `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Invalid Request"}}`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chainID, err := extractor.ExtractChainID(nil, []byte(tt.response))
			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedChainID, chainID)
			}
		})
	}
}

func TestCosmosDataExtractor_IsSyncing(t *testing.T) {
	extractor := NewCosmosDataExtractor()

	tests := []struct {
		name         string
		response     string
		expectedSync bool
		expectError  bool
	}{
		{
			name: "not syncing",
			response: `{
				"jsonrpc": "2.0",
				"id": 1,
				"result": {
					"node_info": {"network": "cosmoshub-4"},
					"sync_info": {"latest_block_height": "12345", "catching_up": false}
				}
			}`,
			expectedSync: false,
			expectError:  false,
		},
		{
			name: "syncing (catching up)",
			response: `{
				"jsonrpc": "2.0",
				"id": 1,
				"result": {
					"node_info": {"network": "cosmoshub-4"},
					"sync_info": {"latest_block_height": "100", "catching_up": true}
				}
			}`,
			expectedSync: true,
			expectError:  false,
		},
		{
			name:        "error response",
			response:    `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Method not found"}}`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isSyncing, err := extractor.IsSyncing(nil, []byte(tt.response))
			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedSync, isSyncing)
			}
		})
	}
}

func TestCosmosDataExtractor_IsArchival(t *testing.T) {
	extractor := NewCosmosDataExtractor()

	tests := []struct {
		name             string
		response         string
		expectedArchival bool
		expectError      bool
	}{
		{
			name:             "archival node (data returned)",
			response:         `{"block": {"header": {"height": "1"}}}`,
			expectedArchival: true,
			expectError:      false,
		},
		{
			name:             "non-archival (pruned)",
			response:         `{"error": "block has been pruned"}`,
			expectedArchival: false,
			expectError:      false,
		},
		{
			name:             "non-archival (not available)",
			response:         `{"error": "height is not available"}`,
			expectedArchival: false,
			expectError:      false,
		},
		{
			name: "non-archival (JSON-RPC error)",
			response: `{
				"jsonrpc": "2.0",
				"id": 1,
				"error": {"code": -32000, "message": "block has been pruned"}
			}`,
			expectedArchival: false,
			expectError:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isArchival, err := extractor.IsArchival(nil, []byte(tt.response))
			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedArchival, isArchival)
			}
		})
	}
}

func TestCosmosDataExtractor_IsValidResponse(t *testing.T) {
	extractor := NewCosmosDataExtractor()

	tests := []struct {
		name          string
		response      string
		expectedValid bool
	}{
		{
			name: "valid JSON-RPC response",
			response: `{
				"jsonrpc": "2.0",
				"id": 1,
				"result": {"node_info": {"network": "cosmoshub-4"}}
			}`,
			expectedValid: true,
		},
		{
			name:          "valid REST response",
			response:      `{"height": "12345"}`,
			expectedValid: true,
		},
		{
			name:          "JSON-RPC error response",
			response:      `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Invalid Request"}}`,
			expectedValid: false,
		},
		{
			name:          "REST error response",
			response:      `{"error": "something went wrong"}`,
			expectedValid: false,
		},
		{
			name:          "invalid json",
			response:      `{invalid}`,
			expectedValid: false,
		},
		{
			name:          "empty response",
			response:      ``,
			expectedValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isValid, err := extractor.IsValidResponse(nil, []byte(tt.response))
			require.NoError(t, err)
			assert.Equal(t, tt.expectedValid, isValid)
		})
	}
}
