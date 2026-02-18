package evm

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/metrics/devtools"
	"github.com/pokt-network/path/protocol"
	qostypes "github.com/pokt-network/path/qos/types"
	"github.com/pokt-network/path/reputation"
)

// QoS implements gateway.QoSService by providing:
//  1. QoSRequestParser - Builds EVM-specific RequestQoSContext objects from HTTP requests
//  2. EndpointSelector - Selects endpoints for service requests
var _ gateway.QoSService = &QoS{}

// devtools.QoSDisqualifiedEndpointsReporter is fulfilled by the QoS struct below.
// This allows the QoS service to report its disqualified endpoints data to the devtools.DisqualifiedEndpointReporter.
var _ devtools.QoSDisqualifiedEndpointsReporter = &QoS{}

// QoS implements ServiceQoS for EVM-based chains.
// It handles chain-specific:
//   - Request parsing
//   - Response building
//   - Endpoint validation and selection
type QoS struct {
	logger polylog.Logger
	*serviceState
	*evmRequestValidator
}

// NewSimpleQoSInstance creates a minimal EVM QoS instance without chain-specific validation.
// Validation (chain ID, archival checks) is now handled by active health checks.
// This constructor only requires the service ID and provides JSON-RPC request parsing.
func NewSimpleQoSInstance(logger polylog.Logger, serviceID protocol.ServiceID) *QoS {
	return NewSimpleQoSInstanceWithSyncAllowance(logger, serviceID, 0)
}

// NewSimpleQoSInstanceWithSyncAllowance creates a minimal EVM QoS instance with custom sync allowance.
// Validation (chain ID, archival checks) is now handled by active health checks.
// If syncAllowance is 0, the default value is used.
func NewSimpleQoSInstanceWithSyncAllowance(logger polylog.Logger, serviceID protocol.ServiceID, syncAllowance uint64) *QoS {
	logger = logger.With(
		"qos_instance", "evm",
		"service_id", serviceID,
	)

	store := &endpointStore{
		logger:    logger,
		endpoints: make(map[protocol.EndpointAddr]endpoint),
	}

	// Create a minimal config wrapper for backward compatibility with serviceState
	minimalConfig := &simpleServiceConfig{
		serviceID: serviceID,
	}
	minimalConfig.syncAllowance.Store(syncAllowance)

	// Create block consensus calculator for robust perceived block height
	blockConsensus := NewBlockHeightConsensus(logger, minimalConfig.getSyncAllowance())

	serviceState := &serviceState{
		logger:            logger,
		serviceQoSConfig:  minimalConfig,
		endpointStore:     store,
		archivalHeuristic: NewArchivalHeuristic(0), // Use default threshold
		blockConsensus:    blockConsensus,
		archivalCache:     NewArchivalCache(), // Local cache for O(1) archival lookups
	}

	evmRequestValidator := &evmRequestValidator{
		logger:       logger,
		serviceID:    serviceID,
		chainID:      "", // No chain ID validation - handled by health checks
		serviceState: serviceState,
	}

	return &QoS{
		logger:              logger,
		serviceState:        serviceState,
		evmRequestValidator: evmRequestValidator,
	}
}

// simpleServiceConfig is a minimal config for services without chain-specific params.
type simpleServiceConfig struct {
	serviceID     protocol.ServiceID
	syncAllowance atomic.Uint64 // If 0, uses default. Updated dynamically when external health check rules are loaded.
}

func (c *simpleServiceConfig) GetServiceID() protocol.ServiceID { return c.serviceID }
func (c *simpleServiceConfig) GetServiceQoSType() string        { return QoSType }
func (c *simpleServiceConfig) getEVMChainID() string            { return "" }
func (c *simpleServiceConfig) getSyncAllowance() uint64 {
	if v := c.syncAllowance.Load(); v > 0 {
		return v
	}
	return defaultEVMBlockNumberSyncAllowance
}
func (c *simpleServiceConfig) getSupportedAPIs() map[sharedtypes.RPCType]struct{} {
	return map[sharedtypes.RPCType]struct{}{sharedtypes.RPCType_JSON_RPC: {}}
}

// SetSyncAllowance dynamically updates the sync allowance for this QoS instance.
// This is called when external health check rules are loaded/refreshed, since those
// rules may specify a sync_allowance that wasn't available at QoS creation time.
func (qos *QoS) SetSyncAllowance(syncAllowance uint64) {
	if cfg, ok := qos.serviceState.serviceQoSConfig.(*simpleServiceConfig); ok {
		cfg.syncAllowance.Store(syncAllowance)
	}
}

// ParseHTTPRequest builds a request context from an HTTP request.
// Returns (requestContext, true) if the request is valid JSONRPC
// Returns (errorContext, false) if the request is not valid JSONRPC.
//
// Implements gateway.QoSService interface.
// Fallback logic for EVM: header → jsonrpc (EVM only supports JSON-RPC)
func (qos *QoS) ParseHTTPRequest(_ context.Context, req *http.Request, detectedRPCType sharedtypes.RPCType) (gateway.RequestQoSContext, bool) {
	return qos.validateHTTPRequest(req, detectedRPCType)
}

// ParseWebsocketRequest builds a request context from the provided Websocket request.
// Websocket connection requests do not have a body, so we don't need to parse it.
//
// Implements gateway.QoSService interface.
func (qos *QoS) ParseWebsocketRequest(_ context.Context) (gateway.RequestQoSContext, bool) {
	return &requestContext{
		logger:       qos.logger,
		serviceState: qos.serviceState,
	}, true
}

// HydrateDisqualifiedEndpointsResponse hydrates the disqualified endpoint response with the QoS-specific data.
//   - takes a pointer to the DisqualifiedEndpointResponse
//   - called by the devtools.DisqualifiedEndpointReporter to fill it with the QoS-specific data.
func (qos *QoS) HydrateDisqualifiedEndpointsResponse(serviceID protocol.ServiceID, details *devtools.DisqualifiedEndpointResponse) {
	qos.logger.Debug().Msgf("hydrating disqualified endpoints response for service ID: %s", serviceID)
	details.QoSLevelDisqualifiedEndpoints = qos.getDisqualifiedEndpointsResponse(serviceID)
}

// UpdateFromExtractedData updates QoS state from extracted observation data.
// Called by the observation pipeline after async parsing completes.
// This updates the perceived block number, archival status, and stores endpoint observations.
//
// Implements gateway.QoSService interface.
func (qos *QoS) UpdateFromExtractedData(endpointAddr protocol.EndpointAddr, data *qostypes.ExtractedData) error {
	if data == nil {
		return nil
	}

	// Lock the endpoint store to update the endpoint observations
	qos.endpointStore.endpointsMu.Lock()
	storedEndpoint := qos.endpointStore.endpoints[endpointAddr]

	// Update block number if extracted
	var blockNumber uint64
	if data.BlockHeight > 0 {
		blockNumber = uint64(data.BlockHeight)
		storedEndpoint.checkBlockNumber = endpointCheckBlockNumber{
			parsedBlockNumberResponse: &blockNumber,
		}
	}

	// Update archival status: Handle both setting and clearing archival capability.
	// This allows both health checks and user request responses to update archival status.
	// - Health checks validate archival capability via eth_getBlockByNumber for ancient blocks
	// - User requests can confirm (IsArchival=true) or invalidate (IsArchival=false) archival status
	if data.ArchivalCheckPerformed {
		// Default TTL for archival status (8 hours - matches health check archival TTL)
		archivalTTL := 8 * time.Hour
		expiresAt := time.Now().Add(archivalTTL)

		storedEndpoint.checkArchival = endpointCheckArchival{
			isArchival: data.IsArchival,
			expiresAt:  expiresAt,
		}

		if data.IsArchival {
			qos.logger.Info().
				Str("endpoint", string(endpointAddr)).
				Time("expires_at", expiresAt).
				Msg("Confirmed archival status - endpoint supports archival queries")

			// Update local cache for fast lookups in hot path
			if qos.archivalCache != nil {
				key := reputation.NewEndpointKey(
					qos.serviceQoSConfig.GetServiceID(),
					endpointAddr,
					sharedtypes.RPCType_JSON_RPC,
				)
				qos.archivalCache.Set(key.String(), true, archivalTTL)
			}

			// Write to Redis for cross-replica sync (async, non-blocking)
			if qos.reputationSvc != nil {
				go func(addr protocol.EndpointAddr, ttl time.Duration) {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					// IMPORTANT: RPCType_JSON_RPC must match between all archival writes and reads
					// for cross-replica consistency. Health checks, UpdateFromExtractedData, and
					// endpoint selection filtering all must use the same RPC type.
					key := reputation.NewEndpointKey(
						qos.serviceQoSConfig.GetServiceID(),
						addr,
						sharedtypes.RPCType_JSON_RPC,
					)
					_ = qos.reputationSvc.SetArchivalStatus(ctx, key, true, ttl)
				}(endpointAddr, archivalTTL)
			}
		} else {
			qos.logger.Warn().
				Str("endpoint", string(endpointAddr)).
				Msg("Cleared archival status - endpoint failed archival query")
		}
	}

	// Store the updated endpoint back
	qos.endpointStore.endpoints[endpointAddr] = storedEndpoint
	qos.endpointStore.endpointsMu.Unlock()

	// Update perceived block using consensus mechanism
	// This protects against malicious endpoints reporting extreme block heights
	if blockNumber > 0 && qos.blockConsensus != nil {
		oldBlock := qos.perceivedBlockNumber.Load()
		newPerceived := qos.blockConsensus.AddObservation(endpointAddr, blockNumber)

		// Update the atomic for fast reads
		qos.perceivedBlockNumber.Store(newPerceived)

		if newPerceived != oldBlock {
			qos.logger.Debug().
				Str("endpoint", string(endpointAddr)).
				Uint64("reported_block", blockNumber).
				Uint64("old_perceived", oldBlock).
				Uint64("new_perceived", newPerceived).
				Msg("Updated perceived block number via consensus")

			// Also update Redis for cross-replica sync (async, non-blocking)
			if qos.reputationSvc != nil {
				go func(bn uint64) {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					_ = qos.reputationSvc.SetPerceivedBlockNumber(ctx, qos.serviceQoSConfig.GetServiceID(), bn)
				}(newPerceived)
			}
		}
	}

	return nil
}

// GetPerceivedBlockNumber returns the perceived current block number.
// Used by health checks for block height validation.
// Returns 0 if no block number has been observed yet.
// Lock-free using atomic operations.
//
// Implements gateway.QoSService interface.
func (qos *QoS) GetPerceivedBlockNumber() uint64 {
	return qos.perceivedBlockNumber.Load()
}

// GetBlockConsensusStats returns the median block and observation count.
// Used for observability to understand how block consensus is calculated.
//
// Implements gateway.QoSBlockConsensusReporter interface.
func (qos *QoS) GetBlockConsensusStats() (medianBlock uint64, observationCount int) {
	if qos.blockConsensus == nil {
		return 0, 0
	}
	return qos.blockConsensus.GetMedianBlock(), qos.blockConsensus.GetObservationCount()
}

// GetEndpointArchivalStatus returns the archival status for a specific endpoint.
// Returns (isArchival, expiresAt) if the endpoint has been checked for archival capability.
// Returns (false, zero time) if the endpoint has not been checked or is not archival-capable.
//
// Checks both local endpointStore AND reputation service (Redis) for archival status.
// This ensures non-leader replicas see archival status set by health checks on the leader.
func (qos *QoS) GetEndpointArchivalStatus(endpointAddr protocol.EndpointAddr) (isArchival bool, expiresAt time.Time) {
	// Check local store first (fast path)
	qos.endpointStore.endpointsMu.RLock()
	endpoint, ok := qos.endpointStore.endpoints[endpointAddr]
	qos.endpointStore.endpointsMu.RUnlock()

	if ok && endpoint.checkArchival.isValid() {
		return true, endpoint.checkArchival.expiresAt
	}

	// Check reputation service (Redis) for shared archival status
	if qos.reputationSvc != nil {
		// IMPORTANT: RPCType_JSON_RPC must match between all archival writes and reads
		// for cross-replica consistency. Health checks, UpdateFromExtractedData, and
		// endpoint selection filtering all must use the same RPC type.
		key := reputation.NewEndpointKey(
			qos.serviceQoSConfig.GetServiceID(),
			endpointAddr,
			sharedtypes.RPCType_JSON_RPC,
		)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		if qos.reputationSvc.IsArchivalCapable(ctx, key) {
			// Get the score to retrieve expiration time
			score, err := qos.reputationSvc.GetScore(ctx, key)
			if err == nil && score.IsArchivalCapable() {
				return true, score.ArchivalExpiresAt
			}
			return true, time.Time{} // Archival but no expiry info
		}
	}

	return false, time.Time{}
}

// SetReputationService sets the reputation service for shared state across replicas.
// The reputation service provides archival status via Redis, enabling non-leader replicas
// to access archival endpoint information set by health checks running on the leader.
func (qos *QoS) SetReputationService(svc reputation.ReputationService) {
	qos.reputationSvc = svc
}

// ConsumeExternalBlockHeight consumes block heights from an external fetcher channel
// and applies them as a floor to the block consensus. This ensures that if all session
// endpoints are behind the real chain tip, the perceived block is corrected.
// Heights are also written to Redis so other replicas benefit.
// The gracePeriod delays applying the external floor after startup, giving suppliers
// time to report their block heights. Use 0 for the default (60s).
func (qos *QoS) ConsumeExternalBlockHeight(ctx context.Context, heights <-chan int64, gracePeriod time.Duration) {
	if gracePeriod > 0 && qos.blockConsensus != nil {
		qos.blockConsensus.SetExternalBlockGracePeriod(gracePeriod)
	}

	// Resolve effective grace period for the Redis write guard.
	// During the grace period, external heights are stored in consensus but NOT
	// written to Redis as perceivedBlockNumber — this prevents other replicas
	// from picking up the external height before suppliers have reported.
	effectiveGrace := defaultExternalBlockGracePeriod
	if gracePeriod > 0 {
		effectiveGrace = gracePeriod
	}
	startedAt := time.Now()

	go func() {
		serviceID := qos.serviceQoSConfig.GetServiceID()
		for {
			select {
			case <-ctx.Done():
				return
			case height, ok := <-heights:
				if !ok {
					return
				}
				if height <= 0 {
					continue
				}
				h := uint64(height)

				if qos.blockConsensus != nil {
					oldExternal := qos.blockConsensus.GetExternalBlockHeight()
					qos.blockConsensus.SetExternalBlockHeight(h)

					if h != oldExternal {
						qos.logger.Debug().
							Str("service_id", string(serviceID)).
							Uint64("external_block", h).
							Uint64("old_external_block", oldExternal).
							Msg("Updated external block height floor")
					}
				}

				// Only write to Redis after the grace period has elapsed.
				// This prevents other replicas from using the external height
				// as perceivedBlockNumber before suppliers have had time to report.
				if time.Since(startedAt) < effectiveGrace {
					continue
				}

				// Write to Redis so other replicas benefit from the external source.
				// Only write if suppliers have already reported (perceived > 0).
				// If no supplier has reported yet, writing external height to Redis
				// could cause other replicas to filter all endpoints.
				if qos.reputationSvc != nil {
					currentPerceived := qos.perceivedBlockNumber.Load()
					if currentPerceived > 0 && h > currentPerceived {
						go func(block uint64) {
							rCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
							defer cancel()
							_ = qos.reputationSvc.SetPerceivedBlockNumber(rCtx, serviceID, block)
						}(h)
					}
				}
			}
		}
	}()
}

// StartBackgroundSync starts a background goroutine that periodically syncs
// perceived block number from Redis. This ensures all replicas converge to
// the same max block number for sync_allowance validation.
//
// The syncInterval determines how often to check Redis (e.g., 5 seconds).
// Call this after SetReputationService to enable cross-replica sync.
//
// IMPORTANT: This performs an immediate sync on startup to ensure the replica
// has the latest perceived block number before serving requests.
func (qos *QoS) StartBackgroundSync(ctx context.Context, syncInterval time.Duration) {
	if qos.reputationSvc == nil {
		qos.logger.Warn().Msg("Cannot start background sync: reputation service not set")
		return
	}

	serviceID := qos.serviceQoSConfig.GetServiceID()

	// syncFromRedis fetches perceived block number from Redis and adds it to local consensus.
	// The Redis value represents consensus from other replicas, so we treat it as another
	// observation and let the local consensus mechanism decide the actual perceived block.
	syncFromRedis := func() {
		fetchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		redisBlockNumber := qos.reputationSvc.GetPerceivedBlockNumber(fetchCtx, serviceID)
		cancel()

		if redisBlockNumber == 0 {
			return // No data in Redis yet
		}

		// Add Redis value as an observation to local consensus
		// Use a synthetic endpoint address to identify Redis-sourced observations
		if qos.blockConsensus != nil {
			oldBlock := qos.perceivedBlockNumber.Load()
			newPerceived := qos.blockConsensus.AddObservation(
				protocol.EndpointAddr("redis-sync"),
				redisBlockNumber,
			)

			// Update the atomic for fast reads
			qos.perceivedBlockNumber.Store(newPerceived)

			if newPerceived != oldBlock {
				qos.logger.Debug().
					Str("service_id", string(serviceID)).
					Uint64("redis_block", redisBlockNumber).
					Uint64("old_perceived", oldBlock).
					Uint64("new_perceived", newPerceived).
					Msg("Updated perceived block number from Redis sync via consensus")
			}
		}
	}

	// Perform immediate sync on startup to have latest data before serving requests
	syncFromRedis()
	qos.logger.Info().
		Str("service_id", string(serviceID)).
		Uint64("perceived_block", qos.perceivedBlockNumber.Load()).
		Msg("Completed initial perceived block number sync from Redis")

	go func() {
		ticker := time.NewTicker(syncInterval)
		defer ticker.Stop()

		qos.logger.Info().
			Str("service_id", string(serviceID)).
			Dur("sync_interval", syncInterval).
			Msg("Started background sync for perceived block number")

		for {
			select {
			case <-ctx.Done():
				qos.logger.Debug().
					Str("service_id", string(serviceID)).
					Msg("Stopping background sync for perceived block number")
				return
			case <-ticker.C:
				syncFromRedis()
			}
		}
	}()
}

// StartArchivalCacheRefreshWorker starts a background goroutine that periodically
// refreshes the archival cache from Redis. This ensures all replicas have consistent
// archival status without blocking the hot request path.
//
// The refreshInterval determines how often to refresh (recommended: 2 hours, with 8-hour TTL).
// Call this after SetReputationService to enable cross-replica sync.
//
// IMPORTANT: Performs immediate refresh on startup to ensure cache is warm before serving requests.
//
// Recommended startup sequence for full cross-replica sync:
//   1. qos.SetReputationService(svc)
//   2. qos.StartBackgroundSync(ctx, 5*time.Second)        // Perceived block number
//   3. qos.StartArchivalCacheRefreshWorker(ctx, 2*time.Hour) // Archival status
func (qos *QoS) StartArchivalCacheRefreshWorker(ctx context.Context, refreshInterval time.Duration) {
	if qos.reputationSvc == nil {
		qos.logger.Warn().Msg("Cannot start archival cache refresh: reputation service not set")
		return
	}
	if qos.archivalCache == nil {
		qos.logger.Warn().Msg("Cannot start archival cache refresh: cache not initialized")
		return
	}

	serviceID := qos.serviceQoSConfig.GetServiceID()

	refreshFromRedis := func() {
		qos.refreshArchivalCacheFromRedis(ctx)
	}

	// Immediate refresh on startup for cache warming
	refreshFromRedis()
	qos.logger.Info().
		Str("service_id", string(serviceID)).
		Msg("Completed initial archival cache refresh from Redis")

	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()

		// Also run cleanup every hour to remove expired entries
		cleanupTicker := time.NewTicker(1 * time.Hour)
		defer cleanupTicker.Stop()

		qos.logger.Info().
			Str("service_id", string(serviceID)).
			Dur("refresh_interval", refreshInterval).
			Msg("Started archival cache refresh worker")

		for {
			select {
			case <-ctx.Done():
				qos.logger.Debug().
					Str("service_id", string(serviceID)).
					Msg("Stopping archival cache refresh worker")
				return
			case <-ticker.C:
				qos.logger.Debug().Msg("Refreshing archival cache from Redis")
				refreshFromRedis()
			case <-cleanupTicker.C:
				qos.logger.Debug().Msg("Cleaning up expired archival cache entries")
				qos.archivalCache.Cleanup()
			}
		}
	}()
}

// refreshArchivalCacheFromRedis fetches archival status for all known endpoints from Redis
// and updates the local cache. This runs in background, not blocking requests.
//
// Two sources of endpoints are checked:
//  1. Local endpointStore - works once endpoints are populated from sessions
//  2. Reputation service cache (GetArchivalEndpoints) - works at cold start when
//     endpointStore is empty but reputation.Start() has already loaded data from Redis
func (qos *QoS) refreshArchivalCacheFromRedis(parentCtx context.Context) {
	serviceID := qos.serviceQoSConfig.GetServiceID()

	// Use 8-hour TTL matching archival status expiry
	const archivalTTL = 8 * time.Hour

	var refreshed, failed int

	// Pass 1: Check all endpoints from local endpointStore
	qos.endpointStore.endpointsMu.RLock()
	endpoints := make([]protocol.EndpointAddr, 0, len(qos.endpointStore.endpoints))
	for addr := range qos.endpointStore.endpoints {
		endpoints = append(endpoints, addr)
	}
	qos.endpointStore.endpointsMu.RUnlock()

	if len(endpoints) > 0 {
		qos.logger.Debug().
			Str("service_id", string(serviceID)).
			Int("endpoint_count", len(endpoints)).
			Msg("Refreshing archival cache from endpoint store")

		for _, addr := range endpoints {
			key := reputation.NewEndpointKey(
				serviceID,
				addr,
				sharedtypes.RPCType_JSON_RPC,
			)

			// Use 2-second timeout per Redis operation (not aggressive per research)
			ctx, cancel := context.WithTimeout(parentCtx, 2*time.Second)
			isArchival := qos.reputationSvc.IsArchivalCapable(ctx, key)
			cancel()

			if ctx.Err() != nil {
				failed++
				continue
			}

			if isArchival {
				qos.archivalCache.Set(key.String(), true, archivalTTL)
				refreshed++
			}
		}
	}

	// Pass 2: Bootstrap from reputation service cache.
	// At cold start, endpointStore is empty but reputation.Start() has already
	// loaded all data from Redis into its local cache. Query it directly.
	archivalKeys := qos.reputationSvc.GetArchivalEndpoints(parentCtx, serviceID)
	var bootstrapped int
	for _, key := range archivalKeys {
		cacheKey := key.String()
		if isArchival, ok := qos.archivalCache.Get(cacheKey); !ok || !isArchival {
			qos.archivalCache.Set(cacheKey, true, archivalTTL)
			bootstrapped++
		}
	}

	qos.logger.Info().
		Str("service_id", string(serviceID)).
		Int("refreshed_from_store", refreshed).
		Int("bootstrapped_from_reputation", bootstrapped).
		Int("failed", failed).
		Int("store_endpoints", len(endpoints)).
		Int("reputation_archival_keys", len(archivalKeys)).
		Msg("Archival cache refresh complete")
}
