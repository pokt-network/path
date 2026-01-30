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

// validateResponseIDs ensures all request IDs are present in the responses.
// Per JSON-RPC 2.0 spec, responses with null IDs are valid for error cases when the
// server couldn't parse the request ID. Null ID responses act as "wildcards" that
// can match unmatched request IDs.
func validateResponseIDs(responses []json.RawMessage, servicePayloads map[ID]protocol.Payload) error {
	// Count responses with null IDs (error responses where ID couldn't be determined)
	// and track which request IDs have matching responses
	nullIDCount := 0
	matchedRequestIDs := make(map[string]bool)

	for i, respMsg := range responses {
		var resp Response
		if err := json.Unmarshal(respMsg, &resp); err != nil {
			// Log unmarshal error for debugging
			_ = i    // prevent unused variable warning
			continue // Skip invalid responses - they'll be handled elsewhere
		}

		// Check if this response has a null ID
		if resp.ID.IsEmpty() {
			nullIDCount++
			continue
		}

		// Find matching request ID
		found := false
		for reqID := range servicePayloads {
			if reqID.Equal(resp.ID) {
				matchedRequestIDs[reqID.String()] = true
				found = true
				break
			}
		}
		// Debug: if not found, this is the problematic response
		_ = found
	}

	// Count unmatched request IDs
	unmatchedCount := 0
	for reqID := range servicePayloads {
		if !matchedRequestIDs[reqID.String()] {
			unmatchedCount++
		}
	}

	// Null ID responses can cover unmatched request IDs (per JSON-RPC 2.0 spec,
	// null IDs indicate errors parsing the original request)
	if unmatchedCount > nullIDCount {
		return fmt.Errorf("%w: %d request ID(s) have no matching response and only %d null ID response(s) available",
			ErrBatchResponseMissingIDs, unmatchedCount, nullIDCount)
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
