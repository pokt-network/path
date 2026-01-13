package cosmos

import (
	"strconv"
	"sync"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"

	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
)

// endpointStore maintains QoS data on the set of available endpoints
// for a CosmosSDK-based blockchain service.
//
// It performs two key tasks:
//  1. Storing the set of endpoints and their quality data.
//  2. Application of endpoints' observations to update the data on endpoints.
type endpointStore struct {
	logger polylog.Logger

	endpointsMu sync.RWMutex
	endpoints   map[protocol.EndpointAddr]endpoint
}

// getEndpoint returns the endpoint for a given endpoint address.
// Used by the request validator to get the endpoint's synthetic QoS checks.
func (es *endpointStore) getEndpoint(endpointAddr protocol.EndpointAddr) endpoint {
	es.endpointsMu.RLock()
	defer es.endpointsMu.RUnlock()
	return es.endpoints[endpointAddr]
}

// updateEndpointsFromObservations creates/updates endpoint entries in the store based
// on the supplied observations. It returns the set of created/updated endpoints.
func (es *endpointStore) updateEndpointsFromObservations(
	cosmosObservations *qosobservations.CosmosRequestObservations,
) map[protocol.EndpointAddr]endpoint {
	es.endpointsMu.Lock()
	defer es.endpointsMu.Unlock()

	endpointObservations := cosmosObservations.GetEndpointObservations()

	logger := es.logger.With(
		"qos_instance", "cosmos",
		"method", "UpdateEndpointsFromObservations",
	)

	logger.Debug().Msgf("About to update endpoints from %d observations.", len(endpointObservations))

	updatedEndpoints := make(map[protocol.EndpointAddr]endpoint)
	for _, observation := range endpointObservations {
		if observation == nil {
			logger.Debug().Msg(" CosmosSDK EndpointStore received a nil observation. SKIPPING...")
			continue
		}

		endpointAddr := protocol.EndpointAddr(observation.EndpointAddr)

		logger := logger.With("endpoint_addr", endpointAddr)
		logger.Debug().Msg("processing observation for endpoint.")

		// It is a valid scenario for an endpoint to not be present in the store.
		// e.g. when the first observation(s) are received for an endpoint.
		storedEndpoint := es.endpoints[endpointAddr]

		endpointWasMutated := applyObservation(
			&storedEndpoint,
			observation,
		)

		// If the observation did not mutate the endpoint, there is no need to update the stored endpoint entry.
		if !endpointWasMutated {
			logger.Debug().Msg(" Endpoint was not mutated by observations. SKIPPING update of internal endpoint store.")
			continue
		}

		es.endpoints[endpointAddr] = storedEndpoint
		updatedEndpoints[endpointAddr] = storedEndpoint
	}

	return updatedEndpoints
}

// applyObservation updates the data stored regarding the endpoint using the supplied observation.
// It returns true if the observation was not unrecognized, i.e. mutated the endpoint.
//
// TODO_TECHDEBT(@adshmh): add a method to distinguish the following two scenarios:
//   - an endpoint that returned in invalid response.
//   - an endpoint with no/incomplete observations.
func applyObservation(
	endpoint *endpoint,
	observation *qosobservations.CosmosEndpointObservation,
) (endpointWasMutated bool) {
	validationResult := observation.EndpointResponseValidationResult
	if validationResult == nil {
		return false
	}

	// Check if there's a validation error
	if validationResult.ValidationError != nil {
		applyValidationErrorObservation(endpoint, *validationResult.ValidationError)
		endpointWasMutated = true
		return
	}

	// Handle specific response types based on the parsed_response oneof field
	switch response := validationResult.ParsedResponse.(type) {
	// CometBFT `health` method observation
	// Reference: https://docs.cometbft.com/v1.0/spec/rpc/#health
	case *qosobservations.CosmosEndpointResponseValidationResult_ResponseCometBftHealth:
		applyCometBFTHealthObservation(endpoint, response.ResponseCometBftHealth)
		endpointWasMutated = true

	// CometBFT `status` method observation
	// Reference: https://docs.cometbft.com/v1.0/spec/rpc/#status
	case *qosobservations.CosmosEndpointResponseValidationResult_ResponseCometBftStatus:
		applyCometBFTStatusObservation(endpoint, response.ResponseCometBftStatus)
		endpointWasMutated = true

	// Cosmos SDK `status` method observation
	// Reference: https://docs.cosmos.network/api#tag/Service/operation/Status
	case *qosobservations.CosmosEndpointResponseValidationResult_ResponseCosmosSdkStatus:
		applyCosmosSDKStatusObservation(endpoint, response.ResponseCosmosSdkStatus)
		endpointWasMutated = true

	// EVM `eth_chainId` method observation
	// Reference: https://ethereum.org/en/developers/docs/apis/json-rpc/#eth_chainid
	case *qosobservations.CosmosEndpointResponseValidationResult_ResponseEvmJsonrpcChainId:
		applyEVMChainIDObservation(endpoint, response.ResponseEvmJsonrpcChainId)
		endpointWasMutated = true

		// TODO_TECHDEBT(@adshmh): Introduce and use a new response type to:
		// - Capture invalid endpoint responses to RESTful API requests
		// - Support sanctions on endpoints based on above.
		// - Export metrics to show endpoints characteristics re RESTful API requests.
		//
	}

	return endpointWasMutated
}

// applyValidationErrorObservation updates the endpoint state when a validation error occurs.
func applyValidationErrorObservation(endpoint *endpoint, validationError qosobservations.CosmosResponseValidationError) {
	endpoint.hasReturnedInvalidResponse = true
	now := time.Now()
	endpoint.invalidResponseLastObserved = &now

	// Set specific error flags based on validation error type
	switch validationError {
	case qosobservations.CosmosResponseValidationError_COSMOS_RESPONSE_VALIDATION_ERROR_EMPTY:
		endpoint.hasReturnedEmptyResponse = true
	case qosobservations.CosmosResponseValidationError_COSMOS_RESPONSE_VALIDATION_ERROR_UNMARSHAL:
		endpoint.hasReturnedUnmarshalingError = true
	}
}

// applyCometBFTHealthObservation updates the health check if a valid observation is provided.
func applyCometBFTHealthObservation(endpoint *endpoint, healthResponse *qosobservations.CosmosResponseCometBFTHealth) {
	healthy := healthResponse.HealthStatus
	endpoint.checkCometBFTHealth = endpointCheckCometBFTHealth{
		healthy:   &healthy,
		expiresAt: time.Now().Add(checkHealthInterval),
	}
}

// applyCometBFTStatusObservation updates the status check if a valid observation is provided.
func applyCometBFTStatusObservation(endpoint *endpoint, statusResponse *qosobservations.CosmosResponseCometBFTStatus) {
	chainID := statusResponse.ChainId
	catchingUp := statusResponse.CatchingUp
	blockHeight := parseBlockHeightResponse(statusResponse.LatestBlockHeight)

	endpoint.checkCometBFTStatus = endpointCheckCometBFTStatus{
		chainID:           &chainID,
		catchingUp:        &catchingUp,
		latestBlockHeight: &blockHeight,
		expiresAt:         time.Now().Add(checkStatusInterval),
	}
}

// applyCosmosSDKStatusObservation updates the status check if a valid observation is provided.
func applyCosmosSDKStatusObservation(endpoint *endpoint, statusResponse *qosobservations.CosmosResponseCosmosSDKStatus) {
	latestBlockHeight := statusResponse.LatestBlockHeight
	endpoint.checkCosmosStatus = endpointCheckCosmosStatus{
		latestBlockHeight: &latestBlockHeight,
	}
}

// applyEVMChainIDObservation updates the chain ID check if a valid observation is provided.
func applyEVMChainIDObservation(endpoint *endpoint, chainIDResponse *qosobservations.CosmosResponseEVMJSONRPCChainID) {
	evmChainID := chainIDResponse.EvmChainId
	endpoint.checkEVMChainID = endpointCheckEVMChainID{
		chainID:   &evmChainID,
		expiresAt: time.Now().Add(checkEVMChainIDInterval),
	}
}

// parseBlockHeightResponse parses the block height response from a string to a uint64.
// CosmosSDK returns block height as a string, so we need to parse it.
func parseBlockHeightResponse(response string) uint64 {
	if response == "" {
		return 0
	}

	// Convert string to uint64 - CosmosSDK returns block height as decimal string
	parsed, err := strconv.ParseUint(response, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}
