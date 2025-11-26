package cosmos

import (
	"io"
	"net/http"
	"strings"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/gateway"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// requestValidator handles validation for all Cosmos service requests
// Coordinates between different protocol validators (JSONRPC, REST)
type requestValidator struct {
	logger        polylog.Logger
	cosmosChainID string
	evmChainID    string // EVM chain ID will be empty if the CosmosSDK service does not support EVM.
	serviceID     protocol.ServiceID
	supportedAPIs map[sharedtypes.RPCType]struct{}
	serviceState  *serviceState
}

// validateHTTPRequest validates an HTTP request and routes to appropriate sub-validator
// Returns (context, true) on success or (errorContext, false) on failure
func (rv *requestValidator) validateHTTPRequest(req *http.Request) (gateway.RequestQoSContext, bool) {
	logger := rv.logger.With(
		"qos", "Cosmos",
		"method", "validateHTTPRequest",
		"path", req.URL.Path,
		"http_method", req.Method,
	)

	// Read the request body.
	// This is necessary to distinguish REST vs. JSONRPC on request with POST HTTP method.
	body, err := io.ReadAll(req.Body)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to parse JSONRPC request")
		// Return a context with a JSONRPC-formatted response, as we cannot detect the request type.
		return rv.createHTTPBodyReadFailureContext(err), false
	}

	// Determine request type and route to appropriate validator
	if isJSONRPCRequest(req.Method, body) {
		logger.Debug().Msg("Routing to JSONRPC validator")

		// Validate the JSONRPC request.
		// Builds and returns a context to handle the request.
		// Uses a specialized context for handling invalid requests.
		return rv.validateJSONRPCRequest(body)
	} else {
		logger.Debug().Msg("Routing to REST validator")

		// Build and returns a request context to handle the REST request.
		// Uses a specialized context for handling invalid requests.
		return rv.validateRESTRequest(req.URL, req.Method, body)
	}
}

// isJSONRPCRequest determines if the incoming HTTP request is a JSONRPC request
// Uses simple heuristics: POST method and specific content.
func isJSONRPCRequest(httpMethod string, httpRequestBody []byte) bool {
	// Stage 1: Non-POST requests are always REST
	if httpMethod != http.MethodPost {
		return false
	}

	// Stage 2: POST requests - check for JSONRPC payload
	if strings.Contains(string(httpRequestBody), "jsonrpc") {
		return true
	}

	// Stage 3: POST without jsonrpc field is REST
	return false
}

// createHTTPBodyReadFailureContext:
// - Creates an error context for HTTP body read failures
// - Used when the HTTP request body cannot be read
func (rv *requestValidator) createHTTPBodyReadFailureContext(err error) gateway.RequestQoSContext {
	// Create the JSON-RPC error response
	response := jsonrpc.NewErrResponseInternalErr(jsonrpc.ID{}, err)

	// Create the observations object with the HTTP body read failure observation
	observations := rv.createHTTPBodyReadFailureObservation(err, response)

	// Build and return the error context
	return &qos.RequestErrorContext{
		Logger:   rv.logger,
		Response: response,
		Observations: &qosobservations.Observations{
			ServiceObservations: observations,
		},
	}
}

// createHTTPBodyReadFailureObservation creates an observation for cases where
// reading the HTTP request body for a Cosmos service request has failed.
func (rv *requestValidator) createHTTPBodyReadFailureObservation(
	err error,
	jsonrpcResponse jsonrpc.Response,
) *qosobservations.Observations_Cosmos {
	return &qosobservations.Observations_Cosmos{
		Cosmos: &qosobservations.CosmosRequestObservations{
			CosmosChainId: rv.cosmosChainID,
			EvmChainId:    rv.evmChainID,
			ServiceId:     string(rv.serviceID),
			RequestLevelError: &qosobservations.RequestError{
				ErrorKind:      qosobservations.RequestErrorKind_REQUEST_ERROR_INTERNAL_READ_HTTP_ERROR,
				ErrorDetails:   err.Error(),
				HttpStatusCode: int32(jsonrpcResponse.GetRecommendedHTTPStatusCode()),
			},
		},
	}
}
