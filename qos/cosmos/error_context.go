package cosmos

import (
	"encoding/json"
	"errors"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/buildwithgrove/path/gateway"
	pathhttp "github.com/buildwithgrove/path/network/http"
	qosobservations "github.com/buildwithgrove/path/observation/qos"
	"github.com/buildwithgrove/path/protocol"
	"github.com/buildwithgrove/path/qos/jsonrpc"
)

// Error recording that endpoint selection was attempted but failed due to an invalid request
var errInvalidSelectorUsage = errors.New("endpoint selection attempted on failed request")

// errorContext provides the support required by the gateway package for handling service requests.
var _ gateway.RequestQoSContext = &errorContext{}

// errorContext terminates CosmosSDK request processing on errors (internal failures or invalid requests).
// Provides:
//  1. Detailed error response to the user
//  2. Observation: feed into Metrics and data pipeline.
//
// Implements gateway.RequestQoSContext
type errorContext struct {
	logger polylog.Logger

	// The observation to return, to be processed by the metrics and data pipeline components.
	cosmosSDKObservations *qosobservations.Observations_Cosmos

	// The response to be returned to the user.
	response jsonrpc.Response

	// HTTP status code for the response
	// If not set, will default to the status code recommended by the JSONRPC response.
	responseHTTPStatusCode int
}

// Implements the gateway.RequestQoSContext interface.
func (ec *errorContext) GetHTTPResponse() pathhttp.HTTPResponse {
	bz, err := json.Marshal(ec.response)
	if err != nil {
		// TODO_IMPROVE(@adshmh): Standardize logger labels across packages
		// 1. Create shared label schema for the cosmossdk package
		// 2. Extend schema to other QoS packages
		ec.logger.With(
			"qos", "cosmossdk",
			"component", "errorContext",
			"method", "GetHTTPResponse",
		).Warn().Err(err).Msg("Failed to serialize client response.")
	}

	return httpResponse{
		responsePayload: bz,
		httpStatusCode:  ec.responseHTTPStatusCode,
	}
}

// GetObservation returns the QoS observation set for the error context.
// Implements the gateway.RequestQoSContext interface.
func (ec *errorContext) GetObservations() qosobservations.Observations {
	return qosobservations.Observations{
		ServiceObservations: ec.cosmosSDKObservations,
	}
}

// GetServicePayload should never be called.
// It logs a warning and returns nil.
// Implements the gateway.RequestQoSContext interface.
func (ec *errorContext) GetServicePayloads() []protocol.Payload {
	ec.logger.Warn().Msg("SHOULD NEVER HAPPEN: errorContext.GetServicePayloads() should never be called.")
	return []protocol.Payload{protocol.EmptyErrorPayload()}
}

// UpdateWithResponse should never be called.
// Only logs a warning.
// Implements the gateway.RequestQoSContext interface.
func (ec *errorContext) UpdateWithResponse(endpointAddr protocol.EndpointAddr, endpointSerializedResponse []byte) {
	ec.logger.With(
		"endpoint_addr", endpointAddr,
		"endpoint_response_len", len(endpointSerializedResponse),
	).Warn().Msg("SHOULD NEVER HAPPEN: errorContext.UpdateWithResponse() should never be called.")
}

// UpdateWithResponse should never be called.
// It logs a warning and returns a failing selector that logs a warning on all selection attempts.
// Implements the gateway.RequestQoSContext interface.
func (ec *errorContext) GetEndpointSelector() protocol.EndpointSelector {
	ec.logger.Warn().Msg("SHOULD NEVER HAPPEN: errorContext.GetEndpointSelector() should never be called.")

	return errorTrackingSelector{
		logger: ec.logger,
	}
}

// errorTrackingSelector prevents panics in request handling goroutines by:
// - Intentionally failing all endpoint selection attempts
// - Logging diagnostic information when endpoint selection is incorrectly attempted on failed requests
// Acts as a failsafe mechanism for request handling.
type errorTrackingSelector struct {
	logger polylog.Logger
}

// Select method of an errorTrackingSelector should never be called.
// It logs a warning and returns an invalid usage error.
// Implements the protocol.EndpointSelector interface.
func (ets errorTrackingSelector) Select(endpoints protocol.EndpointAddrList) (protocol.EndpointAddr, error) {
	ets.logger.With(
		"num_endpoints", len(endpoints),
	).Warn().Msg("SHOULD NEVER HAPPEN: errorTrackingSelector.Select() should never be called.")

	return protocol.EndpointAddr(""), errInvalidSelectorUsage
}

// SelectMultiple method of an errorTrackingSelector should never be called.
// It logs a warning and returns an invalid usage error.
// Implements the protocol.EndpointSelector interface.
func (ets errorTrackingSelector) SelectMultiple(endpoints protocol.EndpointAddrList, numEndpoints uint) (protocol.EndpointAddrList, error) {
	ets.logger.With(
		"num_endpoints", len(endpoints),
		"num_endpoints_to_select", numEndpoints,
	).Warn().Msg("SHOULD NEVER HAPPEN: errorTrackingSelector.SelectMultiple() should never be called.")

	return nil, errInvalidSelectorUsage
}
