package noop

import pathhttp "github.com/pokt-network/path/network/http"

// HTTPResponse provides all the functionality required by the gate.HTTPResponse interface.
var _ pathhttp.HTTPResponse = &HTTPResponse{}

// HTTPResponse stores the data required for building and returning a user-facing HTTP response
// based on the response received from an endpoint to a service request.
type HTTPResponse struct {
	// payload is the raw payload received from the endpoint servicing the request.
	payload []byte
	// httpStatus is the HTTP status code to be returned to the user.
	httpStatusCode int
}

// GetPayload returns the payload of the user-facing HTTP response.
// Implements the pathhttp.HTTPResponse interface.
func (h *HTTPResponse) GetPayload() []byte {
	return h.payload
}

// GetHTTPStatusCode returns the HTTP status code of the user-facing HTTP response.
// Implements the pathhttp.HTTPResponse interface.
func (h *HTTPResponse) GetHTTPStatusCode() int {
	return h.httpStatusCode
}

// GetHTTPHeaders always returns nil, as HTTP headers are not used by noop QoS as of PR #106.
// See: https://github.com/pokt-network/path/pull/106
// Implements the pathhttp.HTTPResponse interface.
func (h *HTTPResponse) GetHTTPHeaders() map[string]string {
	return nil
}
