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
	if cfg, ok := qos.serviceState.serviceQoSConfig.(*simpleCosmosConfig); ok {
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

	// Only update if we extracted a valid block height
	if data.BlockHeight <= 0 {
		return nil
	}

	blockNumber := uint64(data.BlockHeight)

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
				}
				qos.serviceStateLock.Unlock()
			}
		}
	}()
}
