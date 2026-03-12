package solana

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSolanaDataExtractor_ExtractBlockHeight(t *testing.T) {
	extractor := NewSolanaDataExtractor()

	tests := []struct {
		name          string
		response      string
		expectedBlock int64
		expectError   bool
	}{
		{
			name: "valid getEpochInfo response",
			response: `{
				"jsonrpc": "2.0",
				"id": 1,
				"result": {
					"absoluteSlot": 166598,
					"blockHeight": 166500,
					"epoch": 27,
					"slotIndex": 2790,
					"slotsInEpoch": 8192,
					"transactionCount": 22661093
				}
			}`,
			expectedBlock: 166500,
			expectError:   false,
		},
		{
			name: "large block height",
			response: `{
				"jsonrpc": "2.0",
				"id": 1,
				"result": {
					"blockHeight": 250000000,
					"epoch": 500
				}
			}`,
			expectedBlock: 250000000,
			expectError:   false,
		},
		{
			name:        "error response",
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

func TestSolanaDataExtractor_ExtractChainID(t *testing.T) {
	extractor := NewSolanaDataExtractor()

	tests := []struct {
		name            string
		response        string
		expectedChainID string
		expectError     bool
	}{
		{
			name: "getVersion response with solana-core",
			response: `{
				"jsonrpc": "2.0",
				"id": 1,
				"result": {
					"solana-core": "1.14.17",
					"feature-set": 3241752014
				}
			}`,
			expectedChainID: "1.14.17",
			expectError:     false,
		},
		{
			name: "getEpochInfo response (no chain ID)",
			response: `{
				"jsonrpc": "2.0",
				"id": 1,
				"result": {
					"blockHeight": 166500,
					"epoch": 27
				}
			}`,
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

func TestSolanaDataExtractor_IsSyncing(t *testing.T) {
	extractor := NewSolanaDataExtractor()

	tests := []struct {
		name         string
		response     string
		expectedSync bool
		expectError  bool
	}{
		{
			name:         "healthy node (not syncing)",
			response:     `{"jsonrpc":"2.0","id":1,"result":"ok"}`,
			expectedSync: false,
			expectError:  false,
		},
		{
			name:         "node behind (syncing)",
			response:     `{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"Node is behind by 42 slots"}}`,
			expectedSync: true,
			expectError:  false,
		},
		{
			name:         "unhealthy node",
			response:     `{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"Node is unhealthy"}}`,
			expectedSync: true,
			expectError:  false,
		},
		{
			name:        "other error",
			response:    `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Invalid params"}}`,
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

func TestSolanaDataExtractor_IsArchival(t *testing.T) {
	extractor := NewSolanaDataExtractor()

	tests := []struct {
		name             string
		response         string
		expectedArchival bool
		expectError      bool
	}{
		{
			name: "archival node (data returned)",
			response: `{
				"jsonrpc": "2.0",
				"id": 1,
				"result": {
					"blockhash": "3Eq21vXNB5s86c62bVuUfTeaMif1N2kUqRPBmGRJhyTA"
				}
			}`,
			expectedArchival: true,
			expectError:      false,
		},
		{
			name:             "non-archival (slot skipped)",
			response:         `{"jsonrpc":"2.0","id":1,"error":{"code":-32009,"message":"Slot was skipped"}}`,
			expectedArchival: false,
			expectError:      false,
		},
		{
			name:             "non-archival (block not available)",
			response:         `{"jsonrpc":"2.0","id":1,"error":{"code":-32004,"message":"Block not available for slot 100"}}`,
			expectedArchival: false,
			expectError:      false,
		},
		{
			name:             "non-archival (slot too old)",
			response:         `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"Slot too old; min slot 150000000"}}`,
			expectedArchival: false,
			expectError:      false,
		},
		{
			name:        "other error",
			response:    `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Invalid params"}}`,
			expectError: true,
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

func TestSolanaDataExtractor_IsValidResponse(t *testing.T) {
	extractor := NewSolanaDataExtractor()

	tests := []struct {
		name          string
		response      string
		expectedValid bool
	}{
		{
			name:          "valid response with result",
			response:      `{"jsonrpc":"2.0","id":1,"result":"ok"}`,
			expectedValid: true,
		},
		{
			name: "valid response with object result",
			response: `{
				"jsonrpc": "2.0",
				"id": 1,
				"result": {"blockHeight": 166500}
			}`,
			expectedValid: true,
		},
		{
			name:          "error response (not valid for QoS)",
			response:      `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Invalid Request"}}`,
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
		{
			name:          "wrong version",
			response:      `{"jsonrpc":"1.0","id":1,"result":"ok"}`,
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
