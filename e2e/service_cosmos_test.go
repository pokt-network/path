//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	vegeta "github.com/tsenart/vegeta/lib"

	"github.com/pokt-network/path/qos/jsonrpc"
)

func getCosmosSDKVegetaTargets(
	ts *TestService,
	gatewayURL string,
) (map[string]vegeta.Target, error) {
	targets := make(map[string]vegeta.Target)

	for _, supportedAPI := range ts.SupportedAPIs {
		switch getRPCTypeFromString(supportedAPI) {

		case sharedtypes.RPCType_COMET_BFT:
			cometBFTMethods := getCometBFTTestMethods()

			cometBFTTargets, err := getCometBFTVegetaTargets(ts, cometBFTMethods, gatewayURL)
			if err != nil {
				return nil, err
			}
			maps.Copy(targets, cometBFTTargets)

		case sharedtypes.RPCType_REST:
			cosmosSDKMethods := getCosmosSDKRESTTestURLPaths()

			cosmosSDKTargets, err := getCosmosSDKRESTVegetaTargets(ts, cosmosSDKMethods, gatewayURL)
			if err != nil {
				return nil, err
			}
			maps.Copy(targets, cosmosSDKTargets)

		case sharedtypes.RPCType_JSON_RPC:
			// For EVM JSON-RPC, we use the same targets as the EVM service.
			// NOTE: Websocket testing for services like XRPLEVM will only test these EVM JSON-RPC methods,
			// not the CometBFT or REST methods, since WebSockets only support EVM JSON-RPC protocols.
			evmJSONRPCTargets, err := getEVMVegetaTargets(ts, gatewayURL)
			if err != nil {
				return nil, err
			}
			maps.Copy(targets, evmJSONRPCTargets)
		}
	}

	return targets, nil
}

/* -------------------- CometBFT JSON-RPC Method Definitions -------------------- */

var cosmosSDKExpectedID = jsonrpc.IDFromInt(1)

// Reference for all CometBFT RPC methods:
// - https://docs.cometbft.com/v0.38/rpc/
const (
	cometbftMethodStatus         = "status"          // Get node status
	cometbftMethodHealth         = "health"          // Get node health
	cometbftMethodNetInfo        = "net_info"        // Get network info
	cometbftMethodConsensusState = "consensus_state" // Get consensus state
	cometbftMethodCommit         = "commit"          // Get commit
	cometbftMethodABCIInfo       = "abci_info"       // Get ABCI info
	cometbftMethodBlock          = "block"           // Get block at height
	cometbftMethodBlockResults   = "block_results"   // Get block results
	cometbftMethodValidators     = "validators"      // Get validators
)

// getCometBFTTestMethods returns all CometBFT JSON-RPC methods for a service load test.
func getCometBFTTestMethods() []string {
	return []string{
		cometbftMethodStatus,
		cometbftMethodHealth,
		cometbftMethodNetInfo,
		cometbftMethodConsensusState,
		cometbftMethodCommit,
		cometbftMethodABCIInfo,
		cometbftMethodBlock,
		cometbftMethodBlockResults,
		cometbftMethodValidators,
	}
}

func getCometBFTVegetaTargets(
	ts *TestService,
	methods []string,
	gatewayURL string,
) (map[string]vegeta.Target, error) {
	headers := getRequestHeaders(ts.ServiceID)

	targets := make(map[string]vegeta.Target)
	for _, method := range methods {
		// Create JSON-RPC request with no parameters
		jsonrpcReq := jsonrpc.Request{
			JSONRPC: jsonrpc.Version2,
			ID:      jsonrpc.IDFromInt(1),
			Method:  jsonrpc.Method(method),
			// Some CometBFT methods require params to be an empty object to default to "latest" block.
			Params: jsonrpc.BuildParamsFromEmptyObject(),
		}

		// Marshal the request body
		body, err := json.Marshal(jsonrpcReq)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal JSON-RPC request for method '%s' for service '%s': %w", method, ts.ServiceID, err)
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

/* -------------------- Cosmos SDK REST Request Definitions -------------------- */

// Reference for all Cosmos SDK REST endpoints:
// - https://docs.cosmos.network/api
const (
	cosmosSDKEndpointStatus        = "/cosmos/base/node/v1beta1/status"    // Get node status
	cosmosSDKEndpointAuthParams    = "/cosmos/auth/v1beta1/params"         // Get auth module parameters
	cosmosSDKEndpointBankParams    = "/cosmos/bank/v1beta1/params"         // Get bank module parameters
	cosmosSDKEndpointDistribParams = "/cosmos/distribution/v1beta1/params" // Get distribution parameters
)

// getCosmosSDKRESTTestURLPaths returns all Cosmos SDK REST URL paths for a service load test.
func getCosmosSDKRESTTestURLPaths() []string {
	return []string{
		cosmosSDKEndpointStatus,
		cosmosSDKEndpointAuthParams,
		cosmosSDKEndpointBankParams,
		cosmosSDKEndpointDistribParams,
	}
}

func getCosmosSDKRESTVegetaTargets(
	ts *TestService,
	urlPaths []string,
	gatewayURL string,
) (map[string]vegeta.Target, error) {
	headers := getRequestHeaders(ts.ServiceID)

	targets := make(map[string]vegeta.Target)
	for _, urlPath := range urlPaths {
		// Create URL with URL path
		url := gatewayURL + string(urlPath)

		// Create vegeta target
		target := vegeta.Target{
			Method: http.MethodGet,
			URL:    url,
			Header: headers,
		}

		targets[urlPath] = target
	}

	return targets, nil
}
