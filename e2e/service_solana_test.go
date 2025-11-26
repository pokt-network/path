//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	vegeta "github.com/tsenart/vegeta/lib"

	"github.com/pokt-network/path/qos/jsonrpc"
)

/* -------------------- Solana JSON-RPC Method Definitions -------------------- */

var solanaExpectedID = jsonrpc.IDFromInt(1)

// Reference for all Solana JSON-RPC methods:
// - https://solana.com/docs/rpc/http
const (
	getEpochInfo            = "getEpochInfo"
	getHealth               = "getHealth"
	getBalance              = "getBalance"
	getGenesisHash          = "getGenesisHash"
	getSignaturesForAddress = "getSignaturesForAddress"
	getSlot                 = "getSlot"
	getTransaction          = "getTransaction"
	getBlock                = "getBlock"
)

// getSolanaTestMethods returns all Solana JSON-RPC methods for a service load test.
func getSolanaTestMethods() []string {
	return []string{
		getEpochInfo,
		getHealth,
		getBalance,
		getGenesisHash,
		getSignaturesForAddress,
		getSlot,
		getTransaction,
		getBlock,
	}
}

// createSolanaJsonRPCParams builds RPC params for each Solana method using the provided service parameters.
func createSolanaJsonRPCParams(method jsonrpc.Method, sp ServiceParams) jsonrpc.Params {
	switch method {
	// Methods with empty params
	//   Example: []
	case getEpochInfo, getHealth, getSlot, getGenesisHash:
		return jsonrpc.Params{}

	// Methods that just need the transaction signature
	//   Example: ["5UHzPFpc8Gy7LGtEPcuWWZaHRRLzpZQQiVPZyK7H1yvez5F2FBfmHmRo3WzaWuBzKZgPm4ULxh8H6Ha1YJwvLaza"]
	case getTransaction:
		params, _ := jsonrpc.BuildParamsFromString(sp.TransactionHash)
		return params

	// Methods that need account address and commitment
	//   Example: ["83astBRguLMdt2h5U1Tpdq5tjFoJ6noeGwaY3mDLVcri", {"commitment": "finalized"}]
	case getBalance:
		params, _ := jsonrpc.BuildParamsFromStringAndObject(
			sp.ContractAddress,
			map[string]any{
				"commitment": "finalized",
			},
		)
		return params

		// getSignaturesForAddress needs [address, options]
		//   Example: ["Vote111111111111111111111111111111111111111", {"limit": 10}]
	case getSignaturesForAddress:
		params, _ := jsonrpc.BuildParamsFromStringAndObject(
			sp.ContractAddress,
			map[string]any{
				"limit": 10,
			},
		)
		return params

	// getBlock needs [slot, options]
	//   Example: [430, {"encoding": "json", "transactionDetails": "full", "maxSupportedTransactionVersion": 0}]
	case getBlock:
		slotNumber, err := strconv.ParseUint(sp.blockNumber, 10, 64)
		if err != nil {
			// Use slot 0 if parsing fails
			slotNumber = 0
		}
		params, _ := jsonrpc.BuildParamsFromUint64AndObject(
			slotNumber,
			map[string]any{
				"encoding":                       "json",
				"transactionDetails":             "none",
				"maxSupportedTransactionVersion": 0,
				"rewards":                        false,
			},
		)
		return params

	default:
		return jsonrpc.Params{}
	}
}

func getSolanaVegetaTargets(
	ts *TestService,
	gatewayURL string,
) (map[string]vegeta.Target, error) {
	headers := getRequestHeaders(ts.ServiceID)

	// Get the appropriate block number for the test
	blockNumber, err := getSolanaBlockNumber(ts, headers, gatewayURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get Solana block number: %w", err)
	}
	ts.ServiceParams.blockNumber = blockNumber

	targets := make(map[string]vegeta.Target)
	for _, method := range getSolanaTestMethods() {
		// Create JSON-RPC request with appropriate parameters
		jsonrpcReq := jsonrpc.Request{
			JSONRPC: jsonrpc.Version2,
			ID:      jsonrpc.IDFromInt(1),
			Method:  jsonrpc.Method(method),
			Params:  createSolanaJsonRPCParams(jsonrpc.Method(method), ts.ServiceParams),
		}

		// Marshal the request body
		body, err := json.Marshal(jsonrpcReq)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal JSON-RPC request for method %s: %w", method, err)
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
// Get Test Block Number helpers - Used for Solana services
//
// TODO_IMPROVE(@commoddity): Add archival support once Solana archival QoS checks are implemented.
// -----------------------------------------------------------------------------

// getEpochInfoResponse represents the response from getEpochInfo
type getEpochInfoResponse struct {
	AbsoluteSlot     uint64 `json:"absoluteSlot"`
	BlockHeight      uint64 `json:"blockHeight"`
	Epoch            uint64 `json:"epoch"`
	SlotIndex        uint64 `json:"slotIndex"`
	SlotsInEpoch     uint64 `json:"slotsInEpoch"`
	TransactionCount uint64 `json:"transactionCount"`
}

func getSolanaBlockNumber(_ *TestService, headers http.Header, gatewayURL string) (string, error) {
	// Get the current slot number
	slotNumber, err := getCurrentConsensusSlotNumber(gatewayURL, headers)
	if err != nil {
		return "", fmt.Errorf("Could not get current slot number: %w", err)
	}
	return strconv.FormatUint(slotNumber, 10), nil
}

// getCurrentConsensusSlotNumber gets current slot number with consensus from multiple requests.
func getCurrentConsensusSlotNumber(gatewayURL string, headers http.Header) (uint64, error) {
	slotNumbers := make(map[uint64]int)
	maxAttempts := 10
	requiredAgreement := 3
	client := &http.Client{Timeout: 2 * time.Second}

	// Make requests to get current slot number rapidly in parallel
	results := make(chan uint64, maxAttempts)
	for range maxAttempts {
		go func() {
			if height, err := getCurrentSlotNumber(client, gatewayURL, headers); err == nil {
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
			slotNumbers[height]++
			if slotNumbers[height] >= requiredAgreement {
				return height, nil
			}
		case <-timeout:
			break collect
		}
	}

	// Return the most recent height seen if no consensus
	var maxSlotNumber uint64
	for slotNumber := range slotNumbers {
		if slotNumber > maxSlotNumber {
			maxSlotNumber = slotNumber
		}
	}
	return maxSlotNumber, nil
}

// getCurrentSlotNumber makes a single request to get the current slot number using getEpochInfo.
func getCurrentSlotNumber(client *http.Client, gatewayURL string, headers http.Header) (uint64, error) {
	// Build and send request
	req, err := buildEpochInfoRequest(gatewayURL, headers)
	if err != nil {
		return 0, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("bad status code: %d", resp.StatusCode)
	}

	// Parse JSONRPC response
	var jsonrpc jsonrpc.Response
	if err := json.NewDecoder(resp.Body).Decode(&jsonrpc); err != nil {
		return 0, err
	}

	// Unmarshal the result into getEpochInfoResponse
	var epochInfo getEpochInfoResponse
	if err := jsonrpc.UnmarshalResult(&epochInfo); err != nil {
		return 0, fmt.Errorf("failed to unmarshal epoch info: %w", err)
	}

	return epochInfo.AbsoluteSlot, nil
}

// buildEpochInfoRequest creates a JSON-RPC request for getEpochInfo.
func buildEpochInfoRequest(gatewayURL string, headers http.Header) (*http.Request, error) {
	epochInfoReq := jsonrpc.Request{
		JSONRPC: jsonrpc.Version2,
		ID:      jsonrpc.IDFromInt(1),
		Method:  getEpochInfo,
	}

	epochInfoReqBytes, err := json.Marshal(epochInfoReq)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, gatewayURL, bytes.NewReader(epochInfoReqBytes))
	if err != nil {
		return nil, err
	}

	req.Header = headers.Clone()

	return req, nil
}
