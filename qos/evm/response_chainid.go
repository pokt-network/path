package evm

import (
	"encoding/json"

	"github.com/pokt-network/poktroll/pkg/polylog"

	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// responseToChainID provides the functionality required from a response by a requestContext instance.
var _ response = responseToChainID{}

// TODO_TECHDEBT(@adshmh): consider refactoring all unmarshallers to remove any duplicated logic.

// responseUnmarshallerChainID deserializes the provided byte slice into a responseToChainID struct,
// adding any encountered errors to the returned struct for constructing a response payload.
func responseUnmarshallerChainID(
	logger polylog.Logger,
	_ jsonrpc.Request,
	jsonrpcResp jsonrpc.Response,
) (response, error) {
	// The endpoint returned an error: no need to do further processing of the response.
	if jsonrpcResp.IsError() {
		return responseToChainID{
			logger:          logger,
			jsonrpcResponse: jsonrpcResp,
			validationError: nil, // Intentionally set to nil to indicate a valid JSONRPC error response.
		}, nil
	}

	resultBz, err := jsonrpcResp.GetResultAsBytes()
	if err != nil {
		validationError := qosobservations.EVMResponseValidationError_EVM_RESPONSE_VALIDATION_ERROR_UNMARSHAL
		return responseToChainID{
			logger:          logger,
			jsonrpcResponse: jsonrpcResp,
			validationError: &validationError,
		}, err
	}

	var (
		result          string
		validationError *qosobservations.EVMResponseValidationError
	)

	err = json.Unmarshal(resultBz, &result)
	if err != nil {
		errValue := qosobservations.EVMResponseValidationError_EVM_RESPONSE_VALIDATION_ERROR_UNMARSHAL
		validationError = &errValue
	}

	return &responseToChainID{
		logger:          logger,
		jsonrpcResponse: jsonrpcResp,
		result:          result,
		validationError: validationError,
	}, err
}

// responseToChainID captures the fields expected in a
// response to an `eth_chainId` request.
type responseToChainID struct {
	logger polylog.Logger

	// jsonrpcResponse stores the JSONRPC response parsed from an endpoint's response bytes.
	jsonrpcResponse jsonrpc.Response

	// result captures the `result` field of a JSONRPC response to an `eth_chainId` request.
	result string

	// Why the response has failed validation.
	// Only set if the response is invalid.
	// As of PR #152, a response is valid if either of the following holds:
	//	- It is a valid JSONRPC error response
	//	- It is a valid JSONRPC response with any string value in `result` field.
	// Used when generating observations.
	validationError *qosobservations.EVMResponseValidationError
}

// GetObservation returns an observation of the endpoint's response to an `eth_chainId` request.
// Implements the response interface.
// Reference: https://ethereum.org/en/developers/docs/apis/json-rpc/#eth_chainid
func (r responseToChainID) GetObservation() qosobservations.EVMEndpointObservation {
	return qosobservations.EVMEndpointObservation{
		ParsedJsonrpcResponse: r.jsonrpcResponse.GetObservation(),
		ResponseObservation: &qosobservations.EVMEndpointObservation_ChainIdResponse{
			ChainIdResponse: &qosobservations.EVMChainIDResponse{
				HttpStatusCode:          int32(r.getHTTPStatusCode()),
				ChainIdResponse:         r.result,
				ResponseValidationError: r.validationError,
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
//
// GetHTTPResponse builds and returns the httpResponse matching the responseToChainID instance.
// Implements the response interface.
func (r responseToChainID) GetHTTPResponse() jsonrpc.HTTPResponse {
	return jsonrpc.HTTPResponse{
		ResponsePayload: r.getResponsePayload(),
		HTTPStatusCode:  r.getHTTPStatusCode(),
	}
}

// getResponsePayload returns the raw byte slice payload to be returned as the response to the JSONRPC request.
func (r responseToChainID) getResponsePayload() []byte {
	bz, err := marshalJSONPooled(r.jsonrpcResponse)
	if err != nil {
		// This should never happen: log an entry but return the response anyway.
		r.logger.Warn().Err(err).Msg("responseToChainID: Marshaling JSONRPC response failed.")
	}
	return bz
}

// getHTTPStatusCode returns an HTTP status code corresponding to the underlying JSON-RPC response code.
// DEV_NOTE: This is an opinionated mapping following best practice but not enforced by any specifications or standards.
func (r responseToChainID) getHTTPStatusCode() int {
	return r.jsonrpcResponse.GetRecommendedHTTPStatusCode()
}
