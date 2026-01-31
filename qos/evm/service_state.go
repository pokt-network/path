package evm

import (
	"errors"
	"sync/atomic"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/metrics/devtools"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/jsonrpc"
	"github.com/pokt-network/path/reputation"
)

var (
	errNilApplyObservations    = errors.New("ApplyObservations: received nil")
	errNilApplyEVMObservations = errors.New("ApplyObservations: received nil EVM observation")
)

// The serviceState struct maintains the expected current state of the EVM blockchain
// based on the endpoints' responses to different requests.
//
// It is responsible for the following:
//  1. Generate QoS endpoint checks for the hydrator
//  2. Select a valid endpoint for a service request
//  3. Update the stored endpoints from observations.
//  4. Update the stored service state from observations.
type serviceState struct {
	logger polylog.Logger

	// serviceQoSConfig maintains the QoS configs for this service
	serviceQoSConfig EVMServiceQoSConfig

	// endpointStore maintains the set of available endpoints and their quality data
	endpointStore *endpointStore

	// perceivedBlockNumber is the perceived current block number
	// based on endpoints' responses to `eth_blockNumber` requests.
	// Uses atomic.Uint64 for lock-free reads to avoid contention on hot path.
	// Updated by blockConsensus calculations.
	//
	// See the following link for more details:
	// https://ethereum.org/en/developers/docs/apis/json-rpc/#eth_blocknumber
	perceivedBlockNumber atomic.Uint64

	// blockConsensus calculates perceived block using median-anchored consensus.
	// This protects against malicious endpoints reporting extreme block heights.
	// The consensus result is stored in perceivedBlockNumber for fast reads.
	blockConsensus *BlockHeightConsensus

	// archivalHeuristic provides archival request detection for routing decisions.
	// Cached instance to avoid allocations on the hot path.
	archivalHeuristic *ArchivalHeuristic

	// reputationSvc provides shared archival status across replicas via Redis.
	// When set, archival checks use this in addition to the local endpointStore.
	// This ensures archival status set by health check leader is visible to all replicas.
	reputationSvc reputation.ReputationService
}

/* -------------------- QoS Endpoint Check Generator -------------------- */

// serviceState provides the endpoint check generator required by
// the gateway package to augment endpoints' quality data,
// using synthetic service requests.
var _ gateway.QoSEndpointCheckGenerator = &serviceState{}

// CheckWebsocketConnection returns true if the endpoint supports Websocket connections.
func (ss *serviceState) CheckWebsocketConnection() bool {
	_, supportsWebsockets := ss.serviceQoSConfig.getSupportedAPIs()[sharedtypes.RPCType_WEBSOCKET]
	return supportsWebsockets
}

// GetRequiredQualityChecks returns the list of quality checks required for an endpoint.
// It is called in the `gateway/hydrator.go` file on each run of the hydrator.
//
// Note: Archival capability is determined by external health checks, not by synthetic
// QoS requests. The health check system marks endpoints as archival-capable via
// UpdateFromExtractedData when health checks pass.
func (ss *serviceState) GetRequiredQualityChecks(endpointAddr protocol.EndpointAddr) []gateway.RequestQoSContext {
	ss.endpointStore.endpointsMu.RLock()
	defer ss.endpointStore.endpointsMu.RUnlock()

	endpoint := ss.endpointStore.endpoints[endpointAddr]

	var checks = []gateway.RequestQoSContext{
		// Block number check should always run
		ss.getEndpointCheck(endpoint.checkBlockNumber.getRequestID(), endpoint.checkBlockNumber.getServicePayload()),
	}

	// Chain ID check runs infrequently as an endpoint's EVM chain ID is very unlikely to change regularly.
	if ss.shouldChainIDCheckRun(endpoint.checkChainID) {
		checks = append(checks, ss.getEndpointCheck(endpoint.checkChainID.getRequestID(), endpoint.checkChainID.getServicePayload()))
	}

	return checks
}

// getEndpointCheck prepares a request context for a specific endpoint check.
// The pre-selected endpoint address is assigned to the request context in the `endpoint.getChecks` method.
// It is called in the individual `check_*.go` files to build the request context.
// getEndpointCheck prepares a request context for a specific endpoint check.
func (ss *serviceState) getEndpointCheck(jsonrpcReqID jsonrpc.ID, servicePayload protocol.Payload) *requestContext {
	return &requestContext{
		logger:       ss.logger,
		serviceState: ss,
		// Wrap single request in map for consistency
		servicePayloads: map[jsonrpc.ID]protocol.Payload{
			jsonrpcReqID: servicePayload,
		},
		// Set the chain and Service ID: this is required to generate observations with the correct chain ID.
		chainID:   ss.serviceQoSConfig.getEVMChainID(),
		serviceID: ss.serviceQoSConfig.GetServiceID(),
		// Set the origin of the request as Synthetic.
		// The request is generated by the QoS service to collect extra observations on endpoints.
		requestOrigin: qosobservations.RequestOrigin_REQUEST_ORIGIN_SYNTHETIC,
	}
}

// shouldChainIDCheckRun returns true if the chain ID check is not yet initialized or has expired.
func (ss *serviceState) shouldChainIDCheckRun(check endpointCheckChainID) bool {
	return check.expiresAt.IsZero() || check.expiresAt.Before(time.Now())
}

/* -------------------- QoS Endpoint State Updater -------------------- */

// ApplyObservations updates endpoint storage and blockchain state from observations.
func (ss *serviceState) ApplyObservations(observations *qosobservations.Observations) error {
	if observations == nil {
		return errNilApplyObservations
	}

	evmObservations := observations.GetEvm()
	if evmObservations == nil {
		return errNilApplyEVMObservations
	}

	updatedEndpoints := ss.endpointStore.updateEndpointsFromObservations(evmObservations)

	return ss.updateFromEndpoints(updatedEndpoints)
}

// updateFromEndpoints updates the service state based on new observations from endpoints.
// - Only endpoints with received observations are considered.
// - Estimations are derived from these updated endpoints.
func (ss *serviceState) updateFromEndpoints(updatedEndpoints map[protocol.EndpointAddr]endpoint) error {
	for endpointAddr, endpoint := range updatedEndpoints {
		currentPerceived := ss.perceivedBlockNumber.Load()
		logger := ss.logger.With(
			"endpoint_addr", endpointAddr,
			"perceived_block_number", currentPerceived,
		)

		// Do not update the perceived block number if the chain ID is invalid.
		if err := ss.isChainIDValid(endpoint.checkChainID); err != nil {
			logger.Warn().Err(err).Msgf("⚠️ SKIPPING endpoint because it has an invalid chain id: %s", endpointAddr)
			continue
		}

		// Retrieve the block number from the endpoint.
		blockNumber, err := endpoint.checkBlockNumber.getBlockNumber()
		if err != nil {
			logger.Warn().Err(err).Msgf("⚠️ SKIPPING endpoint because it has an invalid block number: %s", endpointAddr)
			continue
		}

		// Update perceived block using consensus mechanism
		// This protects against malicious endpoints reporting extreme block heights
		if blockNumber > 0 && ss.blockConsensus != nil {
			oldBlock := ss.perceivedBlockNumber.Load()
			newPerceived := ss.blockConsensus.AddObservation(endpointAddr, blockNumber)

			// Update the atomic for fast reads
			ss.perceivedBlockNumber.Store(newPerceived)

			if newPerceived != oldBlock {
				ss.logger.Debug().
					Str("endpoint", string(endpointAddr)).
					Uint64("reported_block", blockNumber).
					Uint64("old_perceived", oldBlock).
					Uint64("new_perceived", newPerceived).
					Msg("Updated perceived block number via consensus")
			}
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
	// Use requiresArchival=true to check full validation including archival (most conservative)
	for endpointAddr, endpoint := range ss.endpointStore.endpoints {
		if err := ss.basicEndpointValidation(endpointAddr, endpoint, true); err != nil {
			qosLevelDataResponse.DisqualifiedEndpoints[endpointAddr] = devtools.QoSDisqualifiedEndpoint{
				EndpointAddr: endpointAddr,
				Reason:       err.Error(),
				ServiceID:    serviceID,
			}

			// DEV_NOTE: if new checks are added to a service, we need to add them here.
			switch {
			// Endpoint is disqualified due to an empty qosLevelDataResponse.
			case errors.Is(err, errEmptyResponseObs):
				qosLevelDataResponse.EmptyResponseCount++

			// Endpoint is disqualified due to a missing or invalid block number.
			case errors.Is(err, errNoBlockNumberObs),
				errors.Is(err, errInvalidBlockNumberObs):
				qosLevelDataResponse.BlockNumberCheckErrorsCount++

			// Endpoint is disqualified due to a missing or invalid chain ID.
			case errors.Is(err, errNoChainIDObs),
				errors.Is(err, errInvalidChainIDObs):
				qosLevelDataResponse.ChainIDCheckErrorsCount++

			// Endpoint is disqualified due to not being archival-capable.
			case errors.Is(err, errEndpointNotArchival):
				qosLevelDataResponse.ArchivalCheckErrorsCount++

			default:
				ss.logger.Error().Err(err).Msgf("SHOULD NEVER HAPPEN: unknown error for endpoint: %s", endpointAddr)
			}

			continue
		}
	}

	return qosLevelDataResponse
}
