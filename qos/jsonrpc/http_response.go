package jsonrpc

import (
	"net/http"

	pathhttp "github.com/pokt-network/path/network/http"
)

const (
	// HTTP status code 400 bad request is used if the request cannot be deserialized into JSONRPC.
	HTTPStatusRequestValidationFailureUnmarshalFailure = http.StatusBadRequest

	// TODO_MVP(@adshmh): Remove the error below once the qos interface is updated to replace ParseHTTPRequest with ParseRequest, decoupling the QoS service from the HTTP request.
	// HTTP status code 500 internal server error is used if reading the HTTP request's body fails
	HTTPStatusRequestValidationFailureReadHTTPBodyFailure = http.StatusInternalServerError

	// HTTP status codes returned on response validation failure: no response received
	HTTPStatusResponseValidationFailureNoResponse = http.StatusInternalServerError

	// HTTP status codes returned on response validation failure: empty response received
	HTTPStatusResponseValidationFailureEmptyResponse = http.StatusInternalServerError
)

// HTTPHeadersApplicationJSON is the `Content-Type` HTTP header used in all JSONRPC responses.
var HTTPHeadersApplicationJSON = map[string]string{
	"Content-Type": "application/json",
}

// HTTPResponse is used by the RequestContext to provide
// a JSONRPC-specific implementation of gateway package's HTTPResponse.
var _ pathhttp.HTTPResponse = HTTPResponse{}

// httpResponse encapsulates an HTTP response to be returned to the client
// including payload data and status code.
type HTTPResponse struct {
	// responResponsePayloadsePayload contains the serialized response body.
	ResponsePayload []byte

	// HTTPStatusCode is the HTTP status code to be returned.
	// If not explicitly set, defaults to http.StatusOK (200).
	HTTPStatusCode int
}

// GetPayload returns the response payload as a byte slice.
func (hr HTTPResponse) GetPayload() []byte {
	return hr.ResponsePayload
}

// GetHTTPStatusCode returns the HTTP status code for this response.
// If no status code was explicitly set, returns http.StatusOK (200).
// StatusOK is returned by default from JSONRPC QoS because it is the responsibility of the QoS service to decide on the HTTP status code returned to the client.
func (hr HTTPResponse) GetHTTPStatusCode() int {
	// Return the custom status code if set, otherwise default to 200 OK
	if hr.HTTPStatusCode != 0 {
		return hr.HTTPStatusCode
	}

	// Default to 200 OK HTTP status code.
	return http.StatusOK
}

// GetHTTPHeaders returns the set of headers for the HTTP response.
// As of PR #72, the only header returned for JSONRPC is `Content-Type`.
func (r HTTPResponse) GetHTTPHeaders() map[string]string {
	// JSONRPC only uses the `Content-Type` HTTP header.
	return HTTPHeadersApplicationJSON
}
