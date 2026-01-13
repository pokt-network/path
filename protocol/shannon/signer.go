package shannon

import (
	"context"
	"fmt"
	"sync"

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
// The sdkSigner is created once and reused across requests to benefit from
// SignerContext caching, which pre-computes expensive cryptographic operations.
//
// Ring caching: We cache *ring.Ring instances by (appAddress, sessionEndHeight) because:
// - The SDK's SignerContext cache is keyed by ring pointer
// - GetRing() creates new pointers each call, causing cache misses
// - Ring composition can change at session boundaries (delegation changes)
// - By caching the ring pointer per session, SignerContext cache hits work properly
type signer struct {
	accountClient sdk.AccountClient
	sdkSigner     *sdk.Signer

	// ringCache caches *ring.Ring instances by (appAddress, sessionEndHeight).
	// This ensures the same ring pointer is reused within a session,
	// while allowing new rings when sessions change (delegations may differ).
	ringCache sync.Map // map[ringCacheKey]*ring.Ring
}

// newSigner creates a new signer instance with a pre-initialized SDK signer.
// The SDK signer is created once and reused across all signing operations.
func newSigner(accountClient sdk.AccountClient, privateKeyHex string) (*signer, error) {
	sdkSigner, err := sdk.NewSignerFromHex(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("newSigner: error creating SDK signer: %w", err)
	}
	return &signer{
		accountClient: accountClient,
		sdkSigner:     sdkSigner,
	}, nil
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

	// Sign using the cached ring (enables SignerContext cache hits)
	req, err = s.sdkSigner.SignWithRing(context.Background(), req, appRing)
	if err != nil {
		return nil, fmt.Errorf("SignRequest: error signing relay request: %w", err)
	}

	return req, nil
}

// getOrCreateRing returns a cached ring for the application and session, or creates and caches a new one.
// The ring is cached by (appAddress, sessionEndHeight) since delegation changes take effect at session boundaries.
func (s *signer) getOrCreateRing(app apptypes.Application, sessionEndHeight uint64) (*ring.Ring, error) {
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
