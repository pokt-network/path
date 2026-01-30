package evm

import (
	"fmt"
	"io"
	"net/http"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/gateway"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// maxRequestBodySize is the maximum allowed size for HTTP request bodies (100MB).
// This prevents OOM attacks from unbounded io.ReadAll calls.
const maxRequestBodySize = 100 * 1024 * 1024

// TODO_TECHDEBT(@adshmh): Simplify the qos package by refactoring gateway.QoSContextBuilder.
// Proposed change: Create a new ServiceRequest type containing raw payload data ([]byte)
// Benefits: Decouples the qos package from HTTP-specific error handling.

// TODO_TECHDEBT(@adshmh): Refactor the evmRequestValidator struct to be more generic and reusable.
//
// evmRequestValidator handles request validation, generating:
// - Error contexts when validation fails
// - Request contexts when validation succeeds
// TODO_IMPROVE(@adshmh): Consider creating an interface with method-specific JSONRPC request validation
type evmRequestValidator struct {
	logger       polylog.Logger
	chainID      string
	serviceID    protocol.ServiceID
	serviceState *serviceState
}

// validateHTTPRequest validates an HTTP request, extracting and validating its EVM JSONRPC payload.
// If validation fails, an errorContext is returned along with false.
// If validation succeeds, a fully initialized requestContext is returned along with true.
//
// Archival detection runs on the hot path using gjson (~350ns, 40B alloc) to determine
// if the request requires archival data for routing decisions.
//
// EVM only supports JSON-RPC, so detectedRPCType is logged but not used for routing.
func (erv *evmRequestValidator) validateHTTPRequest(req *http.Request, detectedRPCType sharedtypes.RPCType) (gateway.RequestQoSContext, bool) {
	logger := erv.logger.With(
		"qos", "EVM",
		"method", "validateHTTPRequest",
		"detected_rpc_type", detectedRPCType.String(),
	)

	// Read the HTTP request body with size limit to prevent OOM attacks
	limitedBody := http.MaxBytesReader(nil, req.Body, maxRequestBodySize)
	body, err := io.ReadAll(limitedBody)
	if err != nil {
		// Check if the error is due to body size limit exceeded
		if err.Error() == "http: request body too large" {
			err = fmt.Errorf("request body exceeds %d bytes limit", maxRequestBodySize)
		}
		logger.Warn().Err(err).Msg("HTTP request body read failed - returning generic error response")
		return erv.createHTTPBodyReadFailureContext(err), false
	}

	// Parse and validate the JSONRPC request(s) - handles both single and batch requests
	jsonrpcReqs, isBatch, err := jsonrpc.ParseJSONRPCFromRequestBody(logger, body)
	if err != nil {
		// If no requests parsed or empty ID, requestID will be zero value (empty)
		return erv.createRequestUnmarshalingFailureContext(jsonrpc.ID{}, err), false
	}

	// TODO_MVP(@adshmh): Add JSON-RPC request validation to block invalid requests
	// TODO_IMPROVE(@adshmh): Add method-specific JSONRPC request validation

	servicePayloads := erv.buildServicePayloads(jsonrpcReqs)

	// Run archival heuristic detection (gjson-based, ~350ns, lock-free)
	// This determines if the request needs archival data for endpoint routing.
	var archivalResult ArchivalHeuristicResult
	if erv.serviceState.archivalHeuristic != nil && !isBatch {
		// Get perceived block from serviceState (atomic, lock-free read)
		perceivedBlock := erv.serviceState.perceivedBlockNumber.Load()
		archivalResult = erv.serviceState.archivalHeuristic.IsArchivalRequest(body, perceivedBlock)

		logger.Debug().
			Bool("requires_archival", archivalResult.RequiresArchival).
			Str("archival_reason", archivalResult.Reason).
			Str("method", archivalResult.Method).
			Msg("Archival detection completed")
	}

	// Request is valid, return a fully initialized requestContext
	return &requestContext{
		logger:               erv.logger,
		chainID:              erv.chainID,
		serviceID:            erv.serviceID,
		requestPayloadLength: uint(len(body)),
		servicePayloads:      servicePayloads,
		isBatch:              isBatch,
		serviceState:         erv.serviceState,
		archivalResult:       archivalResult,
		// Set the origin of the request as ORGANIC (i.e. from a user).
		requestOrigin: qosobservations.RequestOrigin_REQUEST_ORIGIN_ORGANIC,
	}, true
}

func (erv *evmRequestValidator) buildServicePayloads(
	jsonrpcReqs map[jsonrpc.ID]jsonrpc.Request,
) map[jsonrpc.ID]protocol.Payload {
	payloads := make(map[jsonrpc.ID]protocol.Payload)

	for reqID, req := range jsonrpcReqs {
		payload, err := req.BuildPayload()
		if err != nil {
			erv.logger.Error().Err(err).Msg("SHOULD RARELY HAPPEN: requestContext.GetServicePayload() should never fail building the JSONRPC request.")
			payloads[reqID] = protocol.EmptyErrorPayload()
			continue
		}
		payloads[reqID] = payload
	}

	return payloads
}

// createHTTPBodyReadFailureContext creates an error context for HTTP body read failures.
func (erv *evmRequestValidator) createHTTPBodyReadFailureContext(err error) gateway.RequestQoSContext {
	// Create the observations object with the HTTP body read failure observation
	observations := erv.createHTTPBodyReadFailureObservation(err)

	// TODO_IMPROVE(@adshmh): Propagate a request ID parameter on internal errors
	// that occur after successful request parsing.
	// There are no such cases as of PR #186.
	//
	// Create the JSON-RPC error response
	response := newErrResponseInternalErr(jsonrpc.ID{}, err)

	// Build and return the error context
	return &errorContext{
		logger:                 erv.logger,
		response:               response,
		responseHTTPStatusCode: jsonrpc.HTTPStatusRequestValidationFailureReadHTTPBodyFailure,
		evmObservations:        observations,
	}
}

// createRequestUnmarshalingFailureContext creates an error context for request unmarshaling failures.
func (erv *evmRequestValidator) createRequestUnmarshalingFailureContext(id jsonrpc.ID, err error) gateway.RequestQoSContext {

	// Create the observations object with the request unmarshaling failure observation
	observations := createRequestUnmarshalingFailureObservation(id, erv.serviceID, erv.chainID, err)
	// Create the JSON-RPC error response
	response := newErrResponseInvalidRequest(err, id)

	// Build and return the error context
	return &errorContext{
		logger:                 erv.logger,
		response:               response,
		responseHTTPStatusCode: jsonrpc.HTTPStatusRequestValidationFailureUnmarshalFailure,
		evmObservations:        observations,
	}
}

// createRequestUnmarshalingFailureObservation creates an observation for an EVM request
// that failed to unmarshal from JSON.
//
// This observation:
// - Captures details about the validation failure (request ID, error message, chain ID)
// - Is used for both reporting metrics and providing context for debugging
//
// Parameters:
// - id: The JSON-RPC request ID associated with the failed request
// - err: The error that occurred during unmarshaling
// - chainID: The EVM chain identifier for which the request was intended
//
// Returns:
// - qosobservations.Observations: A structured observation containing details about the validation failure
func createRequestUnmarshalingFailureObservation(
	_ jsonrpc.ID,
	serviceID protocol.ServiceID,
	chainID string,
	err error,
) *qosobservations.Observations_Evm {
	errorDetails := err.Error()
	return &qosobservations.Observations_Evm{
		Evm: &qosobservations.EVMRequestObservations{
			ServiceId: string(serviceID),
			ChainId:   chainID,
			RequestValidationFailure: &qosobservations.EVMRequestObservations_EvmRequestUnmarshalingFailure{
				EvmRequestUnmarshalingFailure: &qosobservations.EVMRequestUnmarshalingFailure{
					HttpStatusCode:  jsonrpc.HTTPStatusRequestValidationFailureUnmarshalFailure,
					ValidationError: qosobservations.EVMRequestValidationError_EVM_REQUEST_VALIDATION_ERROR_REQUEST_UNMARSHALING_FAILURE,
					ErrorDetails:    &errorDetails,
				},
			},
		},
	}
}

// createHTTPBodyReadFailureObservation creates an observation for cases where
// reading the HTTP request body for an EVM service request has failed.
//
// This observation:
// - Includes the chainID and detailed error information
// - Is useful for diagnosing connectivity or HTTP parsing issues
//
// Parameters:
// - chainID: The EVM chain identifier for which the request was intended
// - err: The error that occurred during HTTP body reading
//
// Returns:
// - qosobservations.Observations: A structured observation containing details about the HTTP read failure
func (erv *evmRequestValidator) createHTTPBodyReadFailureObservation(
	err error,
) *qosobservations.Observations_Evm {
	errorDetails := err.Error()
	return &qosobservations.Observations_Evm{
		Evm: &qosobservations.EVMRequestObservations{
			ChainId:   erv.chainID,
			ServiceId: string(erv.serviceID),
			RequestValidationFailure: &qosobservations.EVMRequestObservations_EvmHttpBodyReadFailure{
				EvmHttpBodyReadFailure: &qosobservations.EVMHTTPBodyReadFailure{
					HttpStatusCode:  jsonrpc.HTTPStatusRequestValidationFailureReadHTTPBodyFailure,
					ValidationError: qosobservations.EVMRequestValidationError_EVM_REQUEST_VALIDATION_ERROR_HTTP_BODY_READ_FAILURE,
					ErrorDetails:    &errorDetails,
				},
			},
		},
	}
}
