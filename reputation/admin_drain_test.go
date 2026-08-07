package reputation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func drainTestService(t *testing.T) (*service, Storage, context.Context) {
	t.Helper()

	ctx := context.Background()
	store := newMockStorage()
	t.Cleanup(func() { _ = store.Close() })

	config := Config{Enabled: true, InitialScore: 100, MinThreshold: 30}
	config.HydrateDefaults()

	svc := NewService(config, store)
	require.NoError(t, svc.Start(ctx))
	t.Cleanup(func() { _ = svc.Stop() })

	return svc.(*service), store, ctx
}

// THE REGRESSION TEST.
//
// The first implementation resolved the target to a fixed set of EndpointKeys and benched
// those. EndpointAddr is `supplierAddr-url` and a session rotates its supplier set every
// rollover, so within ~20 minutes the benched keys were stale, the live endpoints at that
// operator had never been benched, and selection picked them freely — while the metric kept
// reporting the stale count as though the bench held. In production every "drained"
// connection rebound straight back onto the drained operator.
//
// A drain is a property of the OPERATOR, so nothing about which supplier addresses happen to
// be in the current session may affect it.
func TestDrain_SurvivesSessionRotation(t *testing.T) {
	svc, _, ctx := drainTestService(t)

	svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Domain: "op-beta.example", RPCType: "websocket", Duration: 45 * time.Minute,
	})

	// Session N: these supplier addresses are in session now.
	require.True(t, svc.IsDomainDrained("gnosis", "op-beta.example", "websocket"))

	// Session N+1: entirely different supplier addresses front the same operator. The drain
	// is keyed on the operator, so it must still bite — this is what broke in production.
	require.True(t, svc.IsDomainDrained("gnosis", "op-beta.example", "websocket"),
		"a drain must not depend on which supplier addresses are in session")

	// A hostname at the same operator resolves to the same registrable domain upstream, so
	// a machine rotated in mid-drain is covered too.
	require.True(t, svc.IsDomainDrained("gnosis", "OP-BETA.EXAMPLE", "websocket"),
		"matching must be case-insensitive")
}

func TestDrain_ScopedToServiceAndRPCType(t *testing.T) {
	svc, _, ctx := drainTestService(t)

	svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Domain: "op-beta.example", RPCType: "websocket", Duration: 45 * time.Minute,
	})

	require.True(t, svc.IsDomainDrained("gnosis", "op-beta.example", "websocket"))
	require.False(t, svc.IsDomainDrained("gnosis", "op-beta.example", "json_rpc"),
		"draining websocket must leave the operator's HTTP traffic alone")
	require.False(t, svc.IsDomainDrained("bsc", "op-beta.example", "websocket"),
		"a drain must not leak across services")
	require.False(t, svc.IsDomainDrained("gnosis", "op-gamma.example", "websocket"),
		"a drain must not leak across operators")
}

// A drain with no RPC type covers every protocol for the service.
func TestDrain_EmptyRPCTypeCoversAll(t *testing.T) {
	svc, _, ctx := drainTestService(t)

	svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Domain: "op-beta.example", Duration: 45 * time.Minute,
	})

	require.True(t, svc.IsDomainDrained("gnosis", "op-beta.example", "websocket"))
	require.True(t, svc.IsDomainDrained("gnosis", "op-beta.example", "json_rpc"))
	require.True(t, svc.IsDomainDrained("gnosis", "op-beta.example", "rest"))
}

func TestDrain_ReleaseAndExpiry(t *testing.T) {
	svc, _, ctx := drainTestService(t)

	svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Domain: "op-beta.example", RPCType: "websocket", Duration: 45 * time.Minute,
	})
	require.True(t, svc.IsDomainDrained("gnosis", "op-beta.example", "websocket"))

	res := svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Domain: "op-beta.example", RPCType: "websocket", Duration: 0,
	})
	require.True(t, res.Released)
	require.False(t, svc.IsDomainDrained("gnosis", "op-beta.example", "websocket"))

	// A forgotten drain must lift itself — nobody should have to remember to unban anyone.
	svc.mu.Lock()
	svc.drainedDomains = map[DrainKey]time.Time{
		{ServiceID: "gnosis", Domain: "op-beta.example", RPCType: "websocket"}: time.Now().Add(-time.Second),
	}
	svc.mu.Unlock()
	require.False(t, svc.IsDomainDrained("gnosis", "op-beta.example", "websocket"),
		"an expired drain must not still bench")
}

// A drain must never touch reputation: the quality signal has to stay readable while an
// operator is benched, since reading it is usually why the drain exists.
func TestDrain_DoesNotTouchScores(t *testing.T) {
	svc, _, ctx := drainTestService(t)

	key := NewEndpointKey("gnosis", "pokt1abc-https://rm-01.op-beta.example", 0)
	require.NoError(t, svc.RecordSignal(ctx, key, NewSuccessSignal(10*time.Millisecond)))
	before, err := svc.GetScore(ctx, key)
	require.NoError(t, err)

	svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Domain: "op-beta.example", RPCType: "websocket", Duration: 45 * time.Minute,
	})

	after, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Equal(t, before.Value, after.Value)
	require.True(t, after.CooldownUntil.IsZero(), "a drain must never write a cooldown onto the score")
	require.Equal(t, before.CriticalStrikes, after.CriticalStrikes)
	require.Equal(t, before.RateCooldownCount, after.RateCooldownCount,
		"a drain must be invisible to the rate-cooldown escalation ladder")
}

// ---------- Fleet-wide propagation ----------

func TestDrain_PropagatesToOtherReplicas(t *testing.T) {
	ctx := context.Background()
	shared := newMockStorage()
	t.Cleanup(func() { _ = shared.Close() })

	cfg := Config{Enabled: true, InitialScore: 100, MinThreshold: 30}
	cfg.HydrateDefaults()

	podA := NewService(cfg, shared).(*service)
	require.NoError(t, podA.Start(ctx))
	t.Cleanup(func() { _ = podA.Stop() })
	podB := NewService(cfg, shared).(*service)
	require.NoError(t, podB.Start(ctx))
	t.Cleanup(func() { _ = podB.Stop() })

	podA.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Domain: "op-beta.example", RPCType: "websocket", Duration: 45 * time.Minute,
	})
	require.True(t, podA.IsDomainDrained("gnosis", "op-beta.example", "websocket"))
	require.False(t, podB.IsDomainDrained("gnosis", "op-beta.example", "websocket"), "pod B has not refreshed yet")

	podB.refreshDrains(ctx)
	require.True(t, podB.IsDomainDrained("gnosis", "op-beta.example", "websocket"),
		"one admin call must bench every replica")

	// And a release must lift it everywhere, or un-banning would need N calls again.
	podA.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Domain: "op-beta.example", RPCType: "websocket", Duration: 0,
	})
	podB.refreshDrains(ctx)
	require.False(t, podB.IsDomainDrained("gnosis", "op-beta.example", "websocket"))
}

// Losing storage mid-incident must not silently un-ban everyone.
func TestDrain_StorageFailureKeepsLocalDrains(t *testing.T) {
	ctx := context.Background()
	shared := newMockStorage()

	cfg := Config{Enabled: true, InitialScore: 100, MinThreshold: 30}
	cfg.HydrateDefaults()
	pod := NewService(cfg, shared).(*service)
	require.NoError(t, pod.Start(ctx))
	t.Cleanup(func() { _ = pod.Stop() })

	pod.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Domain: "op-beta.example", RPCType: "websocket", Duration: 45 * time.Minute,
	})
	_ = shared.Close()

	pod.refreshDrains(ctx)
	require.True(t, pod.IsDomainDrained("gnosis", "op-beta.example", "websocket"),
		"a storage outage must not clear in-force drains")
}

func TestDrain_ReportsPropagationFailure(t *testing.T) {
	ctx := context.Background()
	shared := newMockStorage()

	cfg := Config{Enabled: true, InitialScore: 100, MinThreshold: 30}
	cfg.HydrateDefaults()
	pod := NewService(cfg, shared).(*service)
	require.NoError(t, pod.Start(ctx))
	t.Cleanup(func() { _ = pod.Stop() })

	_ = shared.Close()
	res := pod.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Domain: "op-beta.example", RPCType: "websocket", Duration: 45 * time.Minute,
	})
	require.NotEmpty(t, res.PropagationError)
	require.True(t, pod.IsDomainDrained("gnosis", "op-beta.example", "websocket"), "the local bench still applies")
}

func TestDrain_DryRunChangesNothing(t *testing.T) {
	svc, _, ctx := drainTestService(t)

	res := svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Domain: "op-beta.example", RPCType: "websocket",
		Duration: 45 * time.Minute, DryRun: true,
	})
	require.True(t, res.DryRun)
	require.False(t, svc.IsDomainDrained("gnosis", "op-beta.example", "websocket"),
		"a dry run must bench nothing")
}

// An empty domain must bench nothing rather than the whole service.
func TestDrain_EmptyDomainBenchesNothing(t *testing.T) {
	svc, _, ctx := drainTestService(t)

	svc.DrainDomain(ctx, DrainRequest{ServiceID: "gnosis", Duration: 45 * time.Minute})
	require.False(t, svc.IsDomainDrained("gnosis", "op-beta.example", "websocket"))
	require.False(t, svc.IsDomainDrained("gnosis", "", "websocket"))
}
