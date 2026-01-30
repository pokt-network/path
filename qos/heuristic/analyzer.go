package heuristic

import (
	"bytes"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
)

// ResponseAnalyzer implements the Analyzer interface using a tiered approach.
// It combines structural, protocol-specific, and indicator-based analysis
// to determine if a response is likely an error.
type ResponseAnalyzer struct {
	config AnalyzerConfig
}

// NewAnalyzer creates a new ResponseAnalyzer with the given configuration.
func NewAnalyzer(config AnalyzerConfig) *ResponseAnalyzer {
	return &ResponseAnalyzer{
		config: config,
	}
}

// NewDefaultAnalyzer creates a new ResponseAnalyzer with default configuration.
func NewDefaultAnalyzer() *ResponseAnalyzer {
	return NewAnalyzer(DefaultConfig())
}

// Analyze performs comprehensive heuristic analysis on a response.
// It uses a tiered approach, short-circuiting as soon as a definitive
// result is found:
//
//  1. HTTP Status Check (free) - 4xx/5xx always indicates error
//  2. Tier 1: Structural Analysis - Empty, HTML, non-JSON
//  3. Tier 2: Protocol Analysis - JSON-RPC result/error fields
//  4. Tier 3: Indicator Analysis - Error pattern matching
//
// Returns an AnalysisResult with retry recommendation and confidence.
func (ra *ResponseAnalyzer) Analyze(responseBytes []byte, httpStatusCode int, rpcType sharedtypes.RPCType) AnalysisResult {
	// Level 0: HTTP Status Code (free - no payload inspection)
	// Only return early for definitive server errors (5xx) and rate limits (429)
	// For other 4xx codes (400, 401, 403), we need to check if it's a valid JSON-RPC error
	statusResult := ra.analyzeHTTPStatus(httpStatusCode)
	if statusResult.ShouldRetry && (httpStatusCode >= 500 || httpStatusCode == 429) {
		// 5xx server errors and 429 rate limits are definitive - retry immediately
		return statusResult
	}

	// Level 1: Structural Analysis
	structResult := StructuralAnalysis(responseBytes)
	if structResult.ShouldRetry {
		return structResult
	}

	// If structure is not valid JSON, we're done
	if structResult.Structure != StructureValid {
		return structResult
	}

	// Get prefix for deeper analysis
	prefixLen := min(ra.config.MaxPrefixBytes, len(responseBytes))
	prefix := responseBytes[:prefixLen]

	// Level 2: Protocol-Specific Analysis
	protocolResult := ProtocolAnalysis(prefix, len(responseBytes), rpcType)

	// Special handling for HTTP 4xx: If protocol detects valid JSON-RPC response, trust it
	// This prevents retrying valid error responses like {"jsonrpc":"2.0","error":{"code":-32600,"message":"invalid request"}}
	if httpStatusCode >= 400 && httpStatusCode < 500 && httpStatusCode != 429 {
		if protocolResult.Reason == "jsonrpc_valid_error" || protocolResult.Reason == "jsonrpc_success" {
			// Valid JSON-RPC response despite 4xx status - return to client (don't retry)
			return protocolResult
		}
		// If not a valid JSON-RPC response, trust the HTTP status (malformed/invalid request)
		return statusResult
	}

	// CRITICAL: If protocol analysis has high confidence (positive or negative), trust it
	// This prevents indicator analysis from overriding valid JSON-RPC error detection
	if protocolResult.Confidence >= ra.config.ConfidenceThreshold {
		return protocolResult
	}

	// If protocol says it's a valid JSON-RPC error, we need to check if it contains
	// blockchain-specific error patterns that indicate node/supplier issues.
	// These errors (like "missing trie node", "node is unhealthy") SHOULD be retried
	// on a different supplier, even though they're valid JSON-RPC error responses.
	if protocolResult.Reason == "jsonrpc_valid_error" {
		prefixLower := bytes.ToLower(prefix)
		indicatorResult := IndicatorAnalysis(prefixLower, true)
		// If we detect a blockchain-specific error, it's a supplier issue - retry
		if indicatorResult.Found && indicatorResult.Category == CategoryBlockchainError {
			return IndicatorAnalysisToResult(indicatorResult)
		}
		// Otherwise, trust the protocol analysis (valid error, don't retry)
		return protocolResult
	}

	// For successful JSON-RPC responses, no need for indicator analysis
	if protocolResult.Reason == "jsonrpc_success" {
		return protocolResult
	}

	// Level 3: Error Indicator Analysis (if enabled)
	// Only run this if protocol analysis was inconclusive
	if ra.config.EnableTier3 {
		prefixLower := bytes.ToLower(prefix)
		indicatorResult := IndicatorAnalysis(prefixLower, true)
		if indicatorResult.Found && indicatorResult.Confidence >= ra.config.ConfidenceThreshold {
			return IndicatorAnalysisToResult(indicatorResult)
		}
	}

	// No error indicators found - likely a valid response
	return AnalysisResult{
		ShouldRetry: false,
		Confidence:  0.0,
		Reason:      "likely_valid",
		Structure:   StructureValid,
		Details:     "No error indicators detected across all tiers",
	}
}

// analyzeHTTPStatus checks the HTTP status code for error conditions.
func (ra *ResponseAnalyzer) analyzeHTTPStatus(statusCode int) AnalysisResult {
	// 4xx Client Errors
	if statusCode >= 400 && statusCode < 500 {
		return AnalysisResult{
			ShouldRetry: true,
			Confidence:  1.0,
			Reason:      "http_4xx",
			Structure:   StructureValid,
			Details:     "HTTP 4xx client error status code",
		}
	}

	// 5xx Server Errors
	if statusCode >= 500 {
		return AnalysisResult{
			ShouldRetry: true,
			Confidence:  1.0,
			Reason:      "http_5xx",
			Structure:   StructureValid,
			Details:     "HTTP 5xx server error status code",
		}
	}

	// Not an error status
	return AnalysisResult{
		ShouldRetry: false,
		Confidence:  0.0,
		Reason:      "http_success",
		Structure:   StructureValid,
		Details:     "HTTP status code indicates success",
	}
}

// AnalyzeQuick performs a fast heuristic check with minimal processing.
// Use this when you need maximum speed and can accept lower accuracy.
//
// This performs:
//  1. HTTP status check
//  2. Empty/first-byte check
//  3. Quick error pattern scan
//
// Cost: O(n) single pass on prefix
// Time: ~2-5μs
func (ra *ResponseAnalyzer) AnalyzeQuick(responseBytes []byte, httpStatusCode int) AnalysisResult {
	// HTTP Status
	if httpStatusCode >= 400 {
		return AnalysisResult{
			ShouldRetry: true,
			Confidence:  1.0,
			Reason:      "http_error",
			Structure:   StructureValid,
		}
	}

	// Empty
	if len(responseBytes) == 0 {
		return AnalysisResult{
			ShouldRetry: true,
			Confidence:  1.0,
			Reason:      "empty",
			Structure:   StructureEmpty,
		}
	}

	// Non-JSON
	firstByte := responseBytes[0]
	if firstByte != '{' && firstByte != '[' && firstByte != ' ' && firstByte != '\n' {
		return AnalysisResult{
			ShouldRetry: true,
			Confidence:  0.9,
			Reason:      "non_json",
			Structure:   StructureNonJSON,
		}
	}

	// Quick pattern check
	prefixLen := min(256, len(responseBytes))
	prefixLower := bytes.ToLower(responseBytes[:prefixLen])
	if QuickErrorCheck(prefixLower) {
		return AnalysisResult{
			ShouldRetry: true,
			Confidence:  0.75,
			Reason:      "quick_error_pattern",
			Structure:   StructureValid,
		}
	}

	return AnalysisResult{
		ShouldRetry: false,
		Confidence:  0.0,
		Reason:      "quick_pass",
		Structure:   StructureValid,
	}
}

// ShouldRetry is a convenience method that returns just the retry decision.
func (ra *ResponseAnalyzer) ShouldRetry(responseBytes []byte, httpStatusCode int, rpcType sharedtypes.RPCType) bool {
	result := ra.Analyze(responseBytes, httpStatusCode, rpcType)
	return result.ShouldRetry
}

// Package-level convenience functions using default analyzer

var defaultAnalyzer = NewDefaultAnalyzer()

// Analyze performs heuristic analysis using the default analyzer.
func Analyze(responseBytes []byte, httpStatusCode int, rpcType sharedtypes.RPCType) AnalysisResult {
	return defaultAnalyzer.Analyze(responseBytes, httpStatusCode, rpcType)
}

// ShouldRetry returns the retry recommendation using the default analyzer.
func ShouldRetry(responseBytes []byte, httpStatusCode int, rpcType sharedtypes.RPCType) bool {
	return defaultAnalyzer.ShouldRetry(responseBytes, httpStatusCode, rpcType)
}

// AnalyzeQuick performs a fast heuristic check using the default analyzer.
func AnalyzeQuick(responseBytes []byte, httpStatusCode int) AnalysisResult {
	return defaultAnalyzer.AnalyzeQuick(responseBytes, httpStatusCode)
}
