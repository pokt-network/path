package jsonrpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

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
	// One response object per request object: drop surplus, fill missing.
	responses = reconcileResponses(logger, responses, servicePayloads)

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

// responseIDOf reads only the id of a raw response. A supplier's answer does not always
// unmarshal as a Response — an error given as a bare string, an id of a type the parser
// rejects — but it is still an answer to some item, and its id is usually readable.
// ok is false when no id can be read at all (including invalid JSON).
func responseIDOf(raw json.RawMessage) (id ID, ok bool) {
	var probe struct {
		ID ID `json:"id"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ID{}, false
	}
	return probe.ID, true
}

// reconcileResponses makes the collected responses line up one-to-one with the batch's
// requests before validation: it drops surplus responses for an id and fills in an error
// object for every request that has none.
//
// Missing (fewer responses than requests): a batch item that fails every attempt (transport
// error, no endpoint left to try) produces no body, so the collected responses came up one
// short of the requests. The length check below then rejected the WHOLE batch with a single
// id:null error — every other item's answer, already relayed and paid for, thrown away
// because one item had none. Every retained log line of that failure on one batch-heavy
// service was "expected N, got N-1" or close.
//
// Surplus (more responses than requests): a one-element batch is a batch, but with one
// payload it runs down the single-request path, which records every response it saw — the
// attempt that failed and then the retry that succeeded, or all N answers of a parallel
// fan-out. The single path returns the latest; the batch assembler collected all of them,
// saw 2 for 1, and replaced a request that had SUCCEEDED with an id:null error.
//
// Per JSON-RPC 2.0 every request object in a batch gets exactly one response object; a
// request the server could not serve gets an error object with that request's id, next to
// the successes. That is what the single-request path already returns (responseNone), and
// what a node does.
//
// Rules:
//   - Matching is by id value (ID.String), so same-value ids — legal in a batch — are counted,
//     not collapsed: two id:1 requests keep two id:1 responses.
//   - Surplus responses for an id are trimmed from the front: the LATEST responses win, the
//     same rule the single-request path applies.
//   - A request without an id is not a notification here: every item is relayed on its own
//     with "id":null written out and the node answers it with a null-id response. It expects
//     one response like any other, and is filled with a null-id error if it gets none.
//   - A null-id response beyond those is the endpoint saying it could not read a request's
//     id. It stands in for one missing request (see validateResponseIDs) and is not doubled up.
//   - A response whose id cannot be read at all is likewise unattributable and stands in for
//     one missing request; it is kept if it is valid JSON and dropped if it is not (it could
//     not be placed in the array anyway). Filling on top of it made the batch one long.
//   - The id keeps its JSON type: the typed ID from servicePayloads is used, not a string.
func reconcileResponses(logger polylog.Logger, responses []json.RawMessage, servicePayloads map[ID]protocol.Payload) []json.RawMessage {
	requestIDs := make([]ID, 0, len(servicePayloads))
	for reqID := range servicePayloads {
		requestIDs = append(requestIDs, reqID)
	}
	return ReconcileBatchResponses(logger, responses, requestIDs)
}

// ReconcileBatchResponses is reconcileResponses for callers that hold the batch's request
// ids as a list rather than a payload map (the pass-through QoS keeps no typed payloads).
// Same rules. An empty request id is keyed as "null", matching ID.String.
func ReconcileBatchResponses(logger polylog.Logger, responses []json.RawMessage, requestIDs []ID) []json.RawMessage {
	// Requests awaiting a response, by id value. One typed representative per value.
	pending := make(map[string]int, len(requestIDs))
	representative := make(map[string]ID, len(requestIDs))
	for _, reqID := range requestIDs {
		key := reqID.String()
		pending[key]++
		representative[key] = reqID
	}
	nullKey := ID{}.String()

	// Positions of the responses carrying each id, in arrival order. Null-id responses are
	// first matched to id-less requests; any beyond those, and responses with no readable
	// id, are wildcards (see below). Invalid JSON is dropped outright.
	positions := make(map[string][]int, len(pending))
	wildcards := 0
	drop := make(map[int]bool)
	for i, respMsg := range responses {
		id, ok := responseIDOf(respMsg)
		if !ok {
			if !json.Valid(respMsg) {
				drop[i] = true
				continue
			}
			wildcards++
			continue
		}
		key := id.String()
		if id.IsEmpty() && len(positions[nullKey]) >= pending[nullKey] {
			wildcards++ // no id-less request left for it to answer
			continue
		}
		positions[key] = append(positions[key], i)
	}

	// Trim surplus: keep the last pending[key] responses for each id.
	trimmed := len(drop)
	for key, idx := range positions {
		want := pending[key]
		if len(idx) > want {
			for _, i := range idx[:len(idx)-want] {
				drop[i] = true
			}
			trimmed += len(idx) - want
			pending[key] = 0
		} else {
			pending[key] -= len(idx)
		}
	}
	if len(drop) > 0 {
		kept := make([]json.RawMessage, 0, len(responses)-len(drop))
		for i, respMsg := range responses {
			if !drop[i] {
				kept = append(kept, respMsg)
			}
		}
		responses = kept
	}

	// Deterministic order so the same batch always fills the same way.
	missing := make([]string, 0, len(pending))
	for key, n := range pending {
		for i := 0; i < n; i++ {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)

	// Wildcards cover missing requests first, as the id validation already allows.
	if wildcards < len(missing) {
		missing = missing[wildcards:]
	} else {
		missing = nil
	}

	if trimmed == 0 && len(missing) == 0 {
		return responses
	}

	logger.Warn().
		Int("batch_requests", len(requestIDs)).
		Int("responses_received", len(responses)+trimmed).
		Int("responses_trimmed", trimmed).
		Int("responses_filled", len(missing)).
		Msg("Batch responses reconciled to one per request: surplus dropped (latest kept), missing filled with a per-request error object")

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

// validateResponseLength ensures response count matches request count. Every request in
// the batch expects exactly one response, id or not — see reconcileResponses.
func validateResponseLength(responses []json.RawMessage, servicePayloads map[ID]protocol.Payload) error {
	if len(responses) != len(servicePayloads) {
		return fmt.Errorf("%w: expected %d responses, got %d (response ids: %s)",
			ErrBatchResponseLengthMismatch, len(servicePayloads), len(responses), summarizeResponseIDs(responses))
	}
	return nil
}

// summarizeResponseIDs lists the ids of the responses, bounded, for the mismatch error — the
// only thing that has ever told the shapes of this failure apart in production. "?" marks a
// response whose id could not be read.
func summarizeResponseIDs(responses []json.RawMessage) string {
	const maxIDs = 24
	ids := make([]string, 0, len(responses))
	for i, respMsg := range responses {
		if i == maxIDs {
			ids = append(ids, fmt.Sprintf("…+%d", len(responses)-maxIDs))
			break
		}
		if id, ok := responseIDOf(respMsg); ok {
			ids = append(ids, id.String())
		} else {
			ids = append(ids, "?")
		}
	}
	return "[" + strings.Join(ids, ",") + "]"
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

	for _, respMsg := range responses {
		// Same id reading as reconcileResponses: a response with no readable id is as much
		// a wildcard as one with a null id — it answers something, we cannot say what.
		respID, ok := responseIDOf(respMsg)
		if !ok || respID.IsEmpty() {
			nullIDCount++
			continue
		}

		// Find matching request ID
		for reqID := range servicePayloads {
			if reqID.Equal(respID) {
				matchedRequestIDs[reqID.String()] = true
				break
			}
		}
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
