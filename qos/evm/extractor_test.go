package evm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEVMDataExtractor_ExtractBlockHeight(t *testing.T) {
	extractor := NewEVMDataExtractor()

	tests := []struct {
		name          string
		response      string
		expectedBlock int64
		expectError   bool
	}{
		{
			name:          "valid block height",
			response:      `{"jsonrpc":"2.0","id":1,"result":"0x10d4f"}`,
			expectedBlock: 68943,
			expectError:   false,
		},
		{
			name:          "block height zero",
			response:      `{"jsonrpc":"2.0","id":1,"result":"0x0"}`,
			expectedBlock: 0,
			expectError:   false,
		},
		{
			name:          "large block height",
			response:      `{"jsonrpc":"2.0","id":1,"result":"0x1234567"}`,
			expectedBlock: 19088743,
			expectError:   false,
		},
		{
			name:        "error response",
			response:    `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Invalid Request"}}`,
			expectError: true,
		},
		{
			name:        "invalid hex",
			response:    `{"jsonrpc":"2.0","id":1,"result":"not-a-hex"}`,
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
		{
			name:        "missing result",
			response:    `{"jsonrpc":"2.0","id":1}`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blockHeight, err := extractor.ExtractBlockHeight([]byte(tt.response))
			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedBlock, blockHeight)
			}
		})
	}
}

func TestEVMDataExtractor_ExtractChainID(t *testing.T) {
	extractor := NewEVMDataExtractor()

	tests := []struct {
		name            string
		response        string
		expectedChainID string
		expectError     bool
	}{
		{
			name:            "ethereum mainnet",
			response:        `{"jsonrpc":"2.0","id":1,"result":"0x1"}`,
			expectedChainID: "0x1",
			expectError:     false,
		},
		{
			name:            "polygon mainnet",
			response:        `{"jsonrpc":"2.0","id":1,"result":"0x89"}`,
			expectedChainID: "0x89",
			expectError:     false,
		},
		{
			name:            "base mainnet",
			response:        `{"jsonrpc":"2.0","id":1,"result":"0x2105"}`,
			expectedChainID: "0x2105",
			expectError:     false,
		},
		{
			name:        "error response",
			response:    `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Invalid Request"}}`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chainID, err := extractor.ExtractChainID([]byte(tt.response))
			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedChainID, chainID)
			}
		})
	}
}

func TestEVMDataExtractor_IsSyncing(t *testing.T) {
	extractor := NewEVMDataExtractor()

	tests := []struct {
		name         string
		response     string
		expectedSync bool
		expectError  bool
	}{
		{
			name:         "not syncing (false)",
			response:     `{"jsonrpc":"2.0","id":1,"result":false}`,
			expectedSync: false,
			expectError:  false,
		},
		{
			name:         "syncing (object)",
			response:     `{"jsonrpc":"2.0","id":1,"result":{"startingBlock":"0x0","currentBlock":"0x100","highestBlock":"0x1000"}}`,
			expectedSync: true,
			expectError:  false,
		},
		{
			name:        "error response",
			response:    `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Method not found"}}`,
			expectError: true,
		},
		{
			name:        "missing result",
			response:    `{"jsonrpc":"2.0","id":1}`,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isSyncing, err := extractor.IsSyncing([]byte(tt.response))
			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedSync, isSyncing)
			}
		})
	}
}

func TestEVMDataExtractor_IsArchival(t *testing.T) {
	extractor := NewEVMDataExtractor()

	tests := []struct {
		name             string
		response         string
		expectedArchival bool
		expectError      bool
	}{
		{
			name:             "archival node (balance returned)",
			response:         `{"jsonrpc":"2.0","id":1,"result":"0x0"}`,
			expectedArchival: true,
			expectError:      false,
		},
		{
			name:             "archival node (non-zero balance)",
			response:         `{"jsonrpc":"2.0","id":1,"result":"0x1234567890"}`,
			expectedArchival: true,
			expectError:      false,
		},
		{
			name:             "non-archival (missing trie node)",
			response:         `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"missing trie node abc123"}}`,
			expectedArchival: false,
			expectError:      false,
		},
		{
			name:             "non-archival (pruned)",
			response:         `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"state has been pruned"}}`,
			expectedArchival: false,
			expectError:      false,
		},
		{
			name:             "non-archival (state not available)",
			response:         `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"state not available"}}`,
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
			isArchival, err := extractor.IsArchival([]byte(tt.response))
			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedArchival, isArchival)
			}
		})
	}
}

func TestEVMDataExtractor_IsValidResponse(t *testing.T) {
	extractor := NewEVMDataExtractor()

	tests := []struct {
		name          string
		response      string
		expectedValid bool
	}{
		{
			name:          "valid response with result",
			response:      `{"jsonrpc":"2.0","id":1,"result":"0x1"}`,
			expectedValid: true,
		},
		{
			name:          "valid response with null result",
			response:      `{"jsonrpc":"2.0","id":1,"result":null}`,
			expectedValid: true,
		},
		{
			name:          "error response (not considered valid for QoS)",
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
			response:      `{"jsonrpc":"1.0","id":1,"result":"0x1"}`,
			expectedValid: false,
		},
		{
			name:          "missing result and error",
			response:      `{"jsonrpc":"2.0","id":1}`,
			expectedValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isValid, err := extractor.IsValidResponse([]byte(tt.response))
			require.NoError(t, err)
			assert.Equal(t, tt.expectedValid, isValid)
		})
	}
}

func TestParseHexToInt64(t *testing.T) {
	tests := []struct {
		name        string
		hexStr      string
		expected    int64
		expectError bool
	}{
		{
			name:     "with 0x prefix",
			hexStr:   "0x10d4f",
			expected: 68943,
		},
		{
			name:     "with 0X prefix",
			hexStr:   "0X10d4f",
			expected: 68943,
		},
		{
			name:     "without prefix",
			hexStr:   "10d4f",
			expected: 68943,
		},
		{
			name:     "zero",
			hexStr:   "0x0",
			expected: 0,
		},
		{
			name:     "one",
			hexStr:   "0x1",
			expected: 1,
		},
		{
			name:     "large number",
			hexStr:   "0xffffffff",
			expected: 4294967295,
		},
		{
			name:        "empty string",
			hexStr:      "",
			expectError: true,
		},
		{
			name:        "just prefix",
			hexStr:      "0x",
			expectError: true,
		},
		{
			name:        "invalid hex",
			hexStr:      "0xGHIJ",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseHexToInt64(tt.hexStr)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}
