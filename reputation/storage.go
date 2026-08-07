package reputation

import (
	"context"
	"errors"
	"time"

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

	// DeletePerceivedBlockNumber removes the stored perceived block number for a
	// service so it can be rebuilt from fresh endpoint observations. Used by the
	// chain-state admin reset to recover from a poisoned/stuck perceived height
	// (the max-wins Set path cannot lower a too-high value).
	DeletePerceivedBlockNumber(ctx context.Context, serviceID protocol.ServiceID) error

	// SetEndpointBlockHeight stores a single endpoint's block height for a service.
	// Uses HSET on a Redis hash keyed by service ID, with endpoint address as field.
	SetEndpointBlockHeight(ctx context.Context, serviceID protocol.ServiceID, endpointAddr protocol.EndpointAddr, blockHeight uint64) error

	// GetEndpointBlockHeights retrieves all endpoint block heights for a service.
	// Returns a map of endpoint address to block height.
	GetEndpointBlockHeights(ctx context.Context, serviceID protocol.ServiceID) (map[protocol.EndpointAddr]uint64, error)

	// RemoveEndpointBlockHeights removes endpoint block height entries for a service.
	// Used by stale endpoint cleanup to remove entries that are no longer in active sessions.
	RemoveEndpointBlockHeights(ctx context.Context, serviceID protocol.ServiceID, addrs []protocol.EndpointAddr) error

	// Close releases any resources held by the storage.
	// SetDrain records an admin drain: this endpoint is benched until the given time.
	//
	// Drains are stored SEPARATELY from scores, and that separation is the whole point.
	// An earlier version carried the bench on Score.CooldownUntil, where refreshFromStorage
	// — which overwrites the local cache from storage unconditionally — erased it within a
	// refresh cycle. Anything that must outlive a storage refresh cannot live on the score.
	//
	// Storing them here (rather than only in pod memory) is what makes one admin call apply
	// fleet-wide: every replica picks the drain up on its next refresh, instead of the
	// operator having to hit all N pods.
	SetDrain(ctx context.Context, key DrainKey, until time.Time) error

	// DeleteDrain lifts an admin drain. Removing it from shared storage is what propagates
	// a release to the other replicas.
	DeleteDrain(ctx context.Context, key DrainKey) error

	// ListDrains returns every live admin drain. Expired entries are filtered out by the
	// implementation, so callers can treat the result as currently-in-force.
	ListDrains(ctx context.Context) (map[DrainKey]time.Time, error)

	Close() error
}

// Cleaner is an optional interface that storage backends can implement
// to support periodic cleanup of expired entries.
type Cleaner interface {
	// Cleanup removes expired entries from storage.
	// This is called periodically by the service to prevent memory bloat.
	Cleanup()
}
