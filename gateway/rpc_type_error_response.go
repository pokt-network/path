package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/pokt-network/path/protocol"
)

// JSON-RPC error codes
const (
	// JSONRPCInvalidRequest represents -32600: Invalid Request
	// Used when the RPC type is not supported by the service
	JSONRPCInvalidRequest = -32600
)

// rpcTypeValidationErrorResponse implements pathhttp.HTTPResponse for RPC type validation errors
type rpcTypeValidationErrorResponse struct {
	serviceID        protocol.ServiceID
	detectedType     string
	allowedRPCTypes  []string
	errorMessage     string
	httpStatusCode   int
	jsonrpcErrorCode int
}

// NewRPCTypeValidationErrorResponse creates a new error response for RPC type validation failures
func NewRPCTypeValidationErrorResponse(
	serviceID protocol.ServiceID,
	detectedType string,
	allowedRPCTypes []string,
	errorMessage string,
) *rpcTypeValidationErrorResponse {
	return &rpcTypeValidationErrorResponse{
		serviceID:        serviceID,
		detectedType:     detectedType,
		allowedRPCTypes:  allowedRPCTypes,
		errorMessage:     errorMessage,
		httpStatusCode:   http.StatusBadRequest, // 400
		jsonrpcErrorCode: JSONRPCInvalidRequest, // -32600
	}
}

// NewServiceNotConfiguredErrorResponse creates a new error response for unconfigured services
func NewServiceNotConfiguredErrorResponse(
	serviceID protocol.ServiceID,
	availableServices []protocol.ServiceID,
	errorMessage string,
) *rpcTypeValidationErrorResponse {
	// Convert available services to strings
	availableServiceStrs := make([]string, len(availableServices))
	for i, svc := range availableServices {
		availableServiceStrs[i] = string(svc)
	}

	return &rpcTypeValidationErrorResponse{
		serviceID:        serviceID,
		detectedType:     "", // Not applicable for service config errors
		allowedRPCTypes:  availableServiceStrs,
		errorMessage:     errorMessage,
		httpStatusCode:   http.StatusBadRequest, // 400
		jsonrpcErrorCode: JSONRPCInvalidRequest, // -32600
	}
}

// GetPayload returns the JSON-RPC error payload
func (r *rpcTypeValidationErrorResponse) GetPayload() []byte {
	// Build error data structure
	errorData := map[string]interface{}{
		"service_id": r.serviceID,
	}

	// For RPC type errors, include detected type and allowed types
	if r.detectedType != "" {
		errorData["detected_type"] = r.detectedType
		errorData["allowed_rpc_types"] = r.allowedRPCTypes
	} else {
		// For service config errors, just show available services
		errorData["available_services"] = r.allowedRPCTypes
	}

	// Build JSON-RPC error response
	response := map[string]interface{}{
		"jsonrpc": "2.0",
		"error": map[string]interface{}{
			"code":    r.jsonrpcErrorCode,
			"message": r.errorMessage,
			"data":    errorData,
		},
		"id": nil, // No request ID available at validation stage
	}

	// Marshal to JSON
	payload, err := json.Marshal(response)
	if err != nil {
		// Fallback to simple error message if JSON marshaling fails
		return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","error":{"code":%d,"message":"Internal error: %s"},"id":null}`,
			r.jsonrpcErrorCode, err.Error()))
	}

	return payload
}

// GetHTTPStatusCode returns the HTTP status code (400 Bad Request)
func (r *rpcTypeValidationErrorResponse) GetHTTPStatusCode() int {
	return r.httpStatusCode
}

// GetHTTPHeaders returns the HTTP headers for the error response
func (r *rpcTypeValidationErrorResponse) GetHTTPHeaders() map[string]string {
	return map[string]string{
		"Content-Type": "application/json",
	}
}
