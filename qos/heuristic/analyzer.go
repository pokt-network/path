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
	if result := ra.analyzeHTTPStatus(httpStatusCode); result.ShouldRetry {
		return result
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
	if protocolResult.ShouldRetry && protocolResult.Confidence >= ra.config.ConfidenceThreshold {
		return protocolResult
	}

	// Level 3: Error Indicator Analysis (if enabled)
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
