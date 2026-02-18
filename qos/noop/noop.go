// package noop implements a noop QoS module, enabling a gateway operator to support services
// which do not yet have a QoS implementation.
package noop

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
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

// maxRequestBodySize is the maximum allowed size for HTTP request bodies (100MB).
// This prevents OOM attacks from unbounded io.ReadAll calls.
const maxRequestBodySize = 100 * 1024 * 1024

var _ gateway.QoSService = &NoOpQoS{}

// NoOpQoS provides a pass-through QoS service with optional block height tracking
// and endpoint filtering. When syncAllowance is 0 (the default), behavior is
// identical to a stateless random selector. When syncAllowance > 0 and block
// height data is available, stale endpoints are filtered out.
type NoOpQoS struct {
	logger    polylog.Logger
	serviceID protocol.ServiceID

	// endpointStore holds per-endpoint block height observations.
	endpointStore *endpointStore

	// serviceStateMu protects perceivedBlockHeight.
	serviceStateMu sync.RWMutex
	// perceivedBlockHeight is the maximum block height seen across all endpoints.
	perceivedBlockHeight uint64

	// syncAllowance is the number of blocks an endpoint may lag behind the
	// perceived block height before being filtered. 0 disables filtering.
	syncAllowance atomic.Uint64

	// reputationSvc provides shared perceived block height across replicas via Redis.
	// Set via SetReputationService; nil when reputation is disabled.
	reputationSvc reputation.ReputationService
}

// NewNoOpQoSService creates a new NoOp QoS service instance.
func NewNoOpQoSService(logger polylog.Logger, serviceID protocol.ServiceID) *NoOpQoS {
	return &NoOpQoS{
		logger:        logger.With("qos", "noop", "service_id", string(serviceID)),
		serviceID:     serviceID,
		endpointStore: newEndpointStore(),
	}
}

// ParseHTTPRequest reads the supplied HTTP request's body and passes it on to a new requestContext instance.
// It intentionally avoids performing any validation on the request, as is the designed behavior of the noop QoS.
// Implements the gateway.QoSService interface.
// Fallback logic for NoOp: header → jsonrpc (NoOp passes through requests without validation)
func (n *NoOpQoS) ParseHTTPRequest(_ context.Context, httpRequest *http.Request, _ sharedtypes.RPCType) (gateway.RequestQoSContext, bool) {
	// Apply size limit to prevent OOM attacks from unbounded io.ReadAll calls
	limitedBody := http.MaxBytesReader(nil, httpRequest.Body, maxRequestBodySize)
	bz, err := io.ReadAll(limitedBody)
	if err != nil {
		// Check if the error is due to body size limit exceeded
		if err.Error() == "http: request body too large" {
			err = fmt.Errorf("request body exceeds %d bytes limit", maxRequestBodySize)
		}
		return requestContextFromError(fmt.Errorf("error reading the HTTP request body: %w", err)), false
	}

	return &requestContext{
		httpRequestBody:   bz,
		httpRequestMethod: httpRequest.Method,
		httpRequestPath:   httpRequest.URL.Path,
		endpointSelector:  n.newFilteringSelector(),
	}, true
}

// ParseWebsocketRequest builds a request context from the provided Websocket request.
// This method implements the gateway.QoSService interface.
func (n *NoOpQoS) ParseWebsocketRequest(_ context.Context) (gateway.RequestQoSContext, bool) {
	return &requestContext{
		endpointSelector: n.newFilteringSelector(),
	}, true
}

// ApplyObservations on noop QoS only fulfills the interface requirements and does not perform any actions.
// Implements the gateway.QoSService interface.
func (n *NoOpQoS) ApplyObservations(_ *qosobservations.Observations) error {
	return nil
}

// CheckWebsocketConnection returns true if the endpoint supports Websocket connections.
// NoOp QoS does not support Websocket connections.
func (n *NoOpQoS) CheckWebsocketConnection() bool {
	return false
}

// GetRequiredQualityChecks on noop QoS only fulfills the interface requirements and does not perform any actions.
// Implements the gateway.QoSService interface.
func (n *NoOpQoS) GetRequiredQualityChecks(_ protocol.EndpointAddr) []gateway.RequestQoSContext {
	return nil
}

// HydrateDisqualifiedEndpointsResponse is a no-op for the noop QoS.
func (n *NoOpQoS) HydrateDisqualifiedEndpointsResponse(_ protocol.ServiceID, _ *devtools.DisqualifiedEndpointResponse) {
}

// UpdateFromExtractedData stores the block height reported by an endpoint and
// updates the perceived block height to the maximum across all endpoints.
// Implements gateway.QoSService interface.
func (n *NoOpQoS) UpdateFromExtractedData(endpointAddr protocol.EndpointAddr, data *qostypes.ExtractedData) error {
	if data == nil {
		return nil
	}

	if data.BlockHeight <= 0 {
		return nil
	}

	blockHeight := uint64(data.BlockHeight)

	// Update endpoint store
	n.endpointStore.mu.Lock()
	n.endpointStore.endpoints[endpointAddr] = endpointState{blockHeight: blockHeight}
	n.endpointStore.mu.Unlock()

	// Update perceived block height (max across all endpoints)
	n.serviceStateMu.Lock()
	defer n.serviceStateMu.Unlock()

	if blockHeight > n.perceivedBlockHeight {
		n.logger.Debug().
			Str("endpoint", string(endpointAddr)).
			Uint64("old_block", n.perceivedBlockHeight).
			Uint64("new_block", blockHeight).
			Msg("Updating perceived block height from observation pipeline")
		n.perceivedBlockHeight = blockHeight

		// Write to Redis for cross-replica sync (async, non-blocking)
		if n.reputationSvc != nil {
			go func(bn uint64, svcID protocol.ServiceID) {
				rCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = n.reputationSvc.SetPerceivedBlockNumber(rCtx, svcID, bn)
			}(blockHeight, n.serviceID)
		}
	}

	return nil
}

// GetPerceivedBlockNumber returns the perceived current block number.
// Returns 0 if no block number has been observed yet.
// Implements gateway.QoSService interface.
func (n *NoOpQoS) GetPerceivedBlockNumber() uint64 {
	n.serviceStateMu.RLock()
	defer n.serviceStateMu.RUnlock()
	return n.perceivedBlockHeight
}

// SetSyncAllowance dynamically updates the sync allowance for this QoS instance.
// This is called when external health check rules are loaded/refreshed, since those
// rules may specify a sync_allowance that wasn't available at QoS creation time.
func (n *NoOpQoS) SetSyncAllowance(syncAllowance uint64) {
	n.syncAllowance.Store(syncAllowance)
	n.logger.Info().
		Uint64("sync_allowance", syncAllowance).
		Msg("Sync allowance updated")
}

// ConsumeExternalBlockHeight consumes block heights from an external fetcher channel
// and uses them as a floor for perceivedBlockHeight. This ensures that if all session
// endpoints are behind the real chain tip, the perceived block is corrected.
// The gracePeriod delays applying the external floor after startup, giving suppliers
// time to report their block heights. Use 0 for the default (60s).
func (n *NoOpQoS) ConsumeExternalBlockHeight(ctx context.Context, heights <-chan int64, gracePeriod time.Duration) {
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
					n.logger.Debug().
						Int64("external_block", height).
						Dur("grace_remaining", effectiveGrace-time.Since(startedAt)).
						Msg("External block floor deferred — grace period active")
					continue
				}

				h := uint64(height)
				n.serviceStateMu.Lock()
				// Only apply external floor if suppliers have already reported.
				// If perceivedBlockHeight is still 0, no supplier has reported yet
				// and applying the floor would filter ALL endpoints.
				if n.perceivedBlockHeight == 0 {
					n.serviceStateMu.Unlock()
					n.logger.Debug().
						Uint64("external_block", h).
						Msg("External block floor skipped — no suppliers have reported yet")
					continue
				}
				if h > n.perceivedBlockHeight {
					n.logger.Info().
						Uint64("old_perceived", n.perceivedBlockHeight).
						Uint64("external_block", h).
						Msg("External block floor applied — raising perceived block height")
					n.perceivedBlockHeight = h

					// Write to Redis so other replicas benefit from the external source.
					if n.reputationSvc != nil {
						go func(bn uint64, svcID protocol.ServiceID) {
							rCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
							defer cancel()
							_ = n.reputationSvc.SetPerceivedBlockNumber(rCtx, svcID, bn)
						}(h, n.serviceID)
					}
				}
				n.serviceStateMu.Unlock()
			}
		}
	}()
}

// SetReputationService sets the reputation service for shared state across replicas.
// The reputation service provides perceived block height via Redis, enabling
// all replicas to converge on the same chain tip for sync_allowance validation.
func (n *NoOpQoS) SetReputationService(svc reputation.ReputationService) {
	n.reputationSvc = svc
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
func (n *NoOpQoS) StartBackgroundSync(ctx context.Context, syncInterval time.Duration) {
	if n.reputationSvc == nil {
		n.logger.Warn().Msg("Cannot start background sync: reputation service not set")
		return
	}

	serviceID := n.serviceID

	// syncFromRedis fetches perceived block height from Redis and updates local
	// state if the Redis value is higher (simple max semantics).
	syncFromRedis := func() {
		fetchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		redisBlockHeight := n.reputationSvc.GetPerceivedBlockNumber(fetchCtx, serviceID)
		cancel()

		if redisBlockHeight == 0 {
			return // No data in Redis yet
		}

		n.serviceStateMu.Lock()
		oldBlock := n.perceivedBlockHeight
		if redisBlockHeight > n.perceivedBlockHeight {
			n.perceivedBlockHeight = redisBlockHeight
		}
		n.serviceStateMu.Unlock()

		if redisBlockHeight > oldBlock {
			n.logger.Debug().
				Str("service_id", string(serviceID)).
				Uint64("redis_block", redisBlockHeight).
				Uint64("old_perceived", oldBlock).
				Uint64("new_perceived", redisBlockHeight).
				Msg("Updated perceived block height from Redis sync")
		}
	}

	// Perform immediate sync on startup to have latest data before serving requests
	syncFromRedis()
	n.logger.Info().
		Str("service_id", string(serviceID)).
		Uint64("perceived_block", n.perceivedBlockHeight).
		Msg("Completed initial perceived block height sync from Redis")

	go func() {
		ticker := time.NewTicker(syncInterval)
		defer ticker.Stop()

		n.logger.Info().
			Str("service_id", string(serviceID)).
			Dur("sync_interval", syncInterval).
			Msg("Started background sync for perceived block height")

		for {
			select {
			case <-ctx.Done():
				n.logger.Debug().
					Str("service_id", string(serviceID)).
					Msg("Stopping background sync for perceived block height")
				return
			case <-ticker.C:
				syncFromRedis()
			}
		}
	}()
}

// requestContextFromError constructs and returns a requestContext instance using the supplied error.
// The returned requestContext will returns a user-facing HTTP request with the supplied error when it GetHTTPResponse method is called.
func requestContextFromError(err error) *requestContext {
	return &requestContext{
		presetFailureResponse: getRequestProcessingError(err),
	}
}
