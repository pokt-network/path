package qos

import (
	"encoding/json"
	"net/http"

	"github.com/pokt-network/poktroll/pkg/polylog"

	pathhttp "github.com/pokt-network/path/network/http"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// httpHeadersApplicationJSON is the `Content-Type` HTTP header used in all JSONRPC responses.
var httpHeadersApplicationJSON = map[string]string{
	"Content-Type": "application/json",
}

// HTTPResponse is used by the RequestContext to provide
// an JSONRPC-specific implementation of gateway package's HTTPResponse.
var _ pathhttp.HTTPResponse = HTTPResponse{}

func BuildHTTPResponseFromJSONRPCResponse(
	logger polylog.Logger,
	jsonrpcResp jsonrpc.Response,
) HTTPResponse {
	bz, err := json.Marshal(jsonrpcResp)
	// Failed to marshal the JSONRPC response.
	if err != nil {
		logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Err(err).Msg("SHOULD RARELY HAPPEN: failed to marshal the JSONRPC response.")
	}

	return HTTPResponse{
		responsePayload: bz,
		// Use the HTTP status code recommended by the JSONRPC response.
		httpStatusCode: jsonrpcResp.GetRecommendedHTTPStatusCode(),
	}
}

func BuildHTTPResponseFromBytes(
	responsePayload []byte,
	httpStatusCode int,
) HTTPResponse {
	return HTTPResponse{
		responsePayload: responsePayload,
		httpStatusCode:  httpStatusCode,
	}
}

// HTTPResponse encapsulates an HTTP response to be returned to the client
// including payload data and status code.
type HTTPResponse struct {
	// responsePayload contains the serialized response body.
	responsePayload []byte

	// httpStatusCode is the HTTP status code to be returned.
	// If not explicitly set, defaults to http.StatusOK (200).
	httpStatusCode int
}

// GetPayload returns the response payload as a byte slice.
func (hr HTTPResponse) GetPayload() []byte {
	return hr.responsePayload
}

// GetHTTPStatusCode returns the HTTP status code for this response.
// If no status code was explicitly set, returns http.StatusOK (200).
// StatusOK is returned by default from QoS because it is the responsibility of the QoS service to decide on the HTTP status code returned to the client.
func (hr HTTPResponse) GetHTTPStatusCode() int {
	// Return the custom status code if set, otherwise default to 200 OK
	if hr.httpStatusCode != 0 {
		return hr.httpStatusCode
	}

	// Default to 200 OK HTTP status code.
	return http.StatusOK
}

// GetHTTPHeaders returns the set of headers for the HTTP response.
// As of PR #72, the only header returned for JSONRPC is `Content-Type`.
func (r HTTPResponse) GetHTTPHeaders() map[string]string {
	// JSONRPC only uses the `Content-Type` HTTP header.
	return httpHeadersApplicationJSON
}
