package cosmos

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
//  1. QoSRequestParser - Builds CosmosSDK-specific RequestQoSContext objects from HTTP requests
//  2. EndpointSelector - Selects endpoints for service requests
var _ gateway.QoSService = &QoS{}

// devtools.QoSDisqualifiedEndpointsReporter is fulfilled by the QoS struct below.
// This allows the QoS service to report its disqualified endpoints data to the devtools.DisqualifiedEndpointReporter.
var _ devtools.QoSDisqualifiedEndpointsReporter = &QoS{}

// QoS implements ServiceQoS for CosmosSDK-based chains.
// It handles chain-specific:
//   - Request parsing (both REST and JSON-RPC)
//   - Response building
//   - Endpoint validation and selection
type QoS struct {
	logger polylog.Logger
	*serviceState
	*requestValidator

	// reputationSvc provides shared perceived block number across replicas via Redis.
	// Set via SetReputationService; nil when reputation is disabled.
	reputationSvc reputation.ReputationService
}

// NewSimpleQoSInstance creates a minimal CosmosSDK QoS instance without chain-specific validation.
// Validation (chain ID, etc.) is now handled by active health checks.
// This constructor only requires the service ID and provides REST/JSON-RPC request parsing.
func NewSimpleQoSInstance(logger polylog.Logger, serviceID protocol.ServiceID) *QoS {
	return NewSimpleQoSInstanceWithSyncAllowance(logger, serviceID, 0)
}

// NewSimpleQoSInstanceWithSyncAllowance creates a minimal CosmosSDK QoS instance with custom sync allowance.
// Validation (chain ID, etc.) is now handled by active health checks.
// If syncAllowance is 0, the default value is used.
func NewSimpleQoSInstanceWithSyncAllowance(logger polylog.Logger, serviceID protocol.ServiceID, syncAllowance uint64) *QoS {
	// Default supported APIs for CosmosSDK chains (REST and CometBFT)
	defaultAPIs := map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {},
		sharedtypes.RPCType_COMET_BFT: {},
	}
	return NewSimpleQoSInstanceWithAPIs(logger, serviceID, syncAllowance, defaultAPIs)
}

// NewSimpleQoSInstanceWithAPIs creates a minimal CosmosSDK QoS instance with custom supported APIs.
// This is useful for hybrid chains (e.g., XRPLEVM) that support both CosmosSDK and EVM APIs.
// Validation (chain ID, etc.) is now handled by active health checks.
// If syncAllowance is 0, the default value is used.
func NewSimpleQoSInstanceWithAPIs(logger polylog.Logger, serviceID protocol.ServiceID, syncAllowance uint64, supportedAPIs map[sharedtypes.RPCType]struct{}) *QoS {
	logger = logger.With(
		"qos_instance", "cosmossdk",
		"service_id", serviceID,
	)

	store := &endpointStore{
		logger:    logger,
		endpoints: make(map[protocol.EndpointAddr]endpoint),
	}

	// Create a minimal config wrapper
	minimalConfig := &simpleCosmosConfig{
		serviceID:     serviceID,
		supportedAPIs: supportedAPIs,
	}
	minimalConfig.syncAllowance.Store(syncAllowance)

	serviceState := &serviceState{
		logger:           logger,
		serviceQoSConfig: minimalConfig,
		endpointStore:    store,
	}

	requestValidator := &requestValidator{
		logger:        logger,
		serviceID:     serviceID,
		cosmosChainID: "", // No chain ID validation - handled by health checks
		evmChainID:    "",
		serviceState:  serviceState,
		supportedAPIs: supportedAPIs,
	}

	return &QoS{
		logger:           logger,
		serviceState:     serviceState,
		requestValidator: requestValidator,
	}
}

// simpleCosmosConfig is a minimal config for services without chain-specific params.
type simpleCosmosConfig struct {
	serviceID     protocol.ServiceID
	syncAllowance atomic.Uint64                    // If 0, uses default. Updated dynamically when external health check rules are loaded.
	supportedAPIs map[sharedtypes.RPCType]struct{} // Supported RPC types
}

func (c *simpleCosmosConfig) GetServiceID() protocol.ServiceID { return c.serviceID }
func (c *simpleCosmosConfig) GetServiceQoSType() string        { return QoSType }
func (c *simpleCosmosConfig) getCosmosSDKChainID() string      { return "" }
func (c *simpleCosmosConfig) getEVMChainID() string            { return "" }
func (c *simpleCosmosConfig) getSyncAllowance() uint64 {
	if v := c.syncAllowance.Load(); v > 0 {
		return v
	}
	return defaultCosmosSDKBlockNumberSyncAllowance
}

// SetSyncAllowance dynamically updates the sync allowance for this QoS instance.
// This is called when external health check rules are loaded/refreshed, since those
// rules may specify a sync_allowance that wasn't available at QoS creation time.
func (qos *QoS) SetSyncAllowance(syncAllowance uint64) {
	if cfg, ok := qos.serviceQoSConfig.(*simpleCosmosConfig); ok {
		cfg.syncAllowance.Store(syncAllowance)
	}
}
func (c *simpleCosmosConfig) getSupportedAPIs() map[sharedtypes.RPCType]struct{} {
	if c.supportedAPIs != nil {
		return c.supportedAPIs
	}
	// Default to REST and COMET_BFT if not configured
	return map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {},
		sharedtypes.RPCType_COMET_BFT: {},
	}
}

// ParseHTTPRequest builds a request context from an HTTP request.
// Returns (requestContext, true) if the request is valid
// Returns (errorContext, false) if the request is not valid.
//
// Supports both REST endpoints (/health, /status) and JSON-RPC requests.
//
// Implements gateway.QoSService interface.
func (qos *QoS) ParseHTTPRequest(_ context.Context, req *http.Request, detectedRPCType sharedtypes.RPCType) (gateway.RequestQoSContext, bool) {
	return qos.validateHTTPRequest(req, detectedRPCType)
}

// ParseWebsocketRequest builds a request context from the provided Websocket request.
// Websocket connection requests do not have a body, so we don't need to parse it.
//
// Implements gateway.QoSService interface.
func (qos *QoS) ParseWebsocketRequest(_ context.Context) (gateway.RequestQoSContext, bool) {
	return qos.validateWebsocketRequest()
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
// This updates the perceived block number and stores the endpoint's block number observation.
//
// Implements gateway.QoSService interface.
func (qos *QoS) UpdateFromExtractedData(endpointAddr protocol.EndpointAddr, data *qostypes.ExtractedData) error {
	if data == nil {
		return nil
	}

	// Determine block number to store
	var blockNumber uint64
	var hasBlock bool

	if data.BlockHeight > 0 {
		blockNumber = uint64(data.BlockHeight)
		hasBlock = true
	} else if data.InvalidBlockHeight {
		// Supplier returned an unparseable block height result — store 0 so filter catches it
		blockNumber = 0
		hasBlock = true
	}

	if !hasBlock {
		return nil
	}

	// Lock the endpoint store to update the endpoint observation
	qos.endpointStore.endpointsMu.Lock()
	storedEndpoint := qos.endpointStore.endpoints[endpointAddr]

	// Update the endpoint's block height observation in CometBFT status check
	storedEndpoint.checkCometBFTStatus.latestBlockHeight = &blockNumber

	// Store the updated endpoint back
	qos.endpointStore.endpoints[endpointAddr] = storedEndpoint
	qos.endpointStore.endpointsMu.Unlock()

	// Lock service state to update perceived block number
	qos.serviceStateLock.Lock()
	defer qos.serviceStateLock.Unlock()

	// Update perceived block number to maximum across all endpoints
	if blockNumber > qos.perceivedBlockNumber {
		qos.logger.Debug().
			Str("endpoint", string(endpointAddr)).
			Uint64("old_block", qos.perceivedBlockNumber).
			Uint64("new_block", blockNumber).
			Msg("Updating perceived block number from observation pipeline")
		qos.perceivedBlockNumber = blockNumber

		// Write to Redis for cross-replica sync (async, non-blocking)
		if qos.reputationSvc != nil {
			go func(bn uint64, svcID protocol.ServiceID) {
				rCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = qos.reputationSvc.SetPerceivedBlockNumber(rCtx, svcID, bn)
			}(blockNumber, qos.serviceQoSConfig.GetServiceID())
		}
	}

	// Write per-endpoint block height to Redis for cross-replica sync (async, non-blocking)
	if qos.reputationSvc != nil {
		go func(addr protocol.EndpointAddr, bn uint64, svcID protocol.ServiceID) {
			rCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = qos.reputationSvc.SetEndpointBlockHeight(rCtx, svcID, addr, bn)
		}(endpointAddr, blockNumber, qos.serviceQoSConfig.GetServiceID())
	}

	return nil
}

// GetPerceivedBlockNumber returns the perceived current block number.
// Used by health checks for block height validation.
// Returns 0 if no block number has been observed yet.
//
// Implements gateway.QoSService interface.
func (qos *QoS) GetPerceivedBlockNumber() uint64 {
	qos.serviceStateLock.RLock()
	defer qos.serviceStateLock.RUnlock()
	return qos.perceivedBlockNumber
}

// ConsumeExternalBlockHeight consumes block heights from an external fetcher channel
// and uses them as a floor for perceivedBlockNumber. This ensures that if all session
// endpoints are behind the real chain tip, the perceived block is corrected.
// The gracePeriod delays applying the external floor after startup, giving suppliers
// time to report their block heights. Use 0 for the default (60s).
func (qos *QoS) ConsumeExternalBlockHeight(ctx context.Context, heights <-chan int64, gracePeriod time.Duration) {
	const defaultGracePeriod = 60 * time.Second
	effectiveGrace := defaultGracePeriod
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

				// Skip during grace period to let suppliers report first
				if time.Since(startedAt) < effectiveGrace {
					qos.logger.Debug().
						Str("service_id", string(serviceID)).
						Int64("external_block", height).
						Dur("grace_remaining", effectiveGrace-time.Since(startedAt)).
						Msg("External block floor deferred — grace period active")
					continue
				}

				h := uint64(height)
				qos.serviceStateLock.Lock()
				// Only apply external floor if suppliers have already reported.
				// If perceivedBlockNumber is still 0, no supplier has reported yet
				// and applying the floor would filter ALL endpoints.
				if qos.perceivedBlockNumber == 0 {
					qos.serviceStateLock.Unlock()
					qos.logger.Debug().
						Str("service_id", string(serviceID)).
						Uint64("external_block", h).
						Msg("External block floor skipped — no suppliers have reported yet")
					continue
				}
				if h > qos.perceivedBlockNumber {
					qos.logger.Info().
						Str("service_id", string(serviceID)).
						Uint64("old_perceived", qos.perceivedBlockNumber).
						Uint64("external_block", h).
						Msg("External block floor applied — raising perceived block")
					qos.perceivedBlockNumber = h

					// Write to Redis so other replicas benefit from the external source.
					if qos.reputationSvc != nil {
						go func(bn uint64, svcID protocol.ServiceID) {
							rCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
							defer cancel()
							_ = qos.reputationSvc.SetPerceivedBlockNumber(rCtx, svcID, bn)
						}(h, serviceID)
					}
				}
				qos.serviceStateLock.Unlock()
			}
		}
	}()
}

// SetReputationService sets the reputation service for shared state across replicas.
// The reputation service provides perceived block number via Redis, enabling
// all replicas to converge on the same chain tip for sync_allowance validation.
func (qos *QoS) SetReputationService(svc reputation.ReputationService) {
	qos.reputationSvc = svc
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

	// syncFromRedis fetches perceived block number from Redis and updates local
	// state if the Redis value is higher (simple max semantics).
	syncFromRedis := func() {
		fetchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		redisBlockNumber := qos.reputationSvc.GetPerceivedBlockNumber(fetchCtx, serviceID)
		cancel()

		if redisBlockNumber == 0 {
			return // No data in Redis yet
		}

		qos.serviceStateLock.Lock()
		oldBlock := qos.perceivedBlockNumber
		if redisBlockNumber > qos.perceivedBlockNumber {
			qos.perceivedBlockNumber = redisBlockNumber
		}
		qos.serviceStateLock.Unlock()

		if redisBlockNumber > oldBlock {
			qos.logger.Debug().
				Str("service_id", string(serviceID)).
				Uint64("redis_block", redisBlockNumber).
				Uint64("old_perceived", oldBlock).
				Uint64("new_perceived", redisBlockNumber).
				Msg("Updated perceived block number from Redis sync")
		}
	}

	// syncEndpointBlocksFromRedis fetches per-endpoint block heights from Redis
	// and updates local endpoint store entries where Redis has a higher value.
	syncEndpointBlocksFromRedis := func() {
		fetchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		heights := qos.reputationSvc.GetEndpointBlockHeights(fetchCtx, serviceID)
		cancel()

		if len(heights) == 0 {
			return
		}

		// Build a URL-only lookup for block heights.
		// Multiple suppliers can share the same relay miner URL, and supplier addresses
		// rotate across sessions. The block height is a property of the URL (the backend),
		// not the supplier address, so we use URL as a fallback key when the full
		// supplierAddr-URL key doesn't match the current session's endpoints.
		urlHeights := make(map[string]uint64, len(heights))
		for addr, h := range heights {
			if url, err := addr.GetURL(); err == nil {
				if existing, ok := urlHeights[url]; !ok || h > existing {
					urlHeights[url] = h
				}
			}
		}

		qos.endpointStore.endpointsMu.Lock()
		updated := 0
		for addr, redisHeight := range heights {
			redisH := redisHeight // copy for pointer
			ep, exists := qos.endpointStore.endpoints[addr]
			if !exists {
				// Create a new entry with just the block height from Redis.
				// This allows non-leader replicas to filter stale endpoints
				// before health checks or relay responses populate the store.
				qos.endpointStore.endpoints[addr] = endpoint{
					checkCometBFTStatus: endpointCheckCometBFTStatus{latestBlockHeight: &redisH},
					lastSeen:            time.Now(),
				}
				updated++
				continue
			}
			localHeight := uint64(0)
			if ep.checkCometBFTStatus.latestBlockHeight != nil {
				localHeight = *ep.checkCometBFTStatus.latestBlockHeight
			}
			if redisHeight > localHeight {
				ep.checkCometBFTStatus.latestBlockHeight = &redisH
				qos.endpointStore.endpoints[addr] = ep
				updated++
			}
		}

		// Second pass: populate session endpoints that weren't in Redis by URL fallback.
		// This handles the case where a session has new supplier addresses for the same
		// relay miner URL — the block height from the previous session's supplier applies.
		for addr, ep := range qos.endpointStore.endpoints {
			if ep.checkCometBFTStatus.latestBlockHeight != nil {
				continue // already has a block height
			}
			url, err := addr.GetURL()
			if err != nil {
				continue
			}
			if h, ok := urlHeights[url]; ok {
				hCopy := h
				ep.checkCometBFTStatus.latestBlockHeight = &hCopy
				qos.endpointStore.endpoints[addr] = ep
				updated++
			}
		}

		qos.endpointStore.endpointsMu.Unlock()

		if updated > 0 {
			qos.logger.Debug().
				Str("service_id", string(serviceID)).
				Int("updated_endpoints", updated).
				Int("redis_endpoints", len(heights)).
				Msg("Synced per-endpoint block heights from Redis")
		}
	}

	// sweepStaleEndpoints removes endpoint entries that haven't been seen
	// in any active session within the TTL, and cleans up their Redis entries.
	sweepStaleEndpoints := func() {
		removed := qos.endpointStore.sweepStaleEndpoints(defaultEndpointTTL)
		if len(removed) == 0 {
			return
		}

		qos.logger.Info().
			Str("service_id", string(serviceID)).
			Int("removed_endpoints", len(removed)).
			Msg("Swept stale endpoints from local store")

		sweepCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := qos.reputationSvc.RemoveEndpointBlockHeights(sweepCtx, serviceID, removed); err != nil {
			qos.logger.Warn().
				Err(err).
				Str("service_id", string(serviceID)).
				Int("count", len(removed)).
				Msg("Failed to remove stale endpoint block heights from Redis")
		}
	}

	// Perform immediate sync on startup to have latest data before serving requests
	syncFromRedis()
	syncEndpointBlocksFromRedis()
	qos.logger.Info().
		Str("service_id", string(serviceID)).
		Uint64("perceived_block", qos.perceivedBlockNumber).
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
				syncEndpointBlocksFromRedis()
				sweepStaleEndpoints()
			}
		}
	}()
}
