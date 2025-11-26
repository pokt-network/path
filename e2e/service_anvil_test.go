//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"

	vegeta "github.com/tsenart/vegeta/lib"

	"github.com/pokt-network/path/qos/jsonrpc"
)

var anvilExpectedID = jsonrpc.IDFromInt(1)

// Anvil JSON-RPC method definitions.
// These are standard Ethereum methods that require no parameters.
const (
	anvilBlockNumber = "eth_blockNumber"
	anvilChainId     = "eth_chainId"
	anvilNetVersion  = "net_version"
)

// getAnvilTestMethods returns Anvil JSON-RPC methods for load testing.
// All methods use empty parameters for simplicity.
func getAnvilTestMethods() []string {
	return []string{
		anvilBlockNumber,
		anvilChainId,
		anvilNetVersion,
	}
}

// getAnvilVegetaTargets creates HTTP targets for Anvil load testing.
// Performs basic health check by fetching current block number.
func getAnvilVegetaTargets(
	ts *TestService,
	gatewayURL string,
) (map[string]vegeta.Target, error) {
	headers := getRequestHeaders(ts.ServiceID)

	targets := make(map[string]vegeta.Target)
	for _, method := range getAnvilTestMethods() {
		// Create JSON-RPC request - Anvil test methods use no parameters
		jsonrpcReq := jsonrpc.Request{
			JSONRPC: jsonrpc.Version2,
			ID:      jsonrpc.IDFromInt(1),
			Method:  jsonrpc.Method(method),
			// Params intentionally omitted - all Anvil test methods use empty params
		}

		body, err := json.Marshal(jsonrpcReq)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal JSON-RPC request for method %s: %w", method, err)
		}

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
