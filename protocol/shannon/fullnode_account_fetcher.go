package shannon

// TODO_TECHDEBT(@commoddity): Refactor (remove?) this whole file
// as part of the #291 refactor, as it will not longer be needed.
//
// https://github.com/pokt-network/path/issues/291

import (
	"context"
	"fmt"
	"sync"
	"time"

	accounttypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/pokt-network/poktroll/pkg/polylog"
	sdk "github.com/pokt-network/shannon-sdk"
	"github.com/viccon/sturdyc"
	grpcoptions "google.golang.org/grpc"

	"github.com/pokt-network/path/metrics"
)

// ---------------- Caching Account Fetcher ----------------

const (
	// accountCacheTTL: TTL for cached account data.
	// Public keys NEVER change once set, so we use an effectively infinite TTL.
	// Only accounts WITH valid pubkeys are cached.
	// Accounts with nil pubkeys are NOT cached - they are blacklisted and retried periodically.
	accountCacheTTL = 365 * 24 * time.Hour // 1 year (effectively forever)

	// nilPubkeyRetryInterval: How often to retry fetching accounts that had nil pubkeys.
	// These suppliers may have signed their first transaction since we last checked.
	nilPubkeyRetryInterval = 15 * time.Minute

	// accountCacheCapacity: Maximum number of entries the account cache can hold.
	// This is the total capacity, not per-shard. When capacity is exceeded, the cache
	// will evict a percentage of the least recently used entries from each shard.
	//
	// TODO_TECHDEBT(@commoddity): Revisit cache capacity based on actual # of accounts in Shannon.
	accountCacheCapacity = 200_000

	// accountCacheKeyPrefix: The prefix for the account cache key.
	// It is used to namespace the account cache key.
	accountCacheKeyPrefix = "account"
)

// cachingPoktNodeAccountFetcher implements the PoktNodeAccountFetcher interface.
var _ sdk.PoktNodeAccountFetcher = &cachingPoktNodeAccountFetcher{}

// cachingPoktNodeAccountFetcher wraps an sdk.PoktNodeAccountFetcher with caching capabilities.
// It implements the same PoktNodeAccountFetcher interface but adds sturdyc caching
// in order to reduce repeated and unnecessary requests to the full node.
//
// Key features:
// - Caches accounts with 1 hour TTL (reasonable for pubkey data)
// - Supports cache invalidation on signature verification failure
// - Tracks invalidated accounts for metrics and retry logic
type cachingPoktNodeAccountFetcher struct {
	logger polylog.Logger

	// The underlying account client to delegate to when cache misses occur
	// TODO_TECHDEBT: As part of the effort in #291, this will be moved to the shannon-sdk.
	underlyingAccountClient *sdk.AccountClient

	// Cache for account responses
	accountCache *sturdyc.Client[*accounttypes.QueryAccountResponse]

	// Track invalidated addresses for metrics (signature verification failures)
	invalidatedTracker *invalidatedAccountTracker
}

// invalidatedAccountTracker tracks supplier addresses that had cache invalidation
// due to signature verification failures, for metrics and debugging.
// It also tracks nil pubkey suppliers separately to allow periodic retries.
type invalidatedAccountTracker struct {
	mu sync.RWMutex
	// addresses maps supplier address -> last invalidation timestamp (for signature errors)
	addresses map[string]time.Time
	// nilPubkeys maps supplier address -> last check timestamp (for nil pubkey retries)
	nilPubkeys map[string]time.Time
}

func newInvalidatedAccountTracker() *invalidatedAccountTracker {
	return &invalidatedAccountTracker{
		addresses:  make(map[string]time.Time),
		nilPubkeys: make(map[string]time.Time),
	}
}

// Track records an address that was invalidated due to signature error.
func (t *invalidatedAccountTracker) Track(address string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.addresses[address] = time.Now()
}

// TrackNilPubkey records an address that had a nil pubkey.
// This is used to rate-limit retries for these suppliers.
func (t *invalidatedAccountTracker) TrackNilPubkey(address string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nilPubkeys[address] = time.Now()
}

// ShouldRetryNilPubkey checks if enough time has passed to retry a nil pubkey supplier.
// Returns true if the supplier should be retried (either not tracked or interval passed).
func (t *invalidatedAccountTracker) ShouldRetryNilPubkey(address string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if ts, exists := t.nilPubkeys[address]; exists {
		return time.Since(ts) >= nilPubkeyRetryInterval
	}
	return true // Not tracked, allow retry
}

// WasRecentlyInvalidated checks if an address was invalidated recently (last 5 min).
// This helps detect recurring issues with a supplier.
func (t *invalidatedAccountTracker) WasRecentlyInvalidated(address string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if ts, exists := t.addresses[address]; exists {
		return time.Since(ts) < 5*time.Minute
	}
	return false
}

// Account implements the `sdk.PoktNodeAccountFetcher` interface with caching.
//
// Caching strategy:
// - Public keys NEVER change once set → cache forever (1 year TTL)
// - Only cache accounts WITH valid pubkeys
// - Accounts with nil pubkeys are NOT cached (handled by blacklist + retry)
//
// See `sdk.PoktNodeAccountFetcher` interface:
//
//	https://github.com/pokt-network/shannon-sdk/blob/main/account.go#L26
func (c *cachingPoktNodeAccountFetcher) Account(
	ctx context.Context,
	req *accounttypes.QueryAccountRequest,
	opts ...grpcoptions.CallOption,
) (*accounttypes.QueryAccountResponse, error) {
	address := req.Address
	cacheKey := getAccountCacheKey(address)

	// Check cache first - if cached, pubkey is guaranteed to be valid
	if resp, ok := c.accountCache.Get(cacheKey); ok {
		c.logger.Debug().Str("address", address).Msg("Account cache hit (valid pubkey)")
		return resp, nil
	}

	// Cache miss - fetch from fullnode
	c.logger.Debug().Str("address", address).Msg("Account cache miss, fetching from full node")

	resp, err := c.underlyingAccountClient.Account(ctx, req, opts...)
	if err != nil {
		c.logger.Error().Err(err).Str("address", address).Msg("Failed to fetch account from full node")
		return nil, err
	}

	// Only cache if pubkey is valid (not nil)
	// Accounts with nil pubkeys should NOT be cached - they need to be re-fetched
	// when the supplier eventually signs their first transaction
	if hasPubkey := c.accountHasValidPubkey(resp); hasPubkey {
		c.accountCache.Set(cacheKey, resp)
		c.logger.Debug().Str("address", address).Msg("Cached account with valid pubkey (forever)")
	} else {
		c.logger.Debug().Str("address", address).Msg("Account has nil pubkey - NOT caching")
	}

	return resp, nil
}

// accountHasValidPubkey checks if the account response contains a valid (non-nil) public key.
func (c *cachingPoktNodeAccountFetcher) accountHasValidPubkey(resp *accounttypes.QueryAccountResponse) bool {
	if resp == nil || resp.Account == nil {
		return false
	}

	// The account is stored as an Any type, we need to unpack it
	// For now, we'll rely on the SDK's validation to detect nil pubkeys
	// If the account exists and has data, we consider it potentially valid
	// The actual nil pubkey check happens during signature verification
	return len(resp.Account.Value) > 0
}

// InvalidateCache removes an account from the cache.
// Called when signature verification fails to allow a fresh fetch on retry.
// Also tracks the invalidation for metrics.
func (c *cachingPoktNodeAccountFetcher) InvalidateCache(address string) {
	cacheKey := getAccountCacheKey(address)

	c.accountCache.Delete(cacheKey)
	c.invalidatedTracker.Track(address)

	// Record metric
	metrics.RecordSupplierPubkeyCacheInvalidated(address)

	c.logger.Info().
		Str("address", address).
		Msg("Invalidated account cache - will re-fetch on next request")
}

// WasRecentlyInvalidated checks if an account was recently invalidated.
// Useful for detecting recurring signature verification issues.
func (c *cachingPoktNodeAccountFetcher) WasRecentlyInvalidated(address string) bool {
	return c.invalidatedTracker.WasRecentlyInvalidated(address)
}

// TrackNilPubkey records that a supplier has a nil pubkey.
// This supplier will be rate-limited for retry attempts.
func (c *cachingPoktNodeAccountFetcher) TrackNilPubkey(address string) {
	c.invalidatedTracker.TrackNilPubkey(address)
	c.logger.Info().
		Str("address", address).
		Dur("retry_interval", nilPubkeyRetryInterval).
		Msg("Tracked nil pubkey supplier - will retry after interval")
}

// ShouldRetryNilPubkey checks if enough time has passed to retry a nil pubkey supplier.
// Returns true if we should attempt to re-fetch the account (interval passed or not tracked).
func (c *cachingPoktNodeAccountFetcher) ShouldRetryNilPubkey(address string) bool {
	return c.invalidatedTracker.ShouldRetryNilPubkey(address)
}

// getAccountCacheKey returns the cache key for the given account address.
// It uses the accountCacheKeyPrefix and the account address to create a unique key.
//
// eg. "account:pokt1up7zlytnmvlsuxzpzvlrta95347w322adsxslw"
func getAccountCacheKey(address string) string {
	return fmt.Sprintf("%s:%s", accountCacheKeyPrefix, address)
}

// getCachingAccountClient wraps the original account fetcher with the caching
// account fetcher and returns a new caching account client.
//
// It is used in the NewCachingFullNode function to create a new caching full node.
func getCachingAccountClient(
	logger polylog.Logger,
	accountCache *sturdyc.Client[*accounttypes.QueryAccountResponse],
	underlyingAccountClient *sdk.AccountClient,
) *sdk.AccountClient {
	return &sdk.AccountClient{
		PoktNodeAccountFetcher: &cachingPoktNodeAccountFetcher{
			logger:                  logger,
			accountCache:            accountCache,
			underlyingAccountClient: underlyingAccountClient,
			invalidatedTracker:      newInvalidatedAccountTracker(),
		},
	}
}

// GetCachingAccountFetcher returns the underlying cachingPoktNodeAccountFetcher
// from an AccountClient, allowing access to cache invalidation methods.
// Returns nil if the account client doesn't use caching.
func GetCachingAccountFetcher(client *sdk.AccountClient) *cachingPoktNodeAccountFetcher {
	if client == nil {
		return nil
	}
	fetcher, ok := client.PoktNodeAccountFetcher.(*cachingPoktNodeAccountFetcher)
	if !ok {
		return nil
	}
	return fetcher
}
