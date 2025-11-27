//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	vegeta "github.com/tsenart/vegeta/lib"

	"github.com/pokt-network/path/qos/evm"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// TODO_TECHDEBT(@commoddity): The service_<SERVICE_TYPE>_test.go files in this package
// are essentially encapsulating knowledge about specific services.
//
// As part of the JUDGE refactor of the QoS packages, we should move some/all of
// service-specific knowledge to the qos package and create method-specific handling.
// This will centralize the measures taken related to an endpoint quality in one place.
//
// Examples:
// - Request/Response validation for a specific JSONRPC method.
// - Format of params field for a specific JSONRPC method.
// - JSONRPC methods an endpoint should support.

/* -------------------- EVM JSON-RPC Method Definitions -------------------- */

var evmExpectedID = jsonrpc.IDFromInt(1)

// Reference for all EVM JSON-RPC methods:
// - https://ethereum.org/en/developers/docs/apis/json-rpc/
const (
	eth_blockNumber           = "eth_blockNumber"
	eth_chainId               = "eth_chainId"
	eth_gasPrice              = "eth_gasPrice"
	eth_getBalance            = "eth_getBalance"
	eth_getBlockByNumber      = "eth_getBlockByNumber"
	eth_getTransactionCount   = "eth_getTransactionCount"
	eth_getTransactionReceipt = "eth_getTransactionReceipt"
	eth_getTransactionByHash  = "eth_getTransactionByHash"
	eth_call                  = "eth_call"

	// Special identifier for batch requests
	// Will send a request containing:
	// 	- eth_blockNumber
	// 	- eth_chainId
	// 	- eth_gasPrice
	batchRequest = "BATCH_REQUEST: [eth_blockNumber, eth_chainId, eth_gasPrice]"
)

// getEVMTestMethods returns all EVM JSON-RPC methods for a service load test.
// If archival is true, all methods are returned.
// If archival is false, only non-archival methods are returned.
func getEVMTestMethods() []string {
	return []string{
		eth_blockNumber,
		eth_chainId,
		eth_gasPrice,
		eth_getBalance,
		eth_getBlockByNumber,
		eth_getTransactionCount,
		eth_getTransactionReceipt,
		eth_getTransactionByHash,
		eth_call,

		// Include batch request testing
		batchRequest,
	}
}

// getEVMTestMethodsForWebSocket returns all EVM JSON-RPC methods for Websocket testing.
// This includes all EVM JSON-RPC methods except batchRequest as EVM websocket connections
// go not support batch requests.
func getEVMTestMethodsForWebSocket() []string {
	return []string{
		eth_blockNumber,
		eth_chainId,
		eth_gasPrice,
		eth_getBalance,
		eth_getBlockByNumber,
		eth_getTransactionCount,
		// TODO_INVESTIGATE(@commoddity): eth_getTransactionReceipt consistently fails on websocket
		// tests for eth only. Disabled to allow our overall E2E websocket tests to work.
		// However, we should investigate why this is happening.
		// Example error:
		// {"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"getReceipt error: seekInFiles(invIndex=unknown index,txNum=2786993528) but data before txNum=2900000000 not available"}
		// eth_getTransactionReceipt,
		eth_getTransactionByHash,
		eth_call,
	}
}

// createEVMJsonRPCParams builds RPC params for each EVM method using the provided service parameters.
func createEVMJsonRPCParams(method jsonrpc.Method, sp ServiceParams) jsonrpc.Params {
	switch method {

	// Methods with empty params
	case eth_blockNumber, eth_chainId, eth_gasPrice:
		return jsonrpc.Params{}

	// Methods that just need the transaction hash
	//   Example: ["0xfeccd627b5b391d04fe45055873de3b2c0b4302d52e96bd41d5f0019a704165f"]
	case eth_getTransactionReceipt, eth_getTransactionByHash:
		params, _ := jsonrpc.BuildParamsFromString(sp.TransactionHash)
		return params

	// Methods that need [address, blockNumber]
	//   Example: ["0xdAC17F958D2ee523a2206206994597C13D831ec7", "latest"]
	case eth_getBalance, eth_getTransactionCount:
		params, _ := jsonrpc.BuildParamsFromStringArray([2]string{
			sp.ContractAddress,
			sp.blockNumber,
		})
		return params

	// eth_getBlockByNumber needs [blockNumber, <boolean>]
	//   Example: ["0xe71e1d", false]
	case eth_getBlockByNumber:
		params, _ := jsonrpc.BuildParamsFromStringAndBool(
			sp.blockNumber,
			false,
		)
		return params

	// eth_call needs [{ to: address, data: calldata }, blockNumber]
	//   Example: [{"to":"0xdAC17F958D2ee523a2206206994597C13D831ec7","data":"0x18160ddd"}, "latest"]
	case eth_call:
		params, _ := jsonrpc.BuildParamsFromObjectAndString(
			map[string]string{
				"to":   sp.ContractAddress,
				"data": sp.CallData,
			},
			sp.blockNumber,
		)
		return params

	default:
		return jsonrpc.Params{}
	}
}

// createEVMBatchRequest creates a batch request containing eth_blockNumber, eth_chainId, and eth_gasPrice.
// Returns the marshaled JSON bytes for the batch request.
func createEVMBatchRequest() ([]byte, error) {
	batchRequests := []jsonrpc.Request{
		{
			JSONRPC: jsonrpc.Version2,
			ID:      jsonrpc.IDFromInt(1),
			Method:  jsonrpc.Method(eth_blockNumber),
			Params:  jsonrpc.Params{},
		},
		{
			JSONRPC: jsonrpc.Version2,
			ID:      jsonrpc.IDFromInt(2),
			Method:  jsonrpc.Method(eth_chainId),
			Params:  jsonrpc.Params{},
		},
		{
			JSONRPC: jsonrpc.Version2,
			ID:      jsonrpc.IDFromInt(3),
			Method:  jsonrpc.Method(eth_gasPrice),
			Params:  jsonrpc.Params{},
		},
	}

	return json.Marshal(batchRequests)
}

func getEVMVegetaTargets(
	ts *TestService,
	gatewayURL string,
) (map[string]vegeta.Target, error) {
	headers := getRequestHeaders(ts.ServiceID)

	blockNumber, err := getEVMBlockNumber(ts, headers, gatewayURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get EVM block number for service '%s': %w", ts.ServiceID, err)
	}
	ts.ServiceParams.blockNumber = blockNumber

	targets := make(map[string]vegeta.Target)
	for _, method := range getEVMTestMethods() {
		var body []byte
		var err error

		// Handle batch request specially
		if method == batchRequest {
			body, err = createEVMBatchRequest()
			if err != nil {
				return nil, fmt.Errorf("failed to create batch request for service '%s': %w", ts.ServiceID, err)
			}
		} else {
			// Create individual JSON-RPC request with appropriate parameters
			jsonrpcReq := jsonrpc.Request{
				JSONRPC: jsonrpc.Version2,
				ID:      jsonrpc.IDFromInt(1),
				Method:  jsonrpc.Method(method),
				Params:  createEVMJsonRPCParams(jsonrpc.Method(method), ts.ServiceParams),
			}

			// Marshal the request body
			body, err = json.Marshal(jsonrpcReq)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal JSON-RPC request for method '%s' for service '%s': %w", method, ts.ServiceID, err)
			}
		}

		// Create vegeta target
		target := vegeta.Target{
			Method: http.MethodPost,
			URL:    gatewayURL,
			Body:   body,
			Header: headers,
		}

		targets[method] = target
	}

	return targets, nil
}

// -----------------------------------------------------------------------------
// Get Test Block Number helpers - Used for EVM archival services
// -----------------------------------------------------------------------------

// getEVMBlockNumber returns the block number to use for testing.
// - If non-archival, returns "latest"
// - If archival, returns a random block number between the current block and the contract start block
func getEVMBlockNumber(
	testService *TestService,
	headers http.Header,
	gatewayURL string,
) (string, error) {
	if !testService.Archival {
		return "latest", nil
	} else {
		// randomBlockNumber is a block between the:
		// - Start height: the start block height of the contract used for testing
		// - End height: the current block height
		randomBlockNumber, err := getTestBlockNumberForArchivalTest(
			gatewayURL,
			headers,
			testService.ServiceParams.ContractStartBlock,
		)
		if err != nil {
			return "", fmt.Errorf("Could not get random block number for archival test: %w", err)
		}
		return randomBlockNumber, nil
	}
}

// getTestBlockNumberForArchivalTest gets an archival block number for testing or fails the test.
// Selected by picking a random block number between the current block and the contract start block.
func getTestBlockNumberForArchivalTest(
	gatewayURL string,
	headers http.Header,
	contractStartBlock uint64,
) (string, error) {
	// Get current block height - fail test if this doesn't work
	currentBlockHeight, err := getCurrentConsensusBlockHeight(gatewayURL, headers)
	if err != nil {
		return "", fmt.Errorf("Could not get current block height: %w", err)
	}

	// Get random historical block number
	return calculateArchivalBlockNumber(currentBlockHeight, contractStartBlock), nil
}

// getCurrentConsensusBlockHeight gets current block height with consensus from multiple requests.
func getCurrentConsensusBlockHeight(gatewayURL string, headers http.Header) (uint64, error) {
	blockHeights := make(map[uint64]int)
	maxAttempts := 10
	requiredAgreement := 3
	client := &http.Client{Timeout: 2 * time.Second}

	// Make requests to get current block height rapidly in parallel
	results := make(chan uint64, maxAttempts)
	for range maxAttempts {
		go func() {
			if height, err := getCurrentBlockHeight(client, gatewayURL, headers); err == nil {
				results <- height
			}
		}()
	}

	// Collect results quickly
	timeout := time.After(5 * time.Second)
collect:
	for range maxAttempts {
		select {
		case height := <-results:
			blockHeights[height]++
			if blockHeights[height] >= requiredAgreement {
				return height, nil
			}
		case <-timeout:
			break collect
		}
	}

	// Return the most recent height seen if no consensus
	var maxHeight uint64
	for height := range blockHeights {
		if height > maxHeight {
			maxHeight = height
		}
	}
	return maxHeight, nil
}

// getCurrentBlockHeight makes a single request to get the current block number.
func getCurrentBlockHeight(client *http.Client, gatewayURL string, headers http.Header) (uint64, error) {
	// Build and send request
	req, err := buildBlockNumberRequest(gatewayURL, headers)
	if err != nil {
		return 0, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("Error getting current block height: %d", resp.StatusCode)
	}

	// Parse response
	var jsonrpc jsonrpc.Response
	if err := json.NewDecoder(resp.Body).Decode(&jsonrpc); err != nil {
		return 0, fmt.Errorf("Error getting current block height: %w", err)
	}

	// Unmarshal the result into a string
	var hexString string
	if err := jsonrpc.UnmarshalResult(&hexString); err != nil {
		return 0, fmt.Errorf("Error unmarshaling block number result: %w", err)
	}

	// Parse hex (remove "0x" prefix if present)
	hexStr := strings.TrimPrefix(hexString, "0x")
	blockNum, err := strconv.ParseUint(hexStr, 16, 64)
	if err != nil {
		return 0, err
	}

	return blockNum, nil
}

// buildBlockNumberRequest creates a JSON-RPC request for the current block number.
func buildBlockNumberRequest(gatewayURL string, headers http.Header) (*http.Request, error) {
	blockNumberReq := jsonrpc.Request{
		JSONRPC: jsonrpc.Version2,
		ID:      jsonrpc.IDFromInt(1),
		Method:  jsonrpc.Method(eth_blockNumber),
	}

	blockNumberReqBytes, err := json.Marshal(blockNumberReq)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, gatewayURL, bytes.NewReader(blockNumberReqBytes))
	if err != nil {
		return nil, err
	}

	req.Header = headers.Clone()

	return req, nil
}

// calculateArchivalBlockNumber picks a random historical block number for archival tests.
func calculateArchivalBlockNumber(currentBlock, contractStartBlock uint64) string {
	var blockNumHex string

	// Case 1: Block number is below or equal to the archival threshold
	if currentBlock <= evm.DefaultEVMArchivalThreshold {
		blockNumHex = blockNumberToHex(1)
	} else {
		// Case 2: Block number is above the archival threshold
		maxBlockNumber := currentBlock - evm.DefaultEVMArchivalThreshold

		// Ensure we don't go below the minimum archival block
		if maxBlockNumber < contractStartBlock {
			blockNumHex = blockNumberToHex(contractStartBlock)
		} else {
			// Generate a random block number within valid range
			r := rand.New(rand.NewSource(time.Now().UnixNano()))
			rangeSize := maxBlockNumber - contractStartBlock + 1
			blockNumHex = blockNumberToHex(contractStartBlock + (r.Uint64() % rangeSize))
		}
	}

	return blockNumHex
}

// blockNumberToHex converts a block number to a hex string.
func blockNumberToHex(blockNumber uint64) string {
	return fmt.Sprintf("0x%x", blockNumber)
}
