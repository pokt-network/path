package jsonrpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

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
	// A request nothing answered still gets a response object of its own.
	responses = fillMissingResponses(logger, responses, servicePayloads)

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

// fillMissingResponses appends an error response object, carrying the request's own id, for
// every request in the batch that has no response.
//
// A batch item that fails every attempt (transport error, no endpoint left to try) produces
// no body, so the collected responses came up one short of the requests. The length check
// below then rejected the WHOLE batch with a single id:null error — every other item's
// answer, already relayed and paid for, thrown away because one item had none. Measured on
// one batch-heavy service at ~7-8% of all requests, almost all of them "expected N, got N-1".
//
// Per JSON-RPC 2.0 every request object in a batch gets exactly one response object; a
// request the server could not serve gets an error object with that request's id, next to
// the successes. That is what the single-request path already returns (responseNone), and
// what a node does.
//
// Rules:
//   - Matching is by id value (ID.String), so same-value ids — legal in a batch — are counted,
//     not collapsed: two id:1 requests with one id:1 response are one short.
//   - A null-id response is the endpoint saying it could not read a request's id. It already
//     stands in for one missing request (see validateResponseIDs) and is not doubled up.
//   - Notifications (no id) never get a response object and are never filled.
//   - The id keeps its JSON type: the typed ID from servicePayloads is used, not a string.
func fillMissingResponses(logger polylog.Logger, responses []json.RawMessage, servicePayloads map[ID]protocol.Payload) []json.RawMessage {
	// Requests awaiting a response, by id value. One typed representative per value.
	pending := make(map[string]int, len(servicePayloads))
	representative := make(map[string]ID, len(servicePayloads))
	for reqID := range servicePayloads {
		if reqID.IsEmpty() {
			continue // notification
		}
		key := reqID.String()
		pending[key]++
		representative[key] = reqID
	}

	nullIDResponses := 0
	for _, respMsg := range responses {
		var resp Response
		if err := json.Unmarshal(respMsg, &resp); err != nil {
			continue // counted by length validation; not attributable to any request here
		}
		if resp.ID.IsEmpty() {
			nullIDResponses++
			continue
		}
		if key := resp.ID.String(); pending[key] > 0 {
			pending[key]--
		}
	}

	// Deterministic order so the same batch always fills the same way.
	missing := make([]string, 0, len(pending))
	for key, n := range pending {
		for i := 0; i < n; i++ {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)

	// Null-id responses cover missing requests first, as the id validation already allows.
	if nullIDResponses >= len(missing) {
		return responses
	}
	missing = missing[nullIDResponses:]

	logger.Warn().
		Int("batch_requests", len(servicePayloads)).
		Int("responses_received", len(responses)).
		Int("responses_filled", len(missing)).
		Msg("Batch items without any endpoint response: returning a per-request error object for each")

	for _, key := range missing {
		errResp := NewErrResponseNoEndpointResponse(representative[key])
		bz, err := json.Marshal(errResp)
		if err != nil {
			// Cannot happen for a Response built here; leave the length check to report it.
			logger.Error().Err(err).Msg("failed to marshal synthesized batch error response")
			continue
		}
		responses = append(responses, json.RawMessage(bz))
	}
	return responses
}

// validateResponseLength ensures response count matches the count of requests that expect
// a response (notifications — requests without an id — do not get one).
func validateResponseLength(responses []json.RawMessage, servicePayloads map[ID]protocol.Payload) error {
	expected := 0
	for reqID := range servicePayloads {
		if !reqID.IsEmpty() {
			expected++
		}
	}
	if len(responses) != expected {
		return fmt.Errorf("%w: expected %d responses, got %d",
			ErrBatchResponseLengthMismatch, expected, len(responses))
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

	// Count unmatched request IDs. Notifications (no id) expect no response.
	unmatchedCount := 0
	for reqID := range servicePayloads {
		if reqID.IsEmpty() {
			continue
		}
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
