package shannon

import (
	"sync"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/pokt-network/poktroll/pkg/crypto/rings"
	servicetypes "github.com/pokt-network/poktroll/x/service/types"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
	ring "github.com/pokt-network/ring-go"
)

// Test_SignerRollover_NoRaceWithConcurrentSigning reproduces the production SIGSEGV:
// a nil-pointer dereference inside internal/sync.(*HashTrieMap).Load, reached from
// getOrCreateSignerContext while a session rollover was clearing the SignerContext cache.
//
// The cause was the SDK's ClearSignerContextCache assigning a fresh sync.Map over one that
// concurrent signers were calling Load on — a multi-word unsynchronized write, so a reader
// could see the new map's keyHash beside the old map's nil root and dereference it.
//
// Run under -race. With the fix (an atomic swap of the whole Signer) this is clean; revert
// to ClearSignerContextCache and the detector reports a write/read race on the map, which
// is the same defect that crashed the pod.
//
// The signing side deliberately calls the SDK method the production path calls, so the
// test exercises the real cache rather than a stand-in.
func Test_SignerRollover_NoRaceWithConcurrentSigning(t *testing.T) {
	const (
		signers   = 8
		rollovers = 4
		iters     = 200
	)

	s := newTestSigner(t)
	// One ring PER GOROUTINE, deliberately.
	//
	// Sharing a single ring across signers trips a SECOND, unrelated race inside ring-go:
	// getOrCreateSignerContext dedups the stored context via LoadOrStore but not the
	// computation, so concurrent cache misses on one ring call Ring.NewSignerContext
	// concurrently, which normalises shared curve points in place. That is a real defect
	// and is tracked separately; giving each goroutine its own ring keeps THIS test
	// pinned to the cache-eviction race it exists to prove.
	rings := make([]*ring.Ring, signers)
	for i := range rings {
		rings[i] = newTestRing(t)
	}
	req := &servicetypes.RelayRequest{
		Meta: servicetypes.RelayRequestMetadata{
			SessionHeader: &sessiontypes.SessionHeader{
				ApplicationAddress:      "app",
				ServiceId:               "svc",
				SessionStartBlockHeight: 1,
				SessionEndBlockHeight:   10,
			},
		},
		Payload: []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber"}`),
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers: hammer the SignerContext cache the way concurrent relay signing does.
	for i := 0; i < signers; i++ {
		wg.Add(1)
		go func(sessionRing *ring.Ring) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// Load through the same atomic pointer the production sign path uses,
				// then touch the per-ring context cache the rollover is evicting.
				sdkSigner := s.sdkSigner.Load()
				if sdkSigner == nil {
					t.Error("sdkSigner must never be nil")
					return
				}
				// The per-ring SignerContext cache lookup inside this call is what raced
				// against the rollover's cache eviction.
				clone := *req
				clone.Meta = req.Meta
				_, _ = sdkSigner.SignOffChainWithRing(t.Context(), &clone, sessionRing)
			}
		}(rings[i])
	}

	// Writers: drive session rollovers, which is what evicts the cache in production.
	for i := 0; i < rollovers; i++ {
		wg.Add(1)
		go func(base uint64) {
			defer wg.Done()
			for n := uint64(1); n <= iters; n++ {
				s.evictStaleRingsOnRollover(base + n)
			}
		}(uint64(i * iters))
	}

	// Let the rollover goroutines finish, then release the readers.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < iters; i++ {
			s.evictStaleRingsOnRollover(uint64(1_000_000 + i))
		}
	}()
	<-done
	close(stop)
	wg.Wait()

	if s.sdkSigner.Load() == nil {
		t.Fatal("signer must remain usable after rollovers")
	}
}

// newTestRing builds a real two-member ring the same way production does — from secp256k1
// public keys via rings.GetRingFromPubKeys — so the cache path under test is the real one.
func newTestRing(t *testing.T) *ring.Ring {
	t.Helper()
	pubKeys := make([]cryptotypes.PubKey, 0, 2)
	for i := 0; i < 2; i++ {
		pubKeys = append(pubKeys, secp256k1.GenPrivKey().PubKey())
	}
	r, err := rings.GetRingFromPubKeys(pubKeys)
	if err != nil {
		t.Fatalf("GetRingFromPubKeys: %v", err)
	}
	return r
}
