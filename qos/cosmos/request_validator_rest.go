package cosmos

import (
	"errors"
	"net/url"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	gateway "github.com/buildwithgrove/path/gateway"
	pathhttp "github.com/buildwithgrove/path/network/http"
	qosobservations "github.com/buildwithgrove/path/observation/qos"
	"github.com/buildwithgrove/path/protocol"
	"github.com/buildwithgrove/path/qos"
	"github.com/buildwithgrove/path/qos/jsonrpc"
)

// restRequestID is a dummy string value used as the request ID for REST requests.
// REST requests do not support batching, so we use a hardcoded string ID to match
// the the response to the request in the servicePayloads map.
const restRequestID = "rest_request"

// validateRESTRequest validates a REST request by:
// 1. Validating HTTP method and path
// 2. Determining the specific RPC type from the path
// 3. Checking if the RPC type is supported
// 4. Creating the request context with all necessary information
func (rv *requestValidator) validateRESTRequest(
	httpRequestURL *url.URL,
	httpRequestMethod string,
	httpRequestBody []byte,
) (gateway.RequestQoSContext, bool) {
	httpRequestPath := httpRequestURL.Path

	logger := rv.logger.With("validator", "REST")

	// Determine the specific RPC type based on path patterns - use existing function
	rpcType := determineRESTRPCType(httpRequestPath)

	logger = logger.With(
		"rpc_type", rpcType.String(),
		"request_path", httpRequestPath,
	)

	// Check if this RPC type is supported by the service
	if _, supported := rv.supportedAPIs[rpcType]; !supported {
		logger.Warn().Msg("Request uses unsupported RPC type")
		// TODO_TECHDEBT(@adshmh): Review error context creation for REST requests.
		return rv.createRESTUnsupportedRPCTypeContext(httpRequestURL, httpRequestMethod, httpRequestBody, rpcType), false
	}

	logger.Debug().
		Int("body_length", len(httpRequestBody)).
		Msg("REST request validation successful")

	// Build and return the request context
	return rv.buildRESTRequestContext(
		rpcType,
		httpRequestURL,
		httpRequestMethod,
		httpRequestBody,
		qosobservations.RequestOrigin_REQUEST_ORIGIN_ORGANIC,
	)
}

// TODO_TECHDEBT(@adshmh): Validate the REST payload based on HTTP request's path.
func (rv *requestValidator) buildRESTRequestContext(
	rpcType sharedtypes.RPCType,
	httpRequestURL *url.URL,
	httpRequestMethod string,
	httpRequestBody []byte,
	requestOrigin qosobservations.RequestOrigin,
) (gateway.RequestQoSContext, bool) {
	logger := rv.logger.With(
		"method", "buildRESTRequestContext",
	)

	// Build service payload
	servicePayload := buildRESTServicePayload(
		rpcType,
		httpRequestURL,
		httpRequestMethod,
		httpRequestBody,
	)

	// Generate the QoS observation for the request.
	// requestContext will amend this with endpoint observation(s).
	requestObservation := rv.buildRESTRequestObservations(
		rpcType,
		httpRequestURL,
		httpRequestMethod,
		httpRequestBody,
		servicePayload,
		requestOrigin,
	)

	// Hydrate the logger with REST request details.
	logger = logger.With(
		"rest_backend_service", rpcType,
		"payload_length", len(servicePayload.Data),
		"rest_request_path", servicePayload.Path,
		"rest_request_method", servicePayload.Method,
	)

	logger.Debug().Msg("REST request validation successful.")

	// Create specialized REST context
	return &requestContext{
		logger:       logger,
		serviceState: rv.serviceState,
		servicePayloads: map[jsonrpc.ID]protocol.Payload{
			jsonrpc.IDFromStr(restRequestID): servicePayload,
		},
		observations:                 requestObservation,
		endpointResponseValidator:    getRESTRequestEndpointResponseValidator(httpRequestURL.Path),
		protocolErrorResponseBuilder: buildRESTProtocolErrorResponse(),
		// Protocol-level request error observation is the same for JSONRPC and REST.
		protocolErrorObservationBuilder: buildProtocolErrorObservation,
	}, true
}

func buildRESTServicePayload(
	rpcType sharedtypes.RPCType,
	httpRequestURL *url.URL,
	httpRequestMethod string,
	httpRequestBody []byte,
) protocol.Payload {
	path := httpRequestURL.Path
	if httpRequestURL.RawQuery != "" {
		path += "?" + httpRequestURL.RawQuery
	}

	return protocol.Payload{
		Data:    string(httpRequestBody),
		Method:  httpRequestMethod,
		Path:    path,
		Headers: map[string]string{},
		RPCType: rpcType, // Add the RPCType hint, so protocol sets correct HTTP headers for the endpoint.
	}
}

func getRESTRequestEndpointResponseValidator(requestPath string) func(polylog.Logger, []byte) response {
	// Delegate the unmarshaling/validation of endpoint response to the specialized REST unmarshaler.
	return func(logger polylog.Logger, endpointResponseBz []byte) response {
		return unmarshalRESTRequestEndpointResponse(logger, requestPath, endpointResponseBz)
	}
}

func (rv *requestValidator) buildRESTRequestObservations(
	rpcType sharedtypes.RPCType,
	httpRequestURL *url.URL,
	httpRequestMethod string,
	httpRequestBody []byte,
	servicePayload protocol.Payload,
	requestOrigin qosobservations.RequestOrigin,
) *qosobservations.CosmosRequestObservations {
	// Determine content type from headers if available, otherwise empty
	contentType := ""
	// Note: We don't have access to headers here, but this would be where we'd extract it

	return &qosobservations.CosmosRequestObservations{
		CosmosChainId: rv.cosmosChainID,
		EvmChainId:    rv.evmChainID,
		ServiceId:     string(rv.serviceID),
		RequestOrigin: requestOrigin,
		RequestProfiles: []*qosobservations.CosmosRequestProfile{
			{
				BackendServiceDetails: &qosobservations.BackendServiceDetails{
					BackendServiceType: convertToProtoBackendServiceType(rpcType),
					SelectionReason:    "REST path detection",
				},
				ParsedRequest: &qosobservations.CosmosRequestProfile_RestRequest{
					RestRequest: &qosobservations.RESTRequest{
						ApiPath:       httpRequestURL.Path,
						HttpMethod:    httpRequestMethod,
						ContentType:   contentType,
						PayloadLength: uint32(len(httpRequestBody)),
					},
				},
			},
		},
	}
}

// TODO_TECHDEBT(@adshmh): Review the expected user experience on protocol errors in REST requests.
func buildRESTProtocolErrorResponse() func(logger polylog.Logger) pathhttp.HTTPResponse {
	return func(logger polylog.Logger) pathhttp.HTTPResponse {
		// For REST requests, we return a JSON-RPC error response with null ID
		// TODO_TECHDEBT(@adshmh): Consider returning proper REST error response format
		errorResp := jsonrpc.NewErrResponseInternalErr(
			jsonrpc.ID{}, // use null as ID.
			errors.New("protocol-level error: no endpoint responses received"),
		)
		return qos.BuildHTTPResponseFromJSONRPCResponse(logger, errorResp)
	}
}

// TODO_TECHDEBT(@adshmh): Review error context creation for REST requests.
// createRESTUnsupportedRPCTypeContext creates an error context for unsupported RPC type in REST requests
func (rv *requestValidator) createRESTUnsupportedRPCTypeContext(
	httpRequestURL *url.URL,
	httpRequestMethod string,
	httpRequestBody []byte,
	rpcType sharedtypes.RPCType,
) gateway.RequestQoSContext {
	// Create the JSON-RPC error response (reusing JSONRPC error format for now)
	err := errors.New("unsupported RPC type: " + rpcType.String())
	response := jsonrpc.NewErrResponseInvalidRequest(jsonrpc.ID{}, err)

	// Create the observations object with the unsupported RPC type observation
	observations := rv.createRESTUnsupportedRPCTypeObservation(
		httpRequestURL,
		httpRequestMethod,
		httpRequestBody,
		rpcType,
		response,
	)

	// Build and return the error context
	return &qos.RequestErrorContext{
		Logger:   rv.logger,
		Response: response,
		Observations: &qosobservations.Observations{
			ServiceObservations: &qosobservations.Observations_Cosmos{
				Cosmos: observations,
			},
		},
	}
}

func (rv *requestValidator) createRESTUnsupportedRPCTypeObservation(
	httpRequestURL *url.URL,
	httpRequestMethod string,
	httpRequestBody []byte,
	rpcType sharedtypes.RPCType,
	jsonrpcResponse jsonrpc.Response,
) *qosobservations.CosmosRequestObservations {
	return &qosobservations.CosmosRequestObservations{
		CosmosChainId: rv.cosmosChainID,
		EvmChainId:    rv.evmChainID,
		ServiceId:     string(rv.serviceID),
		RequestOrigin: qosobservations.RequestOrigin_REQUEST_ORIGIN_ORGANIC,
		RequestProfiles: []*qosobservations.CosmosRequestProfile{
			{
				BackendServiceDetails: &qosobservations.BackendServiceDetails{
					BackendServiceType: convertToProtoBackendServiceType(rpcType),
					SelectionReason:    "REST path detection (unsupported)",
				},
				ParsedRequest: &qosobservations.CosmosRequestProfile_RestRequest{
					RestRequest: &qosobservations.RESTRequest{
						ApiPath:       httpRequestURL.Path,
						HttpMethod:    httpRequestMethod,
						ContentType:   "", // Not available at this point
						PayloadLength: uint32(len(httpRequestBody)),
					},
				},
			},
		},
		RequestLevelError: &qosobservations.RequestError{
			ErrorKind:      qosobservations.RequestErrorKind_REQUEST_ERROR_USER_ERROR_REST_UNSUPPORTED_RPC_TYPE,
			ErrorDetails:   "Unsupported RPC type: " + rpcType.String(),
			HttpStatusCode: int32(500),
		},
	}
}
