package shannon

import (
	"sync"
	"testing"

	ring "github.com/pokt-network/ring-go"
	sdk "github.com/pokt-network/shannon-sdk"
)

// testSignerPrivKeyHex is a valid secp256k1 private key used only to construct an
// SDK signer in tests. Eviction does not sign or touch the network.
const testSignerPrivKeyHex = "1111111111111111111111111111111111111111111111111111111111111111"

// newTestSigner builds a signer directly (bypassing newSigner) so eviction can be
// tested without an AccountClient — the rollover path never touches the network.
func newTestSigner(t *testing.T) *signer {
	t.Helper()
	sdkSigner, err := sdk.NewSignerFromHex(testSignerPrivKeyHex)
	if err != nil {
		t.Fatalf("NewSignerFromHex: %v", err)
	}
	return &signer{sdkSigner: sdkSigner}
}

func (s *signer) putTestRing(sessionEndHeight uint64) {
	s.ringCache.Store(ringCacheKey{appAddress: "app", sessionEndHeight: sessionEndHeight}, new(ring.Ring))
}

func (s *signer) hasSession(sessionEndHeight uint64) bool {
	_, ok := s.ringCache.Load(ringCacheKey{appAddress: "app", sessionEndHeight: sessionEndHeight})
	return ok
}

func (s *signer) ringCacheLen() int {
	n := 0
	s.ringCache.Range(func(_, _ any) bool { n++; return true })
	return n
}

// TestEvictStaleRingsOnRollover verifies the per-session cache stays bounded:
// on a newer session, entries older than the previous session are dropped while
// current + previous are kept, and stale/equal heights are no-ops.
func TestEvictStaleRingsOnRollover(t *testing.T) {
	s := newTestSigner(t)

	// Seed three sessions; mark the newest (30) as the highest seen.
	s.putTestRing(10)
	s.putTestRing(20)
	s.putTestRing(30)
	s.evictStaleRingsOnRollover(30) // prev=0 -> nothing < 0 evicted
	if got := s.ringCacheLen(); got != 3 {
		t.Fatalf("after seeding: want 3 entries, got %d", got)
	}
	if got := s.highestSessionEnd.Load(); got != 30 {
		t.Fatalf("highestSessionEnd: want 30, got %d", got)
	}

	// Rollover to session 40: keep current(40)+previous(30), drop 10 and 20.
	s.putTestRing(40)
	s.evictStaleRingsOnRollover(40) // prev=30 -> evict < 30
	if s.hasSession(10) || s.hasSession(20) {
		t.Fatalf("stale sessions 10/20 not evicted (len=%d)", s.ringCacheLen())
	}
	if !s.hasSession(30) || !s.hasSession(40) {
		t.Fatalf("current/previous sessions dropped (len=%d)", s.ringCacheLen())
	}
	if got := s.highestSessionEnd.Load(); got != 40 {
		t.Fatalf("highestSessionEnd: want 40, got %d", got)
	}

	// Fast path: equal or older heights are no-ops and evict nothing.
	s.putTestRing(35) // in-flight previous-session entry
	before := s.ringCacheLen()
	s.evictStaleRingsOnRollover(40) // equal
	s.evictStaleRingsOnRollover(25) // older
	if got := s.ringCacheLen(); got != before {
		t.Fatalf("no-op rollover changed cache: before=%d after=%d", before, got)
	}
	if got := s.highestSessionEnd.Load(); got != 40 {
		t.Fatalf("highestSessionEnd changed on no-op: %d", got)
	}
}

// TestEvictStaleRingsOnRollover_Concurrent exercises the rollover path under
// concurrent callers (run with -race) to confirm the lock/atomic gating holds
// and highestSessionEnd converges to the max height. The cache bound is not
// asserted here: under arbitrary ordering a late low height can be inserted
// after the max is reached and never pruned. Bounding under monotonic sessions
// (the real case) is covered by TestEvictStaleRingsOnRollover.
func TestEvictStaleRingsOnRollover_Concurrent(t *testing.T) {
	s := newTestSigner(t)

	var wg sync.WaitGroup
	for h := uint64(1); h <= 200; h++ {
		wg.Add(1)
		go func(height uint64) {
			defer wg.Done()
			s.putTestRing(height)
			s.evictStaleRingsOnRollover(height)
		}(h)
	}
	wg.Wait()

	// highestSessionEnd is monotonic, so it must equal the max height observed.
	if got := s.highestSessionEnd.Load(); got != 200 {
		t.Fatalf("highestSessionEnd: want 200, got %d", got)
	}
}
