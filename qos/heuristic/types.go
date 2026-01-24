// Package heuristic provides lightweight response analysis to detect errors
// before full JSON parsing, enabling faster retry decisions.
//
// The package implements a tiered analysis approach:
//   - Tier 1: Structural checks (empty, HTML, non-JSON)
//   - Tier 2: Protocol-specific success indicators
//   - Tier 3: Common error pattern detection
//
// This allows the gateway to quickly identify likely-error responses
// and trigger retries without the latency cost of full JSON parsing.
package heuristic

import (
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
)

// ResponseStructure classifies the structural type of a response.
type ResponseStructure int

const (
	// StructureValid indicates the response has valid JSON structure.
	StructureValid ResponseStructure = iota
	// StructureEmpty indicates an empty response (0 bytes).
	StructureEmpty
	// StructureHTML indicates the response is HTML (likely an error page).
	StructureHTML
	// StructureXML indicates the response is XML (unexpected for JSON APIs).
	StructureXML
	// StructureNonJSON indicates the response is plain text (not JSON).
	StructureNonJSON
	// StructureMalformed indicates the response starts like JSON but appears malformed.
	StructureMalformed
)

// String returns a human-readable representation of the ResponseStructure.
func (rs ResponseStructure) String() string {
	switch rs {
	case StructureValid:
		return "valid_json"
	case StructureEmpty:
		return "empty"
	case StructureHTML:
		return "html"
	case StructureXML:
		return "xml"
	case StructureNonJSON:
		return "non_json"
	case StructureMalformed:
		return "malformed"
	default:
		return "unknown"
	}
}

// AnalysisResult contains the outcome of heuristic response analysis.
type AnalysisResult struct {
	// ShouldRetry indicates whether the response suggests a retry is warranted.
	ShouldRetry bool

	// Confidence represents how confident the analysis is (0.0 to 1.0).
	// Higher values indicate more certainty that the response is an error.
	Confidence float64

	// Reason provides a machine-readable classification of why retry is suggested.
	Reason string

	// Structure contains the structural classification of the response.
	Structure ResponseStructure

	// Details provides additional human-readable context for debugging.
	Details string
}

// AnalyzerConfig holds configuration options for the response analyzer.
type AnalyzerConfig struct {
	// MaxPrefixBytes is the maximum number of bytes to inspect for heuristics.
	// Default: 512 bytes. Larger values may catch more edge cases but cost more CPU.
	MaxPrefixBytes int

	// ConfidenceThreshold is the minimum confidence level to suggest retry.
	// Default: 0.5. Set higher for fewer false positives, lower for more aggressive retries.
	ConfidenceThreshold float64

	// EnableTier3 controls whether error indicator pattern matching is performed.
	// Default: true. Disable if you want only structural and protocol checks.
	EnableTier3 bool
}

// DefaultConfig returns the default analyzer configuration.
func DefaultConfig() AnalyzerConfig {
	return AnalyzerConfig{
		MaxPrefixBytes:      512,
		ConfidenceThreshold: 0.5,
		EnableTier3:         true,
	}
}

// Analyzer defines the interface for heuristic response analysis.
type Analyzer interface {
	// Analyze performs heuristic analysis on a response to determine if it's likely an error.
	// Parameters:
	//   - responseBytes: The raw response payload
	//   - httpStatusCode: The HTTP status code from the endpoint
	//   - rpcType: The RPC type for protocol-specific analysis
	//
	// Returns an AnalysisResult with retry recommendation and confidence level.
	Analyze(responseBytes []byte, httpStatusCode int, rpcType sharedtypes.RPCType) AnalysisResult
}
