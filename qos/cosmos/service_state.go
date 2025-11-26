package cosmos

import (
	"errors"
	"sync"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/metrics/devtools"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
)

var _ protocol.EndpointSelector = &serviceState{}

var (
	errNilApplyObservations          = errors.New("ApplyObservations: received nil")
	errNilApplyCosmosSDKObservations = errors.New("ApplyObservations: received nil CosmosSDK observation")
)

// The serviceState struct maintains the expected current state of the CosmosSDK blockchain
// based on the endpoints' responses to different requests.
//
// It is responsible for the following:
//  1. Generate QoS endpoint checks for the hydrator
//  2. Select a valid endpoint for a service request
//  3. Update the stored endpoints from observations.
//  4. Update the stored service state from observations.
type serviceState struct {
	logger polylog.Logger

	// serviceStateLock is a read-write mutex used to synchronize access to this struct
	serviceStateLock sync.RWMutex

	// serviceQoSConfig maintains the QoS configs for this service
	serviceQoSConfig CosmosSDKServiceQoSConfig

	// endpointStore maintains the set of available endpoints and their quality data
	endpointStore *endpointStore

	// perceivedBlockNumber is the perceived current block number
	// based on endpoints' responses to `/status` requests.
	// It is calculated as the maximum of block height reported by
	// any of the endpoints for the service.
	perceivedBlockNumber uint64
}

/* -------------------- QoS Endpoint State Updater -------------------- */

// ApplyObservations updates endpoint storage and blockchain state from observations.
func (ss *serviceState) ApplyObservations(observations *qosobservations.Observations) error {
	if observations == nil {
		return errNilApplyObservations
	}

	cosmosSDKObservations := observations.GetCosmos()
	if cosmosSDKObservations == nil {
		return errNilApplyCosmosSDKObservations
	}

	updatedEndpoints := ss.endpointStore.updateEndpointsFromObservations(
		cosmosSDKObservations,
	)

	return ss.updateFromEndpoints(updatedEndpoints)
}

// updateFromEndpoints updates the service state based on new observations from endpoints.
// - Only endpoints with received observations are considered.
// - Estimations are derived from these updated endpoints.
func (ss *serviceState) updateFromEndpoints(updatedEndpoints map[protocol.EndpointAddr]endpoint) error {
	ss.serviceStateLock.Lock()
	defer ss.serviceStateLock.Unlock()

	for endpointAddr, endpoint := range updatedEndpoints {
		logger := ss.logger.With(
			"endpoint_addr", endpointAddr,
			"perceived_block_number", ss.perceivedBlockNumber,
		)

		// Do not update the perceived block number if the `status` check fails.
		// Note that this does not check the block height sync allowance as the perceived block number
		// may not yet be set, causing a scenario where the perceived block number is never set.
		if err := ss.isCometBFTStatusValid(endpoint.checkCometBFTStatus); err != nil {
			logger.Warn().Err(err).Msgf("⚠️ SKIPPING endpoint '%s' with invalid status", endpointAddr)
			continue
		}

		// Retrieve the block number from the endpoint.
		blockNumber, err := endpoint.checkCometBFTStatus.GetLatestBlockHeight()
		if err != nil {
			logger.Warn().Err(err).Msgf("⚠️ SKIPPING endpoint '%s' with invalid block height", endpointAddr)
			continue
		}

		// Update perceived block number to maximum instead of overwriting with last endpoint.
		// Per perceivedBlockNumber field documentation, it should be "the maximum of block height reported by any endpoint"
		// but code was incorrectly overwriting with each endpoint, causing validation failures.
		if blockNumber > ss.perceivedBlockNumber {
			logger.Debug().Msgf("Updating perceived block number from %d to %d", ss.perceivedBlockNumber, blockNumber)
			ss.perceivedBlockNumber = blockNumber
		}
	}

	return nil
}

// getDisqualifiedEndpointsResponse gets the QoSLevelDisqualifiedEndpoints map for a devtools.DisqualifiedEndpointResponse.
// It checks the current service state and populates a map with QoS-level disqualified endpoints.
// This data is useful for creating a snapshot of the current QoS state for a given service.
func (ss *serviceState) getDisqualifiedEndpointsResponse(serviceID protocol.ServiceID) devtools.QoSLevelDataResponse {
	qosLevelDataResponse := devtools.QoSLevelDataResponse{
		DisqualifiedEndpoints: make(map[protocol.EndpointAddr]devtools.QoSDisqualifiedEndpoint),
	}

	// Populate the data response object using the endpoints in the endpoint store.
	for endpointAddr, endpoint := range ss.endpointStore.endpoints {
		if err := ss.basicEndpointValidation(endpoint); err != nil {
			qosLevelDataResponse.DisqualifiedEndpoints[endpointAddr] = devtools.QoSDisqualifiedEndpoint{
				EndpointAddr: endpointAddr,
				Reason:       err.Error(),
				ServiceID:    serviceID,
			}

			// DEV_NOTE: if new checks are added to a service, we need to add them here.
			switch {
			// Endpoint is disqualified due to an empty response.
			case errors.Is(err, errEmptyResponseObs):
				qosLevelDataResponse.EmptyResponseCount++

			// Endpoint is disqualified due to a missing or invalid chain ID (status check related).
			case errors.Is(err, errInvalidCometBFTChainIDObs),
				errors.Is(err, errNoEVMChainIDObs),
				errors.Is(err, errInvalidEVMChainIDObs):
				qosLevelDataResponse.ChainIDCheckErrorsCount++

			// Endpoint is disqualified due to block number issues (status check related).
			case errors.Is(err, errOutsideSyncAllowanceBlockNumberObs),
				errors.Is(err, errNoCometBFTStatusObs):
				qosLevelDataResponse.BlockNumberCheckErrorsCount++

			// TODO_TECHDEBT(@commoddity): Update QoSDisqualifiedEndpoint to
			// track all CosmosSDK-specific errors in a dedicated struct.
			//
			// Other CosmosSDK-specific errors - not tracked individually

			default:
				ss.logger.Error().Err(err).Msgf("SHOULD NEVER HAPPEN: unknown error for endpoint: %s", endpointAddr)
			}

			continue
		}
	}

	return qosLevelDataResponse
}
