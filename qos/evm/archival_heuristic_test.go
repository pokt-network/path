package evm

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	gojson "github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test case structure for comprehensive coverage
type archivalTestCase struct {
	name           string
	request        string
	perceivedBlock uint64
	threshold      uint64
	expectArchival bool
	expectReason   string
	expectMethod   string
	expectBlockTag BlockTag
	expectBlockNum uint64
}

// ============================================================================
// UNIT TESTS - Comprehensive Coverage
// ============================================================================

func TestArchivalHeuristic_StateQueryMethods(t *testing.T) {
	tests := []archivalTestCase{
		// eth_getBalance tests
		{
			name:           "eth_getBalance with latest tag",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","latest"]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "block_tag_current",
			expectMethod:   "eth_getBalance",
			expectBlockTag: BlockTagLatest,
		},
		{
			name:           "eth_getBalance with pending tag",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","pending"]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "block_tag_current",
			expectMethod:   "eth_getBalance",
			expectBlockTag: BlockTagPending,
		},
		{
			name:           "eth_getBalance with safe tag",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","safe"]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "block_tag_current",
			expectMethod:   "eth_getBalance",
			expectBlockTag: BlockTagSafe,
		},
		{
			name:           "eth_getBalance with finalized tag",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","finalized"]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "block_tag_current",
			expectMethod:   "eth_getBalance",
			expectBlockTag: BlockTagFinalized,
		},
		{
			name:           "eth_getBalance with earliest tag - ARCHIVAL",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","earliest"]}`,
			perceivedBlock: 20000000,
			expectArchival: true,
			expectReason:   "block_tag_earliest",
			expectMethod:   "eth_getBalance",
			expectBlockTag: BlockTagEarliest,
		},
		{
			name:           "eth_getBalance with recent block - NOT archival",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","0x1312d00"]}`,
			perceivedBlock: 20000000, // 0x1312d00 = 20000000
			expectArchival: false,
			expectReason:   "block_within_threshold",
			expectMethod:   "eth_getBalance",
			expectBlockNum: 20000000,
		},
		{
			name:           "eth_getBalance with old block - ARCHIVAL",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","0xe71e1d"]}`,
			perceivedBlock: 20000000, // 0xe71e1d = 15138333 (decimal), which is ~4.8M blocks behind
			expectArchival: true,
			expectReason:   "block_behind_threshold",
			expectMethod:   "eth_getBalance",
			expectBlockNum: 0xe71e1d, // Use hex literal to ensure correct value
		},
		{
			name:           "eth_getBalance with block exactly at threshold - NOT archival",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","0x1312c80"]}`,
			perceivedBlock: 20000000, // 0x1312c80 = 19999872 = 20000000 - 128
			threshold:      128,
			expectArchival: false,
			expectReason:   "block_within_threshold",
			expectMethod:   "eth_getBalance",
			expectBlockNum: 19999872,
		},
		{
			name:           "eth_getBalance with block just past threshold - ARCHIVAL",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","0x1312c7f"]}`,
			perceivedBlock: 20000000, // 0x1312c7f = 19999871 = 20000000 - 129
			threshold:      128,
			expectArchival: true,
			expectReason:   "block_behind_threshold",
			expectMethod:   "eth_getBalance",
			expectBlockNum: 19999871,
		},
		{
			name:           "eth_getBalance without block param - defaults to latest",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15"]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "no_block_param_defaults_latest",
			expectMethod:   "eth_getBalance",
			expectBlockTag: BlockTagLatest,
		},

		// eth_getTransactionCount tests
		{
			name:           "eth_getTransactionCount with latest",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getTransactionCount","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","latest"]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "block_tag_current",
			expectMethod:   "eth_getTransactionCount",
		},
		{
			name:           "eth_getTransactionCount with earliest - ARCHIVAL",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getTransactionCount","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","earliest"]}`,
			perceivedBlock: 20000000,
			expectArchival: true,
			expectReason:   "block_tag_earliest",
			expectMethod:   "eth_getTransactionCount",
		},

		// eth_getCode tests
		{
			name:           "eth_getCode with latest",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getCode","params":["0xdAC17F958D2ee523a2206206994597C13D831ec7","latest"]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "block_tag_current",
			expectMethod:   "eth_getCode",
		},
		{
			name:           "eth_getCode with old block - ARCHIVAL",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getCode","params":["0xdAC17F958D2ee523a2206206994597C13D831ec7","0x100000"]}`,
			perceivedBlock: 20000000, // 0x100000 = 1048576
			expectArchival: true,
			expectReason:   "block_behind_threshold",
			expectMethod:   "eth_getCode",
			expectBlockNum: 1048576,
		},

		// eth_call tests
		{
			name:           "eth_call with latest",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_call","params":[{"to":"0xdAC17F958D2ee523a2206206994597C13D831ec7","data":"0x18160ddd"},"latest"]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "block_tag_current",
			expectMethod:   "eth_call",
		},
		{
			name:           "eth_call with earliest - ARCHIVAL",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_call","params":[{"to":"0xdAC17F958D2ee523a2206206994597C13D831ec7","data":"0x18160ddd"},"earliest"]}`,
			perceivedBlock: 20000000,
			expectArchival: true,
			expectReason:   "block_tag_earliest",
			expectMethod:   "eth_call",
		},
	}

	runArchivalTests(t, tests)
}

func TestArchivalHeuristic_StorageQueryMethods(t *testing.T) {
	tests := []archivalTestCase{
		// eth_getStorageAt - block param at index 2
		{
			name:           "eth_getStorageAt with latest",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getStorageAt","params":["0x295a70b2de5e3953354a6a8344e616ed314d7251","0x0","latest"]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "block_tag_current",
			expectMethod:   "eth_getStorageAt",
		},
		{
			name:           "eth_getStorageAt with earliest - ARCHIVAL",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getStorageAt","params":["0x295a70b2de5e3953354a6a8344e616ed314d7251","0x0","earliest"]}`,
			perceivedBlock: 20000000,
			expectArchival: true,
			expectReason:   "block_tag_earliest",
			expectMethod:   "eth_getStorageAt",
		},
		{
			name:           "eth_getStorageAt with old block - ARCHIVAL",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getStorageAt","params":["0x295a70b2de5e3953354a6a8344e616ed314d7251","0x0","0x1"]}`,
			perceivedBlock: 20000000,
			expectArchival: true,
			expectReason:   "block_behind_threshold",
			expectMethod:   "eth_getStorageAt",
			expectBlockNum: 1,
		},

		// eth_getProof - block param at index 2
		{
			name:           "eth_getProof with latest",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getProof","params":["0x7F0d15C7FAae65896648C8273B6d7E43f58Fa842",["0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"],"latest"]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "block_tag_current",
			expectMethod:   "eth_getProof",
		},
		{
			name:           "eth_getProof with earliest - ARCHIVAL",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getProof","params":["0x7F0d15C7FAae65896648C8273B6d7E43f58Fa842",["0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"],"earliest"]}`,
			perceivedBlock: 20000000,
			expectArchival: true,
			expectReason:   "block_tag_earliest",
			expectMethod:   "eth_getProof",
		},
	}

	runArchivalTests(t, tests)
}

func TestArchivalHeuristic_BlockQueryMethods(t *testing.T) {
	tests := []archivalTestCase{
		// eth_getBlockByNumber - block param at index 0
		{
			name:           "eth_getBlockByNumber with latest",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["latest",false]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "block_tag_current",
			expectMethod:   "eth_getBlockByNumber",
		},
		{
			name:           "eth_getBlockByNumber with earliest - ARCHIVAL",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["earliest",true]}`,
			perceivedBlock: 20000000,
			expectArchival: true,
			expectReason:   "block_tag_earliest",
			expectMethod:   "eth_getBlockByNumber",
		},
		{
			name:           "eth_getBlockByNumber with recent block",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["0x1312d00",false]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "block_within_threshold",
			expectMethod:   "eth_getBlockByNumber",
		},
		{
			name:           "eth_getBlockByNumber genesis block - ARCHIVAL",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["0x0",true]}`,
			perceivedBlock: 20000000,
			expectArchival: true,
			expectReason:   "block_behind_threshold",
			expectMethod:   "eth_getBlockByNumber",
			expectBlockNum: 0,
		},
		{
			name:           "eth_getBlockByNumber block 1 - ARCHIVAL",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["0x1",true]}`,
			perceivedBlock: 20000000,
			expectArchival: true,
			expectReason:   "block_behind_threshold",
			expectMethod:   "eth_getBlockByNumber",
			expectBlockNum: 1,
		},

		// eth_getBlockTransactionCountByNumber
		{
			name:           "eth_getBlockTransactionCountByNumber with latest",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBlockTransactionCountByNumber","params":["latest"]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "block_tag_current",
			expectMethod:   "eth_getBlockTransactionCountByNumber",
		},

		// eth_getUncleCountByBlockNumber
		{
			name:           "eth_getUncleCountByBlockNumber with old block - ARCHIVAL",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getUncleCountByBlockNumber","params":["0x100"]}`,
			perceivedBlock: 20000000,
			expectArchival: true,
			expectReason:   "block_behind_threshold",
			expectMethod:   "eth_getUncleCountByBlockNumber",
		},
	}

	runArchivalTests(t, tests)
}

func TestArchivalHeuristic_LogMethods(t *testing.T) {
	tests := []archivalTestCase{
		// eth_getLogs - uses object with fromBlock
		{
			name:           "eth_getLogs with latest fromBlock",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getLogs","params":[{"fromBlock":"latest","toBlock":"latest","address":"0xdAC17F958D2ee523a2206206994597C13D831ec7"}]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "block_tag_current",
			expectMethod:   "eth_getLogs",
		},
		{
			name:           "eth_getLogs with earliest fromBlock - ARCHIVAL",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getLogs","params":[{"fromBlock":"earliest","toBlock":"latest","address":"0xdAC17F958D2ee523a2206206994597C13D831ec7"}]}`,
			perceivedBlock: 20000000,
			expectArchival: true,
			expectReason:   "block_tag_earliest",
			expectMethod:   "eth_getLogs",
		},
		{
			name:           "eth_getLogs with old fromBlock - ARCHIVAL",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getLogs","params":[{"fromBlock":"0x100000","toBlock":"latest","address":"0xdAC17F958D2ee523a2206206994597C13D831ec7"}]}`,
			perceivedBlock: 20000000,
			expectArchival: true,
			expectReason:   "block_behind_threshold",
			expectMethod:   "eth_getLogs",
		},
		{
			name:           "eth_getLogs with recent fromBlock",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getLogs","params":[{"fromBlock":"0x1312d00","toBlock":"latest","address":"0xdAC17F958D2ee523a2206206994597C13D831ec7"}]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "block_within_threshold",
			expectMethod:   "eth_getLogs",
		},
		{
			name:           "eth_getLogs without fromBlock - defaults to latest",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getLogs","params":[{"toBlock":"latest","address":"0xdAC17F958D2ee523a2206206994597C13D831ec7"}]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "no_block_param_defaults_latest",
			expectMethod:   "eth_getLogs",
		},

		// eth_newFilter - uses object with fromBlock
		{
			name:           "eth_newFilter with earliest - ARCHIVAL",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_newFilter","params":[{"fromBlock":"earliest","toBlock":"latest"}]}`,
			perceivedBlock: 20000000,
			expectArchival: true,
			expectReason:   "block_tag_earliest",
			expectMethod:   "eth_newFilter",
		},
	}

	runArchivalTests(t, tests)
}

func TestArchivalHeuristic_NonBlockMethods(t *testing.T) {
	tests := []archivalTestCase{
		// Methods without block parameters
		{
			name:           "eth_blockNumber - no block param",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "method_no_block_param",
			expectMethod:   "eth_blockNumber",
		},
		{
			name:           "eth_chainId - no block param",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "method_no_block_param",
			expectMethod:   "eth_chainId",
		},
		{
			name:           "eth_gasPrice - no block param",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_gasPrice","params":[]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "method_no_block_param",
			expectMethod:   "eth_gasPrice",
		},
		{
			name:           "eth_sendRawTransaction - no block param",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_sendRawTransaction","params":["0xf86c..."]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "method_no_block_param",
			expectMethod:   "eth_sendRawTransaction",
		},
		{
			name:           "eth_getTransactionReceipt - no block param",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getTransactionReceipt","params":["0x85d995eba9763907fdf35cd2034144dd9d53ce32cbec21349d4b12823c6860c5"]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "method_no_block_param",
			expectMethod:   "eth_getTransactionReceipt",
		},
		{
			name:           "eth_getTransactionByHash - no block param",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getTransactionByHash","params":["0x88df016429689c079f3b2f6ad39fa052532c56795b733da78a91ebe6a713944b"]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "method_no_block_param",
			expectMethod:   "eth_getTransactionByHash",
		},
		{
			name:           "eth_getBlockByHash - no block param (uses hash not number)",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByHash","params":["0xdc0818cf78f21a8e70579cb46a43643f78291264dda342ae31049421c82d21ae",false]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "method_no_block_param",
			expectMethod:   "eth_getBlockByHash",
		},
		{
			name:           "net_version - no block param",
			request:        `{"jsonrpc":"2.0","id":1,"method":"net_version","params":[]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "method_no_block_param",
			expectMethod:   "net_version",
		},
		{
			name:           "web3_clientVersion - no block param",
			request:        `{"jsonrpc":"2.0","id":1,"method":"web3_clientVersion","params":[]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "method_no_block_param",
			expectMethod:   "web3_clientVersion",
		},
	}

	runArchivalTests(t, tests)
}

func TestArchivalHeuristic_EdgeCases(t *testing.T) {
	tests := []archivalTestCase{
		// Edge cases
		{
			name:           "empty request",
			request:        ``,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "request_too_short",
		},
		{
			name:           "too short request",
			request:        `{"id":1}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "request_too_short", // 8 bytes < 10 byte threshold
		},
		{
			name:           "invalid JSON - gjson tolerates partial JSON",
			request:        `{"method":"eth_getBalance",`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "no_block_param_defaults_latest", // gjson extracts method from partial JSON
			expectMethod:   "eth_getBalance",
		},
		{
			name:           "no perceived block yet",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","0x100000"]}`,
			perceivedBlock: 0,
			expectArchival: false,
			expectReason:   "no_perceived_block",
			expectMethod:   "eth_getBalance",
		},
		{
			name:           "uppercase block tag",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","LATEST"]}`,
			perceivedBlock: 20000000,
			expectArchival: false,
			expectReason:   "block_tag_current",
			expectMethod:   "eth_getBalance",
		},
		{
			name:           "mixed case block tag",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","Earliest"]}`,
			perceivedBlock: 20000000,
			expectArchival: true,
			expectReason:   "block_tag_earliest",
			expectMethod:   "eth_getBalance",
		},
		{
			name:           "block number without 0x prefix",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","100000"]}`,
			perceivedBlock: 20000000,
			expectArchival: true,
			expectReason:   "block_behind_threshold",
			expectMethod:   "eth_getBalance",
		},
		{
			name:           "block param looks like hash (not parseable as number)",
			request:        `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","0xdc0818cf78f21a8e70579cb46a43643f78291264dda342ae31049421c82d21ae"]}`,
			perceivedBlock: 20000000,
			expectArchival: false, // Can't determine, assume not archival
			expectReason:   "block_param_parse_error",
			expectMethod:   "eth_getBalance",
		},
	}

	runArchivalTests(t, tests)
}

func TestArchivalHeuristic_BatchRequests(t *testing.T) {
	// Note: Current implementation only checks the first request in a batch
	// This tests that it at least works with batch format
	heuristic := NewArchivalHeuristic(DefaultArchivalThreshold)

	// Batch request - first item has earliest
	batchRequest := `[{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","earliest"]}]`
	result := heuristic.IsArchivalRequest([]byte(batchRequest), 20000000)

	// Current implementation treats batch as not having method field at top level
	assert.False(t, result.RequiresArchival, "Batch requests not currently supported")
}

func TestArchivalHeuristic_CustomThreshold(t *testing.T) {
	// Test with custom threshold of 1000 blocks
	heuristic := NewArchivalHeuristic(1000)

	// Block 500 behind with threshold 1000 - should NOT be archival
	request := `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","0x1312b0c"]}`
	// 0x1312b0c = 19999500, perceived = 20000000, diff = 500 < 1000
	result := heuristic.IsArchivalRequest([]byte(request), 20000000)
	assert.False(t, result.RequiresArchival)
	assert.Equal(t, "block_within_threshold", result.Reason)

	// Block 1001 behind with threshold 1000 - should BE archival
	request = `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","0x13128f7"]}`
	// 0x13128f7 = 19998999, perceived = 20000000, diff = 1001 > 1000
	result = heuristic.IsArchivalRequest([]byte(request), 20000000)
	assert.True(t, result.RequiresArchival)
	assert.Equal(t, "block_behind_threshold", result.Reason)
}

func TestIsArchivalRequestQuick(t *testing.T) {
	tests := []struct {
		name     string
		request  string
		expected bool
	}{
		{
			name:     "contains earliest",
			request:  `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","earliest"]}`,
			expected: true,
		},
		{
			name:     "contains 0x0 block",
			request:  `{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["0x0",true]}`,
			expected: true,
		},
		{
			name:     "contains 0x1 block",
			request:  `{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["0x1",true]}`,
			expected: true,
		},
		{
			name:     "normal latest request",
			request:  `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","latest"]}`,
			expected: false,
		},
		{
			name:     "recent block",
			request:  `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","0x1312d00"]}`,
			expected: false,
		},
		{
			name:     "eth_blockNumber",
			request:  `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsArchivalRequestQuick([]byte(tt.request))
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetArchivalMethods(t *testing.T) {
	methods := GetArchivalMethods()
	require.NotEmpty(t, methods)

	// Verify some expected methods are present
	methodSet := make(map[string]bool)
	for _, m := range methods {
		methodSet[m] = true
	}

	assert.True(t, methodSet["eth_getBalance"])
	assert.True(t, methodSet["eth_getCode"])
	assert.True(t, methodSet["eth_call"])
	assert.True(t, methodSet["eth_getLogs"])
	assert.True(t, methodSet["eth_getBlockByNumber"])

	// These should NOT be in the list
	assert.False(t, methodSet["eth_blockNumber"])
	assert.False(t, methodSet["eth_chainId"])
}

// Helper function to run archival tests
func runArchivalTests(t *testing.T, tests []archivalTestCase) {
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			threshold := tt.threshold
			if threshold == 0 {
				threshold = DefaultArchivalThreshold
			}
			heuristic := NewArchivalHeuristic(threshold)

			result := heuristic.IsArchivalRequest([]byte(tt.request), tt.perceivedBlock)

			assert.Equal(t, tt.expectArchival, result.RequiresArchival, "RequiresArchival mismatch")
			assert.Equal(t, tt.expectReason, result.Reason, "Reason mismatch")

			if tt.expectMethod != "" {
				assert.Equal(t, tt.expectMethod, result.Method, "Method mismatch")
			}
			if tt.expectBlockTag != "" {
				assert.Equal(t, tt.expectBlockTag, result.BlockTag, "BlockTag mismatch")
			}
			if tt.expectBlockNum > 0 {
				assert.Equal(t, tt.expectBlockNum, result.RequestedBlock, "RequestedBlock mismatch")
			}
		})
	}
}

// ============================================================================
// BENCHMARKS - Performance Testing
// ============================================================================

// Sample requests for benchmarking
var (
	requestLatest   = []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","latest"]}`)
	requestEarliest = []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","earliest"]}`)
	requestHexBlock = []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","0xe71e1d"]}`)
	requestNoBlock  = []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`)
	requestCall     = []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_call","params":[{"to":"0xdAC17F958D2ee523a2206206994597C13D831ec7","data":"0x18160ddd"},"latest"]}`)
	requestLogs     = []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_getLogs","params":[{"fromBlock":"0x100000","toBlock":"latest","address":"0xdAC17F958D2ee523a2206206994597C13D831ec7"}]}`)

	// Larger realistic requests
	requestLargeCall = []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_call","params":[{"to":"0xdAC17F958D2ee523a2206206994597C13D831ec7","from":"0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","gas":"0x100000","gasPrice":"0x4a817c800","value":"0x0","data":"0x70a08231000000000000000000000000742d35cc6634c0532925a3b844bc9e7595f0ab15"},"latest"]}`)
	requestLargeLogs = []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_getLogs","params":[{"fromBlock":"0x100000","toBlock":"latest","address":"0xdAC17F958D2ee523a2206206994597C13D831ec7","topics":["0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef","0x000000000000000000000000742d35cc6634c0532925a3b844bc9e7595f0ab15"]}]}`)
)

// BenchmarkArchivalHeuristic_IsArchivalRequest benchmarks the main detection function
func BenchmarkArchivalHeuristic_IsArchivalRequest(b *testing.B) {
	heuristic := NewArchivalHeuristic(DefaultArchivalThreshold)
	perceivedBlock := uint64(20000000)

	benchmarks := []struct {
		name    string
		request []byte
	}{
		{"latest_tag", requestLatest},
		{"earliest_tag", requestEarliest},
		{"hex_block", requestHexBlock},
		{"no_block_method", requestNoBlock},
		{"eth_call", requestCall},
		{"eth_getLogs", requestLogs},
		{"large_eth_call", requestLargeCall},
		{"large_eth_getLogs", requestLargeLogs},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(bm.request)))
			for i := 0; i < b.N; i++ {
				_ = heuristic.IsArchivalRequest(bm.request, perceivedBlock)
			}
		})
	}
}

// BenchmarkIsArchivalRequestQuick benchmarks the fast variant
func BenchmarkIsArchivalRequestQuick(b *testing.B) {
	benchmarks := []struct {
		name    string
		request []byte
	}{
		{"latest_tag", requestLatest},
		{"earliest_tag", requestEarliest},
		{"hex_block", requestHexBlock},
		{"no_block_method", requestNoBlock},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(bm.request)))
			for i := 0; i < b.N; i++ {
				_ = IsArchivalRequestQuick(bm.request)
			}
		})
	}
}

// BenchmarkComparison compares gjson approach vs full JSON unmarshal
func BenchmarkComparison(b *testing.B) {
	heuristic := NewArchivalHeuristic(DefaultArchivalThreshold)
	perceivedBlock := uint64(20000000)

	// gjson-based approach (our implementation)
	b.Run("gjson_approach", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(requestHexBlock)))
		for i := 0; i < b.N; i++ {
			_ = heuristic.IsArchivalRequest(requestHexBlock, perceivedBlock)
		}
	})

	// Full JSON unmarshal with encoding/json (stdlib)
	b.Run("stdlib_encoding_json", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(requestHexBlock)))
		for i := 0; i < b.N; i++ {
			_ = isArchivalRequestFullUnmarshal(requestHexBlock, perceivedBlock, DefaultArchivalThreshold)
		}
	})

	// Full JSON unmarshal with goccy/go-json (faster drop-in)
	b.Run("goccy_go_json", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(requestHexBlock)))
		for i := 0; i < b.N; i++ {
			_ = isArchivalRequestGoccyJSON(requestHexBlock, perceivedBlock, DefaultArchivalThreshold)
		}
	})

	// Quick bytes.Contains approach
	b.Run("quick_bytes_contains", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(requestHexBlock)))
		for i := 0; i < b.N; i++ {
			_ = IsArchivalRequestQuick(requestHexBlock)
		}
	})
}

// isArchivalRequestGoccyJSON uses goccy/go-json for comparison
func isArchivalRequestGoccyJSON(requestBody []byte, perceivedBlock, threshold uint64) bool {
	var req struct {
		Method string        `json:"method"`
		Params []interface{} `json:"params"`
	}

	if err := gojson.Unmarshal(requestBody, &req); err != nil {
		return false
	}

	blockInfo, ok := evmMethodBlockParams[req.Method]
	if !ok {
		return false
	}

	if blockInfo.paramIndex >= len(req.Params) {
		return false
	}

	var blockParam string
	if blockInfo.isObject {
		paramObj, ok := req.Params[blockInfo.paramIndex].(map[string]interface{})
		if !ok {
			return false
		}
		blockParam, _ = paramObj[blockInfo.objectKey].(string)
	} else {
		blockParam, _ = req.Params[blockInfo.paramIndex].(string)
	}

	if blockParam == "" || blockParam == "latest" || blockParam == "pending" ||
		blockParam == "safe" || blockParam == "finalized" {
		return false
	}
	if blockParam == "earliest" {
		return true
	}

	requestedBlock, err := parseBlockNumber(blockParam)
	if err != nil {
		return false
	}

	if perceivedBlock == 0 {
		return false
	}

	if requestedBlock < perceivedBlock {
		return perceivedBlock-requestedBlock > threshold
	}
	return false
}

// isArchivalRequestFullUnmarshal is the full unmarshal approach for benchmark comparison
func isArchivalRequestFullUnmarshal(requestBody []byte, perceivedBlock, threshold uint64) bool {
	var req struct {
		Method string        `json:"method"`
		Params []interface{} `json:"params"`
	}

	if err := json.Unmarshal(requestBody, &req); err != nil {
		return false
	}

	blockInfo, ok := evmMethodBlockParams[req.Method]
	if !ok {
		return false
	}

	if blockInfo.paramIndex >= len(req.Params) {
		return false
	}

	var blockParam string
	if blockInfo.isObject {
		paramObj, ok := req.Params[blockInfo.paramIndex].(map[string]interface{})
		if !ok {
			return false
		}
		blockParam, _ = paramObj[blockInfo.objectKey].(string)
	} else {
		blockParam, _ = req.Params[blockInfo.paramIndex].(string)
	}

	if blockParam == "" || blockParam == "latest" || blockParam == "pending" ||
		blockParam == "safe" || blockParam == "finalized" {
		return false
	}
	if blockParam == "earliest" {
		return true
	}

	requestedBlock, err := parseBlockNumber(blockParam)
	if err != nil {
		return false
	}

	if perceivedBlock == 0 {
		return false
	}

	if requestedBlock < perceivedBlock {
		return perceivedBlock-requestedBlock > threshold
	}
	return false
}

// BenchmarkMixedWorkload simulates realistic mixed traffic
func BenchmarkMixedWorkload(b *testing.B) {
	heuristic := NewArchivalHeuristic(DefaultArchivalThreshold)
	perceivedBlock := uint64(20000000)

	// Distribution: 70% latest, 15% recent blocks, 10% old blocks, 5% earliest
	requests := make([][]byte, 100)
	for i := 0; i < 70; i++ {
		requests[i] = requestLatest
	}
	for i := 70; i < 85; i++ {
		requests[i] = []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","0x%x"]}`, 20000000-uint64(i)))
	}
	for i := 85; i < 95; i++ {
		requests[i] = requestHexBlock // old block
	}
	for i := 95; i < 100; i++ {
		requests[i] = requestEarliest
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = heuristic.IsArchivalRequest(requests[i%100], perceivedBlock)
	}
}

// BenchmarkParallelWorkload tests concurrent access
func BenchmarkParallelWorkload(b *testing.B) {
	heuristic := NewArchivalHeuristic(DefaultArchivalThreshold)
	perceivedBlock := uint64(20000000)

	b.RunParallel(func(pb *testing.PB) {
		i := 0
		requests := [][]byte{requestLatest, requestEarliest, requestHexBlock, requestNoBlock}
		for pb.Next() {
			_ = heuristic.IsArchivalRequest(requests[i%4], perceivedBlock)
			i++
		}
	})
}

// BenchmarkMemoryPressure tests behavior under memory pressure with many allocations
func BenchmarkMemoryPressure(b *testing.B) {
	heuristic := NewArchivalHeuristic(DefaultArchivalThreshold)
	perceivedBlock := uint64(20000000)

	// Generate unique requests to prevent caching effects
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Create a unique request each iteration
		blockNum := 19000000 + uint64(i%1000000)
		request := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"eth_getBalance","params":["0x742d35Cc6634C0532925a3b844Bc9e7595f0Ab15","0x%x"]}`, i, blockNum))
		_ = heuristic.IsArchivalRequest(request, perceivedBlock)
	}
}

// BenchmarkVaryingRequestSizes tests performance across different request sizes
func BenchmarkVaryingRequestSizes(b *testing.B) {
	heuristic := NewArchivalHeuristic(DefaultArchivalThreshold)
	perceivedBlock := uint64(20000000)

	sizes := []struct {
		name    string
		request []byte
	}{
		{"small_100B", requestLatest},
		{"medium_200B", requestLargeCall},
		{"large_300B", requestLargeLogs},
	}

	for _, s := range sizes {
		b.Run(s.name+"_size_"+strconv.Itoa(len(s.request)), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(s.request)))
			for i := 0; i < b.N; i++ {
				_ = heuristic.IsArchivalRequest(s.request, perceivedBlock)
			}
		})
	}
}

// BenchmarkAllApproachesComparison comprehensive comparison of all JSON parsing approaches
func BenchmarkAllApproachesComparison(b *testing.B) {
	heuristic := NewArchivalHeuristic(DefaultArchivalThreshold)
	perceivedBlock := uint64(20000000)

	requests := []struct {
		name    string
		request []byte
	}{
		{"small_eth_blockNumber", requestNoBlock},
		{"medium_eth_getBalance", requestHexBlock},
		{"large_eth_call", requestLargeCall},
		{"complex_eth_getLogs", requestLargeLogs},
	}

	for _, req := range requests {
		// gjson approach
		b.Run(req.name+"/gjson", func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(req.request)))
			for i := 0; i < b.N; i++ {
				_ = heuristic.IsArchivalRequest(req.request, perceivedBlock)
			}
		})

		// goccy/go-json approach
		b.Run(req.name+"/goccy", func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(req.request)))
			for i := 0; i < b.N; i++ {
				_ = isArchivalRequestGoccyJSON(req.request, perceivedBlock, DefaultArchivalThreshold)
			}
		})

		// stdlib encoding/json approach
		b.Run(req.name+"/stdlib", func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(req.request)))
			for i := 0; i < b.N; i++ {
				_ = isArchivalRequestFullUnmarshal(req.request, perceivedBlock, DefaultArchivalThreshold)
			}
		})
	}
}
