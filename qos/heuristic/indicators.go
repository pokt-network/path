package heuristic

import (
	"bytes"
)

// Tier 3: Error Indicator Pattern Detection
//
// This is the catch-all tier that looks for common error patterns
// in response content. Unlike Tier 1 (structural) and Tier 2 (protocol),
// this tier does content-based pattern matching.
//
// Patterns are organized by category:
//   - HTTP error messages (gateway errors, server errors)
//   - Connection/network errors
//   - Rate limiting indicators
//   - Authentication/authorization errors
//   - Blockchain-specific errors
//
// All patterns are matched case-insensitively for robustness.

// ErrorCategory represents the type of error detected.
type ErrorCategory int

const (
	// CategoryNone indicates no error pattern was detected.
	CategoryNone ErrorCategory = iota
	// CategoryHTTPError indicates an HTTP-level error message.
	CategoryHTTPError
	// CategoryConnectionError indicates a connection/network error.
	CategoryConnectionError
	// CategoryRateLimit indicates rate limiting.
	CategoryRateLimit
	// CategoryAuthError indicates authentication/authorization error.
	CategoryAuthError
	// CategoryServiceError indicates a service-level error.
	CategoryServiceError
	// CategoryBlockchainError indicates a blockchain-specific error.
	CategoryBlockchainError
	// CategoryProtocolError indicates a protocol-level error.
	CategoryProtocolError
)

// String returns a human-readable representation of the ErrorCategory.
func (ec ErrorCategory) String() string {
	switch ec {
	case CategoryNone:
		return "none"
	case CategoryHTTPError:
		return "http_error"
	case CategoryConnectionError:
		return "connection_error"
	case CategoryRateLimit:
		return "rate_limit"
	case CategoryAuthError:
		return "auth_error"
	case CategoryServiceError:
		return "service_error"
	case CategoryBlockchainError:
		return "blockchain_error"
	case CategoryProtocolError:
		return "protocol_error"
	default:
		return "unknown"
	}
}

// errorPattern defines a pattern to match and its associated category/confidence.
type errorPattern struct {
	pattern    []byte
	category   ErrorCategory
	confidence float64
}

// errorPatterns contains all known error patterns, organized for efficient matching.
// Patterns are in lowercase for case-insensitive matching.
var errorPatterns = []errorPattern{
	// HTTP Gateway Errors (very high confidence - these are definitive)
	{[]byte("bad gateway"), CategoryHTTPError, 0.95},
	{[]byte("502 bad gateway"), CategoryHTTPError, 0.98},
	{[]byte("503 service unavailable"), CategoryHTTPError, 0.98},
	{[]byte("504 gateway timeout"), CategoryHTTPError, 0.98},
	{[]byte("500 internal server error"), CategoryHTTPError, 0.98},
	{[]byte("internal server error"), CategoryHTTPError, 0.90},
	{[]byte("service unavailable"), CategoryHTTPError, 0.90},
	{[]byte("gateway timeout"), CategoryHTTPError, 0.90},

	// Connection Errors (from error_classification.go patterns)
	{[]byte("connection refused"), CategoryConnectionError, 0.95},
	{[]byte("connection reset"), CategoryConnectionError, 0.95},
	{[]byte("no route to host"), CategoryConnectionError, 0.95},
	{[]byte("network is unreachable"), CategoryConnectionError, 0.95},
	{[]byte("no such host"), CategoryConnectionError, 0.95},
	{[]byte("dial tcp"), CategoryConnectionError, 0.85},
	{[]byte("write tcp"), CategoryConnectionError, 0.85},
	{[]byte("read tcp"), CategoryConnectionError, 0.85},
	{[]byte("broken pipe"), CategoryConnectionError, 0.90},
	{[]byte("unexpected eof"), CategoryConnectionError, 0.85},
	{[]byte("server closed"), CategoryConnectionError, 0.80},
	{[]byte("tls handshake"), CategoryConnectionError, 0.85},
	{[]byte("certificate"), CategoryConnectionError, 0.70}, // May be cert error

	// Rate Limiting
	{[]byte("rate limit"), CategoryRateLimit, 0.95},
	{[]byte("rate-limit"), CategoryRateLimit, 0.95},
	{[]byte("ratelimit"), CategoryRateLimit, 0.95},
	{[]byte("too many request"), CategoryRateLimit, 0.95},
	{[]byte("quota exceeded"), CategoryRateLimit, 0.90},
	{[]byte("throttl"), CategoryRateLimit, 0.85}, // throttle, throttled, throttling

	// Authentication/Authorization Errors
	{[]byte("unauthorized"), CategoryAuthError, 0.90},
	{[]byte("forbidden"), CategoryAuthError, 0.90},
	{[]byte("access denied"), CategoryAuthError, 0.90},
	{[]byte("invalid api key"), CategoryAuthError, 0.95},
	{[]byte("authentication failed"), CategoryAuthError, 0.95},
	{[]byte("not authorized"), CategoryAuthError, 0.90},

	// Service Errors (from error_classification.go)
	{[]byte("service not configured"), CategoryServiceError, 0.95},
	{[]byte("service endpoint not handled"), CategoryServiceError, 0.95},
	{[]byte("backend service"), CategoryServiceError, 0.80},
	{[]byte("upstream error"), CategoryServiceError, 0.85},
	{[]byte("proxy error"), CategoryServiceError, 0.85},
	{[]byte("supplier"), CategoryServiceError, 0.60}, // May indicate supplier issue

	// Protocol Errors
	{[]byte("proto: illegal wiretype"), CategoryProtocolError, 0.95},
	{[]byte("proto: relayrequest"), CategoryProtocolError, 0.95},
	{[]byte("malformed"), CategoryProtocolError, 0.80},
	{[]byte("invalid request"), CategoryProtocolError, 0.75},
	{[]byte("parse error"), CategoryProtocolError, 0.80},
	{[]byte("invalid json"), CategoryProtocolError, 0.85},
	{[]byte("syntax error"), CategoryProtocolError, 0.85},

	// Blockchain-Specific Errors (EVM)
	{[]byte("missing trie node"), CategoryBlockchainError, 0.95},
	{[]byte("state has been pruned"), CategoryBlockchainError, 0.95},
	{[]byte("state not available"), CategoryBlockchainError, 0.90},
	{[]byte("block not found"), CategoryBlockchainError, 0.85},
	{[]byte("transaction not found"), CategoryBlockchainError, 0.80},
	{[]byte("header not found"), CategoryBlockchainError, 0.85},
	{[]byte("nonce too low"), CategoryBlockchainError, 0.80},
	{[]byte("nonce too high"), CategoryBlockchainError, 0.80},
	{[]byte("insufficient funds"), CategoryBlockchainError, 0.85},
	{[]byte("gas too low"), CategoryBlockchainError, 0.85},
	{[]byte("execution reverted"), CategoryBlockchainError, 0.80},

	// Blockchain-Specific Errors (Solana)
	{[]byte("slot skipped"), CategoryBlockchainError, 0.85},
	{[]byte("slot too old"), CategoryBlockchainError, 0.85},
	{[]byte("block not available"), CategoryBlockchainError, 0.85},
	{[]byte("node is behind"), CategoryBlockchainError, 0.90},
	{[]byte("node is unhealthy"), CategoryBlockchainError, 0.95},

	// Blockchain-Specific Errors (Cosmos)
	{[]byte("block has been pruned"), CategoryBlockchainError, 0.95},
	{[]byte("height is not available"), CategoryBlockchainError, 0.90},
	{[]byte("validator set is nil"), CategoryBlockchainError, 0.85},
}

// IndicatorResult contains the result of error indicator analysis.
type IndicatorResult struct {
	// Found indicates whether an error pattern was detected.
	Found bool
	// Category is the type of error detected.
	Category ErrorCategory
	// Pattern is the specific pattern that matched.
	Pattern string
	// Confidence is how confident we are this indicates an error.
	Confidence float64
}

// IndicatorAnalysis performs Tier 3 error indicator pattern matching.
// It searches for known error patterns in the response content.
//
// Parameters:
//   - content: Response content to analyze (should be lowercased for efficiency)
//   - alreadyLowercased: Set to true if content is already lowercase
//
// Cost: O(n*m) where n is content length and m is number of patterns
// Time: ~3-10μs for 512 byte content
func IndicatorAnalysis(content []byte, alreadyLowercased bool) IndicatorResult {
	// Convert to lowercase if not already
	searchContent := content
	if !alreadyLowercased {
		searchContent = bytes.ToLower(content)
	}

	var bestMatch IndicatorResult

	// Search for each pattern
	for _, ep := range errorPatterns {
		if bytes.Contains(searchContent, ep.pattern) {
			// Found a match - keep the highest confidence one
			if ep.confidence > bestMatch.Confidence {
				bestMatch = IndicatorResult{
					Found:      true,
					Category:   ep.category,
					Pattern:    string(ep.pattern),
					Confidence: ep.confidence,
				}
			}
		}
	}

	return bestMatch
}

// IndicatorAnalysisToResult converts an IndicatorResult to an AnalysisResult.
func IndicatorAnalysisToResult(ir IndicatorResult) AnalysisResult {
	if !ir.Found {
		return AnalysisResult{
			ShouldRetry: false,
			Confidence:  0.0,
			Reason:      "no_error_indicators",
			Structure:   StructureValid,
			Details:     "No error indicator patterns detected",
		}
	}

	return AnalysisResult{
		ShouldRetry: true,
		Confidence:  ir.Confidence,
		Reason:      "error_indicator_" + ir.Category.String(),
		Structure:   StructureValid,
		Details:     "Detected error pattern: " + ir.Pattern,
	}
}

// QuickErrorCheck performs a fast check for obvious error indicators.
// This is a simplified version that only checks the most common patterns.
// Use this when you need maximum speed and can tolerate some false negatives.
//
// Cost: O(n) single pass
// Time: ~1-2μs for 512 byte content
func QuickErrorCheck(contentLower []byte) bool {
	// Most common error patterns - these catch ~80% of errors
	quickPatterns := [][]byte{
		[]byte("bad gateway"),
		[]byte("internal server"),
		[]byte("service unavailable"),
		[]byte("connection refused"),
		[]byte("rate limit"),
		[]byte("unauthorized"),
		[]byte("error"),
	}

	for _, pattern := range quickPatterns {
		if bytes.Contains(contentLower, pattern) {
			return true
		}
	}

	return false
}
