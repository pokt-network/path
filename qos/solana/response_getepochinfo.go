package solana

import (
	"encoding/json"

	"github.com/pokt-network/poktroll/pkg/polylog"

	qosobservations "github.com/buildwithgrove/path/observation/qos"
	"github.com/buildwithgrove/path/qos/jsonrpc"
)

// responseUnmarshallerGetEpochInfo deserializes the provided payload into a responseToGetEpochInfo struct,
// adding any encountered errors to the returned struct.
func responseUnmarshallerGetEpochInfo(
	logger polylog.Logger,
	jsonrpcReq jsonrpc.Request,
	jsonrpcResp jsonrpc.Response,
) response {
	logger = logger.With("response_processor", "getEpochInfo")

	getEpochInfoResponse := responseToGetEpochInfo{
		Logger:          logger,
		jsonrpcResponse: jsonrpcResp,
	}

	// The endpoint returned an error: no need to do further processing of the response.
	if jsonrpcResp.IsError() {
		// Note: this assumes the `getEpochInfo` request sent to the endpoint was valid.
		return getEpochInfoResponse
	}

	resultBz, err := jsonrpcResp.GetResultAsBytes()
	if err != nil {
		logger.Error().Err(err).Msg("❌ Solana endpoint will fail QoS check because JSONRPC response result field is not a byte slice.")
		return getEpochInfoResponse
	}

	var epochInfo epochInfo
	err = json.Unmarshal(resultBz, &epochInfo)
	if err != nil {
		logger.Error().Err(err).Msg("❌ Solana endpoint will fail QoS check because JSONRPC response result failed to parse.")
		return getEpochInfoResponse
	}

	getEpochInfoResponse.epochInfo = epochInfo
	return getEpochInfoResponse
}

// epochInfo captures all the fields expected from a response to a `getEpochInfo` request.
type epochInfo struct {
	AbsoluteSlot     uint64 `json:"absoluteSlot"`
	BlockHeight      uint64 `json:"blockHeight"`
	Epoch            uint64 `json:"epoch"`
	SlotIndex        uint64 `json:"slotIndex"`
	SlotsInEpoch     uint64 `json:"slotsInEpoch"`
	TransactionCount uint64 `json:"transactionCount"`
}

// responseToGetEpochInfo captures the fields expected in a
// response to a `getEpochInfo` request.
type responseToGetEpochInfo struct {
	Logger polylog.Logger

	// Response stores the JSONRPC response parsed from an endpoint's response bytes.
	jsonrpcResponse jsonrpc.Response

	// epochInfo stores the epochInfo struct that is parsed from the response to a `getEpochInfo` request.
	epochInfo epochInfo
}

// GetObservation returns a Solana Endpoint observation based on an endpoint's response to a `getEpochInfo` request.
// Implements the response interface used by the requestContext struct.
func (r responseToGetEpochInfo) GetObservation() qosobservations.SolanaEndpointObservation {
	return qosobservations.SolanaEndpointObservation{
		// Set the HTTP status code using the JSONRPC Response
		HttpStatusCode: int32(200),
		ResponseObservation: &qosobservations.SolanaEndpointObservation_GetEpochInfoResponse{
			GetEpochInfoResponse: &qosobservations.SolanaGetEpochInfoResponse{
				BlockHeight: r.epochInfo.BlockHeight,
				Epoch:       r.epochInfo.Epoch,
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
func (r responseToGetEpochInfo) GetJSONRPCResponse() jsonrpc.Response {
	return r.jsonrpcResponse
}
