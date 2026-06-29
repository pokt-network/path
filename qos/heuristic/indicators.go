package heuristic

import (
	"bytes"
	"strings"
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
	// Relay-miner over-servicing (per-app stake exhausted) — must take precedence
	// over generic rate-limit patterns so MatchedPattern carries the specific
	// phrase and downstream handlers can route to IsOverServicedError().
	// Confidence 0.99 ensures these win over the 0.95 generic patterns above.
	{[]byte("offchain rate limit hit by relayer proxy"), CategoryRateLimit, 0.99},
	{[]byte("session relay limit reached"), CategoryRateLimit, 0.99},
	{[]byte("claimable portion fully consumed"), CategoryRateLimit, 0.99},

	// Authentication/Authorization Errors
	{[]byte("unauthorized"), CategoryAuthError, 0.90},
	{[]byte("forbidden"), CategoryAuthError, 0.90},
	{[]byte("access denied"), CategoryAuthError, 0.90},
	{[]byte("invalid api key"), CategoryAuthError, 0.95},
	{[]byte("authentication failed"), CategoryAuthError, 0.95},
	{[]byte("not authorized"), CategoryAuthError, 0.90},
	// Provider-side key rejection: the supplier proxies to a third-party RPC
	// provider (e.g. QuickNode emits "API key is not allowed to access
	// blockchain") whose key isn't authorized for this chain. Persistent
	// supplier fault — see IsProviderAuthError.
	{[]byte("api key is not allowed"), CategoryAuthError, 0.95},

	// Service Errors (from error_classification.go)
	{[]byte("service not configured"), CategoryServiceError, 0.95},
	{[]byte("service endpoint not handled"), CategoryServiceError, 0.95},
	{[]byte("backend service"), CategoryServiceError, 0.80},
	{[]byte("upstream error"), CategoryServiceError, 0.85},
	{[]byte("proxy error"), CategoryServiceError, 0.85},
	{[]byte("supplier"), CategoryServiceError, 0.60}, // May indicate supplier issue

	// Protocol Errors
	// NOTE: Protocol-level errors from Shannon/gRPC, NOT JSON-RPC error messages
	{[]byte("proto: illegal wiretype"), CategoryProtocolError, 0.95},
	{[]byte("proto: relayrequest"), CategoryProtocolError, 0.95},

	// REMOVED GENERIC ERROR PATTERNS that appear in valid JSON-RPC error responses:
	// - "invalid request" - Appears in JSON-RPC -32600 errors (valid client error)
	// - "parse error" - Appears in JSON-RPC -32700 errors
	// - "invalid json" - Too generic, matches both errors and valid responses
	// - "syntax error" - Too generic
	// - "malformed" - Too generic
	//
	// These patterns were causing false positives where valid JSON-RPC error responses
	// (e.g., {"jsonrpc":"2.0","error":{"code":-32600,"message":"invalid request"}})
	// were being classified as supplier errors and triggering unnecessary retries.
	//
	// The protocol analyzer now properly handles JSON-RPC error detection.

	// Blockchain-Specific Errors (EVM)
	// ONLY include errors that indicate supplier/node problems, NOT application-level errors
	{[]byte("mdbx_panic"), CategoryBlockchainError, 0.98},                 // Erigon MDBX database corruption/disk full
	{[]byte("missing trie node"), CategoryBlockchainError, 0.95},          // Data corruption/sync issue
	{[]byte("failed to call fallback"), CategoryBlockchainError, 0.95},    // Node's internal fallback for archival data failed
	{[]byte("state has been pruned"), CategoryBlockchainError, 0.95},      // Archival data not available
	{[]byte("is pruned"), CategoryBlockchainError, 0.95},                  // Generic pruned error (e.g., "state at block #X is pruned")
	{[]byte("state not available"), CategoryBlockchainError, 0.90},        // Node sync issue
	{[]byte("haven't been fully indexed"), CategoryBlockchainError, 0.95}, // Archival indexing not complete (BSC)
	{[]byte("not been fully indexed"), CategoryBlockchainError, 0.95},     // Archival indexing not complete (variant)
	{[]byte("historical state"), CategoryBlockchainError, 0.85},           // Historical state not available

	// Blockchain-Specific Errors (Solana)
	{[]byte("node is behind"), CategoryBlockchainError, 0.90},    // Sync issue
	{[]byte("node is unhealthy"), CategoryBlockchainError, 0.95}, // Health issue

	// Blockchain-Specific Errors (Cosmos)
	{[]byte("block has been pruned"), CategoryBlockchainError, 0.95},   // Archival issue
	{[]byte("height is not available"), CategoryBlockchainError, 0.90}, // Sync issue

	// REMOVED APPLICATION-LEVEL ERRORS (valid JSON-RPC errors that should be returned to client):
	// - "block not found" - Often a valid response (block doesn't exist, future block, or user error)
	// - "transaction not found" - Often a valid response (transaction doesn't exist)
	// - "header not found" - Often a valid response
	// - "nonce too low/high" - Client error, not supplier error
	// - "insufficient funds" - Client error, not supplier error
	// - "gas too low" - Client error, not supplier error
	// - "execution reverted" - Smart contract rejection, not supplier error
	// - "slot skipped" - Normal Solana behavior, not an error
	// - "slot too old" - Often a valid response
	// - "block not available" - Similar to "block not found"
	// - "validator set is nil" - Often a valid response for certain queries
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
		ShouldRetry:    true,
		Confidence:     ir.Confidence,
		Reason:         "error_indicator_" + ir.Category.String(),
		Structure:      StructureValid,
		Details:        "Detected error pattern: " + ir.Pattern,
		MatchedPattern: ir.Pattern,
	}
}

// IsArchivalRelatedError returns true if the indicator pattern is an archival/pruning error.
// These errors are expected from non-archival nodes and should NOT trigger domain-level
// circuit breaking — the domain isn't broken, it just doesn't have historical state.
// Retrying on a different (archival-capable) supplier is correct, but punishing the
// domain for a capability mismatch causes death spirals where all domains get locked out.
func IsArchivalRelatedError(pattern string) bool {
	switch pattern {
	case "historical state",
		"state has been pruned",
		"is pruned",
		"state not available",
		"haven't been fully indexed",
		"not been fully indexed",
		"missing trie node",
		"block has been pruned",
		"height is not available":
		return true
	default:
		return false
	}
}

// IsCapabilityLimitationError returns true if the pattern indicates a node capability
// limitation rather than a broken domain. These include archival errors AND other
// capability mismatches like Tron lite fullnodes that can't serve certain API calls.
// The request should retry on a different supplier but should NOT circuit-break the domain.
func IsCapabilityLimitationError(pattern string) bool {
	if IsArchivalRelatedError(pattern) {
		return true
	}
	switch pattern {
	case "capability_limitation": // Tron lite fullnode, "api not supported" plain text responses
		return true
	default:
		return false
	}
}

// providerAuthErrorPatterns are the matched substrings that indicate the supplier's
// UPSTREAM RPC provider rejected the supplier's API key. Single source of truth for
// both IsProviderAuthError (matched against AnalysisResult.MatchedPattern) and the
// plain-text detector in structural.go. All lower-cased.
var providerAuthErrorPatterns = []string{
	"api key is not allowed", // QuickNode: "API key is not allowed to access blockchain"
	"invalid api key",
}

// IsProviderAuthError reports whether the matched pattern indicates the supplier's
// UPSTREAM RPC provider rejected the supplier's API key (e.g. a supplier proxying
// to QuickNode whose key isn't authorized for this chain — "API key is not allowed
// to access blockchain"). Unlike a capability limitation, this is a persistent
// supplier-side fault: every relay to this supplier on this service fails the same
// way until the operator fixes their provider config. Callers should both retry on
// a different supplier AND feed the strike/cooldown system so the broken supplier
// is taken out of rotation, rather than applying a one-off reputation ding.
//
// Scoped to the explicit provider-key-rejection phrases only — generic auth
// patterns ("unauthorized", "forbidden") stay non-cooldown because they're more
// often transient or client-side.
func IsProviderAuthError(pattern string) bool {
	for _, p := range providerAuthErrorPatterns {
		if pattern == p {
			return true
		}
	}
	return false
}

// overServicedPatterns are exact substrings emitted by relay miners when a relay
// is rejected because the application's per-(supplier, session) stake allocation
// has been exhausted. These are NOT supplier faults — the supplier is correctly
// enforcing protocol-level rate limits — and must not penalize reputation or
// trigger circuit-breaking.
//
// Sources:
//   - poktroll main relay-miner: pkg/relayer/proxy/errors.go
//     ErrRelayerProxyRateLimited = "offchain rate limit hit by relayer proxy"
//   - HA relay-miner (pocket-relay-miner): relayer/proxy.go sends HTTP 429 with
//     body "session relay limit reached: claimable portion fully consumed".
//
// All matched lower-cased.
var overServicedPatterns = []string{
	"offchain rate limit hit by relayer proxy",
	"session relay limit reached",
	"claimable portion fully consumed",
}

// IsOverServicedError reports whether the given pattern or response payload
// indicates the supplier's relay-miner rejected the relay because the
// application's per-session stake budget is exhausted.
//
// Accepts either a heuristic pattern (the matched substring stored on
// AnalysisResult.MatchedPattern) or a raw payload string. Match is
// case-insensitive substring.
func IsOverServicedError(patternOrPayload string) bool {
	lower := strings.ToLower(patternOrPayload)
	for _, p := range overServicedPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// IsOverServicedRelayMinerError reports whether the structured RelayMinerError
// fields indicate an offchain rate-limit / over-servicing rejection from the
// poktroll main relay-miner. This is the most reliable signal — text-based
// detection on the payload is the fallback for relay miners (e.g. HA) that
// don't populate RelayMinerError correctly.
//
// codespace="relayer_proxy", code=7 corresponds to ErrRelayerProxyRateLimited
// in github.com/pokt-network/poktroll/pkg/relayer/proxy/errors.go.
func IsOverServicedRelayMinerError(codespace string, code uint32) bool {
	return codespace == "relayer_proxy" && code == 7
}

// capabilityLimitationSubstrings are the patterns to search for in error strings.
// These correspond to the same patterns in IsCapabilityLimitationError but are used
// for substring matching when the structured AnalysisResult is not available.
// Includes archival patterns AND capability limitation patterns (e.g., lite fullnode).
var capabilityLimitationSubstrings = []string{
	// Archival-related
	"historical state",
	"state has been pruned",
	"is pruned",
	"state not available",
	"haven't been fully indexed",
	"not been fully indexed",
	"missing trie node",
	"block has been pruned",
	"height is not available",
	// Capability limitation (e.g., Tron lite fullnodes)
	"lite fullnode",
	"api is not supported",
	// rest_protocol_mismatch_error: heuristic-detected honest JSON-RPC error
	// returned to a REST-shaped request (supplier's backend doesn't speak REST).
	// The structured AnalysisResult is lost when this surfaces through the
	// hedge_failed retry path; we match on the embedded reason string instead.
	// Distinct from "rest_protocol_mismatch" (canned-response gaming) — the
	// suffix prevents the substring check from matching the gaming variant.
	"rest_protocol_mismatch_error",
}

// ErrorContainsArchivalPattern checks if an error string contains any archival-related
// or capability-limitation pattern. This is a fallback for when the structured heuristic
// AnalysisResult is not available (e.g., protocol-layer errors propagated through the
// hedge_failed path).
func ErrorContainsArchivalPattern(errStr string) bool {
	lower := strings.ToLower(errStr)
	for _, pattern := range capabilityLimitationSubstrings {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
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
