package shannon

// TODO_TECHDEBT(@commoddity): Refactor (remove?) this whole file
// as part of the #291 refactor, as it will not longer be needed.
//
// https://github.com/pokt-network/path/issues/291

import (
	"context"
	"fmt"
	"math"
	"time"

	accounttypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/pokt-network/poktroll/pkg/polylog"
	sdk "github.com/pokt-network/shannon-sdk"
	"github.com/viccon/sturdyc"
	grpcoptions "google.golang.org/grpc"
)

// ---------------- Caching Account Fetcher ----------------

// accountCacheTTL: No TTL for the account cache since account data never changes.
//
// time.Duration(math.MaxInt64) equals ~292 years, which is effectively infinite.
const accountCacheTTL = time.Duration(math.MaxInt64)

// accountCacheCapacity: Maximum number of entries the account cache can hold.
// This is the total capacity, not per-shard. When capacity is exceeded, the cache
// will evict a percentage of the least recently used entries from each shard.
//
// TODO_TECHDEBT(@commoddity): Revisit cache capacity based on actual # of accounts in Shannon.
const accountCacheCapacity = 200_000

// accountCacheKeyPrefix: The prefix for the account cache key.
// It is used to namespace the account cache key.
const accountCacheKeyPrefix = "account"

// cachingPoktNodeAccountFetcher implements the PoktNodeAccountFetcher interface.
var _ sdk.PoktNodeAccountFetcher = &cachingPoktNodeAccountFetcher{}

// cachingPoktNodeAccountFetcher wraps an sdk.PoktNodeAccountFetcher with caching capabilities.
// It implements the same PoktNodeAccountFetcher interface but adds sturdyc caching
// in order to reduce repeated and unnecessary requests to the full node.
type cachingPoktNodeAccountFetcher struct {
	logger polylog.Logger

	// The underlying account client to delegate to when cache misses occur
	// TODO_TECHDEBT: Ass part of the effort in #291, this will be moved to the shannon-sdk.
	underlyingAccountClient *sdk.AccountClient

	// Cache for account responses
	accountCache *sturdyc.Client[*accounttypes.QueryAccountResponse]
}

// Account implements the `sdk.PoktNodeAccountFetcher` interface with caching.
//
// See `sdk.PoktNodeAccountFetcher` interface:
//
//	https://github.com/pokt-network/shannon-sdk/blob/main/account.go#L26
//
// It matches the function signature of the CosmosSDK's account fetcher
// in order to satisfy the `sdk.PoktNodeAccountFetcher` interface.
//
// See CosmosSDK's account fetcher:
//
//	https://github.com/cosmos/cosmos-sdk/blob/main/x/auth/types/query.pb.go#L1090
func (c *cachingPoktNodeAccountFetcher) Account(
	ctx context.Context,
	req *accounttypes.QueryAccountRequest,
	opts ...grpcoptions.CallOption,
) (*accounttypes.QueryAccountResponse, error) {
	return c.accountCache.GetOrFetch(
		ctx,
		getAccountCacheKey(req.Address),
		func(fetchCtx context.Context) (*accounttypes.QueryAccountResponse, error) {
			c.logger.Debug().Str("account_key", getAccountCacheKey(req.Address)).Msgf(
				"[cachingPoktNodeAccountFetcher.Account] Making request to full node",
			)
			return c.underlyingAccountClient.Account(fetchCtx, req, opts...)
		},
	)
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
		},
	}
}
