package shannon

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/pokt-network/poktroll/pkg/crypto/rings"
	apptypes "github.com/pokt-network/poktroll/x/application/types"
	servicetypes "github.com/pokt-network/poktroll/x/service/types"
	ring "github.com/pokt-network/ring-go"
	sdk "github.com/pokt-network/shannon-sdk"
)

// ringCacheKey is the key for caching rings by app address and session.
type ringCacheKey struct {
	appAddress       string
	sessionEndHeight uint64
}

// signer wraps an SDK signer for signing relay requests.
// The sdkSigner is reused across requests to benefit from SignerContext caching,
// which pre-computes expensive cryptographic operations.
//
// Ring caching: We cache *ring.Ring instances by (appAddress, sessionEndHeight) because:
// - The SDK's SignerContext cache is keyed by ring pointer
// - GetRing() creates new pointers each call, causing cache misses
// - Ring composition can change at session boundaries (delegation changes)
// - By caching the ring pointer per session, SignerContext cache hits work properly
type signer struct {
	accountClient sdk.AccountClient

	// sdkSigner is held behind an atomic pointer because rollover REPLACES it rather
	// than mutating it.
	//
	// The SDK's only cache-eviction method is ClearSignerContextCache, which does
	// `s.signerContextCache = sync.Map{}` — a multi-word, unsynchronized assignment over
	// a map that concurrent signers are calling Load on. That is a genuine data race:
	// a reader can observe the new map's keyHash together with the old map's nil root
	// and dereference it. Seen in production as a SIGSEGV in
	// internal/sync.(*HashTrieMap).Load, reached from the hedge path — hedging doubles
	// the concurrent signing rate, which is what makes the window reachable.
	//
	// Swapping the whole Signer is race-free by construction: each signer observes one
	// immutable Signer for the duration of its call, and the replaced one is collected
	// once its in-flight users finish. Do NOT reintroduce ClearSignerContextCache here.
	sdkSigner atomic.Pointer[sdk.Signer]

	// ringCache caches *ring.Ring instances by (appAddress, sessionEndHeight).
	// This ensures the same ring pointer is reused within a session,
	// while allowing new rings when sessions change (delegations may differ).
	ringCache sync.Map // map[ringCacheKey]*ring.Ring

	// highestSessionEnd is the newest session end height observed. Used to
	// detect session rollover and evict stale per-session cache entries.
	highestSessionEnd atomic.Uint64
	// rolloverMu serializes rollover eviction so a single goroutine prunes
	// per new session.
	rolloverMu sync.Mutex
}

// newSigner creates a new signer instance with a pre-initialized SDK signer.
// The SDK signer is created once and reused across all signing operations.
func newSigner(accountClient sdk.AccountClient, privateKeyHex string) (*signer, error) {
	sdkSigner, err := sdk.NewSignerFromHex(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("newSigner: error creating SDK signer: %w", err)
	}
	s := &signer{accountClient: accountClient}
	s.sdkSigner.Store(sdkSigner)
	return s, nil
}

// SignRelayRequest signs the relay request using the application's ring signature.
// Uses cached ring and SignerContext for optimal performance when signing multiple
// requests for the same application within the same session.
func (s *signer) SignRelayRequest(req *servicetypes.RelayRequest, app apptypes.Application) (*servicetypes.RelayRequest, error) {
	sessionEndHeight := uint64(req.Meta.SessionHeader.SessionEndBlockHeight)

	// Get or create cached ring for this application and session
	appRing, err := s.getOrCreateRing(app, sessionEndHeight)
	if err != nil {
		return nil, fmt.Errorf("SignRequest: error getting ring for app %s: %w", app.Address, err)
	}

	// Sign using the cached ring (enables SignerContext cache hits).
	// SignOffChainWithRing uses ring-go's hash-cache fast path — safe here because
	// PATH relay signing is off-chain (not consensus-critical). The deterministic
	// Signer.Sign path is intentionally not used on this hot path.
	req, err = s.sdkSigner.Load().SignOffChainWithRing(context.Background(), req, appRing)
	if err != nil {
		return nil, fmt.Errorf("SignRequest: error signing relay request: %w", err)
	}

	return req, nil
}

// getOrCreateRing returns a cached ring for the application and session, or creates and caches a new one.
// The ring is cached by (appAddress, sessionEndHeight) since delegation changes take effect at session boundaries.
func (s *signer) getOrCreateRing(app apptypes.Application, sessionEndHeight uint64) (*ring.Ring, error) {
	// Bound the per-session caches (ringCache + the SDK's SignerContext cache)
	// by evicting stale entries when a new session is observed.
	s.evictStaleRingsOnRollover(sessionEndHeight)

	cacheKey := ringCacheKey{
		appAddress:       app.Address,
		sessionEndHeight: sessionEndHeight,
	}

	// Check cache first
	if cached, ok := s.ringCache.Load(cacheKey); ok {
		return cached.(*ring.Ring), nil
	}

	// Create new ring using the same logic as ApplicationRing.GetRing()
	currentGatewayAddresses := rings.GetRingAddressesAtSessionEndHeight(&app, sessionEndHeight)

	ringAddresses := make([]string, 0)
	ringAddresses = append(ringAddresses, app.Address)

	if len(currentGatewayAddresses) == 0 {
		ringAddresses = append(ringAddresses, app.Address)
	} else {
		ringAddresses = append(ringAddresses, currentGatewayAddresses...)
	}

	// Fetch public keys for all ring addresses
	ringPubKeys := make([]cryptotypes.PubKey, 0, len(ringAddresses))
	for _, address := range ringAddresses {
		pubKey, err := s.accountClient.GetPubKeyFromAddress(context.Background(), address)
		if err != nil {
			return nil, fmt.Errorf("getOrCreateRing: error fetching pubkey for %s: %w", address, err)
		}
		ringPubKeys = append(ringPubKeys, pubKey)
	}

	// Create the ring
	newRing, err := rings.GetRingFromPubKeys(ringPubKeys)
	if err != nil {
		return nil, fmt.Errorf("getOrCreateRing: error creating ring: %w", err)
	}

	// Cache it (use LoadOrStore to handle concurrent creation)
	actual, _ := s.ringCache.LoadOrStore(cacheKey, newRing)
	return actual.(*ring.Ring), nil
}

// evictStaleRingsOnRollover bounds the per-session caches. Both s.ringCache and
// the SDK's SignerContext cache are keyed per session/ring and would otherwise
// grow without bound as sessions roll over (~hourly). On observing a session end
// height newer than any seen, it drops rings older than the previous session
// (keeping current + previous for in-flight/grace-period requests) and clears the
// SDK's SignerContext cache. The SDK exposes only a full clear; rings still in
// ringCache rebuild their context lazily on the next sign.
func (s *signer) evictStaleRingsOnRollover(sessionEndHeight uint64) {
	// Fast path (hot): no newer session, nothing to evict. Atomic load only.
	if sessionEndHeight <= s.highestSessionEnd.Load() {
		return
	}

	s.rolloverMu.Lock()
	defer s.rolloverMu.Unlock()

	// Re-check under the lock; another goroutine may have handled this rollover.
	prevHighest := s.highestSessionEnd.Load()
	if sessionEndHeight <= prevHighest {
		return
	}
	s.highestSessionEnd.Store(sessionEndHeight)

	// Drop rings older than the previous session (keep current + previous).
	s.ringCache.Range(func(k, _ any) bool {
		if k.(ringCacheKey).sessionEndHeight < prevHighest {
			s.ringCache.Delete(k)
		}
		return true
	})

	// Release the SDK's per-ring SignerContexts by replacing the Signer outright.
	//
	// ClearSignerContextCache would be the obvious call and is unsafe: it assigns a
	// fresh sync.Map over one that concurrent signers are reading (see sdkSigner).
	// A pointer swap publishes the new Signer atomically, so an in-flight sign keeps
	// using the Signer it loaded and the old one is collected when it finishes.
	//
	// Rebuilding re-derives the key scalar from the retained hex. That is once per
	// session rollover (~20 min), against a signing path that runs thousands of times
	// a second, so the cost is irrelevant next to the race it removes.
	prev := s.sdkSigner.Load()
	fresh, err := sdk.NewSignerFromHex(prev.PrivateKeyHex)
	if err != nil {
		// Keep the existing Signer: a stale-but-working cache is strictly better than
		// no signer. The cache stays bounded by the next successful rollover.
		return
	}
	s.sdkSigner.Store(fresh)
}
