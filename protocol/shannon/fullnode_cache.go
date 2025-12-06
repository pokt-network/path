package shannon

import (
	"context"
	"fmt"
	"time"

	accounttypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/pokt-network/poktroll/pkg/polylog"
	apptypes "github.com/pokt-network/poktroll/x/application/types"
	servicetypes "github.com/pokt-network/poktroll/x/service/types"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	sdk "github.com/pokt-network/shannon-sdk"
	"github.com/viccon/sturdyc"

	sessionmetrics "github.com/pokt-network/path/metrics/session"
	"github.com/pokt-network/path/protocol"
)

// ---------------- Cache Configuration ----------------
const (
	// Retry base delay for exponential backoff on failed refreshes
	retryBaseDelay = 100 * time.Millisecond

	// cacheCapacity:
	//   - Max entries across all shards (not per-shard)
	//   - Exceeding capacity triggers LRU eviction per shard
	//   - 100k supports most large deployments
	//   - TODO_TECHDEBT(@commoddity): Revisit based on real-world usage; consider making configurable
	cacheCapacity = 100_000

	// numShards:
	//   - Number of independent cache shards for concurrency
	//   - Reduces lock contention, improves parallelism
	//   - 10 is a good balance for most workloads
	numShards = 10

	// evictionPercentage:
	//   - % of LRU entries evicted per shard when full
	//   - 10% = incremental cleanup, avoids memory spikes
	//   - SturdyC also evicts expired entries in background
	evictionPercentage = 10

	// TODO_TECHDEBT(@commoddity): See Issue #291 for improvements to refresh logic
	// minEarlyRefreshPercentage:
	//   - Earliest point (as % of TTL) to start background refresh
	//   - 0.75 = 75% of TTL (e.g. 22.5s for 30s TTL)
	minEarlyRefreshPercentage = 0.75

	// maxEarlyRefreshPercentage:
	//   - Latest point (as % of TTL) to start background refresh
	//   - 0.9 = 90% of TTL (e.g. 27s for 30s TTL)
	//   - Ensures refresh always completes before expiry
	maxEarlyRefreshPercentage = 0.9

	// Cache key prefixes to avoid collisions between different data types.
	sessionCacheKeyPrefix = "session"

	// TODO_IMPROVE: Make this configurable
	sharedParamsCacheKey = "shared_params"
	// TODO_IMPROVE: Make this configurable
	sharedParamsCacheTTL      = 2 * time.Minute // Shared params change infrequently
	sharedParamsCacheCapacity = 3               // Only need to cache the last couple of shared params at any point in time

	// TODO_IMPROVE: Make this configurable
	blockHeightCacheKey = "block_height"
	// TODO_IMPROVE: Make this configurable
	blockHeightCacheTTL      = 15 * time.Second // Block height changes frequently
	blockHeightCacheCapacity = 5                // Only need to cache the last few blocks at any point in time

	// TODO_IMPROVE: Make this configurable
	// - Grace period scale down factor forces the gateway to respect a smaller
	//   grace period than the one specified onchain to ensure we start using
	//   the new session as soon as possible.
	// - It must be between 0 and 1. Default: 0.8
	gracePeriodScaleDownFactor = 0.8
)

// getCacheDelays returns the min/max delays for SturdyC's Early Refresh strategy.
// - Proactively refreshes cache before expiry (prevents misses/latency spikes)
// - Refresh window: 75-90% of TTL (e.g. 22.5-27s for 30s TTL)
// - Spreads requests to avoid thundering herd
// See: https://github.com/viccon/sturdyc?tab=readme-ov-file#early-refreshes
func getCacheDelays(ttl time.Duration) (min, max time.Duration) {
	minFloat := float64(ttl) * minEarlyRefreshPercentage
	maxFloat := float64(ttl) * maxEarlyRefreshPercentage

	// Round to the nearest second
	min = time.Duration(minFloat/float64(time.Second)+0.5) * time.Second
	max = time.Duration(maxFloat/float64(time.Second)+0.5) * time.Second
	return
}

var _ FullNode = &cachingFullNode{}

// cachingFullNode wraps a LazyFullNode with SturdyC-based caching.
// - Early refresh: background updates before expiry (prevents thundering herd/latency spikes)
// - Example: 30s TTL, refresh at 22.5–27s (75–90%)
// - Benefits: zero-latency reads, graceful degradation, auto load balancing
// Docs: https://github.com/viccon/sturdyc
type cachingFullNode struct {
	logger polylog.Logger

	// Underlying node for protocol data fetches
	lazyFullNode *LazyFullNode

	// Session cache
	// TODO_MAINNET_MIGRATION(@Olshansk): Revisit after mainnet
	sessionCache *sturdyc.Client[sessiontypes.Session]

	// Shared params cache
	sharedParamsCache *sturdyc.Client[*sharedtypes.Params]

	// Block height cache
	blockHeightCache *sturdyc.Client[int64]

	// Account client wrapped with SturdyC cache
	cachingAccountClient *sdk.AccountClient
}

// NewCachingFullNode wraps a LazyFullNode with:
//   - Session cache: caches sessions, refreshes early
//   - Account cache: indefinite cache for account data
//
// Both use early refresh to avoid thundering herd/latency spikes.
func NewCachingFullNode(
	logger polylog.Logger,
	lazyFullNode *LazyFullNode,
	cacheConfig CacheConfig,
) (*cachingFullNode, error) {
	// Set default session TTL if not set
	cacheConfig.hydrateDefaults()

	// Log cache configuration
	logger.Debug().
		Str("cache_config_session_ttl", cacheConfig.SessionTTL.String()).
		Msgf("cachingFullNode - Cache Configuration")

	// Create the session cache with early refreshes
	sessionMinRefreshDelay, sessionMaxRefreshDelay := getCacheDelays(cacheConfig.SessionTTL)
	sessionCache := sturdyc.New[sessiontypes.Session](
		cacheCapacity,
		numShards,
		cacheConfig.SessionTTL,
		evictionPercentage,
		// See: https://github.com/viccon/sturdyc?tab=readme-ov-file#early-refreshes
		sturdyc.WithEarlyRefreshes(
			sessionMinRefreshDelay,
			sessionMaxRefreshDelay,
			cacheConfig.SessionTTL,
			retryBaseDelay,
		),
	)

	// Account cache: infinite for app lifetime; no early refresh needed.
	accountCache := sturdyc.New[*accounttypes.QueryAccountResponse](
		accountCacheCapacity,
		numShards,
		accountCacheTTL,
		evictionPercentage,
	)

	// Shared params cache
	sharedParamsMinRefreshDelay, sharedParamsMaxRefreshDelay := getCacheDelays(sharedParamsCacheTTL)
	sharedParamsCache := sturdyc.New[*sharedtypes.Params](
		sharedParamsCacheCapacity,
		1,
		sharedParamsCacheTTL,
		evictionPercentage,
		sturdyc.WithEarlyRefreshes(
			sharedParamsMinRefreshDelay,
			sharedParamsMaxRefreshDelay,
			sharedParamsCacheTTL,
			retryBaseDelay,
		),
	)

	// Block height cache
	blockHeightMinRefreshDelay, blockHeightMaxRefreshDelay := getCacheDelays(blockHeightCacheTTL)
	blockHeightCache := sturdyc.New[int64](
		blockHeightCacheCapacity,
		1,
		blockHeightCacheTTL,
		evictionPercentage,
		sturdyc.WithEarlyRefreshes(
			blockHeightMinRefreshDelay,
			blockHeightMaxRefreshDelay,
			blockHeightCacheTTL,
			retryBaseDelay,
		),
	)

	// Initialize the caching full node with the modified lazy full node
	return &cachingFullNode{
		logger:            logger,
		lazyFullNode:      lazyFullNode,
		sessionCache:      sessionCache,
		sharedParamsCache: sharedParamsCache,
		blockHeightCache:  blockHeightCache,
		// Wrap the underlying account fetcher with a SturdyC caching layer.
		cachingAccountClient: getCachingAccountClient(
			logger,
			accountCache,
			lazyFullNode.accountClient,
		),
	}, nil
}

// GetApp is only used at startup; relaying fetches sessions for app/session sync.
func (cfn *cachingFullNode) GetApp(ctx context.Context, appAddr string) (*apptypes.Application, error) {
	return cfn.lazyFullNode.GetApp(ctx, appAddr)
}

// GetSession returns (and auto-refreshes) the session for a service/app from cache.
func (cfn *cachingFullNode) GetSession(
	ctx context.Context,
	serviceID protocol.ServiceID,
	appAddr string,
) (sessiontypes.Session, error) {
	logger := cfn.logger.With(
		"service_id", string(serviceID),
		"app_addr", appAddr,
		"method", "GetSession",
	)

	height, err := cfn.GetCurrentBlockHeight(ctx)
	if err != nil {
		logger.Error().Err(err).Msgf(
			"[cachingFullNode.GetSession] Failed to get current block height",
		)
		return sessiontypes.Session{}, err
	}
	sessionKey := getSessionCacheKey(serviceID, appAddr, height)

	// See: https://github.com/viccon/sturdyc?tab=readme-ov-file#get-or-fetch
	session, err := cfn.sessionCache.GetOrFetch(
		ctx,
		sessionKey,
		func(fetchCtx context.Context) (sessiontypes.Session, error) {
			logger.Debug().Str("session_key", sessionKey).Msgf("Fetching session from full node")
			// Record session refresh metric when fetching from node (cache miss or early refresh)
			session, fetchErr := cfn.lazyFullNode.GetSession(ctx, serviceID, appAddr)
			if fetchErr != nil {
				sessionmetrics.RecordSessionRefresh(string(serviceID), sessionmetrics.RefreshStatusError)
				return session, fetchErr
			}
			sessionmetrics.RecordSessionRefresh(string(serviceID), sessionmetrics.RefreshStatusSuccess)
			return session, nil
		},
	)

	return session, err
}

// GetSessionWithExtendedValidity implements session retrieval with support for
// Pocket Network's "session grace period" business logic.
//
// It is used to account for the case when:
// - RelayMiner.FullNode.Height > Gateway.FullNode.Height
// AND
// - RelayMiner.FullNode.Session > Gateway.FullNode.Session
//
// In the context of PATH, it is used to account for the case when:
// - Gateway.FullNode.Height > RelayMiner.FullNode.Height
// AND
// - Gateway.FullNode.Session > RelayMiner.FullNode.Session
//
// Protocol References:
// - https://github.com/pokt-network/poktroll/blob/main/proto/pocket/shared/params.proto
// - https://dev.poktroll.com/protocol/governance/gov_params
// - https://dev.poktroll.com/protocol/primitives/claim_and_proof_lifecycle
func (cfn *cachingFullNode) GetSessionWithExtendedValidity(
	ctx context.Context,
	serviceID protocol.ServiceID,
	appAddr string,
) (sessiontypes.Session, error) {
	logger := cfn.logger.With(
		"service_id", string(serviceID),
		"app_addr", appAddr,
		"method", "GetSessionWithExtendedValidity",
	)

	// Get the current session from cache
	currentSession, err := cfn.GetSession(ctx, serviceID, appAddr)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to get current session")
		return sessiontypes.Session{}, fmt.Errorf("error getting current session: %w", err)
	}

	// Get shared parameters to determine grace period
	sharedParams, err := cfn.GetSharedParams(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("Failed to get shared params, falling back to current session")
		return currentSession, nil
	}

	// Get current block height
	currentHeight, err := cfn.GetCurrentBlockHeight(ctx)
	if err != nil {
		logger.Warn().Err(err).Msg("Failed to get current block height, falling back to current session")
		return currentSession, nil
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
		return currentSession, nil
	}

	// Scale down the grace period to aggressively start using the new session
	prevSessionEndHeightWithExtendedValidityScaled := prevSessionEndHeight + int64(float64(sharedParams.GracePeriodEndOffsetBlocks)*gracePeriodScaleDownFactor)
	if currentHeight > prevSessionEndHeightWithExtendedValidityScaled {
		logger.Debug().
			Int64("prev_session_end_height_with_extended_validity_scaled", prevSessionEndHeightWithExtendedValidityScaled).
			Msg("IS WITHIN GRACE PERIOD BUT: Returning current session to aggressively start using the new session")
		return currentSession, nil
	}

	logger.Debug().Msg("IS WITHIN GRACE PERIOD: Going to fetch previous session")

	// Record session rollover metric (we're using fallback to previous session)
	sessionmetrics.RecordSessionRollover(string(serviceID), true)

	// Use cache for previous session lookup with a specific key
	prevSessionKey := getSessionCacheKey(serviceID, appAddr, prevSessionEndHeight)
	prevSession, err := cfn.sessionCache.GetOrFetch(
		ctx,
		prevSessionKey,
		func(fetchCtx context.Context) (sessiontypes.Session, error) {
			cfn.logger.Debug().Msg("Fetching previous session from full node")
			session, fetchErr := cfn.lazyFullNode.GetSessionWithExtendedValidity(fetchCtx, serviceID, appAddr)
			if fetchErr != nil {
				cfn.logger.Error().Err(fetchErr).Msg("Failed to fetch previous session from full node")
			}
			return session, fetchErr
		},
	)

	return prevSession, err
}

// getSessionCacheKey builds a unique cache key for session: <prefix>:<serviceID>:<appAddr>:<height>
func getSessionCacheKey(serviceID protocol.ServiceID, appAddr string, height int64) string {
	return fmt.Sprintf("%s:%s:%s:%d", sessionCacheKeyPrefix, serviceID, appAddr, height)
}

// ValidateRelayResponse:
//   - Validates the raw response bytes received from an endpoint.
//   - Uses the SDK and the caching full node's account client for validation.
//   - Will use the caching account client to fetch the account pub key.
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

// GetAccountClient: passthrough to underlying node (returns caching client).
func (cfn *cachingFullNode) GetAccountClient() *sdk.AccountClient {
	return cfn.cachingAccountClient
}

// IsHealthy: passthrough to underlying node.
// TODO_IMPROVE(@commoddity):
//   - Add smarter health checks (e.g. verify cached apps/sessions)
//   - Currently always true (cache fills as needed)
func (cfn *cachingFullNode) IsHealthy() bool {
	return cfn.lazyFullNode.IsHealthy()
}

// GetSharedParams: cached shared params with early refresh for governance changes.
func (cfn *cachingFullNode) GetSharedParams(ctx context.Context) (*sharedtypes.Params, error) {
	params, err := cfn.sharedParamsCache.GetOrFetch(
		ctx,
		sharedParamsCacheKey,
		func(fetchCtx context.Context) (*sharedtypes.Params, error) {
			cfn.logger.Debug().Msg("Fetching shared params from full node")
			params, fetchErr := cfn.lazyFullNode.GetSharedParams(fetchCtx)
			if fetchErr != nil {
				cfn.logger.Error().Err(fetchErr).Msg("Failed to fetch shared params from full node")
			}
			return params, fetchErr
		},
	)

	return params, err
}

// GetCurrentBlockHeight: cached block height with a sho TTL and early refresh.
func (cfn *cachingFullNode) GetCurrentBlockHeight(ctx context.Context) (int64, error) {
	height, err := cfn.blockHeightCache.GetOrFetch(
		ctx,
		blockHeightCacheKey,
		func(fetchCtx context.Context) (int64, error) {
			cfn.logger.Debug().Msg("Fetching current block height from full node")
			height, fetchErr := cfn.lazyFullNode.GetCurrentBlockHeight(fetchCtx)
			if fetchErr != nil {
				cfn.logger.Error().Err(fetchErr).Msg("Failed to fetch current block height from full node")
			}
			return height, fetchErr
		},
	)

	return height, err
}

// IsInSessionRollover: passthrough to underlying lazy full node.
// The lazy full node manages session rollover monitoring in the background,
// so we simply delegate to its rollover state.
func (cfn *cachingFullNode) IsInSessionRollover() bool {
	return cfn.lazyFullNode.IsInSessionRollover()
}
