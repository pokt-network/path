package jsonrpc

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/log"
)

// TODO_MVP(@adshmh): Add a JSON-RPC request validator to reject invalid/unsupported
// method calls early in request flow.
//
// ParseJSONRPCFromRequestBody parses HTTP request bodies into JSON-RPC request structures.
// Supports both single requests and batch requests according to the JSON-RPC 2.0 specification.
// Returns a normalized map of JSON-RPC requests keyed by ID.
//
// Reference: https://www.jsonrpc.org/specification#batch
func ParseJSONRPCFromRequestBody(
	logger polylog.Logger,
	requestBody []byte,
) (map[ID]Request, bool, error) {
	// Validate and parse the request body into a slice of requests
	requests, isBatch, err := parseRequestsFromBody(logger, requestBody)
	if err != nil {
		return nil, false, err
	}

	// Validate batch constraints and convert to map
	requestsMap, err := validateAndMapRequests(logger, requests)
	if err != nil {
		return nil, false, err
	}

	return requestsMap, isBatch, nil
}

// parseRequestsFromBody converts raw request body into a slice of JSON-RPC requests.
// Returns the requests, whether it was originally a batch format, and any error.
func parseRequestsFromBody(logger polylog.Logger, requestBody []byte) ([]Request, bool, error) {
	trimmedBody, err := validateRequestBodyNotEmpty(logger, requestBody)
	if err != nil {
		return nil, false, err
	}

	// Dispatch on the first byte instead of speculatively unmarshaling: a JSON
	// array ('[') is a batch, anything else is treated as a single object. This
	// avoids a second full json.Unmarshal on the common single-request path.
	// trimmedBody is guaranteed non-empty by validateRequestBodyNotEmpty.
	if trimmedBody[0] == '[' {
		return tryUnmarshalAsBatch(logger, trimmedBody)
	}

	return tryUnmarshalAsSingle(logger, trimmedBody)
}

// validateRequestBodyNotEmpty validates the request body is not empty after trimming.
func validateRequestBodyNotEmpty(logger polylog.Logger, requestBody []byte) ([]byte, error) {
	trimmedBody := bytes.TrimSpace(requestBody)

	if len(trimmedBody) == 0 {
		logger.Error().Msg("❌ Request failed JSON-RPC validation - empty request body")
		return nil, fmt.Errorf("empty request body")
	}

	return trimmedBody, nil
}

// tryUnmarshalAsBatch attempts to unmarshal the request body as a JSON array.
// Returns the requests and true if successful, or an error if it's not a valid array.
func tryUnmarshalAsBatch(logger polylog.Logger, requestBody []byte) ([]Request, bool, error) {
	var requests []Request
	if err := json.Unmarshal(requestBody, &requests); err != nil {
		logger.Error().
			Err(err).
			Str("request_preview", log.Preview(string(requestBody))).
			Msg("❌ Request failed JSON-RPC validation - malformed batch request")
		return nil, false, err
	}

	return requests, true, nil
}

// tryUnmarshalAsSingle attempts to unmarshal the request body as a JSON object.
// Returns the request as a single-element slice and false, or an error if invalid.
func tryUnmarshalAsSingle(logger polylog.Logger, requestBody []byte) ([]Request, bool, error) {
	var singleRequest Request
	if err := json.Unmarshal(requestBody, &singleRequest); err != nil {
		logger.Error().
			Err(err).
			Str("request_preview", log.Preview(string(requestBody))).
			Msg("❌ Request failed JSON-RPC validation - returning generic error response")
		return nil, false, err
	}

	// Convert single request to slice for uniform downstream processing
	requests := []Request{singleRequest}

	return requests, false, nil
}

// validateAndMapRequests validates batch constraints and converts requests to a map.
// Ensures no duplicate IDs exist and batch is not empty per JSON-RPC specification.
func validateAndMapRequests(logger polylog.Logger, requests []Request) (map[ID]Request, error) {
	// Validate batch is not empty (per JSON-RPC spec)
	if len(requests) == 0 {
		logger.Error().
			Msg("❌ Empty batch request not allowed per JSON-RPC specification")
		return nil, fmt.Errorf("empty batch request not allowed")
	}

	// Convert to map and validate no duplicate IDs exist.
	//
	// Duplicate detection is keyed by ID.String(), NOT by the ID struct itself:
	// ID holds *int/*string pointers, so two IDs with the same value compare
	// unequal as map keys (different pointer addresses) and duplicates would
	// slip through. String() collapses to the value and matches the cross-type
	// semantics of ID.Equal (the integer 1 and the string "1" are treated as
	// the same ID), keeping detection consistent with response correlation.
	requestsMap := make(map[ID]Request, len(requests))
	seenIDs := make(map[string]struct{}, len(requests))
	for _, req := range requests {
		// Check for duplicate IDs (skip notifications which have empty IDs)
		if !req.ID.IsEmpty() {
			idKey := req.ID.String()
			if _, exists := seenIDs[idKey]; exists {
				logger.Error().Msg("❌ Duplicate ID found in batch request")
				return nil, fmt.Errorf("duplicate ID '%s' found in batch request - IDs must be unique for proper request-response correlation", idKey)
			}
			seenIDs[idKey] = struct{}{}
		}
		requestsMap[req.ID] = req
	}

	logger.Debug().Int("request_count", len(requests)).Msg("Parsed JSON-RPC request(s)")

	return requestsMap, nil
}
