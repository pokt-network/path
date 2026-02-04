package evm

import (
	"fmt"
	"time"
)

// FilterLayer identifies which filtering layer rejected an endpoint.
type FilterLayer string

const (
	// FilterLayerLocalStore indicates the endpoint was rejected based on local in-memory store data.
	FilterLayerLocalStore FilterLayer = "local_store"
	// FilterLayerRedis indicates the endpoint was rejected based on Redis/shared state lookup.
	FilterLayerRedis FilterLayer = "redis"
	// FilterLayerReputation indicates the endpoint was rejected based on reputation scoring.
	FilterLayerReputation FilterLayer = "reputation"
	// FilterLayerArchival indicates the endpoint failed archival capability check.
	FilterLayerArchival FilterLayer = "archival"
)

// EndpointFilterError provides structured context about why an endpoint was filtered out.
// This enables operators to diagnose "no endpoints available" errors by understanding
// exactly which layer rejected each endpoint.
type EndpointFilterError struct {
	// Layer identifies which filtering layer caused the rejection.
	Layer FilterLayer
	// Reason is a short, machine-readable reason code.
	Reason string
	// EndpointAddr is the endpoint that was rejected.
	EndpointAddr string
	// Details contains layer-specific diagnostic information.
	Details map[string]interface{}
	// Cause is the underlying error if any.
	Cause error
}

// Error implements the error interface.
func (e *EndpointFilterError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] endpoint %s filtered: %s (%v)", e.Layer, e.EndpointAddr, e.Reason, e.Cause)
	}
	return fmt.Sprintf("[%s] endpoint %s filtered: %s", e.Layer, e.EndpointAddr, e.Reason)
}

// Unwrap returns the underlying error for errors.Is/As support.
func (e *EndpointFilterError) Unwrap() error {
	return e.Cause
}

// NewEndpointFilterError creates a new structured filter error.
func NewEndpointFilterError(layer FilterLayer, reason string, addr string, cause error, details map[string]interface{}) *EndpointFilterError {
	return &EndpointFilterError{
		Layer:        layer,
		Reason:       reason,
		EndpointAddr: addr,
		Cause:        cause,
		Details:      details,
	}
}

// ArchivalFilterDetails contains diagnostic details for archival filtering failures.
type ArchivalFilterDetails struct {
	// IsArchival indicates if endpoint was marked as archival in local store.
	IsArchival bool
	// ExpiresAt is when the archival status expires (zero if never set).
	ExpiresAt time.Time
	// IsExpired indicates if the archival status has expired.
	IsExpired bool
	// TimeSinceExpiry is how long ago the status expired (if expired).
	TimeSinceExpiry time.Duration
	// RedisChecked indicates if Redis was consulted for archival status.
	// DEPRECATED: Kept for backward compatibility. Cache is now used instead.
	RedisChecked bool
	// RedisIsArchival is the archival status from Redis (if checked).
	// DEPRECATED: Kept for backward compatibility. Cache is now used instead.
	RedisIsArchival bool
	// CacheChecked indicates if local cache was consulted for archival status.
	CacheChecked bool
	// CacheIsArchival is the archival status from local cache (if checked).
	CacheIsArchival bool
}

// ToMap converts ArchivalFilterDetails to a map for error Details field.
func (d *ArchivalFilterDetails) ToMap() map[string]interface{} {
	m := map[string]interface{}{
		"is_archival":   d.IsArchival,
		"redis_checked": d.RedisChecked, // Keep for backward compatibility
		"cache_checked": d.CacheChecked,
	}
	if !d.ExpiresAt.IsZero() {
		m["expires_at"] = d.ExpiresAt.Format(time.RFC3339)
		m["is_expired"] = d.IsExpired
		if d.IsExpired {
			m["expired_ago"] = d.TimeSinceExpiry.String()
		}
	} else {
		m["expires_at"] = "never_set"
	}
	if d.RedisChecked {
		m["redis_is_archival"] = d.RedisIsArchival
	}
	if d.CacheChecked {
		m["cache_is_archival"] = d.CacheIsArchival
	}
	return m
}

// NewArchivalFilterError creates a structured error for archival filtering failures.
func NewArchivalFilterError(addr string, details *ArchivalFilterDetails, cause error) *EndpointFilterError {
	return NewEndpointFilterError(
		FilterLayerArchival,
		"archival_check_failed",
		addr,
		cause,
		details.ToMap(),
	)
}

// BlockHeightFilterDetails contains diagnostic details for block height filtering failures.
type BlockHeightFilterDetails struct {
	// EndpointBlock is the endpoint's reported block number.
	EndpointBlock uint64
	// PerceivedBlock is the QoS perceived block number.
	PerceivedBlock uint64
	// SyncAllowance is the configured sync allowance.
	SyncAllowance uint64
	// BlocksBehind is how many blocks the endpoint is behind.
	BlocksBehind int64
}

// ToMap converts BlockHeightFilterDetails to a map for error Details field.
func (d *BlockHeightFilterDetails) ToMap() map[string]interface{} {
	return map[string]interface{}{
		"endpoint_block":  d.EndpointBlock,
		"perceived_block": d.PerceivedBlock,
		"sync_allowance":  d.SyncAllowance,
		"blocks_behind":   d.BlocksBehind,
	}
}

// NewBlockHeightFilterError creates a structured error for block height filtering failures.
func NewBlockHeightFilterError(addr string, details *BlockHeightFilterDetails, cause error) *EndpointFilterError {
	return NewEndpointFilterError(
		FilterLayerReputation,
		"block_behind_threshold",
		addr,
		cause,
		details.ToMap(),
	)
}
