package evm

import (
	"context"
	"net/http"
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
		serviceID:     serviceID,
		syncAllowance: syncAllowance,
	}

	// Create block consensus calculator for robust perceived block height
	blockConsensus := NewBlockHeightConsensus(logger, minimalConfig.getSyncAllowance())

	serviceState := &serviceState{
		logger:            logger,
		serviceQoSConfig:  minimalConfig,
		endpointStore:     store,
		archivalHeuristic: NewArchivalHeuristic(0), // Use default threshold
		blockConsensus:    blockConsensus,
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
	syncAllowance uint64 // If 0, uses default
}

func (c *simpleServiceConfig) GetServiceID() protocol.ServiceID { return c.serviceID }
func (c *simpleServiceConfig) GetServiceQoSType() string        { return QoSType }
func (c *simpleServiceConfig) getEVMChainID() string            { return "" }
func (c *simpleServiceConfig) getSyncAllowance() uint64 {
	if c.syncAllowance == 0 {
		return defaultEVMBlockNumberSyncAllowance
	}
	return c.syncAllowance
}
func (c *simpleServiceConfig) getSupportedAPIs() map[sharedtypes.RPCType]struct{} {
	return map[sharedtypes.RPCType]struct{}{sharedtypes.RPCType_JSON_RPC: {}}
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

	// Update archival status from health check.
	// If IsArchival is true, mark the endpoint as archival-capable.
	// This status is used by endpoint selection to filter archival-capable endpoints
	// when a request requires historical blockchain data.
	if data.IsArchival {
		storedEndpoint.checkArchival = endpointCheckArchival{
			isArchival: true,
			expiresAt:  time.Now().Add(checkArchivalTTL),
		}
		qos.logger.Debug().
			Str("endpoint", string(endpointAddr)).
			Msg("Marked endpoint as archival-capable from health check")
	}

	// Store the updated endpoint back
	qos.endpointStore.endpoints[endpointAddr] = storedEndpoint
	qos.endpointStore.endpointsMu.Unlock()

	// Update perceived block using consensus mechanism
	// This protects against malicious endpoints reporting extreme block heights
	if blockNumber > 0 && qos.serviceState.blockConsensus != nil {
		oldBlock := qos.perceivedBlockNumber.Load()
		newPerceived := qos.serviceState.blockConsensus.AddObservation(endpointAddr, blockNumber)

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
			if qos.serviceState.reputationSvc != nil {
				go func(bn uint64) {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					_ = qos.serviceState.reputationSvc.SetPerceivedBlockNumber(ctx, qos.serviceState.serviceQoSConfig.GetServiceID(), bn)
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
	if qos.serviceState.blockConsensus == nil {
		return 0, 0
	}
	return qos.serviceState.blockConsensus.GetMedianBlock(), qos.serviceState.blockConsensus.GetObservationCount()
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
	if qos.serviceState.reputationSvc != nil {
		key := reputation.NewEndpointKey(
			qos.serviceState.serviceQoSConfig.GetServiceID(),
			endpointAddr,
			sharedtypes.RPCType_JSON_RPC,
		)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		if qos.serviceState.reputationSvc.IsArchivalCapable(ctx, key) {
			// Get the score to retrieve expiration time
			score, err := qos.serviceState.reputationSvc.GetScore(ctx, key)
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
	qos.serviceState.reputationSvc = svc
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
	if qos.serviceState.reputationSvc == nil {
		qos.logger.Warn().Msg("Cannot start background sync: reputation service not set")
		return
	}

	serviceID := qos.serviceState.serviceQoSConfig.GetServiceID()

	// syncFromRedis fetches perceived block number from Redis and adds it to local consensus.
	// The Redis value represents consensus from other replicas, so we treat it as another
	// observation and let the local consensus mechanism decide the actual perceived block.
	syncFromRedis := func() {
		fetchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		redisBlockNumber := qos.serviceState.reputationSvc.GetPerceivedBlockNumber(fetchCtx, serviceID)
		cancel()

		if redisBlockNumber == 0 {
			return // No data in Redis yet
		}

		// Add Redis value as an observation to local consensus
		// Use a synthetic endpoint address to identify Redis-sourced observations
		if qos.serviceState.blockConsensus != nil {
			oldBlock := qos.perceivedBlockNumber.Load()
			newPerceived := qos.serviceState.blockConsensus.AddObservation(
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
