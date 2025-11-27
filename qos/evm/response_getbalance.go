package evm

import (
	"encoding/json"

	"github.com/pokt-network/poktroll/pkg/polylog"

	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// responseToGetBalance provides the functionality required from a response by a requestContext instance.
var _ response = responseToGetBalance{}

// responseUnmarshallerGetBalance deserializes the provided JSONRPC payload into
// a responseToGetBalance struct, adding any encountered errors to the returned struct.
//
// The results from this method are used to update the endpoint's archival state
// only if they are for the currently selected archival block number.
//
// Reference: https://ethereum.org/en/developers/docs/apis/json-rpc/#eth_getbalance
func responseUnmarshallerGetBalance(
	logger polylog.Logger,
	jsonrpcReq jsonrpc.Request,
	jsonrpcResp jsonrpc.Response,
) (response, error) {
	// The endpoint returned an error: no need to do further processing of the response.
	if jsonrpcResp.IsError() {
		return responseToGetBalance{
			logger:          logger,
			jsonrpcResponse: jsonrpcResp,
			validationError: nil, // Intentionally set to nil to indicate a valid JSONRPC error response.
		}, nil
	}

	responseBz, err := jsonrpcResp.GetResultAsBytes()
	if err != nil {
		validationError := qosobservations.EVMResponseValidationError_EVM_RESPONSE_VALIDATION_ERROR_UNMARSHAL
		return responseToGetBalance{
			logger:          logger,
			jsonrpcResponse: jsonrpcResp,
			validationError: &validationError,
		}, err
	}

	var validationError *qosobservations.EVMResponseValidationError
	var balanceResponse string
	if err := json.Unmarshal(responseBz, &balanceResponse); err != nil {
		errValue := qosobservations.EVMResponseValidationError_EVM_RESPONSE_VALIDATION_ERROR_UNMARSHAL
		validationError = &errValue
	}

	// Extract the contract address and block number from the JSON-RPC request.
	requestParams := getRequestParams(jsonrpcReq)

	// Valid eth_getBalance response: no error and a valid balance.
	return responseToGetBalance{
		logger:          logger,
		jsonrpcResponse: jsonrpcResp,
		contractAddress: requestParams[0],
		blockNumber:     requestParams[1],
		balance:         balanceResponse,
		validationError: validationError,
	}, nil
}

// responseToGetBalance captures the fields expected in a response to an `eth_getBalance` request.
type responseToGetBalance struct {
	logger polylog.Logger

	// jsonrpcResponse stores the JSONRPC response parsed from an endpoint's response bytes.
	jsonrpcResponse jsonrpc.Response

	// the contract address from the request params (first item in the params array)
	contractAddress string

	// the block number from the request params (second item in the params array)
	blockNumber string

	// the balance value returned in the response
	balance string

	// validationError indicates why the response failed validation, if it did.
	validationError *qosobservations.EVMResponseValidationError
}

// GetObservation returns an observation based on the archival response.
// Implements the response interface.
func (r responseToGetBalance) GetObservation() qosobservations.EVMEndpointObservation {
	return qosobservations.EVMEndpointObservation{
		ParsedJsonrpcResponse: r.jsonrpcResponse.GetObservation(),
		ResponseObservation: &qosobservations.EVMEndpointObservation_GetBalanceResponse{
			GetBalanceResponse: &qosobservations.EVMGetBalanceResponse{
				HttpStatusCode:          int32(r.getHTTPStatusCode()),
				ContractAddress:         r.contractAddress,
				BlockNumber:             r.blockNumber,
				Balance:                 r.balance,
				ResponseValidationError: r.validationError,
			},
		},
	}
}

// GetHTTPResponse returns the HTTP response corresponding to the JSON-RPC response.
// Implements the response interface.
func (r responseToGetBalance) GetHTTPResponse() jsonrpc.HTTPResponse {
	return jsonrpc.HTTPResponse{
		ResponsePayload: r.getResponsePayload(),
		HTTPStatusCode:  r.getHTTPStatusCode(),
	}
}

// getResponsePayload returns the JSON-RPC response payload as a byte slice.
func (r responseToGetBalance) getResponsePayload() []byte {
	responseBz, err := json.Marshal(r.jsonrpcResponse)
	if err != nil {
		r.logger.Warn().Err(err).Msg("responseToGetBalance: Marshaling JSONRPC response failed.")
	}
	return responseBz
}

// getHTTPStatusCode returns an HTTP status code corresponding to the underlying JSON-RPC response.
func (r responseToGetBalance) getHTTPStatusCode() int {
	return r.jsonrpcResponse.GetRecommendedHTTPStatusCode()
}

// getRequestParams extracts the string params from the JSONRPC request.
// For 'eth_getBalance', the params are contains ["contract_address", "block"number"] in that order.
//
// For example, the following JSON-RPC request:
//
//	`{"jsonrpc": "2.0", "method": "eth_getBalance", "params": ["0x407d73d8a49eeb85d32cf465507dd71d507100c1", "0x59E8A"]}`
//
// will return the following params array: `["0x407d73d8a49eeb85d32cf465507dd71d507100c1", "0x59E8A"]`
func getRequestParams(req jsonrpc.Request) [2]string {
	if req.Params.IsEmpty() {
		return [2]string{}
	}

	paramsBz, err := json.Marshal(req.Params)
	if err != nil {
		return [2]string{}
	}

	// eth_getBalance params are always an array of two strings
	var paramsArray [2]string
	if err := json.Unmarshal(paramsBz, &paramsArray); err != nil {
		return [2]string{}
	}

	return paramsArray
}
