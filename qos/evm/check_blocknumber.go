package evm

import (
	"fmt"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// EVM checks begin with 1 for JSON-RPC requests.
//
// This is an arbitrary ID selected by the engineering team at Grove.
// It is used for compatibility with the JSON-RPC spec.
// It is a loose convention in the QoS package.

// ID for the eth_blockNumber check.
const idBlockNumberCheck = 1002

// methodBlockNumber is the JSON-RPC method for getting the latest block number.
// Reference: https://ethereum.org/en/developers/docs/apis/json-rpc/#eth_blocknumber
const methodBlockNumber = jsonrpc.Method("eth_blockNumber")

var (
	errNoBlockNumberObs                   = fmt.Errorf("endpoint has not had an observation of its response to a %q request", methodBlockNumber)
	errInvalidBlockNumberObs              = fmt.Errorf("endpoint returned an invalid response to a %q request", methodBlockNumber)
	errOutsideSyncAllowanceBlockNumberObs = fmt.Errorf("endpoint's block height is outside the sync allowance in response to a %q request", methodBlockNumber)
)

// endpointCheckBlockNumber is a check that ensures the endpoint's block height within the sync allowance.
// It is used to ensure that the endpoint is not behind the chain.
//
// Note that this check does not have an expiry as it is expected to be run frequently.
// This serves two purposes:
//   - It ensures that the endpoint is not behind the chain.
//   - It ensures that the hydrator is always sending at least some request to enforce protocol-level sanctions.
type endpointCheckBlockNumber struct {
	// parsedBlockNumberResponse stores the result of processing the endpoint's response to an `eth_blockNumber` request.
	// It is nil if there has NOT been an observation of the endpoint's response to an `eth_blockNumber` request.
	parsedBlockNumberResponse *uint64
}

func (e *endpointCheckBlockNumber) getRequestID() jsonrpc.ID {
	return jsonrpc.IDFromInt(idBlockNumberCheck)
}

// getRequest returns a JSONRPC request to check the block number.
// eg. '{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber"}'
func (e *endpointCheckBlockNumber) getServicePayload() protocol.Payload {
	req := jsonrpc.Request{
		JSONRPC: jsonrpc.Version2,
		ID:      jsonrpc.IDFromInt(idBlockNumberCheck),
		Method:  jsonrpc.Method(methodBlockNumber),
	}
	// Hardcoded request will never fail to build the payload
	payload, _ := req.BuildPayload()
	return payload
}

// getBlockNumber returns the parsed block number value for the endpoint.
func (e *endpointCheckBlockNumber) getBlockNumber() (uint64, error) {
	if e.parsedBlockNumberResponse == nil {
		return 0, errNoBlockNumberObs
	}
	if *e.parsedBlockNumberResponse == 0 {
		return 0, errInvalidBlockNumberObs
	}
	return *e.parsedBlockNumberResponse, nil
}
