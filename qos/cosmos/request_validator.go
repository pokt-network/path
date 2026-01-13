package cosmos

import (
	"fmt"
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

// maxRequestBodySize is the maximum allowed size for HTTP request bodies (100MB).
// This prevents OOM attacks from unbounded io.ReadAll calls.
const maxRequestBodySize = 100 * 1024 * 1024

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
//
// Fallback logic for RPC type detection (Cosmos):
//  1. If detectedRPCType != UNKNOWN_RPC, use it (gateway already detected via header)
//  2. Else, check path (for REST vs COMET_BFT distinction)
//  3. Else, check payload (for JSONRPC vs REST)
//  4. Else, default to JSON_RPC
func (rv *requestValidator) validateHTTPRequest(req *http.Request, detectedRPCType sharedtypes.RPCType) (gateway.RequestQoSContext, bool) {
	logger := rv.logger.With(
		"qos", "Cosmos",
		"method", "validateHTTPRequest",
		"path", req.URL.Path,
		"http_method", req.Method,
		"detected_rpc_type", detectedRPCType.String(),
	)

	// Read the request body with a size limit to prevent OOM attacks.
	// This is necessary to distinguish REST vs. JSONRPC on request with POST HTTP method.
	limitedBody := http.MaxBytesReader(nil, req.Body, maxRequestBodySize)
	body, err := io.ReadAll(limitedBody)
	if err != nil {
		// Check if the error is due to body size limit exceeded
		if err.Error() == "http: request body too large" {
			err = fmt.Errorf("request body exceeds %d bytes limit", maxRequestBodySize)
		}
		logger.Error().Err(err).Msg("Failed to read request body")
		// Return a context with a JSONRPC-formatted response, as we cannot detect the request type.
		return rv.createHTTPBodyReadFailureContext(err), false
	}

	// Step 1: If gateway detected the RPC type, use it
	if detectedRPCType != sharedtypes.RPCType_UNKNOWN_RPC {
		logger.Debug().Msg("Using gateway-detected RPC type")

		// Route based on detected type
		switch detectedRPCType {
		case sharedtypes.RPCType_JSON_RPC:
			logger.Debug().Msg("Routing to JSONRPC validator (gateway-detected)")
			return rv.validateJSONRPCRequest(body)
		case sharedtypes.RPCType_REST, sharedtypes.RPCType_COMET_BFT:
			logger.Debug().Msg("Routing to REST validator (gateway-detected)")
			return rv.validateRESTRequest(req.URL, req.Method, body)
		default:
			// Unexpected RPC type for Cosmos - log and fall through to detection
			logger.Warn().Msgf("Unexpected RPC type %s for Cosmos, falling back to detection", detectedRPCType.String())
		}
	}

	// Step 2-3: Gateway couldn't detect (UNKNOWN_RPC) or unexpected type - use existing detection logic
	// Determine request type and route to appropriate validator
	if isJSONRPCRequest(req.Method, body) {
		logger.Debug().Msg("Routing to JSONRPC validator (payload-detected)")

		// Validate the JSONRPC request.
		// Builds and returns a context to handle the request.
		// Uses a specialized context for handling invalid requests.
		return rv.validateJSONRPCRequest(body)
	} else {
		logger.Debug().Msg("Routing to REST validator (payload-detected)")

		// Build and returns a request context to handle the REST request.
		// Uses a specialized context for handling invalid requests.
		// This will call determineRESTRPCType to distinguish between REST and COMET_BFT (path-based detection)
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
