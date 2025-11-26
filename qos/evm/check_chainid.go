package evm

import (
	"fmt"
	"time"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// EVM checks begin with 1 for JSON-RPC requests.
//
// This is an arbitrary ID selected by the engineering team at Grove.
// It is used for compatibility with the JSON-RPC spec.
// It is a loose convention in the QoS package.

// ID for the eth_chainId check.
const idChainIDCheck = 1001

// methodChainID is the JSON-RPC method for getting the chain ID.
// Reference: https://ethereum.org/en/developers/docs/apis/json-rpc/#eth_chainid
const methodChainID = jsonrpc.Method("eth_chainId")

// TODO_IMPROVE(@commoddity): determine an appropriate interval for checking the chain ID.
const checkChainIDInterval = 20 * time.Minute

var (
	errNoChainIDObs      = fmt.Errorf("endpoint has not had an observation of its response to a %q request", methodChainID)
	errInvalidChainIDObs = fmt.Errorf("endpoint returned an invalid response to a %q request", methodChainID)
)

// endpointCheckChainID is a check that ensures the endpoint's chain ID is the same as the expected chain ID.
// It is used to ensure that the endpoint is on the correct chain.
type endpointCheckChainID struct {
	// chainIDResponse stores the result of processing the endpoint's response to an `eth_chainId` request.
	// It is nil if there has NOT been an observation of the endpoint's response to an `eth_chainId` request.
	chainID   *string
	expiresAt time.Time
}

func (e *endpointCheckChainID) getRequestID() jsonrpc.ID {
	return jsonrpc.IDFromInt(idChainIDCheck)
}

// getRequest returns a JSONRPC request to check the chain ID.
// eg. '{"jsonrpc":"2.0","id":1,"method":"eth_chainId"}'
func (e *endpointCheckChainID) getServicePayload() protocol.Payload {
	req := jsonrpc.Request{
		JSONRPC: jsonrpc.Version2,
		ID:      jsonrpc.IDFromInt(idChainIDCheck),
		Method:  jsonrpc.Method(methodChainID),
	}
	// Hardcoded request will never fail to build the payload
	payload, _ := req.BuildPayload()
	return payload
}
