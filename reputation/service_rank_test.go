package reputation

import (
	"context"
	"testing"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/protocol"
)

func rankTestKey(addr string) EndpointKey {
	return NewEndpointKey("eth", protocol.EndpointAddr(addr), sharedtypes.RPCType_JSON_RPC)
}

func newRankTestService(t *testing.T) *service {
	t.Helper()
	config := Config{Enabled: true, InitialScore: 80, MinThreshold: 30}
	config.HydrateDefaults()
	return NewService(config, newMockStorage()).(*service)
}

func TestRankEndpointsByScore_Empty(t *testing.T) {
	svc := newRankTestService(t)
	got, err := svc.RankEndpointsByScore(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for empty input, got %v", got)
	}
}

func TestRankEndpointsByScore_Descending(t *testing.T) {
	svc := newRankTestService(t)
	low, mid, high := rankTestKey("low"), rankTestKey("mid"), rankTestKey("high")
	svc.cache[low] = Score{Value: 10}
	svc.cache[mid] = Score{Value: 20}
	svc.cache[high] = Score{Value: 30}

	// Input deliberately unsorted.
	got, err := svc.RankEndpointsByScore(context.Background(), []EndpointKey{mid, low, high})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []EndpointKey{high, mid, low}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("position %d = %v, want %v (full: %v)", i, got[i], want[i], got)
		}
	}
}

// TestRankEndpointsByScore_StableForTies verifies equal scores keep input order,
// matching the original selection-sort behavior the O(n log n) sort replaced.
func TestRankEndpointsByScore_StableForTies(t *testing.T) {
	svc := newRankTestService(t)
	a, b, c := rankTestKey("a"), rankTestKey("b"), rankTestKey("c")
	for _, k := range []EndpointKey{a, b, c} {
		svc.cache[k] = Score{Value: 50}
	}

	input := []EndpointKey{a, b, c}
	got, err := svc.RankEndpointsByScore(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := range input {
		if got[i] != input[i] {
			t.Fatalf("tie order not preserved at %d: got %v, want %v", i, got[i], input[i])
		}
	}
}

// TestRankEndpointsByScore_UncachedUsesInitialScore verifies a key absent from
// the cache is ranked using the service's initial score (benefit of the doubt).
func TestRankEndpointsByScore_UncachedUsesInitialScore(t *testing.T) {
	svc := newRankTestService(t) // InitialScore = 80
	cachedLow := rankTestKey("cached-low")
	svc.cache[cachedLow] = Score{Value: 10}
	uncached := rankTestKey("uncached") // no cache entry -> initial score 80

	got, err := svc.RankEndpointsByScore(context.Background(), []EndpointKey{cachedLow, uncached})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got[0] != uncached {
		t.Fatalf("expected uncached (initial score) first, got %v", got)
	}
}
