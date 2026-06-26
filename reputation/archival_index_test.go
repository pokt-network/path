package reputation

import (
	"context"
	"testing"
	"time"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/protocol"
)

func newArchivalTestService() *service {
	config := Config{Enabled: true, InitialScore: 80, MinThreshold: 30}
	config.HydrateDefaults()
	return NewService(config, newMockStorage()).(*service)
}

func archKey(serviceID protocol.ServiceID, addr string) EndpointKey {
	return NewEndpointKey(serviceID, protocol.EndpointAddr(addr), sharedtypes.RPCType_JSON_RPC)
}

func archAddrSet(keys []EndpointKey) map[protocol.EndpointAddr]bool {
	out := make(map[protocol.EndpointAddr]bool, len(keys))
	for _, k := range keys {
		out[k.EndpointAddr] = true
	}
	return out
}

func TestGetArchivalEndpoints_ReturnsOnlyArchival(t *testing.T) {
	ctx := context.Background()
	svc := newArchivalTestService()

	if err := svc.SetArchivalStatus(ctx, archKey("eth", "a"), true, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetArchivalStatus(ctx, archKey("eth", "b"), true, time.Hour); err != nil {
		t.Fatal(err)
	}
	// Non-archival endpoint must not appear.
	if err := svc.SetArchivalStatus(ctx, archKey("eth", "c"), false, time.Hour); err != nil {
		t.Fatal(err)
	}

	got := archAddrSet(svc.GetArchivalEndpoints(ctx, "eth"))
	if len(got) != 2 || !got["a"] || !got["b"] || got["c"] {
		t.Fatalf("expected {a,b}, got %v", got)
	}
}

func TestGetArchivalEndpoints_FiltersByService(t *testing.T) {
	ctx := context.Background()
	svc := newArchivalTestService()

	_ = svc.SetArchivalStatus(ctx, archKey("eth", "a"), true, time.Hour)
	_ = svc.SetArchivalStatus(ctx, archKey("poly", "b"), true, time.Hour)

	eth := archAddrSet(svc.GetArchivalEndpoints(ctx, "eth"))
	if len(eth) != 1 || !eth["a"] {
		t.Fatalf("eth: expected {a}, got %v", eth)
	}
	if svc.GetArchivalEndpoints(ctx, "unknown") != nil {
		t.Fatal("unknown service should return nil")
	}
}

func TestGetArchivalEndpoints_ExcludesExpired(t *testing.T) {
	ctx := context.Background()
	svc := newArchivalTestService()

	// Negative TTL => ArchivalExpiresAt in the past => archival-capable is false,
	// even though IsArchival is set (so it lives in the index).
	_ = svc.SetArchivalStatus(ctx, archKey("eth", "stale"), true, -time.Second)
	_ = svc.SetArchivalStatus(ctx, archKey("eth", "fresh"), true, time.Hour)

	got := archAddrSet(svc.GetArchivalEndpoints(ctx, "eth"))
	if len(got) != 1 || !got["fresh"] || got["stale"] {
		t.Fatalf("expected only {fresh}, got %v", got)
	}
}

func TestGetArchivalEndpoints_RemovedWhenUnset(t *testing.T) {
	ctx := context.Background()
	svc := newArchivalTestService()
	key := archKey("eth", "a")

	_ = svc.SetArchivalStatus(ctx, key, true, time.Hour)
	if len(svc.GetArchivalEndpoints(ctx, "eth")) != 1 {
		t.Fatal("expected 1 archival endpoint after set")
	}

	_ = svc.SetArchivalStatus(ctx, key, false, time.Hour)
	if got := svc.GetArchivalEndpoints(ctx, "eth"); len(got) != 0 {
		t.Fatalf("expected 0 after unset, got %v", archAddrSet(got))
	}
	// Index entry should be removed, not just filtered.
	if idx := svc.archivalIndex["eth"]; len(idx) != 0 {
		t.Fatalf("archivalIndex should be empty after unset, got %d", len(idx))
	}
}

// TestGetArchivalEndpoints_SurvivesRecordSignal ensures a later score update
// (which rewrites the cache entry via setScoreLocked) keeps the archival index
// entry, since RecordSignal preserves the IsArchival flag.
func TestGetArchivalEndpoints_SurvivesRecordSignal(t *testing.T) {
	ctx := context.Background()
	svc := newArchivalTestService()
	key := archKey("eth", "a")

	_ = svc.SetArchivalStatus(ctx, key, true, time.Hour)
	if err := svc.RecordSignal(ctx, key, NewSuccessSignal(50*time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	got := archAddrSet(svc.GetArchivalEndpoints(ctx, "eth"))
	if len(got) != 1 || !got["a"] {
		t.Fatalf("archival status lost after RecordSignal: %v", got)
	}
}
