package evm

import (
	"time"

	qosobservations "github.com/pokt-network/path/observation/qos"
)

// endpoint captures the details required to validate an EVM endpoint.
//
// It contains all checks that should be run for the endpoint to validate
// it is providing a valid response to service requests.
//
// TODO_IMPROVE: Rename to 'endpointValidation'
type endpoint struct {
	invalidResponseLastObserved *time.Time

	hasReturnedEmptyResponse   bool
	hasReturnedInvalidResponse bool

	invalidResponseError qosobservations.EVMResponseValidationError

	checkBlockNumber endpointCheckBlockNumber
	checkChainID     endpointCheckChainID
	checkArchival    endpointCheckArchival
}
