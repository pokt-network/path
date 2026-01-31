package reputation

import (
	"context"
	"errors"

	"github.com/pokt-network/path/protocol"
)

// Common errors returned by storage implementations.
var (
	// ErrNotFound is returned when an endpoint's score is not found.
	ErrNotFound = errors.New("endpoint score not found")

	// ErrStorageClosed is returned when operations are attempted on a closed storage.
	ErrStorageClosed = errors.New("storage is closed")
)

// Storage defines the interface for reputation score persistence.
// Implementations must be safe for concurrent use.
type Storage interface {
	// Get retrieves the score for an endpoint.
	// Returns ErrNotFound if the endpoint has no stored score.
	Get(ctx context.Context, key EndpointKey) (Score, error)

	// GetMultiple retrieves scores for multiple endpoints.
	// Returns a map containing only the endpoints that were found.
	// Missing endpoints are omitted from the result (no error).
	GetMultiple(ctx context.Context, keys []EndpointKey) (map[EndpointKey]Score, error)

	// Set stores or updates the score for an endpoint.
	Set(ctx context.Context, key EndpointKey, score Score) error

	// SetMultiple stores or updates scores for multiple endpoints.
	SetMultiple(ctx context.Context, scores map[EndpointKey]Score) error

	// Delete removes the score for an endpoint.
	// Returns nil if the endpoint doesn't exist.
	Delete(ctx context.Context, key EndpointKey) error

	// List returns all stored endpoint keys for a service.
	// If serviceID is empty, returns all endpoint keys.
	List(ctx context.Context, serviceID string) ([]EndpointKey, error)

	// SetPerceivedBlockNumber stores the perceived block number for a service.
	// Uses atomic max semantics: only updates if new value > stored value.
	// This enables sharing chain state across replicas.
	SetPerceivedBlockNumber(ctx context.Context, serviceID protocol.ServiceID, blockNumber uint64) error

	// GetPerceivedBlockNumber retrieves the perceived block number for a service.
	// Returns 0 if no block number has been stored yet.
	GetPerceivedBlockNumber(ctx context.Context, serviceID protocol.ServiceID) (uint64, error)

	// Close releases any resources held by the storage.
	Close() error
}

// Cleaner is an optional interface that storage backends can implement
// to support periodic cleanup of expired entries.
type Cleaner interface {
	// Cleanup removes expired entries from storage.
	// This is called periodically by the service to prevent memory bloat.
	Cleanup()
}
