package shannon

import (
	"context"
	"fmt"
	"sync"
	"time"

	accounttypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/pokt-network/poktroll/pkg/polylog"
	apptypes "github.com/pokt-network/poktroll/x/application/types"
	servicetypes "github.com/pokt-network/poktroll/x/service/types"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	sdk "github.com/pokt-network/shannon-sdk"
	"github.com/viccon/sturdyc"

	"github.com/buildwithgrove/path/protocol"
)

// ---------------- Cache Configuration ----------------
const (
	// TODO_IMPROVE: Make this configurable
	sharedParamsCacheTTL = 2 * time.Minute // Shared params change infrequently

	// TODO_IMPROVE: Make this configurable
	blockHeightCacheTTL = 15 * time.Second // Block height changes frequently

	// Cache key prefixes to avoid collisions between different data types.
	sessionCacheKeyPrefix = "session"

	// TODO_IMPROVE: Make this configurable
	// - Grace period scale down factor forces the gateway to respect a smaller
	//   grace period than the one specified onchain to ensure we start using
	//   the new session as soon as possible.
	// - It must be between 0 and 1. Default: 0.8
	gracePeriodScaleDownFactor = 0.8
)

// hydratedSession contains a session along with pre-computed endpoints
// to avoid repeatedly calling endpointsFromSession for the same session
type hydratedSession struct {
	session   *sessiontypes.Session
	endpoints map[protocol.EndpointAddr]endpoint
}

// blockHeightCache represents a simple cache for block heights
type blockHeightCache struct {
	mu          sync.RWMutex
	height      int64
	lastUpdated time.Time
}

// sessionCache represents a simple cache for hydrated sessions
type sessionCache struct {
	mu       sync.RWMutex
	sessions map[string]sessionCacheEntry
}

type sessionCacheEntry struct {
	hydratedSession hydratedSession
	lastUpdated     time.Time
}

// sharedParamsCache represents a simple cache for shared parameters
type sharedParamsCache struct {
	mu          sync.RWMutex
	params      *sharedtypes.Params
	lastUpdated time.Time
}

var _ FullNode = &cachingFullNode{}

// cachingFullNode wraps a LazyFullNode with simple map-based caching.
// Background goroutines periodically refresh cached data to ensure freshness.
type cachingFullNode struct {
	logger polylog.Logger

	// Underlying node for protocol data fetches
	lazyFullNode *LazyFullNode

	// Simple caches with RWMutex for thread safety
	blockCache    *blockHeightCache
	sessionsCache *sessionCache
	sharedCache   *sharedParamsCache
	cacheConfig   CacheConfig

	// Account client wrapped with cache (keeping original implementation)
	cachingAccountClient *sdk.AccountClient

	// Context and cancel function for background goroutines
	ctx    context.Context
	cancel context.CancelFunc

	// ownedApps is the list of apps owned by the gateway operator
	// This is used to prefetch and cache all sessions.
	// Necessary to avoid individual requests getting stuck waiting for a session fetch.
	ownedApps map[protocol.ServiceID][]string

	// Tracks whether the endpoint is healthy.
	// Set to true once at least 1 iteration of fetching sessions succeeds.
	isHealthy   bool
	isHealthyMu sync.RWMutex
}

// NewCachingFullNode wraps a LazyFullNode with simple map-based caches
// and starts background goroutines for periodic cache updates.
func NewCachingFullNode(
	logger polylog.Logger,
	lazyFullNode *LazyFullNode,
	cacheConfig CacheConfig,
	gatewayConfig GatewayConfig,
) (*cachingFullNode, error) {
	// Set default session TTL if not set
	cacheConfig.hydrateDefaults()

	// Log cache configuration
	logger.Debug().
		Str("cache_config_session_ttl", cacheConfig.SessionTTL.String()).
		Msgf("cachingFullNode - Cache Configuration")

	// TODO_TECHDEBT(@adshmh): refactor to remove duplicate owned apps processing at startup.
	//
	// Retrieve the list of apps owned by the gateway.
	ownedApps, err := getOwnedApps(logger, gatewayConfig.OwnedAppsPrivateKeysHex, lazyFullNode)
	if err != nil {
		return nil, fmt.Errorf("failed to get app addresses from config: %w", err)
	}

	// Account cache: keeping original sturdyc implementation for account data
	accountCache := sturdyc.New[*accounttypes.QueryAccountResponse](
		accountCacheCapacity,
		10, // numShards
		accountCacheTTL,
		10, // evictionPercentage
	)

	// Create context for background goroutines
	ctx, cancel := context.WithCancel(context.Background())

	// Initialize the caching full node
	cfn := &cachingFullNode{
		logger:       logger,
		lazyFullNode: lazyFullNode,
		cacheConfig:  cacheConfig,
		ctx:          ctx,
		cancel:       cancel,
		blockCache: &blockHeightCache{
			height: 0,
		},
		sessionsCache: &sessionCache{
			sessions: make(map[string]sessionCacheEntry),
		},
		sharedCache: &sharedParamsCache{
			params: nil,
		},
		// Wrap the underlying account fetcher with a SturdyC caching layer.
		cachingAccountClient: getCachingAccountClient(
			logger,
			accountCache,
			lazyFullNode.accountClient,
		),

		ownedApps: ownedApps,
	}

	// Start background cache update goroutines
	cfn.startCacheUpdateRoutines()

	return cfn, nil
}

// startCacheUpdateRoutines starts background goroutines to periodically update caches
func (cfn *cachingFullNode) startCacheUpdateRoutines() {
	// Start block height cache update routine
	go cfn.updateBlockHeightCache()

	// Start shared params cache update routine
	go cfn.updateSharedParamsCache()

	// Start session cache update routine
	go cfn.updateSessionCache()
}

// updateBlockHeightCache periodically updates the block height cache
func (cfn *cachingFullNode) updateBlockHeightCache() {
	ticker := time.NewTicker(blockHeightCacheTTL)
	defer ticker.Stop()

	var updatedOnce bool
	for {
		if !updatedOnce {
			if err := cfn.fetchAndUpdateBlockHeightCache(); err == nil {
				updatedOnce = true
			}
			time.Sleep(1 * time.Second)
			continue
		}

		select {
		case <-cfn.ctx.Done():
			return
		case <-ticker.C:
			if err := cfn.fetchAndUpdateBlockHeightCache(); err != nil {
				cfn.logger.Error().Err(err).Msg("Failed to update block height cache")
			}
		}
	}
}

func (cfn *cachingFullNode) fetchAndUpdateBlockHeightCache() error {
	height, err := cfn.lazyFullNode.GetCurrentBlockHeight(cfn.ctx)
	if err != nil {
		return err
	}

	cfn.blockCache.mu.Lock()
	cfn.blockCache.height = height
	cfn.blockCache.lastUpdated = time.Now()
	cfn.blockCache.mu.Unlock()

	cfn.logger.Debug().Int64("height", height).Msg("Updated block height cache")
	return nil
}

// updateSharedParamsCache periodically updates the shared params cache
func (cfn *cachingFullNode) updateSharedParamsCache() {
	ticker := time.NewTicker(sharedParamsCacheTTL)
	defer ticker.Stop()

	for {
		select {
		case <-cfn.ctx.Done():
			return
		case <-ticker.C:
			params, err := cfn.lazyFullNode.GetSharedParams(cfn.ctx)
			if err != nil {
				cfn.logger.Error().Err(err).Msg("Failed to update shared params cache")
				continue
			}

			cfn.sharedCache.mu.Lock()
			cfn.sharedCache.params = params
			cfn.sharedCache.lastUpdated = time.Now()
			cfn.sharedCache.mu.Unlock()

			cfn.logger.Debug().Msg("Updated shared params cache")
		}
	}
}

// updateSessionCache periodically updates the session cache for active sessions
func (cfn *cachingFullNode) updateSessionCache() {

	for {
		// Fetch all sessions for caching.
		updatedSessions, err := cfn.fetchAllSessions()
		if err != nil {
			cfn.logger.Error().Err(err).Msg("Failed to get updated sessions. Skipping session cache update")

			// Add a short delay before retrying.
			time.Sleep(1 * time.Second)
			continue
		}

		// Update existing sessions in cache
		cfn.sessionsCache.mu.Lock()
		cfn.sessionsCache.sessions = updatedSessions
		cfn.sessionsCache.mu.Unlock()

		// Mark the caching full node as healthy
		cfn.isHealthyMu.Lock()
		cfn.isHealthy = true
		cfn.isHealthyMu.Unlock()

		// Sleep until the cache expiry.
		time.Sleep(cfn.cacheConfig.SessionTTL)
	}
}

// TODO_UPNEXT(@adshmh): Support height-based session retrieval.
func (cfn *cachingFullNode) fetchAllSessions() (map[string]sessionCacheEntry, error) {
	// Initialize updated sessions
	updatedSessions := make(map[string]sessionCacheEntry)

	// TODO_UPNEXT(@adshmh): Fetch sessions concurrently.
	//
	// Iterate over owned apps
	for serviceID, appsAddrs := range cfn.ownedApps {
		for _, appAddr := range appsAddrs {
			// Fetch updated session
			// TODO_TECHDEBT(@adshmh): Set a deadline for fetching a session.
			session, err := cfn.lazyFullNode.GetSession(context.TODO(), serviceID, appAddr)
			if err != nil {
				cfn.logger.Error().
					Str("service_id", string(serviceID)).
					Str("app_addr", appAddr).
					Err(err).
					Msg("Failed to fetch session")
				continue
			}

			// Update the session with new cache key based on current height
			newKey := getSessionCacheKey(serviceID, appAddr)
			updatedSessions[newKey] = sessionCacheEntry{
				hydratedSession: session,
				lastUpdated:     time.Now(),
			}
		}
	}

	if len(updatedSessions) == 0 {
		return nil, fmt.Errorf("failed to get any sessions")
	}

	return updatedSessions, nil
}

// GetApp is only used at startup; relaying fetches sessions for app/session sync.
func (cfn *cachingFullNode) GetApp(ctx context.Context, appAddr string) (*apptypes.Application, error) {
	return cfn.lazyFullNode.GetApp(ctx, appAddr)
}

// GetSession returns the session for a service/app from cache, fetching if not present.
func (cfn *cachingFullNode) GetSession(
	ctx context.Context,
	serviceID protocol.ServiceID,
	appAddr string,
) (hydratedSession, error) {
	sessionKey := getSessionCacheKey(serviceID, appAddr)

	// Try to get from cache first
	cfn.sessionsCache.mu.RLock()
	entry, exists := cfn.sessionsCache.sessions[sessionKey]
	cfn.sessionsCache.mu.RUnlock()

	if exists {
		return entry.hydratedSession, nil
	}

	return hydratedSession{}, fmt.Errorf("session not found")
}

// TODO_UPNEXT(@adshmh): Refactor to handle height-based session retrieval from the cache.
//
// GetSessionWithExtendedValidity implements session retrieval with support for
// Pocket Network's "session grace period" business logic.
func (cfn *cachingFullNode) GetSessionWithExtendedValidity(
	ctx context.Context,
	serviceID protocol.ServiceID,
	appAddr string,
) (hydratedSession, error) {
	logger := cfn.logger.With(
		"service_id", string(serviceID),
		"app_addr", appAddr,
		"method", "GetSessionWithExtendedValidity",
	)

	// Get the current session from cache
	cachedSession, err := cfn.GetSession(ctx, serviceID, appAddr)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to get current session")
		return hydratedSession{}, fmt.Errorf("error getting current session: %w", err)
	}

	// Extract the underlying session.
	currentSession := cachedSession.session

	// Get shared parameters to determine grace period
	sharedParams, err := cfn.GetSharedParams(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("Failed to get shared params, falling back to current session")
		return cachedSession, nil
	}

	// Get current block height
	currentHeight, err := cfn.GetCurrentBlockHeight(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("Failed to get current block height, falling back to current session")
		return cachedSession, nil
	}

	// Calculate when the previous session's grace period would end
	prevSessionEndHeight := currentSession.Header.SessionStartBlockHeight - 1
	prevSessionEndHeightWithExtendedValidity := prevSessionEndHeight + int64(sharedParams.GracePeriodEndOffsetBlocks)

	logger = logger.With(
		"prev_session_end_height", prevSessionEndHeight,
		"prev_session_end_height_with_extended_validity", prevSessionEndHeightWithExtendedValidity,
		"current_height", currentHeight,
		"current_session_start_height", currentSession.Header.SessionStartBlockHeight,
		"current_session_end_height", currentSession.Header.SessionEndBlockHeight,
	)

	// If we're not within the grace period of the previous session, return the current session
	if currentHeight > prevSessionEndHeightWithExtendedValidity {
		logger.Debug().Msg("IS NOT WITHIN GRACE PERIOD: Returning current session")
		return cachedSession, nil
	}

	// Scale down the grace period to aggressively start using the new session
	prevSessionEndHeightWithExtendedValidityScaled := prevSessionEndHeight + int64(float64(sharedParams.GracePeriodEndOffsetBlocks)*gracePeriodScaleDownFactor)
	if currentHeight > prevSessionEndHeightWithExtendedValidityScaled {
		logger.Debug().
			Int64("prev_session_end_height_with_extended_validity_scaled", prevSessionEndHeightWithExtendedValidityScaled).
			Msg("IS WITHIN GRACE PERIOD BUT: Returning current session to aggressively start using the new session")
		return cachedSession, nil
	}

	logger.Debug().Msg("IS WITHIN GRACE PERIOD: Going to fetch previous session")

	// TODO_UPNEXT(@adshmh): Support height-based session retrieval.
	//
	// Try to get previous session from cache
	prevSessionKey := getSessionCacheKey(serviceID, appAddr)

	cfn.sessionsCache.mu.RLock()
	entry, exists := cfn.sessionsCache.sessions[prevSessionKey]
	cfn.sessionsCache.mu.RUnlock()

	if exists {
		logger.Debug().Str("prev_session_key", prevSessionKey).Msg("Previous session found in cache")
		return entry.hydratedSession, nil
	}

	// Not in cache, fetch from underlying node
	logger.Debug().Str("prev_session_key", prevSessionKey).Msg("Previous session not in cache, fetching from full node")
	prevSession, err := cfn.lazyFullNode.GetSessionWithExtendedValidity(ctx, serviceID, appAddr)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to fetch previous session from full node")
		return hydratedSession{}, err
	}
	return prevSession, nil
}

// getSessionCacheKey builds a unique cache key for session: <prefix>:<serviceID>:<appAddr>
func getSessionCacheKey(serviceID protocol.ServiceID, appAddr string) string {
	return fmt.Sprintf("%s:%s:%s", sessionCacheKeyPrefix, serviceID, appAddr)
}

// ValidateRelayResponse uses the SDK and the caching full node's account client for validation.
func (cfn *cachingFullNode) ValidateRelayResponse(
	supplierAddr sdk.SupplierAddress,
	responseBz []byte,
) (*servicetypes.RelayResponse, error) {
	return sdk.ValidateRelayResponse(
		context.Background(),
		supplierAddr,
		responseBz,
		cfn.cachingAccountClient,
	)
}

// GetAccountClient returns the caching account client.
func (cfn *cachingFullNode) GetAccountClient() *sdk.AccountClient {
	return cfn.cachingAccountClient
}

// IsHealthy: passthrough to underlying node.
func (cfn *cachingFullNode) IsHealthy() bool {
	// Check if the caching full node has been marked healthy.
	// i.e. if at least one iteration of fetching sessions has succeeded.
	cfn.isHealthyMu.RLock()
	defer cfn.isHealthyMu.RUnlock()

	if !cfn.isHealthy {
		return false
	}

	// Delegate to the lazy full node's health status.
	return cfn.lazyFullNode.IsHealthy()
}

// GetSharedParams returns cached shared params.
func (cfn *cachingFullNode) GetSharedParams(ctx context.Context) (*sharedtypes.Params, error) {
	cfn.sharedCache.mu.RLock()
	params := cfn.sharedCache.params
	cfn.sharedCache.mu.RUnlock()

	if params == nil {
		// Cache not initialized yet, fetch directly
		cfn.logger.Debug().Msg("Shared params cache not initialized, fetching from full node")
		return nil, fmt.Errorf("shared params not cached yet")
	}

	return params, nil
}

// TODO_TECHDEBT(@adshmh): Add timeout on fetching current block height.
// GetCurrentBlockHeight returns cached block height.
func (cfn *cachingFullNode) GetCurrentBlockHeight(ctx context.Context) (int64, error) {
	cfn.blockCache.mu.RLock()
	defer cfn.blockCache.mu.RUnlock()

	height := cfn.blockCache.height
	if height == 0 {
		return 0, fmt.Errorf("height not fetched yet")
	}

	return height, nil
}

// IsInSessionRollover: passthrough to underlying lazy full node.
func (cfn *cachingFullNode) IsInSessionRollover() bool {
	return cfn.lazyFullNode.IsInSessionRollover()
}

// Stop gracefully shuts down the caching full node and stops background goroutines.
func (cfn *cachingFullNode) Stop() {
	if cfn.cancel != nil {
		cfn.cancel()
	}
}
