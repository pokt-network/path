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

	// Try to unmarshal as batch (array) first - let encoding/json determine the format
	requests, isBatch, err := tryUnmarshalAsBatch(trimmedBody)
	if err == nil {
		return requests, isBatch, nil
	}

	// If batch unmarshaling failed, try as single request
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
func tryUnmarshalAsBatch(requestBody []byte) ([]Request, bool, error) {
	var requests []Request
	if err := json.Unmarshal(requestBody, &requests); err != nil {
		// This is expected for single requests - not an error to log
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

	// Convert to map and validate no duplicate IDs exist
	requestsMap := make(map[ID]Request)
	for _, req := range requests {
		// Check for duplicate IDs (skip notifications which have empty IDs)
		if !req.ID.IsEmpty() {
			if _, exists := requestsMap[req.ID]; exists {
				logger.Error().Msg("❌ Duplicate ID found in batch request")
				return nil, fmt.Errorf("duplicate ID '%s' found in batch request - IDs must be unique for proper request-response correlation", req.ID.String())
			}
		}
		requestsMap[req.ID] = req
	}

	logger.Debug().Int("request_count", len(requests)).Msg("Parsed JSON-RPC request(s)")

	return requestsMap, nil
}
