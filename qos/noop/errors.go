package noop

import (
	"fmt"
	"net/http"

	pathhttp "github.com/pokt-network/path/network/http"
)

const (
	// errTemplate is the template for the error response to ensure
	// that the error response is returned in a valid JSON format.
	errTemplate = `{"error":"%s","msg":"%s"}`

	// clientRespMsgNoProtocolEndpoints is the error message sent to clients when
	// the underlying protocol fails to register any endpoint responses with the NoOp QoS service.
	// This can occur due to:
	//   - User error: invalid service ID in the request's HTTP header
	//   - Protocol error: selected endpoint failed to provide a valid response
	//   - System timeout: no endpoints responded within the allowed time window
	clientRespMsgNoProtocolEndpoints = "no-op qos service error: no responses received from any service endpoints"

	// clientRespMsgRequestProcessingError is the error message sent to clients when
	// the NoOp QoS service encounters an error while reading the request body.
	// This can occur due to:
	//   - User error: invalid request body caused the an error reading the request body
	clientRespMsgRequestProcessingError = "no-op qos service error: error processing the request"
)

// formatJSONError creates a JSON formatted error response with the provided error and message.
func formatJSONError(message string, err error) []byte {
	return []byte(fmt.Sprintf(errTemplate, err.Error(), message))
}

// getRequestProcessingError creates a HTTP response for request processing errors.
func getRequestProcessingError(err error) pathhttp.HTTPResponse {
	return &HTTPResponse{
		httpStatusCode: http.StatusBadRequest,
		payload:        formatJSONError(clientRespMsgRequestProcessingError, err),
	}
}

// getNoEndpointResponse creates a HTTP response for no endpoint responses.
func getNoEndpointResponse() pathhttp.HTTPResponse {
	err := fmt.Errorf("no protocol endpoint responses")
	return &HTTPResponse{
		httpStatusCode: http.StatusInternalServerError,
		payload:        formatJSONError(clientRespMsgNoProtocolEndpoints, err),
	}
}

// getNoEndpointResponseWithError creates a HTTP response for no endpoint responses
// with a specific error message for more informative client feedback.
func getNoEndpointResponseWithError(protocolErr error) pathhttp.HTTPResponse {
	return &HTTPResponse{
		httpStatusCode: http.StatusInternalServerError,
		payload:        formatJSONError(clientRespMsgNoProtocolEndpoints, protocolErr),
	}
}
