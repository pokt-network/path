package solana

import (
	"encoding/json"

	"github.com/pokt-network/poktroll/pkg/polylog"

	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// responseUnmarshallerGetHealth deserializes the provided payload
// into a responseToBlockNumber struct, adding any encountered errors
// to the returned struct.
func responseUnmarshallerGetHealth(logger polylog.Logger, jsonrpcReq jsonrpc.Request, jsonrpcResp jsonrpc.Response) response {
	logger = logger.With("response_processor", "getHealth")

	getHealthResponse := responseToGetHealth{
		Logger:          logger,
		jsonrpcResponse: jsonrpcResp,
	}

	// TODO_MVP(@adshmh): validate a `getHealth` request before sending it out to an endpoint.
	// e.g. If the request contains a params field, it is invalid and should not be sent to any endpoints.
	//
	// There are 2 possible valid responses to a `getHealth` request:
	// 1. A JSONRPC response with `result` field set to "ok".
	// 2. A JSONRPC response with an error field indicating unhealthy status for the endpoint.
	//
	// See the following link for more details:
	// https://solana.com/docs/rpc/http/gethealth
	// The endpoint returned an error: no need to do further processing of the response.
	if jsonrpcResp.IsError() {
		return getHealthResponse
	}

	resultBz, err := jsonrpcResp.GetResultAsBytes()
	// endpoint failed to provide a valid response to `getHealth` request.
	if err != nil {
		logger.Error().Err(err).Msg("❌ Solana endpoint will fail QoS check because JSONRPC response result field is not a byte slice.")
		return getHealthResponse
	}

	var getHealthResult string
	err = json.Unmarshal(resultBz, &getHealthResult)
	if err != nil {
		logger.Error().Err(err).Msg("❌ Solana endpoint will fail QoS check because JSONRPC response result could not be parsed as a string.")
	}

	// Set the string response to `getHealth` request.
	getHealthResponse.HealthResult = getHealthResult
	return getHealthResponse
}

// responseToGetHealth captures the fields expected in a
// response to a `getHealth` request.
type responseToGetHealth struct {
	Logger polylog.Logger

	// Response stores the JSONRPC response parsed from an endpoint's response bytes.
	jsonrpcResponse jsonrpc.Response

	// HealthResult stores the result field of a response to a `getHealth` request.
	HealthResult string
}

// GetObservation returns a Solana Endpoint observation based on an endpoint's response to a `getHealth` request.
// Implements the response interface used by the requestContext struct.
func (r responseToGetHealth) GetObservation() qosobservations.SolanaEndpointObservation {
	return qosobservations.SolanaEndpointObservation{
		// Set the HTTP status code using the JSONRPC Response
		HttpStatusCode: int32(r.jsonrpcResponse.GetRecommendedHTTPStatusCode()),
		ResponseObservation: &qosobservations.SolanaEndpointObservation_GetHealthResponse{
			GetHealthResponse: &qosobservations.SolanaGetHealthResponse{
				Result: r.HealthResult,
			},
		},
	}
}

// TODO_MVP(@adshmh): handle the following scenarios:
//  1. An endpoint returned a malformed, i.e. Not in JSONRPC format, response.
//     The user-facing response should include the request's ID.
//  2. An endpoint returns a JSONRPC response indicating a user error:
//     This should be returned to the user as-is.
//  3. An endpoint returns a valid JSONRPC response to a valid user request:
//     This should be returned to the user as-is.
func (r responseToGetHealth) GetJSONRPCResponse() jsonrpc.Response {
	return r.jsonrpcResponse
}
