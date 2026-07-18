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
//
// Duplicate IDs are intentionally NOT rejected. Public RPC nodes accept and
// serve batches with repeated IDs (and cross-type IDs such as 1 and "1"), so
// PATH mirrors that behavior and stays a transparent pass-through — rejecting
// them made PATH stricter than the JSON-RPC 2.0 spec (which does not require a
// server to reject duplicates) and stricter than the very nodes it fronts.
// Response correlation is best-effort by ID; a client that reuses IDs owns the
// resulting ambiguity, exactly as it would talking to a node directly. Each
// parsed request carries its own ID pointer, so same-value IDs remain distinct
// map keys and every payload is preserved (N requests → N relays → N responses).
//
// An empty batch is still rejected per the JSON-RPC 2.0 spec.
func validateAndMapRequests(logger polylog.Logger, requests []Request) (map[ID]Request, error) {
	// Validate batch is not empty (per JSON-RPC spec)
	if len(requests) == 0 {
		logger.Error().
			Msg("❌ Empty batch request not allowed per JSON-RPC specification")
		return nil, fmt.Errorf("empty batch request not allowed")
	}

	requestsMap := make(map[ID]Request, len(requests))
	for _, req := range requests {
		requestsMap[req.ID] = req
	}

	logger.Debug().Int("request_count", len(requests)).Msg("Parsed JSON-RPC request(s)")

	return requestsMap, nil
}
