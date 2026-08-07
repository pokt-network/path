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
