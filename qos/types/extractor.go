// Package types provides core QoS types that can be imported without cycles.
//
// The DataExtractor interface defines how endpoint quality data is extracted from
// responses. This enables both hardcoded extractors (EVM, Cosmos, Solana) and
// future dynamic/generic extractors (YAML-configured rules).
//
// This package is separate from qos to avoid import cycles with gateway.
package types

import (
	"time"

	"github.com/pokt-network/path/protocol"
)

// DataExtractor defines how endpoint quality data is extracted from responses.
// All QoS services (EVM, Cosmos, Solana, generic) implement this interface.
//
// This interface enables:
//   - Hardcoded extractors: Know exactly how to parse their protocol's responses
//   - Dynamic extractors: Use configurable rules for unknown protocols
type DataExtractor interface {
	// ExtractBlockHeight extracts the latest block height from a response.
	// This is used to determine sync status - endpoints behind in block height
	// are potentially stale or syncing.
	//
	// Returns:
	//   - Block height as int64
	//   - Error if extraction fails or response doesn't contain block height
	ExtractBlockHeight(response []byte) (int64, error)

	// ExtractChainID extracts the chain identifier from a response.
	// This is used to validate endpoints are on the correct chain.
	//
	// Returns:
	//   - Chain ID as string (hex for EVM, chain-name for Cosmos)
	//   - Error if extraction fails or response doesn't contain chain ID
	ExtractChainID(response []byte) (string, error)

	// IsSyncing determines if the endpoint is currently syncing.
	// Syncing endpoints may return stale data and should be deprioritized.
	//
	// Returns:
	//   - true if endpoint is syncing
	//   - false if endpoint is synced
	//   - Error if sync status cannot be determined
	IsSyncing(response []byte) (bool, error)

	// IsArchival determines if the endpoint supports archival queries.
	// Archival endpoints can serve historical data queries.
	//
	// Parameters:
	//   - response: Response from an archival-specific query (e.g., eth_getBalance at historical block)
	//
	// Returns:
	//   - true if endpoint is archival (query succeeded)
	//   - false if endpoint is not archival (query failed)
	//   - Error if archival status cannot be determined
	IsArchival(response []byte) (bool, error)

	// IsValidResponse checks if the response is valid for the protocol.
	// This performs basic validation without extracting specific data.
	//
	// Returns:
	//   - true if response is valid (correct format, no errors)
	//   - false if response is invalid (malformed, contains error)
	//   - Error if validation fails unexpectedly
	IsValidResponse(response []byte) (bool, error)
}

// ExtractedData holds all data extracted from a response.
// This is the result of running all extractors on a response.
type ExtractedData struct {
	// BlockHeight is the latest block number (0 if not extracted).
	BlockHeight int64

	// ChainID is the chain identifier (empty if not extracted).
	ChainID string

	// IsSyncing indicates if the endpoint is syncing.
	IsSyncing bool

	// IsArchival indicates if the endpoint supports archival queries.
	IsArchival bool

	// IsValidResponse indicates if the response was valid.
	IsValidResponse bool

	// ResponseTime is how long the request took.
	ResponseTime time.Duration

	// EndpointAddr identifies the endpoint that responded.
	EndpointAddr protocol.EndpointAddr

	// HTTPStatusCode is the HTTP status code from the response.
	HTTPStatusCode int

	// RawResponse is the original response bytes (for re-parsing if needed).
	RawResponse []byte

	// ExtractionErrors holds any errors that occurred during extraction.
	// Map from field name (e.g., "block_height") to error message.
	ExtractionErrors map[string]string
}

// NewExtractedData creates a new ExtractedData with defaults.
func NewExtractedData(endpointAddr protocol.EndpointAddr, statusCode int, response []byte, latency time.Duration) *ExtractedData {
	return &ExtractedData{
		EndpointAddr:     endpointAddr,
		HTTPStatusCode:   statusCode,
		RawResponse:      response,
		ResponseTime:     latency,
		ExtractionErrors: make(map[string]string),
	}
}

// ExtractAll runs all extractors and populates the ExtractedData.
// Errors are captured in ExtractionErrors rather than returned directly,
// allowing partial extraction even when some fields fail.
func (ed *ExtractedData) ExtractAll(extractor DataExtractor) {
	// Extract block height
	if blockHeight, err := extractor.ExtractBlockHeight(ed.RawResponse); err == nil {
		ed.BlockHeight = blockHeight
	} else {
		ed.ExtractionErrors["block_height"] = err.Error()
	}

	// Extract chain ID
	if chainID, err := extractor.ExtractChainID(ed.RawResponse); err == nil {
		ed.ChainID = chainID
	} else {
		ed.ExtractionErrors["chain_id"] = err.Error()
	}

	// Check sync status
	if isSyncing, err := extractor.IsSyncing(ed.RawResponse); err == nil {
		ed.IsSyncing = isSyncing
	} else {
		ed.ExtractionErrors["is_syncing"] = err.Error()
	}

	// Check archival status
	if isArchival, err := extractor.IsArchival(ed.RawResponse); err == nil {
		ed.IsArchival = isArchival
	} else {
		ed.ExtractionErrors["is_archival"] = err.Error()
	}

	// Validate response
	if isValid, err := extractor.IsValidResponse(ed.RawResponse); err == nil {
		ed.IsValidResponse = isValid
	} else {
		ed.ExtractionErrors["is_valid"] = err.Error()
	}
}

// HasErrors returns true if any extraction errors occurred.
func (ed *ExtractedData) HasErrors() bool {
	return len(ed.ExtractionErrors) > 0
}

// ExtractionConfig configures which extractions to run.
// Used to skip extractions that aren't relevant for a given check.
type ExtractionConfig struct {
	// ExtractBlockHeight enables block height extraction.
	ExtractBlockHeight bool

	// ExtractChainID enables chain ID extraction.
	ExtractChainID bool

	// CheckSyncStatus enables sync status checking.
	CheckSyncStatus bool

	// CheckArchival enables archival status checking.
	CheckArchival bool

	// ValidateResponse enables response validation.
	ValidateResponse bool
}

// DefaultExtractionConfig returns a config with all extractions enabled.
func DefaultExtractionConfig() ExtractionConfig {
	return ExtractionConfig{
		ExtractBlockHeight: true,
		ExtractChainID:     true,
		CheckSyncStatus:    true,
		CheckArchival:      true,
		ValidateResponse:   true,
	}
}

// ExtractWithConfig runs extractions based on the provided config.
func (ed *ExtractedData) ExtractWithConfig(extractor DataExtractor, config ExtractionConfig) {
	if config.ExtractBlockHeight {
		if blockHeight, err := extractor.ExtractBlockHeight(ed.RawResponse); err == nil {
			ed.BlockHeight = blockHeight
		} else {
			ed.ExtractionErrors["block_height"] = err.Error()
		}
	}

	if config.ExtractChainID {
		if chainID, err := extractor.ExtractChainID(ed.RawResponse); err == nil {
			ed.ChainID = chainID
		} else {
			ed.ExtractionErrors["chain_id"] = err.Error()
		}
	}

	if config.CheckSyncStatus {
		if isSyncing, err := extractor.IsSyncing(ed.RawResponse); err == nil {
			ed.IsSyncing = isSyncing
		} else {
			ed.ExtractionErrors["is_syncing"] = err.Error()
		}
	}

	if config.CheckArchival {
		if isArchival, err := extractor.IsArchival(ed.RawResponse); err == nil {
			ed.IsArchival = isArchival
		} else {
			ed.ExtractionErrors["is_archival"] = err.Error()
		}
	}

	if config.ValidateResponse {
		if isValid, err := extractor.IsValidResponse(ed.RawResponse); err == nil {
			ed.IsValidResponse = isValid
		} else {
			ed.ExtractionErrors["is_valid"] = err.Error()
		}
	}
}
