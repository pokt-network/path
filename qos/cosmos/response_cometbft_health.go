package cosmos

import (
	"github.com/pokt-network/poktroll/pkg/polylog"

	pathhttp "github.com/pokt-network/path/network/http"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/qos"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// responseValidatorCometBFTHealth implements jsonrpcResponseValidator for /health endpoint
// Takes a parsed JSONRPC response and validates it as a health response
func responseValidatorCometBFTHealth(logger polylog.Logger, jsonrpcResponse jsonrpc.Response) response {
	logger = logger.With("response_validator", "health")

	// The endpoint returned an error: no need to do further processing of the response
	if jsonrpcResponse.IsError() {
		logger.Warn().
			Str("jsonrpc_error", jsonrpcResponse.Error.Message).
			Int("jsonrpc_error_code", jsonrpcResponse.Error.Code).
			Msg("Endpoint returned JSON-RPC error for /health request")

		return &responseCometBFTHealth{
			logger:              logger,
			userJSONRPCResponse: jsonrpcResponse,
			healthy:             false, // Error means unhealthy
		}
	}

	// Any valid, non-error JSONRPC response to `/health` is accepted as valid.
	// Reference: https://docs.cometbft.com/main/spec/rpc/#health
	logger.Debug().Msg("Successfully validated /health response")

	return &responseCometBFTHealth{
		logger:              logger,
		userJSONRPCResponse: jsonrpcResponse,
		healthy:             true,
	}
}

// responseCometBFTHealth captures a Cosmos JSONRPC /health endpoint response
// Reference: https://docs.cometbft.com/v1.0/spec/rpc/#health
type responseCometBFTHealth struct {
	logger polylog.Logger

	// healthy indicates if the endpoint is healthy
	healthy bool

	// tracks the JSONRPC response to return to the user.
	userJSONRPCResponse jsonrpc.Response
}

// GetObservation returns a Cosmos-based /health observation
// Implements the response interface
func (r *responseCometBFTHealth) GetObservation() qosobservations.CosmosEndpointObservation {
	return qosobservations.CosmosEndpointObservation{
		EndpointResponseValidationResult: &qosobservations.CosmosEndpointResponseValidationResult{
			ResponseValidationType: qosobservations.CosmosResponseValidationType_COSMOS_RESPONSE_VALIDATION_TYPE_JSONRPC,
			HttpStatusCode:         int32(r.userJSONRPCResponse.GetRecommendedHTTPStatusCode()),
			ValidationError:        nil, // No validation error for successfully processed responses
			UserJsonrpcResponse:    r.userJSONRPCResponse.GetObservation(),
			ParsedResponse: &qosobservations.CosmosEndpointResponseValidationResult_ResponseCometBftHealth{
				ResponseCometBftHealth: &qosobservations.CosmosResponseCometBFTHealth{
					HealthStatus: r.healthy,
				},
			},
		},
	}
}

// GetHTTPResponse builds and returns the HTTP response
// Implements the response interface
func (r *responseCometBFTHealth) GetHTTPResponse() pathhttp.HTTPResponse {
	return qos.BuildHTTPResponseFromJSONRPCResponse(r.logger, r.userJSONRPCResponse)
}
