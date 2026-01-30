// Package types provides the NoOpDataExtractor for passthrough services.
//
// NoOpDataExtractor is used for services that don't need response parsing.
// All extraction methods return zero values or indicate "not available".
// This is the default fallback for unknown services.
package types

import "errors"

// ErrNoOpExtractor is returned when extraction is not supported.
var ErrNoOpExtractor = errors.New("noop extractor: extraction not supported for this service")

// Verify NoOpDataExtractor implements DataExtractor at compile time.
var _ DataExtractor = (*NoOpDataExtractor)(nil)

// NoOpDataExtractor is a passthrough extractor that doesn't parse responses.
// Used for:
//   - Services explicitly configured as "passthrough"
//   - Unknown services (fallback)
//   - Services where response parsing isn't needed or possible
type NoOpDataExtractor struct{}

// NewNoOpDataExtractor creates a new NoOp data extractor.
func NewNoOpDataExtractor() *NoOpDataExtractor {
	return &NoOpDataExtractor{}
}

// ExtractBlockHeight returns an error indicating extraction is not supported.
func (e *NoOpDataExtractor) ExtractBlockHeight(_, _ []byte) (int64, error) {
	return 0, ErrNoOpExtractor
}

// ExtractChainID returns an error indicating extraction is not supported.
func (e *NoOpDataExtractor) ExtractChainID(_, _ []byte) (string, error) {
	return "", ErrNoOpExtractor
}

// IsSyncing returns an error indicating extraction is not supported.
func (e *NoOpDataExtractor) IsSyncing(_, _ []byte) (bool, error) {
	return false, ErrNoOpExtractor
}

// IsArchival returns an error indicating extraction is not supported.
func (e *NoOpDataExtractor) IsArchival(_, _ []byte) (bool, error) {
	return false, ErrNoOpExtractor
}

// IsValidResponse returns true for any non-empty response.
// This is the only method that provides a meaningful result for NoOp.
// A response is considered "valid" if it has content.
func (e *NoOpDataExtractor) IsValidResponse(_, response []byte) (bool, error) {
	return len(response) > 0, nil
}
