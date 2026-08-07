package reputation

import (
	"context"
	"testing"
	"time"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
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

func seedScored(t *testing.T, svc *service, ctx context.Context, addr string, rpcType sharedtypes.RPCType) EndpointKey {
	t.Helper()
	key := NewEndpointKey("gnosis", protocol.EndpointAddr(addr), rpcType)
	require.NoError(t, svc.RecordSignal(ctx, key, NewSuccessSignal(10*time.Millisecond)))
	return key
}

// isBenched asks the question selection actually asks, through the call selection makes.
//
// The gate lives in protocol/shannon/reputation.go, which reads scores via GetScores and
// drops any endpoint whose IsInCooldown() is true. So the contract a drain must satisfy is
// precisely "GetScores reports this key as in cooldown" — not "the cached Score struct has
// CooldownUntil set", which is what the original tests asserted and is exactly why they
// passed while a storage refresh silently erased every bench.
//
// Note FilterByScore is NOT the gate: it only compares Value against the threshold and
// ignores cooldown entirely.
func isBenched(t *testing.T, svc *service, ctx context.Context, key EndpointKey) bool {
	t.Helper()
	scores, err := svc.GetScores(ctx, []EndpointKey{key})
	require.NoError(t, err)
	score, ok := scores[key]
	return ok && score.IsInCooldown()
}

// THE REGRESSION TEST. The first implementation wrote CooldownUntil onto the Score, and
// refreshFromStorage overwrites the local cache from storage unconditionally — so every
// drain silently evaporated on the next refresh tick while the endpoint kept reporting
// drained=N. Any drain that does not survive this is not a drain.
func TestDrainDomain_SurvivesStorageRefresh(t *testing.T) {
	svc, store, ctx := drainTestService(t)
	key := seedScored(t, svc, ctx, "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)

	// Persist the pre-drain score, so a refresh has something to clobber the drain with.
	pre, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.NoError(t, store.Set(ctx, key, pre))

	svc.DrainDomain(ctx, DrainRequest{
		ServiceID:   "gnosis",
		Identifiers: []string{"https://rm-01.spacebelt.xyz"},
		Duration:    15 * time.Minute,
	})
	require.True(t, isBenched(t, svc, ctx, key), "endpoint must be benched immediately after draining")

	require.NoError(t, svc.refreshFromStorage(ctx))

	require.True(t, isBenched(t, svc, ctx, key),
		"drain must survive a storage refresh — this is the bug that shipped")
}

// Ordinary traffic must not wash the bench out either: RecordSignal rewrites the cached
// Score on every observation, and health checks alone fire constantly.
func TestDrainDomain_SurvivesRecordSignal(t *testing.T) {
	svc, _, ctx := drainTestService(t)
	key := seedScored(t, svc, ctx, "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)

	svc.DrainDomain(ctx, DrainRequest{
		ServiceID:   "gnosis",
		Identifiers: []string{"https://rm-01.spacebelt.xyz"},
		Duration:    15 * time.Minute,
	})

	for i := 0; i < 25; i++ {
		require.NoError(t, svc.RecordSignal(ctx, key, NewSuccessSignal(5*time.Millisecond)))
	}

	require.True(t, isBenched(t, svc, ctx, key), "a stream of successes must not lift an admin drain")
}

func TestDrainDomain_ReleaseRestoresSelectability(t *testing.T) {
	svc, _, ctx := drainTestService(t)
	key := seedScored(t, svc, ctx, "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)

	svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Identifiers: []string{"https://rm-01.spacebelt.xyz"}, Duration: 15 * time.Minute,
	})
	require.True(t, isBenched(t, svc, ctx, key))

	res := svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Identifiers: []string{"https://rm-01.spacebelt.xyz"}, Duration: 0,
	})
	require.Equal(t, 1, res.Released)
	require.False(t, isBenched(t, svc, ctx, key), "release must return the endpoint to selection")
}

// A drain must expire on its own — a forgotten bench that never lifts is an outage.
func TestDrainDomain_ExpiresOnItsOwn(t *testing.T) {
	svc, _, ctx := drainTestService(t)
	key := seedScored(t, svc, ctx, "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)

	svc.mu.Lock()
	svc.drainedKeys = map[EndpointKey]time.Time{key: time.Now().Add(-time.Second)}
	svc.mu.Unlock()

	require.False(t, isBenched(t, svc, ctx, key), "an expired drain must not still bench")
}

// Releasing must never disturb a cooldown the endpoint earned on its own. With the drain
// held as an overlay this is true by construction — the drain never wrote to the Score —
// but it is the property operators rely on, so it is pinned.
func TestDrainDomain_ReleaseLeavesEarnedCooldownAlone(t *testing.T) {
	svc, _, ctx := drainTestService(t)
	key := seedScored(t, svc, ctx, "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)

	svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Identifiers: []string{"https://rm-01.spacebelt.xyz"}, Duration: 15 * time.Minute,
	})

	earned := time.Now().Add(42 * time.Minute)
	svc.mu.Lock()
	sc := svc.cache[key]
	sc.CooldownUntil = earned
	svc.setScoreLocked(key, sc)
	svc.mu.Unlock()

	svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Identifiers: []string{"https://rm-01.spacebelt.xyz"}, Duration: 0,
	})

	require.True(t, isBenched(t, svc, ctx, key), "an independently earned cooldown must survive a release")
	got, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.WithinDuration(t, earned, got.CooldownUntil, time.Second)
}

func TestDrainDomain_ScopedToServiceAndRPCType(t *testing.T) {
	svc, _, ctx := drainTestService(t)

	ws := seedScored(t, svc, ctx, "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)
	httpKey := seedScored(t, svc, ctx, "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_JSON_RPC)
	otherSvc := NewEndpointKey("bsc", "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)
	require.NoError(t, svc.RecordSignal(ctx, otherSvc, NewSuccessSignal(time.Millisecond)))

	svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Identifiers: []string{"https://rm-01.spacebelt.xyz"},
		RPCType: "websocket", Duration: 15 * time.Minute,
	})

	require.True(t, isBenched(t, svc, ctx, ws))
	require.False(t, isBenched(t, svc, ctx, httpKey), "draining websocket must leave HTTP serving")
	require.False(t, isBenched(t, svc, ctx, otherSvc), "a drain must not leak across services")
}

// Identifier matching is exact, so a superset of granularities is safe. This pins that a
// supplier-address-keyed service is benchable — the case that defeated the eTLD+1 filter
// in production and returned matched=0 while looking successful.
func TestDrainDomain_MatchesSupplierAddressGranularity(t *testing.T) {
	svc, _, ctx := drainTestService(t)
	supplierKey := seedScored(t, svc, ctx, "pokt1pzdwzgmj9ttfjcmv2r9anwqlajnjzsz6yhzdmn", sharedtypes.RPCType_WEBSOCKET)

	res := svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis",
		Identifiers: []string{
			"spacebelt.xyz", "rm-01.spacebelt.xyz", "https://rm-01.spacebelt.xyz",
			"pokt1pzdwzgmj9ttfjcmv2r9anwqlajnjzsz6yhzdmn",
		},
		Duration: 15 * time.Minute,
	})
	require.Equal(t, 1, res.Matched)
	require.True(t, isBenched(t, svc, ctx, supplierKey))
}

func TestDrainDomain_DryRunChangesNothing(t *testing.T) {
	svc, _, ctx := drainTestService(t)
	key := seedScored(t, svc, ctx, "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)

	res := svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Identifiers: []string{"https://rm-01.spacebelt.xyz"},
		Duration: 15 * time.Minute, DryRun: true,
	})
	require.Equal(t, 1, res.Matched)
	require.Equal(t, 1, res.Drained)
	require.False(t, isBenched(t, svc, ctx, key), "a dry run must bench nothing")
}

// An empty identifier set must bench nothing rather than everything — a failed resolution
// upstream must never widen into a service-wide outage.
func TestDrainDomain_EmptyIdentifiersBenchNothing(t *testing.T) {
	svc, _, ctx := drainTestService(t)
	key := seedScored(t, svc, ctx, "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)

	res := svc.DrainDomain(ctx, DrainRequest{ServiceID: "gnosis", Duration: 15 * time.Minute})
	require.Equal(t, 0, res.Matched)
	require.NotEmpty(t, res.Warning)
	require.False(t, isBenched(t, svc, ctx, key))
}

// ---------- Fleet-wide propagation ----------

// The point of shared storage: one admin call must bench the endpoint on EVERY replica.
// Before this, a drain was pod-local, so an operator had to hit all N pods — and in
// practice got a partial drain without realising it was partial.
func TestDrainDomain_PropagatesToOtherReplicas(t *testing.T) {
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

	key := NewEndpointKey("gnosis", "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)
	require.NoError(t, podA.RecordSignal(ctx, key, NewSuccessSignal(time.Millisecond)))
	require.NoError(t, podB.RecordSignal(ctx, key, NewSuccessSignal(time.Millisecond)))

	// Drain on pod A only.
	res := podA.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Identifiers: []string{"https://rm-01.spacebelt.xyz"}, Duration: 20 * time.Minute,
	})
	require.Equal(t, 1, res.Drained)
	require.Empty(t, res.PropagationError)

	require.True(t, isBenched(t, podA, ctx, key), "the pod that issued the drain must bench")
	require.False(t, isBenched(t, podB, ctx, key), "pod B has not refreshed yet")

	// Pod B picks it up on its next refresh, with no admin call of its own.
	podB.refreshDrains(ctx)
	require.True(t, isBenched(t, podB, ctx, key), "one admin call must bench every replica")
}

// A release must propagate too, or lifting a fleet-wide drain would need N calls again —
// and a replica that merged instead of replacing would bench forever.
func TestDrainDomain_ReleasePropagatesToOtherReplicas(t *testing.T) {
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

	key := NewEndpointKey("gnosis", "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)
	require.NoError(t, podA.RecordSignal(ctx, key, NewSuccessSignal(time.Millisecond)))
	require.NoError(t, podB.RecordSignal(ctx, key, NewSuccessSignal(time.Millisecond)))

	podA.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Identifiers: []string{"https://rm-01.spacebelt.xyz"}, Duration: 20 * time.Minute,
	})
	podB.refreshDrains(ctx)
	require.True(t, isBenched(t, podB, ctx, key))

	// Release on pod A.
	podA.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Identifiers: []string{"https://rm-01.spacebelt.xyz"}, Duration: 0,
	})

	podB.refreshDrains(ctx)
	require.False(t, isBenched(t, podB, ctx, key), "a release must lift the bench on every replica")
}

// A forgotten drain must lift itself. Nobody should have to remember to unlock anyone.
func TestDrainDomain_ExpiredDrainIsNotPropagated(t *testing.T) {
	ctx := context.Background()
	shared := newMockStorage()
	t.Cleanup(func() { _ = shared.Close() })

	cfg := Config{Enabled: true, InitialScore: 100, MinThreshold: 30}
	cfg.HydrateDefaults()
	pod := NewService(cfg, shared).(*service)
	require.NoError(t, pod.Start(ctx))
	t.Cleanup(func() { _ = pod.Stop() })

	key := NewEndpointKey("gnosis", "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)
	require.NoError(t, pod.RecordSignal(ctx, key, NewSuccessSignal(time.Millisecond)))
	require.NoError(t, shared.SetDrain(ctx, key, time.Now().Add(-time.Minute)))

	pod.refreshDrains(ctx)
	require.False(t, isBenched(t, pod, ctx, key), "an expired drain must never be applied")

	drains, err := shared.ListDrains(ctx)
	require.NoError(t, err)
	require.Empty(t, drains, "expired drains must be reaped from shared storage")
}

// Losing storage mid-incident must not silently un-bench everything.
func TestDrainDomain_StorageFailureKeepsLocalDrains(t *testing.T) {
	ctx := context.Background()
	shared := newMockStorage()

	cfg := Config{Enabled: true, InitialScore: 100, MinThreshold: 30}
	cfg.HydrateDefaults()
	pod := NewService(cfg, shared).(*service)
	require.NoError(t, pod.Start(ctx))
	t.Cleanup(func() { _ = pod.Stop() })

	key := NewEndpointKey("gnosis", "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)
	require.NoError(t, pod.RecordSignal(ctx, key, NewSuccessSignal(time.Millisecond)))
	pod.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Identifiers: []string{"https://rm-01.spacebelt.xyz"}, Duration: 20 * time.Minute,
	})
	require.True(t, isBenched(t, pod, ctx, key))

	_ = shared.Close() // storage now errors

	pod.refreshDrains(ctx)
	require.True(t, isBenched(t, pod, ctx, key),
		"a storage outage must not clear in-force drains")
}

// When storage is unreachable at drain time the operator must be told the bench is
// pod-local, rather than being left to assume it went fleet-wide.
func TestDrainDomain_ReportsPropagationFailure(t *testing.T) {
	ctx := context.Background()
	shared := newMockStorage()

	cfg := Config{Enabled: true, InitialScore: 100, MinThreshold: 30}
	cfg.HydrateDefaults()
	pod := NewService(cfg, shared).(*service)
	require.NoError(t, pod.Start(ctx))
	t.Cleanup(func() { _ = pod.Stop() })

	key := NewEndpointKey("gnosis", "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)
	require.NoError(t, pod.RecordSignal(ctx, key, NewSuccessSignal(time.Millisecond)))
	_ = shared.Close()

	res := pod.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Identifiers: []string{"https://rm-01.spacebelt.xyz"}, Duration: 20 * time.Minute,
	})
	require.Equal(t, 1, res.Drained)
	require.NotEmpty(t, res.PropagationError)
	require.Contains(t, res.Warning, "THIS POD ONLY")
	require.True(t, isBenched(t, pod, ctx, key), "the local bench still applies")
}

// ---------- Isolation from the scoring system ----------

// A drain must not contaminate the persisted score. If the overlay ever leaked into a
// write, an operator benched for a 20-minute experiment would carry a real cooldown
// afterwards — and the reputation record would be a lie about their behaviour.
func TestDrainDomain_DoesNotContaminateStoredScore(t *testing.T) {
	svc, store, ctx := drainTestService(t)
	key := seedScored(t, svc, ctx, "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)

	before, err := svc.GetScore(ctx, key)
	require.NoError(t, err)

	svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Identifiers: []string{"https://rm-01.spacebelt.xyz"}, Duration: 20 * time.Minute,
	})
	require.True(t, isBenched(t, svc, ctx, key))

	// The RAW cached score — not the overlaid read — must be untouched.
	svc.mu.RLock()
	raw := svc.cache[key]
	svc.mu.RUnlock()

	require.True(t, raw.CooldownUntil.IsZero(), "a drain must never write CooldownUntil onto the score")
	require.Equal(t, before.Value, raw.Value)
	require.Equal(t, before.CriticalStrikes, raw.CriticalStrikes)
	require.Equal(t, before.RateCooldownCount, raw.RateCooldownCount)

	// And nothing benched may reach persistence either. The drain queues no score write at
	// all now, so whatever is stored came from ordinary signal recording.
	if stored, storeErr := store.Get(ctx, key); storeErr == nil {
		require.True(t, stored.CooldownUntil.IsZero(), "the drain must not be persisted onto the score")
	}
}

// The rate-cooldown escalation ladder keys off the PREVIOUS CooldownUntil: each
// consecutive trip benches for longer. If a drain were visible to it, benching an operator
// for an experiment would silently escalate their next real cooldown — punishing them for
// something we did.
func TestDrainDomain_DoesNotEscalateRateCooldown(t *testing.T) {
	svc, _, ctx := drainTestService(t)
	key := seedScored(t, svc, ctx, "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)

	svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Identifiers: []string{"https://rm-01.spacebelt.xyz"}, Duration: 20 * time.Minute,
	})

	for i := 0; i < 20; i++ {
		require.NoError(t, svc.RecordSignal(ctx, key, NewSuccessSignal(time.Millisecond)))
	}

	svc.mu.RLock()
	raw := svc.cache[key]
	svc.mu.RUnlock()

	require.Zero(t, raw.RateCooldownCount, "a drain must be invisible to the escalation ladder")
	require.True(t, raw.CooldownUntil.IsZero())
}

// Recovery resets the Score. It must neither lift the drain nor be lifted by it.
func TestDrainDomain_UnaffectedByScoreRecovery(t *testing.T) {
	svc, _, ctx := drainTestService(t)
	key := seedScored(t, svc, ctx, "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)

	svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis", Identifiers: []string{"https://rm-01.spacebelt.xyz"}, Duration: 20 * time.Minute,
	})

	svc.recoverScore(ctx, key)

	require.True(t, isBenched(t, svc, ctx, key), "recovering the score must not lift an admin drain")

	svc.mu.RLock()
	raw := svc.cache[key]
	svc.mu.RUnlock()
	require.True(t, raw.CooldownUntil.IsZero(), "recovery must not pick up the overlay either")
}
