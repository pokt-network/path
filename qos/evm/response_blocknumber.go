package evm

import (
	"encoding/json"

	"github.com/pokt-network/poktroll/pkg/polylog"

	qosobservations "github.com/buildwithgrove/path/observation/qos"
	"github.com/buildwithgrove/path/qos/jsonrpc"
)

// responseToBlockNumber provides the functionality required from a response by a requestContext instance.
var _ response = responseToBlockNumber{}

// responseUnmarshallerBlockNumber deserializes the provided payload
// into a responseToBlockNumber struct, adding any encountered errors
// to the returned struct.
func responseUnmarshallerBlockNumber(
	logger polylog.Logger,
	_ jsonrpc.Request,
	jsonrpcResp jsonrpc.Response,
) (response, error) {
	// The endpoint returned an error: no need to do further processing of the response.
	if jsonrpcResp.IsError() {
		// TODO_TECHDEBT(@adshmh): validate the `eth_blockNumber`
		// request that was sent to the endpoint.
		return responseToBlockNumber{
			logger:          logger,
			jsonrpcResponse: jsonrpcResp,
			validationError: nil, // Intentionally set to nil to indicate a valid JSONRPC error response.
		}, nil
	}

	resultBz, err := jsonrpcResp.GetResultAsBytes()
	if err != nil {
		validationError := qosobservations.EVMResponseValidationError_EVM_RESPONSE_VALIDATION_ERROR_UNMARSHAL
		return responseToBlockNumber{
			logger:          logger,
			jsonrpcResponse: jsonrpcResp,
			validationError: &validationError,
		}, err
	}

	var (
		result          string
		validationError *qosobservations.EVMResponseValidationError
	)

	// TODO_MVP(@adshmh): use the contents of the result field to determine the validity of the response.
	// e.g. a response that fails parsing as a number is not valid.
	err = json.Unmarshal(resultBz, &result)
	if err != nil {
		errValue := qosobservations.EVMResponseValidationError_EVM_RESPONSE_VALIDATION_ERROR_UNMARSHAL
		validationError = &errValue
	}

	return responseToBlockNumber{
		logger:          logger,
		jsonrpcResponse: jsonrpcResp,
		result:          result,
		validationError: validationError,
	}, err
}

// responseToBlockNumber captures the fields expected in a
// response to an `eth_blockNumber` request.
type responseToBlockNumber struct {
	logger polylog.Logger

	// jsonrpcResponse stores the JSONRPC response parsed from an endpoint's response bytes.
	jsonrpcResponse jsonrpc.Response

	// result stores the result field of a response to a `eth_blockNumber` request.
	result string

	// Why the response has failed validation.
	// Only set if the response is invalid.
	// As of PR #152, a response is valid if either of the following holds:
	//	- It is a valid JSONRPC error response
	//	- It is a valid JSONRPC response with any string value in `result` field.
	// Used when generating observations.
	validationError *qosobservations.EVMResponseValidationError
}

// GetObservation returns an observation using an `eth_blockNumber` request's response.
// Implements the response interface.
// Reference: https://ethereum.org/en/developers/docs/apis/json-rpc/#eth_blocknumber
func (r responseToBlockNumber) GetObservation() qosobservations.EVMEndpointObservation {
	return qosobservations.EVMEndpointObservation{
		ParsedJsonrpcResponse: r.jsonrpcResponse.GetObservation(),
		ResponseObservation: &qosobservations.EVMEndpointObservation_BlockNumberResponse{
			BlockNumberResponse: &qosobservations.EVMBlockNumberResponse{
				HttpStatusCode:          int32(200),
				BlockNumberResponse:     r.result,
				ResponseValidationError: r.validationError,
			},
		},
	}
}

// GetHTTPResponse builds and returns the httpResponse matching the responseToBlockNumber instance.
// Implements the response interface.
func (r responseToBlockNumber) GetHTTPResponse() jsonrpc.HTTPResponse {
	return jsonrpc.HTTPResponse{
		ResponsePayload: r.getResponsePayload(),
		HTTPStatusCode:  200,
	}
}

// getResponsePayload returns the raw byte slice payload to be returned as the response to the JSONRPC request.
func (r responseToBlockNumber) getResponsePayload() []byte {
	// TODO_MVP(@adshmh): return a JSONRPC response indicating the error if unmarshaling failed.
	bz, err := json.Marshal(r.jsonrpcResponse)
	if err != nil {
		// This should never happen: log an entry but return the response anyway.
		r.logger.Warn().Err(err).Msg("responseToBlockNumber: Marshaling JSONRPC response failed.")
	}
	return bz
}

// GetJSONRPCID returns the JSONRPC ID of the response.
// Implements the response interface.
func (r responseToBlockNumber) GetJSONRPCID() jsonrpc.ID {
	return r.jsonrpcResponse.ID
}
