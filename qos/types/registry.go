// Package types provides the ExtractorRegistry for mapping service IDs to DataExtractors.
//
// The registry enables service-agnostic request processing on the hot path,
// with service-specific parsing deferred to async workers.
//
// # Usage
//
//	registry := types.NewExtractorRegistry()
//	registry.Register("eth", evm.NewEVMDataExtractor())
//	registry.Register("osmosis", cosmos.NewCosmosDataExtractor())
//	registry.Register("text-to-text", types.NewNoOpDataExtractor()) // passthrough
//
//	// In async worker:
//	extractor := registry.Get(serviceID)
//	data := types.NewExtractedData(endpoint, statusCode, response, latency)
//	data.ExtractAll(extractor)
package types

import (
	"sync"

	"github.com/pokt-network/path/protocol"
)

// ExtractorRegistry maps service IDs to their DataExtractor implementations.
// This enables the hot path to be completely service-agnostic, with
// service-specific parsing deferred to async workers.
//
// Thread-safe for concurrent reads/writes.
type ExtractorRegistry struct {
	mu         sync.RWMutex
	extractors map[protocol.ServiceID]DataExtractor
	fallback   DataExtractor // Used for unknown services
}

// NewExtractorRegistry creates a new registry with a NoOp fallback.
func NewExtractorRegistry() *ExtractorRegistry {
	return &ExtractorRegistry{
		extractors: make(map[protocol.ServiceID]DataExtractor),
		fallback:   NewNoOpDataExtractor(),
	}
}

// Register adds a DataExtractor for a service ID.
// Overwrites any existing extractor for the same service.
func (r *ExtractorRegistry) Register(serviceID protocol.ServiceID, extractor DataExtractor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.extractors[serviceID] = extractor
}

// RegisterMultiple adds multiple DataExtractors at once.
// Useful for bulk registration during initialization.
func (r *ExtractorRegistry) RegisterMultiple(extractors map[protocol.ServiceID]DataExtractor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for serviceID, extractor := range extractors {
		r.extractors[serviceID] = extractor
	}
}

// Get returns the DataExtractor for a service ID.
// Returns the fallback (NoOp) extractor if the service is not registered.
func (r *ExtractorRegistry) Get(serviceID protocol.ServiceID) DataExtractor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if extractor, exists := r.extractors[serviceID]; exists {
		return extractor
	}
	return r.fallback
}

// Has returns true if a service ID has a registered extractor.
func (r *ExtractorRegistry) Has(serviceID protocol.ServiceID) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, exists := r.extractors[serviceID]
	return exists
}

// SetFallback sets the fallback extractor for unknown services.
// By default, this is a NoOpDataExtractor.
func (r *ExtractorRegistry) SetFallback(extractor DataExtractor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fallback = extractor
}

// List returns all registered service IDs.
func (r *ExtractorRegistry) List() []protocol.ServiceID {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]protocol.ServiceID, 0, len(r.extractors))
	for id := range r.extractors {
		ids = append(ids, id)
	}
	return ids
}

// Count returns the number of registered extractors.
func (r *ExtractorRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.extractors)
}
