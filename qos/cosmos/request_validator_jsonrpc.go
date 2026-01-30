package cosmos

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	gateway "github.com/pokt-network/path/gateway"
	pathhttp "github.com/pokt-network/path/network/http"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos"
	"github.com/pokt-network/path/qos/jsonrpc"
)

const (
	// maximum length of the error message stored in request validation failure observations and logs.
	maxErrMessageLen = 1000
)

// validateJSONRPCRequest validates a JSONRPC request by:
// 1. Reading and parsing the JSONRPC request
// 2. Determining the specific RPC type from the method
// 3. Checking if the RPC type is supported
// 4. Creating the request context with all necessary information
func (rv *requestValidator) validateJSONRPCRequest(
	body []byte,
) (gateway.RequestQoSContext, bool) {

	logger := rv.logger.With("validator", "JSONRPC")

	// Parse and validate the JSONRPC request(s) - handles both single and batch requests
	jsonrpcReqs, isBatch, err := jsonrpc.ParseJSONRPCFromRequestBody(logger, body)
	if err != nil {
		// If no requests parsed or empty ID, requestID will be zero value (empty)
		return rv.createJSONRPCParseFailureContext(err), false
	}

	// Build and return the request context
	return rv.buildJSONRPCRequestContext(
		jsonrpcReqs,
		isBatch,
		qosobservations.RequestOrigin_REQUEST_ORIGIN_ORGANIC,
	)
}

func (rv *requestValidator) buildJSONRPCServicePayloadsFromRequests(
	jsonrpcReqs map[jsonrpc.ID]jsonrpc.Request,
) (map[jsonrpc.ID]protocol.Payload, error) {
	servicePayloads := make(map[jsonrpc.ID]protocol.Payload)

	for reqID, req := range jsonrpcReqs {
		method := string(req.Method)
		rpcType := detectJSONRPCServiceType(method)

		// Hydrate the logger with data extracted from the request.
		logger := rv.logger.With(
			"rpc_type", rpcType.String(),
			"jsonrpc_method", method,
		)

		// Check if this RPC type is supported by the service
		if _, supported := rv.supportedAPIs[rpcType]; !supported {
			logger.Warn().Msg("Request uses unsupported RPC type")
			return servicePayloads, errors.New("request uses unsupported RPC type")
		}

		servicePayload, err := buildJSONRPCServicePayload(rpcType, req)
		if err != nil {
			return servicePayloads, err
		}
		servicePayloads[reqID] = servicePayload
	}

	return servicePayloads, nil
}

func (rv *requestValidator) buildJSONRPCRequestContext(
	jsonrpcReqs map[jsonrpc.ID]jsonrpc.Request,
	isBatch bool,
	requestOrigin qosobservations.RequestOrigin,
) (gateway.RequestQoSContext, bool) {
	logger := rv.logger.With(
		"method", "buildJSONRPCRequestContext",
	)

	// Build service payloads
	servicePayloads, err := rv.buildJSONRPCServicePayloadsFromRequests(jsonrpcReqs)
	if err != nil {
		logger.Warn().Err(err).Msg("SHOULD NEVER HAPPEN: failed to marshal JSONRPC service payload")
		// For batch failure, use helper to get appropriate ID for error response
		errorID := getJsonRpcIDForErrorResponse(jsonrpcReqs)
		return rv.createJSONRPCServicePayloadBuildFailureContext(errorID, err), false
	}

	// Generate the QoS observation for the request.
	// requestContext will amend this with endpoint observation(s).
	requestObservation := rv.buildJSONRPCRequestObservations(
		jsonrpcReqs,
		requestOrigin,
	)

	// Create specialized JSONRPC context
	return &requestContext{
		logger:                       logger,
		serviceState:                 rv.serviceState,
		servicePayloads:              servicePayloads,
		isBatch:                      isBatch,
		observations:                 requestObservation,
		protocolErrorResponseBuilder: buildJSONRPCProtocolErrorResponse(getJsonRpcIDForErrorResponse(jsonrpcReqs)),
		// Protocol-level request error observation is the same for JSONRPC and REST.
		protocolErrorObservationBuilder: buildProtocolErrorObservation,
	}, true
}

// buildJSONRPCServicePayload builds a protocol payload for a JSONRPC request.
func buildJSONRPCServicePayload(rpcType sharedtypes.RPCType, jsonrpcReq jsonrpc.Request) (protocol.Payload, error) {
	// DEV_NOTE: marshaling the request, rather than using the original payload, is necessary.
	// Otherwise, a request missing `id` field could fail.
	// See the Request struct in `jsonrpc` package for the details.
	reqBz, err := json.Marshal(jsonrpcReq)
	if err != nil {
		return protocol.EmptyErrorPayload(), err
	}

	return protocol.Payload{
		Data:    string(reqBz),
		Method:  http.MethodPost, // JSONRPC always uses POST
		Path:    "",              // JSONRPC does not use paths
		Headers: map[string]string{},
		RPCType: rpcType, // Add the RPCType hint the so protocol sets correct HTTP headers for the endpoint.
	}, nil
}

func getJSONRPCRequestEndpointResponseValidator(
	jsonrpcReqs map[jsonrpc.ID]jsonrpc.Request,
) func(polylog.Logger, []byte, string) response {

	// Delegate the unmarshaling/validation of endpoint response to the specialized JSONRPC unmarshaler.
	// The requestID parameter is used for batch request error handling.
	return func(logger polylog.Logger, endpointResponseBz []byte, requestID string) response {
		return unmarshalJSONRPCRequestEndpointResponse(logger, jsonrpcReqs, endpointResponseBz, requestID)
	}
}

func buildJSONRPCProtocolErrorResponse(
	jsonrpcRequestID jsonrpc.ID,
) func(logger polylog.Logger) pathhttp.HTTPResponse {
	return func(logger polylog.Logger) pathhttp.HTTPResponse {
		errorResp := jsonrpc.NewErrResponseInternalErr(
			jsonrpcRequestID,
			errors.New("protocol-level error: no endpoint responses received"),
		)
		return qos.BuildHTTPResponseFromJSONRPCResponse(logger, errorResp)
	}
}

func (rv *requestValidator) buildJSONRPCRequestObservations(
	jsonrpcReqs map[jsonrpc.ID]jsonrpc.Request,
	requestOrigin qosobservations.RequestOrigin,
) *qosobservations.CosmosRequestObservations {
	// Build request profiles for each JSON-RPC request
	var requestProfiles []*qosobservations.CosmosRequestProfile

	for _, jsonrpcReq := range jsonrpcReqs {
		method := string(jsonrpcReq.Method)
		rpcType := detectJSONRPCServiceType(method)

		requestProfile := &qosobservations.CosmosRequestProfile{
			BackendServiceDetails: &qosobservations.BackendServiceDetails{
				BackendServiceType: convertToProtoBackendServiceType(rpcType),
				SelectionReason:    "JSONRPC method detection",
			},
			ParsedRequest: &qosobservations.CosmosRequestProfile_JsonrpcRequest{
				JsonrpcRequest: jsonrpcReq.GetObservation(),
			},
		}
		requestProfiles = append(requestProfiles, requestProfile)
	}

	return &qosobservations.CosmosRequestObservations{
		CosmosChainId:   rv.cosmosChainID,
		EvmChainId:      rv.evmChainID,
		ServiceId:       string(rv.serviceID),
		RequestOrigin:   requestOrigin,
		RequestProfiles: requestProfiles,
	}
}

// Used for both JSONRPC and REST requests.
func buildProtocolErrorObservation() *qosobservations.RequestError {
	return &qosobservations.RequestError{
		ErrorKind:      qosobservations.RequestErrorKind_REQUEST_ERROR_INTERNAL_PROTOCOL_ERROR,
		ErrorDetails:   "No endpoint responses received",
		HttpStatusCode: int32(http.StatusInternalServerError),
	}
}

// convertToProtoBackendServiceType converts sharedtypes.RPCType to proto BackendServiceType
func convertToProtoBackendServiceType(rpcType sharedtypes.RPCType) qosobservations.BackendServiceType {
	switch rpcType {
	case sharedtypes.RPCType_JSON_RPC:
		return qosobservations.BackendServiceType_BACKEND_SERVICE_TYPE_JSONRPC
	case sharedtypes.RPCType_REST:
		return qosobservations.BackendServiceType_BACKEND_SERVICE_TYPE_REST
	case sharedtypes.RPCType_COMET_BFT:
		return qosobservations.BackendServiceType_BACKEND_SERVICE_TYPE_COMETBFT
	default:
		return qosobservations.BackendServiceType_BACKEND_SERVICE_TYPE_UNSPECIFIED
	}
}

// createJSONRPCParseFailureContext creates an error context for JSONRPC parsing failures
func (rv *requestValidator) createJSONRPCParseFailureContext(err error) gateway.RequestQoSContext {
	// Create the JSON-RPC error response with empty ID since we couldn't parse the request
	response := jsonrpc.NewErrResponseInvalidRequest(jsonrpc.ID{}, err)

	// Create the observations object with the parse failure observation
	observations := rv.createJSONRPCParseFailureObservation(err, response)

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

func (rv *requestValidator) createJSONRPCParseFailureObservation(
	err error,
	jsonrpcResponse jsonrpc.Response,
) *qosobservations.CosmosRequestObservations {
	return &qosobservations.CosmosRequestObservations{
		CosmosChainId: rv.cosmosChainID,
		EvmChainId:    rv.evmChainID,
		ServiceId:     string(rv.serviceID),
		RequestOrigin: qosobservations.RequestOrigin_REQUEST_ORIGIN_ORGANIC,
		RequestLevelError: &qosobservations.RequestError{
			ErrorKind:      qosobservations.RequestErrorKind_REQUEST_ERROR_USER_ERROR_JSONRPC_PARSE_ERROR,
			ErrorDetails:   truncateErrorMessage(err.Error()),
			HttpStatusCode: int32(jsonrpcResponse.GetRecommendedHTTPStatusCode()),
		},
	}
}

// createJSONRPCServicePayloadBuildFailureContext creates an error context for service payload build failures
func (rv *requestValidator) createJSONRPCServicePayloadBuildFailureContext(jsonrpcID jsonrpc.ID, err error) gateway.RequestQoSContext {
	// Create the JSON-RPC error response
	response := jsonrpc.NewErrResponseInternalErr(jsonrpcID, err)

	// Create the observations object with the payload build failure observation
	observations := rv.createJSONRPCServicePayloadBuildFailureObservation(jsonrpcID, err, response)

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

func (rv *requestValidator) createJSONRPCServicePayloadBuildFailureObservation(
	jsonrpcID jsonrpc.ID,
	err error,
	jsonrpcResponse jsonrpc.Response,
) *qosobservations.CosmosRequestObservations {
	return &qosobservations.CosmosRequestObservations{
		CosmosChainId: rv.cosmosChainID,
		EvmChainId:    rv.evmChainID,
		ServiceId:     string(rv.serviceID),
		RequestOrigin: qosobservations.RequestOrigin_REQUEST_ORIGIN_ORGANIC,
		RequestLevelError: &qosobservations.RequestError{
			ErrorKind:      qosobservations.RequestErrorKind_REQUEST_ERROR_INTERNAL_JSONRPC_PAYLOAD_BUILD_ERROR,
			ErrorDetails:   truncateErrorMessage(err.Error()),
			HttpStatusCode: int32(jsonrpcResponse.GetRecommendedHTTPStatusCode()),
		},
	}
}

// truncateErrorMessage truncates error message to maxErrMessageLen
func truncateErrorMessage(errMsg string) string {
	if len(errMsg) <= maxErrMessageLen {
		return errMsg
	}
	return errMsg[:maxErrMessageLen]
}
