package cosmos

import (
	"net/http"

	pathhttp "github.com/pokt-network/path/network/http"
)

// httpHeadersApplicationJSON is the `Content-Type` HTTP header used in all CosmosSDK responses.
var httpHeadersApplicationJSON = map[string]string{
	"Content-Type": "application/json",
}

// httpResponse is used by the RequestContext to provide
// a CosmosSDK-specific implementation of gateway package's HTTPResponse.
var _ pathhttp.HTTPResponse = httpResponse{}

// httpResponse encapsulates an HTTP response to be returned to the client
// including payload data and status code.
type httpResponse struct {
	// responsePayload contains the serialized response body.
	responsePayload []byte

	// httpStatusCode is the HTTP status code to be returned.
	// If not explicitly set, defaults to http.StatusOK (200).
	httpStatusCode int
}

// GetPayload returns the response payload as a byte slice.
func (hr httpResponse) GetPayload() []byte {
	return hr.responsePayload
}

// GetHTTPStatusCode returns the HTTP status code for this response.
// If no status code was explicitly set, returns http.StatusOK (200).
// StatusOK is returned by default from CosmosSDK QoS because it is the responsibility of the QoS service to decide on the HTTP status code returned to the client.
func (hr httpResponse) GetHTTPStatusCode() int {
	// Return the custom status code if set, otherwise default to 200 OK
	if hr.httpStatusCode != 0 {
		return hr.httpStatusCode
	}

	// Default to 200 OK HTTP status code.
	return http.StatusOK
}

// GetHTTPHeaders returns the set of headers for the HTTP response.
// As of PR #72, the only header returned for CosmosSDK is `Content-Type`.
func (r httpResponse) GetHTTPHeaders() map[string]string {
	// CosmosSDK only uses the `Content-Type` HTTP header.
	return httpHeadersApplicationJSON
}
