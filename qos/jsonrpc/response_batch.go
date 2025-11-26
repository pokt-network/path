package jsonrpc

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/poktroll/pkg/polylog"
)

// Batch validation errors - standard Go error variables
var (
	ErrBatchResponseLengthMismatch = errors.New("batch response length mismatch")
	ErrBatchResponseMissingIDs     = errors.New("batch response missing required IDs")
	ErrBatchResponseEmpty          = errors.New("empty batch response not allowed per JSON-RPC specification")
	ErrBatchResponseMarshalFailure = errors.New("failed to marshal batch response")
)

// ValidateBatchResponse validates and constructs a batch response according to JSON-RPC 2.0 specification.
//
// It performs comprehensive validation including:
//   - Empty batch handling per JSON-RPC spec (returns empty payload)
//   - Response length matches request length
//   - All request IDs are present in responses
//   - Proper JSON array construction
//
// Returns the marshaled JSON byte array for the response payload.
// Note that response validation of the individual responses is not performed here;
// this is handled in the unmarshalResponse function inside the respective QoS package.
func ValidateAndBuildBatchResponse(
	logger polylog.Logger,
	responses []json.RawMessage,
	servicePayloads map[ID]protocol.Payload,
) ([]byte, error) {
	// Validate response length matches request length
	if err := validateResponseLength(responses, servicePayloads); err != nil {
		return nil, err
	}

	// Validate all request IDs are present in responses
	if err := validateResponseIDs(responses, servicePayloads); err != nil {
		return nil, err
	}

	// Marshal responses into JSON array
	return marshalBatchResponse(responses)
}

// validateResponseLength ensures response count matches request count
func validateResponseLength(responses []json.RawMessage, servicePayloads map[ID]protocol.Payload) error {
	if len(responses) != len(servicePayloads) {
		return fmt.Errorf("%w: expected %d responses, got %d",
			ErrBatchResponseLengthMismatch, len(servicePayloads), len(responses))
	}
	return nil
}

// validateResponseIDs ensures all request IDs are present in the responses
func validateResponseIDs(responses []json.RawMessage, servicePayloads map[ID]protocol.Payload) error {
	// Check each request ID has a corresponding response
	for reqID := range servicePayloads {
		found := false
		for _, respMsg := range responses {
			var resp Response
			if err := json.Unmarshal(respMsg, &resp); err != nil {
				continue // Skip invalid responses - they'll be handled elsewhere
			}
			if reqID.Equal(resp.ID) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: missing response for request ID '%s'", ErrBatchResponseMissingIDs, reqID.String())
		}
	}
	return nil
}

// marshalBatchResponse constructs the final JSON array from individual responses
func marshalBatchResponse(responses []json.RawMessage) ([]byte, error) {
	batchResponse, err := json.Marshal(responses)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBatchResponseMarshalFailure, err)
	}
	return batchResponse, nil
}
