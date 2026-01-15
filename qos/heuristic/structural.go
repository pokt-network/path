package heuristic

import (
	"bytes"
)

// Tier 1: Structural Response Checks
//
// These checks analyze the basic structure of a response without
// understanding its semantic content. They are extremely fast
// (typically < 1μs) and catch obvious error cases:
//   - Empty responses
//   - HTML error pages
//   - XML responses (unexpected for JSON APIs)
//   - Non-JSON text responses
//   - Malformed JSON structures

// Common byte sequences for detecting response types
var (
	// HTML detection (lowercase for case-insensitive matching after ToLower)
	htmlOpenTag = []byte("<html")
	doctypeTag  = []byte("<!doctype")

	// XML detection (lowercase for case-insensitive matching after ToLower)
	xmlProlog = []byte("<?xml")
)

// ClassifyStructure performs Tier 1 structural analysis on the response.
// It determines what type of content the response contains based on
// its structure, without parsing the full content.
//
// Cost: O(1) - only inspects first few bytes
// Time: < 1μs typically
func ClassifyStructure(responseBytes []byte) ResponseStructure {
	// Empty response
	if len(responseBytes) == 0 {
		return StructureEmpty
	}

	// Check first byte to determine content type
	firstByte := responseBytes[0]

	// Whitespace-leading responses - find first non-whitespace
	if isWhitespace(firstByte) {
		trimmed := bytes.TrimLeft(responseBytes, " \t\n\r")
		if len(trimmed) == 0 {
			return StructureEmpty
		}
		firstByte = trimmed[0]
		responseBytes = trimmed
	}

	// HTML detection (error pages typically start with < )
	if firstByte == '<' {
		return classifyMarkup(responseBytes)
	}

	// Valid JSON starts with { or [
	if firstByte == '{' || firstByte == '[' {
		return classifyJSONLike(responseBytes)
	}

	// Everything else is non-JSON (raw text error messages, etc.)
	return StructureNonJSON
}

// classifyMarkup determines if markup content is HTML or XML.
func classifyMarkup(data []byte) ResponseStructure {
	// Need at least a few bytes to check
	checkLen := min(50, len(data))
	prefix := bytes.ToLower(data[:checkLen])

	// Check for HTML
	if bytes.Contains(prefix, htmlOpenTag) ||
		bytes.Contains(prefix, doctypeTag) {
		return StructureHTML
	}

	// Check for XML
	if bytes.HasPrefix(prefix, xmlProlog) {
		return StructureXML
	}

	// Generic markup - likely HTML error page
	return StructureHTML
}

// classifyJSONLike checks if JSON-like content appears well-formed.
func classifyJSONLike(data []byte) ResponseStructure {
	firstByte := data[0]
	length := len(data)

	// Very short responses - check for matching brackets
	if length < 50 {
		lastByte := data[length-1]

		// Handle trailing whitespace
		trimmed := bytes.TrimRight(data, " \t\n\r")
		if len(trimmed) > 0 {
			lastByte = trimmed[len(trimmed)-1]
		}

		// Check for matching brackets
		if firstByte == '{' && lastByte != '}' {
			return StructureMalformed
		}
		if firstByte == '[' && lastByte != ']' {
			return StructureMalformed
		}
	}

	return StructureValid
}

// isWhitespace checks if a byte is JSON whitespace.
func isWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// StructuralAnalysis performs Tier 1 analysis and returns a result.
// This is the entry point for structural-only analysis.
func StructuralAnalysis(responseBytes []byte) AnalysisResult {
	structure := ClassifyStructure(responseBytes)

	switch structure {
	case StructureEmpty:
		return AnalysisResult{
			ShouldRetry: true,
			Confidence:  1.0,
			Reason:      "empty_response",
			Structure:   structure,
			Details:     "Response body is empty",
		}

	case StructureHTML:
		return AnalysisResult{
			ShouldRetry: true,
			Confidence:  0.99,
			Reason:      "html_error_page",
			Structure:   structure,
			Details:     "Response is HTML (likely error page)",
		}

	case StructureXML:
		return AnalysisResult{
			ShouldRetry: true,
			Confidence:  0.95,
			Reason:      "xml_response",
			Structure:   structure,
			Details:     "Response is XML (unexpected for JSON API)",
		}

	case StructureNonJSON:
		return AnalysisResult{
			ShouldRetry: true,
			Confidence:  0.95,
			Reason:      "non_json_response",
			Structure:   structure,
			Details:     "Response is plain text (not JSON)",
		}

	case StructureMalformed:
		return AnalysisResult{
			ShouldRetry: true,
			Confidence:  0.90,
			Reason:      "malformed_json",
			Structure:   structure,
			Details:     "Response appears to be malformed JSON",
		}

	default:
		// StructureValid - no issues at structural level
		return AnalysisResult{
			ShouldRetry: false,
			Confidence:  0.0,
			Reason:      "valid_structure",
			Structure:   structure,
			Details:     "Response has valid JSON structure",
		}
	}
}
