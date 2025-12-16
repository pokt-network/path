package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
)

// Standard HTTP header name for explicit RPC type specification.
// No X- prefix (deprecated by RFC 6648).
// Clients can send this header to bypass detection and improve performance.
const RPCTypeHeader = "RPC-Type"

// Maximum bytes to read from request body for RPC type detection.
// This prevents memory exhaustion from large payloads.
const maxPayloadInspectionBytes = 100 * 1024 // 100KB

// RPCTypeDetector provides smart RPC type detection from HTTP requests.
// It uses a multi-step approach to minimize latency:
//  1. Check RPC-Type header (fastest, explicit)
//  2. Easy detection from request properties (websocket, grpc)
//  3. Process of elimination based on service config
//  4. Payload inspection (only when absolutely necessary)
type RPCTypeDetector struct {
	mapper *RPCTypeMapper
}

// NewRPCTypeDetector creates a new RPC type detector.
func NewRPCTypeDetector() *RPCTypeDetector {
	return &RPCTypeDetector{
		mapper: NewRPCTypeMapper(),
	}
}

// DetectRPCType detects the RPC type from an HTTP request.
// It uses smart elimination to avoid payload inspection when possible.
//
// Parameters:
//   - httpReq: The incoming HTTP request
//   - serviceID: Service identifier (for error messages)
//   - serviceRPCTypes: List of RPC types supported by the service (e.g., ["json_rpc", "websocket"])
//
// Returns the detected RPC type or an error if:
//   - RPC-Type header is invalid or not supported by service
//   - Detection is ambiguous and payload inspection fails
//   - Service doesn't support the detected RPC type
func (d *RPCTypeDetector) DetectRPCType(
	httpReq *http.Request,
	serviceID string,
	serviceRPCTypes []string,
) (sharedtypes.RPCType, error) {
	// Step 1: Check RPC-Type header (preferred path for performance)
	if rpcType, ok, err := d.checkRPCTypeHeader(httpReq, serviceID, serviceRPCTypes); err != nil {
		return sharedtypes.RPCType_UNKNOWN_RPC, err
	} else if ok {
		return rpcType, nil
	}

	// Step 2: Easy detection from request properties (no payload inspection needed)
	if rpcType, ok := d.easyDetection(httpReq); ok {
		// Validate detected type is in service's allowed types
		if !d.isRPCTypeAllowed(rpcType, serviceRPCTypes) {
			return sharedtypes.RPCType_UNKNOWN_RPC, fmt.Errorf(
				"detected RPC type '%s' not supported by service '%s'. Allowed types: %v",
				d.mapper.FormatRPCType(rpcType), serviceID, serviceRPCTypes,
			)
		}
		return rpcType, nil
	}

	// Step 3: Process of elimination (optimize for common case)
	if rpcType, ok := d.processOfElimination(serviceRPCTypes); ok {
		// Successfully eliminated to single HTTP type
		return rpcType, nil
	}

	// Step 4: Payload inspection (last resort - only when multiple HTTP types remain)
	// This is expensive but necessary when service supports multiple conflicting types
	return d.inspectPayload(httpReq, serviceID, serviceRPCTypes)
}

// checkRPCTypeHeader checks the RPC-Type header for explicit type specification.
// Returns (rpcType, true, nil) if header is valid and allowed.
// Returns (_, false, nil) if header is not present (continue to next step).
// Returns (_, _, error) if header is invalid or not allowed (fail fast).
func (d *RPCTypeDetector) checkRPCTypeHeader(
	httpReq *http.Request,
	serviceID string,
	serviceRPCTypes []string,
) (sharedtypes.RPCType, bool, error) {
	rpcTypeHeader := httpReq.Header.Get(RPCTypeHeader)
	if rpcTypeHeader == "" {
		return sharedtypes.RPCType_UNKNOWN_RPC, false, nil
	}

	// Parse and validate header value
	rpcType, err := d.mapper.ParseRPCType(rpcTypeHeader)
	if err != nil {
		// FAIL FAST: Invalid RPC type in header
		return sharedtypes.RPCType_UNKNOWN_RPC, false, fmt.Errorf(
			"invalid %s header value '%s': %w. Allowed types for service '%s': %v",
			RPCTypeHeader, rpcTypeHeader, err, serviceID, serviceRPCTypes,
		)
	}

	// Validate against service's allowed types
	if !d.isRPCTypeAllowed(rpcType, serviceRPCTypes) {
		// FAIL FAST: RPC type not supported by service
		return sharedtypes.RPCType_UNKNOWN_RPC, false, fmt.Errorf(
			"RPC type '%s' from %s header not supported by service '%s'. Allowed types: %v",
			rpcTypeHeader, RPCTypeHeader, serviceID, serviceRPCTypes,
		)
	}

	return rpcType, true, nil
}

// easyDetection detects RPC types that can be identified from request properties
// without inspecting the payload (websocket, grpc).
// Returns (rpcType, true) if detected, (_, false) otherwise.
func (d *RPCTypeDetector) easyDetection(httpReq *http.Request) (sharedtypes.RPCType, bool) {
	// Check for WebSocket upgrade
	if isWebSocketUpgrade(httpReq) {
		return sharedtypes.RPCType_WEBSOCKET, true
	}

	// Check for gRPC content type
	if isGRPCRequest(httpReq) {
		return sharedtypes.RPCType_GRPC, true
	}

	return sharedtypes.RPCType_UNKNOWN_RPC, false
}

// processOfElimination uses the service's rpc_types config to eliminate possibilities.
// This is optimized for the most common case: services with ["json_rpc", "websocket"].
// Returns (rpcType, true) if successfully eliminated to single HTTP type.
// Returns (_, false) if multiple HTTP types remain (need payload inspection).
func (d *RPCTypeDetector) processOfElimination(serviceRPCTypes []string) (sharedtypes.RPCType, bool) {
	// Filter to only HTTP-based types (exclude websocket, grpc already checked in step 2)
	httpTypes := make([]string, 0, len(serviceRPCTypes))
	for _, rpcTypeStr := range serviceRPCTypes {
		if IsHTTPBasedRPCType(rpcTypeStr) {
			httpTypes = append(httpTypes, rpcTypeStr)
		}
	}

	// If only one HTTP type remains, use it!
	// Common case: service has ["json_rpc", "websocket"]
	// After filtering out websocket, only "json_rpc" remains.
	if len(httpTypes) == 1 {
		rpcType, err := d.mapper.ParseRPCType(httpTypes[0])
		if err != nil {
			// Should never happen - service config already validated
			return sharedtypes.RPCType_UNKNOWN_RPC, false
		}
		return rpcType, true
	}

	// If no HTTP types remain, this is an error condition
	// Service only supports websocket/grpc, but request is HTTP
	if len(httpTypes) == 0 {
		return sharedtypes.RPCType_UNKNOWN_RPC, false
	}

	// Multiple HTTP types remain - need payload inspection
	return sharedtypes.RPCType_UNKNOWN_RPC, false
}

// inspectPayload reads the request body to determine RPC type.
// This is the last resort when we can't determine type from other signals.
// Only called when service has multiple HTTP-based types (e.g., ["json_rpc", "rest", "comet_bft"]).
func (d *RPCTypeDetector) inspectPayload(
	httpReq *http.Request,
	serviceID string,
	serviceRPCTypes []string,
) (sharedtypes.RPCType, error) {
	// Read body with size limit
	body, err := readBodySafely(httpReq, maxPayloadInspectionBytes)
	if err != nil {
		return sharedtypes.RPCType_UNKNOWN_RPC, fmt.Errorf(
			"failed to read request body for RPC type detection: %w", err,
		)
	}

	// Restore body for downstream handlers
	httpReq.Body = io.NopCloser(bytes.NewReader(body))

	// Try JSON-RPC detection first (most common)
	if hasJSONRPCStructure(body) {
		// Check if it's a CometBFT method (if comet_bft is supported)
		if d.isTypeAllowed(RPCTypeStringCometBFT, serviceRPCTypes) {
			method := extractJSONRPCMethod(body)
			if isCometBFTMethod(method) {
				rpcType, _ := d.mapper.ParseRPCType(RPCTypeStringCometBFT)
				return rpcType, nil
			}
		}

		// Standard JSON-RPC
		if d.isTypeAllowed(RPCTypeStringJSONRPC, serviceRPCTypes) {
			rpcType, _ := d.mapper.ParseRPCType(RPCTypeStringJSONRPC)
			return rpcType, nil
		}
	}

	// Check URL path patterns for REST vs CometBFT
	if isCometBFTPath(httpReq.URL.Path) {
		if d.isTypeAllowed(RPCTypeStringCometBFT, serviceRPCTypes) {
			rpcType, _ := d.mapper.ParseRPCType(RPCTypeStringCometBFT)
			return rpcType, nil
		}
	}

	// Default to REST for HTTP requests
	if d.isTypeAllowed(RPCTypeStringREST, serviceRPCTypes) {
		rpcType, _ := d.mapper.ParseRPCType(RPCTypeStringREST)
		return rpcType, nil
	}

	// Could not determine RPC type
	return sharedtypes.RPCType_UNKNOWN_RPC, fmt.Errorf(
		"unable to detect RPC type for service '%s'. Service supports: %v. "+
			"Consider using %s header for explicit type specification",
		serviceID, serviceRPCTypes, RPCTypeHeader,
	)
}

// isRPCTypeAllowed checks if an RPC type enum is in the service's allowed types list.
func (d *RPCTypeDetector) isRPCTypeAllowed(rpcType sharedtypes.RPCType, serviceRPCTypes []string) bool {
	rpcTypeStr := d.mapper.FormatRPCType(rpcType)
	return d.isTypeAllowed(rpcTypeStr, serviceRPCTypes)
}

// isTypeAllowed checks if a string RPC type is in the allowed list.
func (d *RPCTypeDetector) isTypeAllowed(rpcTypeStr string, allowedTypes []string) bool {
	for _, allowed := range allowedTypes {
		if strings.EqualFold(allowed, rpcTypeStr) {
			return true
		}
	}
	return false
}

// Helper functions for request analysis

// isWebSocketUpgrade checks if the request is a WebSocket upgrade.
func isWebSocketUpgrade(req *http.Request) bool {
	return strings.ToLower(req.Header.Get("Upgrade")) == "websocket"
}

// isGRPCRequest checks if the request uses gRPC content type.
func isGRPCRequest(req *http.Request) bool {
	contentType := req.Header.Get("Content-Type")
	return strings.HasPrefix(contentType, "application/grpc")
}

// readBodySafely reads up to maxBytes from the request body.
func readBodySafely(req *http.Request, maxBytes int64) ([]byte, error) {
	if req.Body == nil {
		return []byte{}, nil
	}

	limitedReader := io.LimitReader(req.Body, maxBytes)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, err
	}

	return body, nil
}

// hasJSONRPCStructure checks if the body contains JSON-RPC structure.
func hasJSONRPCStructure(body []byte) bool {
	if len(body) == 0 {
		return false
	}

	// Quick check for JSON-RPC fields
	bodyStr := string(body)
	return strings.Contains(bodyStr, `"jsonrpc"`) ||
		strings.Contains(bodyStr, `"method"`) ||
		strings.Contains(bodyStr, `"id"`)
}

// extractJSONRPCMethod extracts the method field from a JSON-RPC request.
func extractJSONRPCMethod(body []byte) string {
	var payload struct {
		Method string `json:"method"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}

	return payload.Method
}

// isCometBFTMethod checks if a method name is a CometBFT RPC method.
// CometBFT methods typically match patterns like: abci_*, block*, broadcast_*, consensus_*, etc.
func isCometBFTMethod(method string) bool {
	// CometBFT method prefixes
	cometBFTPrefixes := []string{
		"abci_",
		"block",
		"broadcast_",
		"consensus_",
		"commit",
		"genesis",
		"health",
		"net_",
		"status",
		"subscribe",
		"tx",
		"unconfirmed_",
		"validators",
	}

	methodLower := strings.ToLower(method)
	for _, prefix := range cometBFTPrefixes {
		if strings.HasPrefix(methodLower, prefix) {
			return true
		}
	}

	return false
}

// isCometBFTPath checks if a URL path indicates a CometBFT RPC endpoint.
// CometBFT paths typically look like: /block, /status, /health, etc.
func isCometBFTPath(path string) bool {
	// CometBFT path patterns
	cometBFTPaths := []string{
		"/abci_",
		"/block",
		"/broadcast_",
		"/commit",
		"/consensus_",
		"/genesis",
		"/health",
		"/net_",
		"/status",
		"/subscribe",
		"/tx",
		"/unconfirmed_",
		"/validators",
	}

	pathLower := strings.ToLower(path)
	for _, pattern := range cometBFTPaths {
		if strings.HasPrefix(pathLower, pattern) {
			return true
		}
	}

	return false
}
