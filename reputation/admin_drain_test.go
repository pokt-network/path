package reputation

import (
	"context"
	"testing"
	"time"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// drainTestService builds a started service seeded with endpoints across three operators,
// mirroring the shape that motivated the drain: several backends per operator, one
// service, one RPC type unless a test says otherwise.
func drainTestService(t *testing.T) (ReputationService, context.Context) {
	t.Helper()

	ctx := context.Background()
	store := newMockStorage()
	t.Cleanup(func() { _ = store.Close() })

	config := Config{Enabled: true, InitialScore: 100, MinThreshold: 30}
	config.HydrateDefaults()

	svc := NewService(config, store)
	require.NoError(t, svc.Start(ctx))
	t.Cleanup(func() { _ = svc.Stop() })

	return svc, ctx
}

func seedScored(t *testing.T, svc ReputationService, ctx context.Context, addr string, rpcType sharedtypes.RPCType) EndpointKey {
	t.Helper()
	key := NewEndpointKey("gnosis", protocol.EndpointAddr(addr), rpcType)
	require.NoError(t, svc.RecordSignal(ctx, key, NewSuccessSignal(10*time.Millisecond)))
	return key
}

func TestDrainDomain_BenchesOnlyTheRequestedOperator(t *testing.T) {
	svc, ctx := drainTestService(t)

	target := seedScored(t, svc, ctx, "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)
	target2 := seedScored(t, svc, ctx, "https://rm-02.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)
	bystander := seedScored(t, svc, ctx, "https://rm-01.kalorius.tech", sharedtypes.RPCType_WEBSOCKET)

	before, err := svc.GetScore(ctx, target)
	require.NoError(t, err)

	res := svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis",
		Domain:    "spacebelt.xyz",
		Duration:  15 * time.Minute,
	})
	require.Equal(t, 2, res.Matched)
	require.Equal(t, 2, res.Drained)

	for _, k := range []EndpointKey{target, target2} {
		score, err := svc.GetScore(ctx, k)
		require.NoError(t, err)
		require.True(t, score.IsInCooldown(), "drained endpoint must be benched")
	}

	// The bystander operator is untouched — a drain is scoped to one operator or it is
	// not a drain, it is an outage.
	other, err := svc.GetScore(ctx, bystander)
	require.NoError(t, err)
	require.False(t, other.IsInCooldown())

	// Reputation itself must survive the drain: the whole point is to read quality while
	// an operator is benched, which is impossible if benching rewrites the score.
	after, err := svc.GetScore(ctx, target)
	require.NoError(t, err)
	require.Equal(t, before.Value, after.Value, "drain must not alter score value")
	require.Equal(t, before.CriticalStrikes, after.CriticalStrikes)
	require.Equal(t, before.SuccessCount, after.SuccessCount)
}

func TestDrainDomain_RPCTypeNarrowing(t *testing.T) {
	svc, ctx := drainTestService(t)

	ws := seedScored(t, svc, ctx, "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)
	jsonRPC := seedScored(t, svc, ctx, "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_JSON_RPC)

	res := svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis",
		Domain:    "spacebelt.xyz",
		Duration:  15 * time.Minute,
		RPCType:   "websocket",
	})
	require.Equal(t, 1, res.Matched)

	wsScore, err := svc.GetScore(ctx, ws)
	require.NoError(t, err)
	require.True(t, wsScore.IsInCooldown())

	// Draining an operator's websocket endpoints must not take its HTTP traffic with it.
	httpScore, err := svc.GetScore(ctx, jsonRPC)
	require.NoError(t, err)
	require.False(t, httpScore.IsInCooldown())
}

func TestDrainDomain_DryRunWritesNothing(t *testing.T) {
	svc, ctx := drainTestService(t)
	key := seedScored(t, svc, ctx, "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)

	res := svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis",
		Domain:    "spacebelt.xyz",
		Duration:  15 * time.Minute,
		DryRun:    true,
	})
	require.Equal(t, 1, res.Matched)
	require.Equal(t, 1, res.Drained, "dry run reports what it would do")

	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.False(t, score.IsInCooldown(), "dry run must not bench anything")
}

// A release must lift the drain's own bench and nothing else. If it cleared cooldowns
// wholesale it would un-bench an endpoint that failed for real while the drain was up —
// the one outcome nobody running an experiment would intend.
func TestDrainDomain_ReleaseLeavesEarnedCooldownAlone(t *testing.T) {
	svc, ctx := drainTestService(t)

	drained := seedScored(t, svc, ctx, "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)
	earned := seedScored(t, svc, ctx, "https://rm-02.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)

	svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis",
		Domain:    "spacebelt.xyz",
		Duration:  15 * time.Minute,
	})

	// Simulate the second endpoint earning a real cooldown after the drain landed, which
	// overwrites the drain's expiry with a different one.
	realCooldown := time.Now().Add(42 * time.Minute)
	svcImpl := svc.(*service)
	svcImpl.mu.Lock()
	s := svcImpl.cache[earned]
	s.CooldownUntil = realCooldown
	svcImpl.setScoreLocked(earned, s)
	svcImpl.mu.Unlock()

	res := svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis",
		Domain:    "spacebelt.xyz",
		Duration:  0,
	})
	require.Equal(t, 1, res.Released, "only the untouched drain should be released")

	releasedScore, err := svc.GetScore(ctx, drained)
	require.NoError(t, err)
	require.False(t, releasedScore.IsInCooldown(), "drain-applied bench must be lifted")

	keptScore, err := svc.GetScore(ctx, earned)
	require.NoError(t, err)
	require.True(t, keptScore.IsInCooldown(), "independently earned cooldown must survive a release")
	require.WithinDuration(t, realCooldown, keptScore.CooldownUntil, time.Second)
}

func TestDrainDomain_UnknownDomainReportsWhatExists(t *testing.T) {
	svc, ctx := drainTestService(t)
	seedScored(t, svc, ctx, "https://rm-01.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)

	res := svc.DrainDomain(ctx, DrainRequest{
		ServiceID: "gnosis",
		Domain:    "typo.example",
		Duration:  15 * time.Minute,
	})
	require.Equal(t, 0, res.Matched)
	require.Contains(t, res.DomainsSeen, "spacebelt.xyz",
		"a typo must surface the real domains rather than look like a successful drain")
	require.NotEmpty(t, res.UnscoredWarning)
}
