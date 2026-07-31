package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/metrics"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
	reputationstorage "github.com/pokt-network/path/reputation/storage"
)

func topRankedCandidate(t *testing.T, serviceID, domain string) float64 {
	t.Helper()
	return testutil.ToFloat64(metrics.SelectionCandidateTotal.WithLabelValues(serviceID, domain, metrics.SelectionPathTopRanked))
}

func topRankedSelected(t *testing.T, serviceID, domain string) float64 {
	t.Helper()
	return testutil.ToFloat64(metrics.SelectionSelectedTotal.WithLabelValues(serviceID, domain, metrics.SelectionPathTopRanked))
}

func bandExcluded(t *testing.T, serviceID, domain string) float64 {
	t.Helper()
	return testutil.ToFloat64(metrics.SelectionBandExcludedTotal.WithLabelValues(serviceID, domain))
}

// selectTopRankedEndpoint is the PRIMARY selector for batch items and retries, not just an
// overflow path — so it is where a skewed traffic distribution is actually decided. These
// metrics exist because the previously-instrumented selectors' output is discarded on that
// path (SelectMultipleWithArchival is called with numEndpoints = len(pool), as a validation
// filter), which made their win rates anti-correlate with real relay counts.
//
// The specific thing being pinned here: an operator that is present, healthy, and passes QoS
// validation can still be structurally ineligible because it sits below the score band. That
// must show up as an EXCLUSION, not as a low win rate — a low win rate reads as bad luck.
func TestTopRankedInstrumentation_RecordsBandAndExclusions(t *testing.T) {
	ctx := context.Background()

	config := reputation.Config{
		Enabled:         true,
		InitialScore:    80,
		MinThreshold:    30,
		RecoveryTimeout: 5 * time.Minute,
		StorageType:     "memory",
	}
	config.HydrateDefaults()
	store := reputationstorage.NewMemoryStorage(config.RecoveryTimeout)
	svc := reputation.NewService(config, store)
	// Not started on purpose: background sync races with the synchronous cache writes below.
	// Same rationale as TestSelectTopRankedEndpoint_SpreadsOverflowAcrossBand.

	const serviceID = protocol.ServiceID("instr-top-ranked")
	rpcType := sharedtypes.RPCType_JSON_RPC

	// Distinct eTLD+1 per endpoint so each is its own operator bucket. A/B are in-band,
	// C is well below it — the "healthy but ineligible" case.
	a := protocol.EndpointAddr("supplierA-https://rm.alpha-op.com")
	b := protocol.EndpointAddr("supplierB-https://rm.beta-op.com")
	c := protocol.EndpointAddr("supplierC-https://rm.gamma-op.com")

	kb := svc.KeyBuilderForService(serviceID)
	record := func(ep protocol.EndpointAddr, successes int) {
		k := kb.BuildKey(serviceID, ep, rpcType)
		for i := 0; i < successes; i++ {
			require.NoError(t, svc.RecordSignal(ctx, k, reputation.NewSuccessSignal(0)))
		}
	}
	record(a, 20) // -> 100 (top)
	record(b, 19) // ->  99 (within epsilon 2.0)
	record(c, 10) // ->  90 (outside the band)

	rc := &requestContext{
		context:   ctx,
		serviceID: serviceID,
		logger:    polyzero.NewLogger(),
		protocol:  &mockProtocolForRetry{reputationSvc: svc},
	}

	beforeCandA := topRankedCandidate(t, string(serviceID), "alpha-op.com")
	beforeCandB := topRankedCandidate(t, string(serviceID), "beta-op.com")
	beforeCandC := topRankedCandidate(t, string(serviceID), "gamma-op.com")
	beforeExclC := bandExcluded(t, string(serviceID), "gamma-op.com")

	endpoints := protocol.EndpointAddrList{a, b, c}
	const runs = 50
	for i := 0; i < runs; i++ {
		require.NotEmpty(t, rc.selectTopRankedEndpoint(endpoints, rpcType, metrics.CapPathRetry))
	}

	// In-band operators are candidates on every selection: they could actually win.
	require.Equal(t, float64(runs), topRankedCandidate(t, string(serviceID), "alpha-op.com")-beforeCandA,
		"in-band operator alpha must be counted as a candidate every selection")
	require.Equal(t, float64(runs), topRankedCandidate(t, string(serviceID), "beta-op.com")-beforeCandB,
		"in-band operator beta must be counted as a candidate every selection")

	// The below-band operator must NOT dilute the win-rate denominator — it had zero chance.
	require.Equal(t, float64(0), topRankedCandidate(t, string(serviceID), "gamma-op.com")-beforeCandC,
		"below-band operator must not be counted as an eligible candidate")
	require.Equal(t, float64(runs), bandExcluded(t, string(serviceID), "gamma-op.com")-beforeExclC,
		"below-band operator must be counted as excluded, so its zero traffic is explainable")

	// Wins are attributed and sum to the number of selections.
	wins := topRankedSelected(t, string(serviceID), "alpha-op.com") +
		topRankedSelected(t, string(serviceID), "beta-op.com")
	require.Equal(t, float64(runs), wins, "every selection must be attributed to a winner")
}

// A single-endpoint pool is a concentration cause in its own right: the selector cannot
// spread what it was not given. It must still be recorded, or "pool already collapsed" is
// indistinguishable from "selector keeps picking the same operator".
func TestTopRankedInstrumentation_RecordsSingleEndpointPool(t *testing.T) {
	const serviceID = protocol.ServiceID("instr-top-ranked-solo")

	rc := &requestContext{
		context:   context.Background(),
		serviceID: serviceID,
		logger:    polyzero.NewLogger(),
		protocol:  &mockProtocolForRetry{},
	}

	ep := protocol.EndpointAddr("supplierZ-https://rm.solo-op.com")
	before := topRankedSelected(t, string(serviceID), "solo-op.com")

	const runs = 8
	for i := 0; i < runs; i++ {
		require.Equal(t, ep, rc.selectTopRankedEndpoint(protocol.EndpointAddrList{ep}, sharedtypes.RPCType_JSON_RPC, metrics.CapPathRetry))
	}

	require.Equal(t, float64(runs), topRankedSelected(t, string(serviceID), "solo-op.com")-before,
		"single-endpoint pool must still be recorded")
}
