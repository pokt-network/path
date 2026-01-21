package solana

import (
	"context"
	"errors"
	"net/http"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/metrics/devtools"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	qostypes "github.com/pokt-network/path/qos/types"
)

// QoS implements gateway.QoSService by providing:
//  1. QoSRequestParser - Builds Solana-specific RequestQoSContext objects from HTTP requests
//  2. EndpointSelector - Selects endpoints for service requests
var _ gateway.QoSService = &QoS{}

// devtools.QoSDisqualifiedEndpointsReporter is fulfilled by the QoS struct below.
// This allows the QoS service to report its disqualified endpoints data to the devtools.DisqualifiedEndpointReporter.
// TODO_TECHDEBT(@commoddity): implement this for Solana to enable debugging QoS results.
var _ devtools.QoSDisqualifiedEndpointsReporter = &QoS{}

// QoS implements ServiceQoS for Solana-based chains.
// It handles chain-specific:
//   - Request parsing
//   - Response building
//   - Endpoint validation and selection
type QoS struct {
	logger polylog.Logger
	*EndpointStore
	*ServiceState
	*requestValidator
}

// ParseHTTPRequest builds a request context from the provided HTTP request.
// It returns an error if the HTTP request cannot be parsed as a JSONRPC request.
//
// Implements the gateway.QoSService interface.
// Fallback logic for Solana: header → jsonrpc (Solana only supports JSON-RPC)
func (qos *QoS) ParseHTTPRequest(_ context.Context, req *http.Request, detectedRPCType sharedtypes.RPCType) (gateway.RequestQoSContext, bool) {
	return qos.validateHTTPRequest(req, detectedRPCType)
}

// ParseWebsocketRequest builds a request context from the provided Websocket request.
// Websocket connection requests do not have a body, so we don't need to parse it.
//
// This method implements the gateway.QoSService interface.
func (qos *QoS) ParseWebsocketRequest(_ context.Context) (gateway.RequestQoSContext, bool) {
	return &requestContext{
		logger:        qos.logger,
		endpointStore: qos.EndpointStore,
		// Set the origin of the request as Organic (i.e. user request)
		// The request is from a user.
		requestOrigin: qosobservations.RequestOrigin_REQUEST_ORIGIN_ORGANIC,
	}, true
}

// ApplyObservations updates the stored endpoints and the perceived blockchain state using the supplied observations.
// Implements the gateway.QoSService interface.
func (q *QoS) ApplyObservations(observations *qosobservations.Observations) error {
	if observations == nil {
		return errors.New("ApplyObservations: received nil observations")
	}

	solanaObservations := observations.GetSolana()
	if solanaObservations == nil {
		return errors.New("ApplyObservations: received nil Solana observation")
	}

	updatedEndpoints := q.UpdateEndpointsFromObservations(solanaObservations)

	// update the perceived current state of the blockchain.
	return q.UpdateFromEndpoints(updatedEndpoints)
}

// HydrateDisqualifiedEndpointsResponse is a no-op for the Solana QoS.
// TODO_TECHDEBT(@commoddity): implement this for Solana to enable debugging QoS results.
func (QoS) HydrateDisqualifiedEndpointsResponse(_ protocol.ServiceID, _ *devtools.DisqualifiedEndpointResponse) {
}

// UpdateFromExtractedData updates QoS state from extracted observation data.
// Called by the observation pipeline after async parsing completes.
// This updates the perceived block height without blocking user requests.
//
// Implements gateway.QoSService interface.
func (q *QoS) UpdateFromExtractedData(endpointAddr protocol.EndpointAddr, data *qostypes.ExtractedData) error {
	if data == nil {
		return nil
	}

	// Only update if we extracted a valid block height
	if data.BlockHeight <= 0 {
		return nil
	}

	q.serviceStateLock.Lock()
	defer q.serviceStateLock.Unlock()

	// Update perceived block height to maximum across all endpoints
	blockHeight := uint64(data.BlockHeight)
	if blockHeight > q.perceivedBlockHeight {
		q.logger.Debug().
			Str("endpoint", string(endpointAddr)).
			Uint64("old_block", q.perceivedBlockHeight).
			Uint64("new_block", blockHeight).
			Msg("Updating perceived block height from observation pipeline")
		q.perceivedBlockHeight = blockHeight
	}

	return nil
}

// GetPerceivedBlockNumber returns the perceived current block height.
// Used by health checks for block height validation.
// Returns 0 if no block height has been observed yet.
//
// Implements gateway.QoSService interface.
func (q *QoS) GetPerceivedBlockNumber() uint64 {
	q.serviceStateLock.RLock()
	defer q.serviceStateLock.RUnlock()
	return q.perceivedBlockHeight
}
