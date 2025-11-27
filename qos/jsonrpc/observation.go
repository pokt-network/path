package jsonrpc

import (
	"github.com/pokt-network/path/observation/qos"
)

// TODO_TECHDEBT(@adshmh): Add a method to response struct to build validation error observations.
// - Prerequisite: updating of Response unmarshaling to define and use exported errors.
// - Makes this file the single source of truth on observation.qos.JsonRpcResponseValidationError struct contents.
//
// Include only the first 100 characters of the JSONRPC response's result field in the observation.
const maxResponseResultPreviewLength = 100

// GetObservation returns a qos.JsonRpcRequest struct that can be used by QoS services
// to populate observation fields.
func (r Request) GetObservation() *qos.JsonRpcRequest {
	return &qos.JsonRpcRequest{
		Id:     r.ID.String(),
		Method: string(r.Method),
	}
}

// GetObservation builds and returns an observation.qos.JsonRpcResponse struct
// Used to populate observation fields.
// Truncates the result
func (r Response) GetObservation() *qos.JsonRpcResponse {
	// Build a preview string of the JSONRPC response's result field.
	var resultPreview string
	if err := r.UnmarshalResult(&resultPreview); err == nil {
		// Pick a maximum of 100 characters to include in the observation
		resultPreview = resultPreview[:min(len(resultPreview), maxResponseResultPreviewLength)]
	}

	// Build the JSONRPC response's observation.
	responseObservation := &qos.JsonRpcResponse{
		Id:            r.ID.String(),
		ResultPreview: resultPreview,
	}

	// Update the observation with the JSONRPC response's error field, if present.
	if r.Error != nil {
		responseObservation.Error = r.Error.GetObservation()
	}

	return responseObservation
}

// GetObservation builds and returns an observation.qos.JsonRpcResponseError struct.
// Used to populate observation fields.
func (re ResponseError) GetObservation() *qos.JsonRpcResponseError {
	return &qos.JsonRpcResponseError{
		Code:    int64(re.Code),
		Message: re.Message,
	}
}
