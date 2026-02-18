package solana

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/metrics/devtools"
	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	qostypes "github.com/pokt-network/path/qos/types"
	"github.com/pokt-network/path/reputation"
)

// QoS implements gateway.QoSService by providing:
//  1. QoSRequestParser - Builds Solana-specific RequestQoSContext objects from HTTP requests
//  2. EndpointSelector - Selects endpoints for service requests
var _ gateway.QoSService = &QoS{}

// devtools.QoSDisqualifiedEndpointsReporter is fulfilled by the QoS struct below.
// This allows the QoS service to report its disqualified endpoints data to the devtools.DisqualifiedEndpointReporter.
// TODO_TECHDEBT(@commoddity): implement this for Solana to enable debugging QoS results.
var _ devtools.QoSDisqualifiedEndpointsReporter = &QoS{}

// QoS implements ServiceQoS for Solana-based chains.
// It handles chain-specific:
//   - Request parsing
//   - Response building
//   - Endpoint validation and selection
type QoS struct {
	logger polylog.Logger
	*EndpointStore
	*ServiceState
	*requestValidator

	// reputationSvc provides shared perceived block height across replicas via Redis.
	// Set via SetReputationService; nil when reputation is disabled.
	reputationSvc reputation.ReputationService
}

// ParseHTTPRequest builds a request context from the provided HTTP request.
// It returns an error if the HTTP request cannot be parsed as a JSONRPC request.
//
// Implements the gateway.QoSService interface.
// Fallback logic for Solana: header → jsonrpc (Solana only supports JSON-RPC)
func (qos *QoS) ParseHTTPRequest(_ context.Context, req *http.Request, detectedRPCType sharedtypes.RPCType) (gateway.RequestQoSContext, bool) {
	return qos.validateHTTPRequest(req, detectedRPCType)
}

// ParseWebsocketRequest builds a request context from the provided Websocket request.
// Websocket connection requests do not have a body, so we don't need to parse it.
//
// This method implements the gateway.QoSService interface.
func (qos *QoS) ParseWebsocketRequest(_ context.Context) (gateway.RequestQoSContext, bool) {
	return &requestContext{
		logger:        qos.logger,
		endpointStore: qos.EndpointStore,
		// Set the origin of the request as Organic (i.e. user request)
		// The request is from a user.
		requestOrigin: qosobservations.RequestOrigin_REQUEST_ORIGIN_ORGANIC,
	}, true
}

// ApplyObservations updates the stored endpoints and the perceived blockchain state using the supplied observations.
// Implements the gateway.QoSService interface.
func (q *QoS) ApplyObservations(observations *qosobservations.Observations) error {
	if observations == nil {
		return errors.New("ApplyObservations: received nil observations")
	}

	solanaObservations := observations.GetSolana()
	if solanaObservations == nil {
		return errors.New("ApplyObservations: received nil Solana observation")
	}

	updatedEndpoints := q.UpdateEndpointsFromObservations(solanaObservations)

	// update the perceived current state of the blockchain.
	return q.UpdateFromEndpoints(updatedEndpoints)
}

// HydrateDisqualifiedEndpointsResponse is a no-op for the Solana QoS.
// TODO_TECHDEBT(@commoddity): implement this for Solana to enable debugging QoS results.
func (QoS) HydrateDisqualifiedEndpointsResponse(_ protocol.ServiceID, _ *devtools.DisqualifiedEndpointResponse) {
}

// UpdateFromExtractedData updates QoS state from extracted observation data.
// Called by the observation pipeline after async parsing completes.
// This updates the perceived block height and stores the endpoint's block height observation.
//
// Implements gateway.QoSService interface.
func (q *QoS) UpdateFromExtractedData(endpointAddr protocol.EndpointAddr, data *qostypes.ExtractedData) error {
	if data == nil {
		return nil
	}

	// Only update if we extracted a valid block height
	if data.BlockHeight <= 0 {
		return nil
	}

	blockHeight := uint64(data.BlockHeight)

	// Lock the endpoint store to update the endpoint observation
	q.endpointsMu.Lock()
	defer q.endpointsMu.Unlock()

	// Initialize the map if nil (can happen if UpdateFromExtractedData is called
	// before any UpdateEndpointsFromObservations calls)
	if q.endpoints == nil {
		q.endpoints = make(map[protocol.EndpointAddr]endpoint)
	}

	storedEndpoint := q.endpoints[endpointAddr]

	// Update the endpoint's block height observation (Solana uses block height from getEpochInfo)
	// Create or update the SolanaGetEpochInfoResponse with just the block height
	if storedEndpoint.SolanaGetEpochInfoResponse == nil {
		storedEndpoint.SolanaGetEpochInfoResponse = &qosobservations.SolanaGetEpochInfoResponse{}
	}
	storedEndpoint.BlockHeight = blockHeight

	// Store the updated endpoint back
	q.endpoints[endpointAddr] = storedEndpoint

	// Lock service state to update perceived block height
	q.serviceStateLock.Lock()
	defer q.serviceStateLock.Unlock()

	// Update perceived block height to maximum across all endpoints
	if blockHeight > q.perceivedBlockHeight {
		q.logger.Debug().
			Str("endpoint", string(endpointAddr)).
			Uint64("old_block", q.perceivedBlockHeight).
			Uint64("new_block", blockHeight).
			Msg("Updating perceived block height from observation pipeline")
		q.perceivedBlockHeight = blockHeight

		// Write to Redis for cross-replica sync (async, non-blocking)
		if q.reputationSvc != nil {
			go func(bn uint64, svcID protocol.ServiceID) {
				rCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = q.reputationSvc.SetPerceivedBlockNumber(rCtx, svcID, bn)
			}(blockHeight, q.ServiceState.serviceID)
		}
	}

	// Write per-endpoint block height to Redis for cross-replica sync (async, non-blocking)
	if q.reputationSvc != nil {
		go func(addr protocol.EndpointAddr, bn uint64, svcID protocol.ServiceID) {
			rCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = q.reputationSvc.SetEndpointBlockHeight(rCtx, svcID, addr, bn)
		}(endpointAddr, blockHeight, q.ServiceState.serviceID)
	}

	return nil
}

// GetPerceivedBlockNumber returns the perceived current block height.
// Used by health checks for block height validation.
// Returns 0 if no block height has been observed yet.
//
// Implements gateway.QoSService interface.
func (q *QoS) GetPerceivedBlockNumber() uint64 {
	q.serviceStateLock.RLock()
	defer q.serviceStateLock.RUnlock()
	return q.perceivedBlockHeight
}

// ConsumeExternalBlockHeight consumes block heights from an external fetcher channel
// and uses them as a floor for perceivedBlockHeight. This ensures that if all session
// endpoints are behind the real chain tip, the perceived block is corrected.
// The gracePeriod delays applying the external floor after startup, giving suppliers
// time to report their block heights. Use 0 for the default (60s).
func (q *QoS) ConsumeExternalBlockHeight(ctx context.Context, heights <-chan int64, gracePeriod time.Duration) {
	const defaultGracePeriod = 60 * time.Second
	effectiveGrace := defaultGracePeriod
	if gracePeriod > 0 {
		effectiveGrace = gracePeriod
	}
	startedAt := time.Now()

	go func() {
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
					q.logger.Debug().
						Int64("external_block", height).
						Dur("grace_remaining", effectiveGrace-time.Since(startedAt)).
						Msg("External block floor deferred — grace period active")
					continue
				}

				h := uint64(height)
				q.serviceStateLock.Lock()
				// Only apply external floor if suppliers have already reported.
				// If perceivedBlockHeight is still 0, no supplier has reported yet
				// and applying the floor would filter ALL endpoints.
				if q.perceivedBlockHeight == 0 {
					q.serviceStateLock.Unlock()
					q.logger.Debug().
						Uint64("external_block", h).
						Msg("External block floor skipped — no suppliers have reported yet")
					continue
				}
				if h > q.perceivedBlockHeight {
					q.logger.Info().
						Uint64("old_perceived", q.perceivedBlockHeight).
						Uint64("external_block", h).
						Msg("External block floor applied — raising perceived block height")
					q.perceivedBlockHeight = h

					// Write to Redis so other replicas benefit from the external source.
					if q.reputationSvc != nil {
						go func(bn uint64, svcID protocol.ServiceID) {
							rCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
							defer cancel()
							_ = q.reputationSvc.SetPerceivedBlockNumber(rCtx, svcID, bn)
						}(h, q.ServiceState.serviceID)
					}
				}
				q.serviceStateLock.Unlock()
			}
		}
	}()
}

// SetReputationService sets the reputation service for shared state across replicas.
// The reputation service provides perceived block height via Redis, enabling
// all replicas to converge on the same chain tip for sync_allowance validation.
func (q *QoS) SetReputationService(svc reputation.ReputationService) {
	q.reputationSvc = svc
}

// StartBackgroundSync starts a background goroutine that periodically syncs
// perceived block height from Redis. This ensures all replicas converge to
// the same max block height for sync_allowance validation.
//
// The syncInterval determines how often to check Redis (e.g., 5 seconds).
// Call this after SetReputationService to enable cross-replica sync.
//
// IMPORTANT: This performs an immediate sync on startup to ensure the replica
// has the latest perceived block height before serving requests.
func (q *QoS) StartBackgroundSync(ctx context.Context, syncInterval time.Duration) {
	if q.reputationSvc == nil {
		q.logger.Warn().Msg("Cannot start background sync: reputation service not set")
		return
	}

	serviceID := q.ServiceState.serviceID

	// syncFromRedis fetches perceived block height from Redis and updates local
	// state if the Redis value is higher (simple max semantics).
	syncFromRedis := func() {
		fetchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		redisBlockHeight := q.reputationSvc.GetPerceivedBlockNumber(fetchCtx, serviceID)
		cancel()

		if redisBlockHeight == 0 {
			return // No data in Redis yet
		}

		q.serviceStateLock.Lock()
		oldBlock := q.perceivedBlockHeight
		if redisBlockHeight > q.perceivedBlockHeight {
			q.perceivedBlockHeight = redisBlockHeight
		}
		q.serviceStateLock.Unlock()

		if redisBlockHeight > oldBlock {
			q.logger.Debug().
				Str("service_id", string(serviceID)).
				Uint64("redis_block", redisBlockHeight).
				Uint64("old_perceived", oldBlock).
				Uint64("new_perceived", redisBlockHeight).
				Msg("Updated perceived block height from Redis sync")
		}
	}

	// syncEndpointBlocksFromRedis fetches per-endpoint block heights from Redis
	// and updates local endpoint store entries where Redis has a higher value.
	// Only updates EXISTING entries (Solana's validateBasic needs health+epoch data).
	syncEndpointBlocksFromRedis := func() {
		fetchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		heights := q.reputationSvc.GetEndpointBlockHeights(fetchCtx, serviceID)
		cancel()

		if len(heights) == 0 {
			return
		}

		q.endpointsMu.Lock()
		updated := 0
		for addr, redisHeight := range heights {
			ep, exists := q.endpoints[addr]
			if !exists {
				continue // Only update existing entries
			}
			if ep.SolanaGetEpochInfoResponse == nil {
				continue // Need epoch info to be initialized
			}
			if redisHeight > ep.BlockHeight {
				ep.BlockHeight = redisHeight
				q.endpoints[addr] = ep
				updated++
			}
		}
		q.endpointsMu.Unlock()

		if updated > 0 {
			q.logger.Debug().
				Str("service_id", string(serviceID)).
				Int("updated_endpoints", updated).
				Int("redis_endpoints", len(heights)).
				Msg("Synced per-endpoint block heights from Redis")
		}
	}

	// Perform immediate sync on startup to have latest data before serving requests
	syncFromRedis()
	syncEndpointBlocksFromRedis()
	q.logger.Info().
		Str("service_id", string(serviceID)).
		Uint64("perceived_block", q.perceivedBlockHeight).
		Msg("Completed initial perceived block height sync from Redis")

	go func() {
		ticker := time.NewTicker(syncInterval)
		defer ticker.Stop()

		q.logger.Info().
			Str("service_id", string(serviceID)).
			Dur("sync_interval", syncInterval).
			Msg("Started background sync for perceived block height")

		for {
			select {
			case <-ctx.Done():
				q.logger.Debug().
					Str("service_id", string(serviceID)).
					Msg("Stopping background sync for perceived block height")
				return
			case <-ticker.C:
				syncFromRedis()
				syncEndpointBlocksFromRedis()
			}
		}
	}()
}
