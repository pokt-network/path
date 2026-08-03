package gateway

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/pokt-network/path/metrics"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/selector"
	"github.com/pokt-network/path/reputation"
	reputationstorage "github.com/pokt-network/path/reputation/storage"
)

func floatPtr(v float64) *float64 { return &v }

// bandCapMetric reads the retry/hedge band cap counter for one (service, path, outcome).
func bandCapMetric(t *testing.T, serviceID, capPath, outcome string) float64 {
	t.Helper()
	return testutil.ToFloat64(metrics.ConcentrationCapBandTotal.WithLabelValues(serviceID, capPath, outcome))
}

// capRC builds a requestContext whose service has the retry/hedge concentration cap configured
// as given. capEnabled == nil leaves the key unset, exercising the shipped default.
func capRC(serviceID protocol.ServiceID, capEnabled *bool, maxOperatorShare *float64, repSvc reputation.ReputationService) *requestContext {
	return &requestContext{
		context:   context.Background(),
		serviceID: serviceID,
		logger:    polyzero.NewLogger(),
		protocol: &mockProtocolForRetry{
			reputationSvc: repSvc,
			unifiedConfig: &UnifiedServicesConfig{
				Services: []ServiceConfig{{
					ID:                     serviceID,
					CapRetryHedgeSelection: capEnabled,
					MaxOperatorShare:       maxOperatorShare,
				}},
			},
		},
	}
}

// operatorBand builds a band where operator i owns counts[i] distinct backends, using the
// production "<supplier>-<url>" address format so the eTLD+1 resolves.
func operatorBand(counts []int) (protocol.EndpointAddrList, []string) {
	var band protocol.EndpointAddrList
	operators := make([]string, len(counts))
	for i, n := range counts {
		domain := fmt.Sprintf("op%d.example", i)
		operators[i] = domain
		for k := 0; k < n; k++ {
			band = append(band, protocol.EndpointAddr(fmt.Sprintf("pokt1op%dsup%d-https://node%d.%s", i, k, k, domain)))
		}
	}
	return band, operators
}

func bandOperatorShares(t *testing.T, rc *requestContext, band protocol.EndpointAddrList, capPath string, draws int) map[string]float64 {
	t.Helper()
	counts := map[string]int{}
	for i := 0; i < draws; i++ {
		sel := rc.pickFromBand(band, capPath)
		require.NotEmpty(t, sel, "a band pick must always resolve to an endpoint")
		require.Contains(t, band, sel, "a band pick must return a member of the band")
		counts[selector.OperatorKey(sel)]++
	}
	shares := make(map[string]float64, len(counts))
	for op, c := range counts {
		shares[op] = float64(c) / float64(draws)
	}
	return shares
}

// The behavior this change adds: with the gate on, one operator can no longer take its full
// backend-proportional share of the retry/hedge band.
func TestPickFromBand_CapAppliesWhenEnabled(t *testing.T) {
	c := require.New(t)
	const serviceID = protocol.ServiceID("cap-band-enabled")

	band, ops := operatorBand([]int{8, 1, 1})
	rc := capRC(serviceID, boolPtr(true), floatPtr(0.5), nil)

	before := bandCapMetric(t, string(serviceID), metrics.CapPathRetry, selector.BandCapReshaped.String())
	shares := bandOperatorShares(t, rc, band, metrics.CapPathRetry, 40_000)

	c.InDelta(0.5, shares[ops[0]], 0.02, "the dominant operator's retry share must be bounded by the cap")
	c.InDelta(0.25, shares[ops[1]], 0.02, "redistributed retry mass must reach the other operators")
	c.InDelta(0.25, shares[ops[2]], 0.02, "redistributed retry mass must reach the other operators")

	c.Equal(float64(40_000), bandCapMetric(t, string(serviceID), metrics.CapPathRetry, selector.BandCapReshaped.String())-before,
		"every reshaped retry pick must be attributable to the retry path")
}

// Requirement: the off-switch fully disables the behavior. Not "softens it" — the distribution
// must be the pre-change one, and the new counter must stay silent so the metric itself proves
// the gate is off.
func TestPickFromBand_OffSwitchFullyDisables(t *testing.T) {
	c := require.New(t)
	band, ops := operatorBand([]int{8, 1, 1})

	// Reference: the pre-change pick, uniform over distinct backend URLs.
	reference := map[string]int{}
	for i := 0; i < 40_000; i++ {
		reference[selector.OperatorKey(selector.PickBackendUniform(band))]++
	}

	cases := []struct {
		name       string
		serviceID  protocol.ServiceID
		capEnabled *bool
	}{
		{"explicitly disabled", "cap-band-off", boolPtr(false)},
		// NOTE: "unset" is deliberately absent. The shipped default is ON, so an unset service
		// is covered by TestGetCapRetryHedgeSelectionForService and by the reshape tests above —
		// it is no longer a way to reach the disabled path.
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rc := capRC(tc.serviceID, tc.capEnabled, floatPtr(0.5), nil)
			shares := bandOperatorShares(t, rc, band, metrics.CapPathRetry, 40_000)

			for _, op := range ops {
				c.InDelta(float64(reference[op])/40_000, shares[op], 0.02,
					"disabled cap must reproduce the pre-change distribution for %s", op)
			}
			// No outcome label may be touched at all: with the gate off the code must not even
			// consult the cap.
			for _, outcome := range []selector.BandCapOutcome{
				selector.BandCapReshaped, selector.BandCapNoOp, selector.BandCapNoRoom, selector.BandCapDisabled,
			} {
				c.Zero(bandCapMetric(t, string(tc.serviceID), metrics.CapPathRetry, outcome.String()),
					"a disabled cap must record nothing for outcome %s", outcome)
			}
		})
	}
}

// The documented degradation: the band has collapsed to one operator, so honoring the cap would
// mean excluding every candidate. The pick is left uncapped and the degradation is counted.
// A retry needs somewhere to go more than it needs the cap honored.
func TestPickFromBand_SingleOperatorBandDegradesUncapped(t *testing.T) {
	c := require.New(t)
	const serviceID = protocol.ServiceID("cap-band-no-room")

	band, ops := operatorBand([]int{4})
	rc := capRC(serviceID, boolPtr(true), floatPtr(0.5), nil)

	before := bandCapMetric(t, string(serviceID), metrics.CapPathRetry, selector.BandCapNoRoom.String())
	shares := bandOperatorShares(t, rc, band, metrics.CapPathRetry, 2_000)

	c.InDelta(1.0, shares[ops[0]], 0.0001, "the only operator available must still receive every pick")
	c.Equal(float64(2_000), bandCapMetric(t, string(serviceID), metrics.CapPathRetry, selector.BandCapNoRoom.String())-before,
		"the degraded case must be counted, not silent")
}

// Requirement: do not regress the retry path's success rate. The cap reweights the band and
// never filters it, so the set of endpoints a retry can reach is bit-for-bit the set it could
// reach before — whatever the cap value, and whatever the band's shape. This is the structural
// guarantee that the change cannot narrow an already-marginal retry pool.
func TestPickFromBand_NeverNarrowsTheRetryCandidateSet(t *testing.T) {
	c := require.New(t)
	const serviceID = protocol.ServiceID("cap-band-no-starve")

	shapes := [][]int{{1}, {6}, {3, 1}, {9, 1}, {1, 1}, {20, 1, 1}}
	for _, shape := range shapes {
		band, _ := operatorBand(shape)
		for _, maxShare := range []float64{0.1, 0.3, 0.5, 0.65, 0.9} {
			rc := capRC(serviceID, boolPtr(true), floatPtr(maxShare), nil)
			reachable := map[protocol.EndpointAddr]bool{}
			for i := 0; i < 400*len(band); i++ {
				sel := rc.pickFromBand(band, metrics.CapPathRetry)
				c.NotEmpty(sel, "shape %v at cap %v starved the retry path", shape, maxShare)
				reachable[sel] = true
			}
			c.Len(reachable, len(band),
				"shape %v at cap %v must leave every band member reachable", shape, maxShare)
		}
	}
}

// The other half of the documented empty-candidate answer: if the band itself arrives empty the
// cap reports no_candidates and selects nothing, and the CALLER degrades to an uncapped pick from
// the unfiltered pool (selectTopRankedEndpoint's endpoints[0] fallback, which warns). The cap
// never invents a candidate and never swallows the emptiness silently.
func TestPickFromBand_EmptyBandReportsNoCandidatesAndCallerDegrades(t *testing.T) {
	c := require.New(t)
	const serviceID = protocol.ServiceID("cap-band-empty")

	rc := capRC(serviceID, boolPtr(true), floatPtr(0.5), nil)

	before := bandCapMetric(t, string(serviceID), metrics.CapPathRetry, selector.BandCapNoCandidates.String())
	c.Empty(rc.pickFromBand(protocol.EndpointAddrList{}, metrics.CapPathRetry),
		"an empty band must not produce an endpoint")
	c.Equal(float64(1), bandCapMetric(t, string(serviceID), metrics.CapPathRetry, selector.BandCapNoCandidates.String())-before,
		"an empty band must be counted, so the degradation is visible rather than inferred")

	// The caller's contract: a non-empty pool always yields a candidate, whatever the band does.
	// Here the reputation service is nil, so ranking is skipped and the uncapped fallback runs.
	pool, _ := operatorBand([]int{2, 1})
	c.Contains(pool, rc.selectTopRankedEndpoint(pool, sharedtypes.RPCType_JSON_RPC, metrics.CapPathRetry),
		"a non-empty pool must never come back empty because of the cap")
}

// Wiring test: the cap must actually be reached from the RETRY path and from the HEDGE path,
// with each attributable separately. Their production volumes differ by an order of magnitude
// (~209/s retries against ~25/s winning hedges), so a shared label would make the change
// unmeasurable.
func TestSelectTopRankedEndpoint_CapReachesRetryAndHedgePaths(t *testing.T) {
	c := require.New(t)
	ctx := context.Background()
	const serviceID = protocol.ServiceID("cap-band-paths")
	rpcType := sharedtypes.RPCType_JSON_RPC

	config := reputation.Config{
		Enabled:         true,
		InitialScore:    80,
		MinThreshold:    30,
		RecoveryTimeout: 5 * time.Minute,
		StorageType:     "memory",
	}
	config.HydrateDefaults()
	store := reputationstorage.NewMemoryStorage(config.RecoveryTimeout)
	repSvc := reputation.NewService(config, store)
	// Deliberately not started: the background sync races with the synchronous cache writes
	// below. Same rationale as TestSelectTopRankedEndpoint_SpreadsOverflowAcrossBand.

	// A dominant operator (4 backends) plus two solo operators, all scored equally so the whole
	// pool lands inside the top-score band, plus a separate operator to act as the hedge primary.
	band, ops := operatorBand([]int{4, 1, 1})
	primary := protocol.EndpointAddr("pokt1prim-https://node0.primary-op.example")

	kb := repSvc.KeyBuilderForService(serviceID)
	for _, ep := range append(protocol.EndpointAddrList{primary}, band...) {
		k := kb.BuildKey(serviceID, ep, rpcType)
		for i := 0; i < 20; i++ { // all -> MaxScore, so every candidate is in-band
			require.NoError(t, repSvc.RecordSignal(ctx, k, reputation.NewSuccessSignal(0)))
		}
	}

	rc := capRC(serviceID, boolPtr(true), floatPtr(0.5), repSvc)

	const draws = 20_000

	// RETRY path: selectTopRankedEndpoint called with the retry label.
	beforeRetry := bandCapMetric(t, string(serviceID), metrics.CapPathRetry, selector.BandCapReshaped.String())
	retryCounts := map[string]int{}
	for i := 0; i < draws; i++ {
		sel := rc.selectTopRankedEndpoint(band, rpcType, metrics.CapPathRetry)
		c.Contains(band, sel)
		retryCounts[selector.OperatorKey(sel)]++
	}
	c.InDelta(0.5, float64(retryCounts[ops[0]])/draws, 0.03,
		"the dominant operator's share of retries must be capped")
	c.Equal(float64(draws), bandCapMetric(t, string(serviceID), metrics.CapPathRetry, selector.BandCapReshaped.String())-beforeRetry,
		"retry-path reshapes must land on path=retry")

	// HEDGE path: through the hedge racer's own selection, which additionally excludes the
	// primary's operator. Proves the cap is reached by the real hedge code, not just by a
	// hand-passed label.
	beforeHedge := bandCapMetric(t, string(serviceID), metrics.CapPathHedge, selector.BandCapReshaped.String())
	racer := newHedgeRacer(rc, polyzero.NewLogger(), rpcType, 10*time.Millisecond, 0)
	hedgeCandidates := append(protocol.EndpointAddrList{primary}, band...)
	hedgeCounts := map[string]int{}
	for i := 0; i < draws; i++ {
		sel := racer.selectHedgeEndpoint(hedgeCandidates, primary)
		c.Contains(band, sel, "the hedge must never return the primary or its operator")
		hedgeCounts[selector.OperatorKey(sel)]++
	}
	c.InDelta(0.5, float64(hedgeCounts[ops[0]])/draws, 0.03,
		"the dominant operator's share of hedges must be capped")
	c.Equal(float64(draws), bandCapMetric(t, string(serviceID), metrics.CapPathHedge, selector.BandCapReshaped.String())-beforeHedge,
		"hedge-path reshapes must land on path=hedge, separable from retries")

	// And the primary path's own counter must be untouched by any of this: its existing
	// per-service rate has to stay a valid before/after baseline.
	// Checked on BOTH primary-path series — the counter is labelled by selector path so the
	// websocket pick and the HTTP serving pick stay separable; a band reshape must land on
	// neither.
	for _, path := range []string{metrics.SelectionPathDiversity, metrics.SelectionPathConcentrationCap} {
		c.Zero(testutil.ToFloat64(metrics.ConcentrationCapReshapedTotal.WithLabelValues(string(serviceID), path)),
			"band-path reshapes must not be charged to the primary path's counter (path=%s)", path)
	}
}

// Two-stage selection must survive the cap on the band paths too: the winner is always a
// concrete supplier registration, because a relay is signed against a supplier's session and
// each supplier carries its own per-session allowance.
func TestPickFromBand_ReturnsConcreteSupplier(t *testing.T) {
	c := require.New(t)
	const serviceID = protocol.ServiceID("cap-band-supplier")

	// One operator fronting a single backend URL behind three supplier registrations, plus a
	// solo operator. Deduped, the stacked backend counts once — but the pick must still name
	// one of its three suppliers, and over time all three.
	band := protocol.EndpointAddrList{
		"pokt1stack1-https://shared.stacked-op.example",
		"pokt1stack2-https://shared.stacked-op.example",
		"pokt1stack3-https://shared.stacked-op.example",
		"pokt1solo1-https://only.solo-op.example",
	}
	rc := capRC(serviceID, boolPtr(true), floatPtr(0.5), nil)

	seen := map[protocol.EndpointAddr]int{}
	for i := 0; i < 20_000; i++ {
		sel := rc.pickFromBand(band, metrics.CapPathRetry)
		c.Contains(band, sel, "%q must be a supplier registration from the band, not a bare URL", sel)
		seen[sel]++
	}
	c.Len(seen, len(band), "every supplier registration must remain reachable so allowance spreads across siblings")
}

// Config resolution, including the shipped default and the process-wide override.
func TestGetCapRetryHedgeSelectionForService(t *testing.T) {
	c := require.New(t)

	t.Run("shipped default is ON", func(t *testing.T) {
		// Ships live rather than opt-in: a switch that ships off never gets measured, and the
		// risk here is bounded by construction — the cap reweights the band and never filters
		// it, so a retry's reachable set is unchanged at any cap value. Reverting is
		// PATH_CAP_RETRY_HEDGE_SELECTION=false, a pod restart rather than a deploy.
		c.True(DefaultCapRetryHedgeSelection, "the band cap ships live so it can be measured")
		cfg := &UnifiedServicesConfig{Services: []ServiceConfig{{ID: "svc"}}}
		c.True(cfg.GetCapRetryHedgeSelectionForService("svc"))
		c.True((&UnifiedServicesConfig{}).GetCapRetryHedgeSelectionForService("unknown"))
	})

	t.Run("per-service false still opts out", func(t *testing.T) {
		cfg := &UnifiedServicesConfig{
			Services: []ServiceConfig{{ID: "svc", CapRetryHedgeSelection: boolPtr(false)}},
		}
		c.False(cfg.GetCapRetryHedgeSelectionForService("svc"))
	})

	t.Run("global default applies when per-service is unset", func(t *testing.T) {
		cfg := &UnifiedServicesConfig{
			Defaults: ServiceDefaults{CapRetryHedgeSelection: boolPtr(true)},
			Services: []ServiceConfig{{ID: "svc"}},
		}
		c.True(cfg.GetCapRetryHedgeSelectionForService("svc"))
	})

	t.Run("per-service overrides the global default", func(t *testing.T) {
		cfg := &UnifiedServicesConfig{
			Defaults: ServiceDefaults{CapRetryHedgeSelection: boolPtr(true)},
			Services: []ServiceConfig{{ID: "svc", CapRetryHedgeSelection: boolPtr(false)}},
		}
		c.False(cfg.GetCapRetryHedgeSelectionForService("svc"))
	})

	// The YAML key is what operators type and what config.schema.yaml validates. Asserted here
	// so a rename of the struct tag cannot silently make every config's setting a no-op.
	t.Run("resolves from the documented YAML key", func(t *testing.T) {
		var cfg UnifiedServicesConfig
		require.NoError(t, yaml.Unmarshal([]byte(`
defaults:
  cap_retry_hedge_selection: true
services:
  - id: on-by-default
  - id: off-explicitly
    cap_retry_hedge_selection: false
`), &cfg))
		c.True(cfg.GetCapRetryHedgeSelectionForService("on-by-default"))
		c.False(cfg.GetCapRetryHedgeSelectionForService("off-explicitly"))
	})

	t.Run("process override wins over config in both directions", func(t *testing.T) {
		t.Cleanup(func() { retryHedgeCapOverride.Store(0) })

		onByConfig := &UnifiedServicesConfig{Services: []ServiceConfig{{ID: "svc", CapRetryHedgeSelection: boolPtr(true)}}}
		offByConfig := &UnifiedServicesConfig{Services: []ServiceConfig{{ID: "svc", CapRetryHedgeSelection: boolPtr(false)}}}

		SetRetryHedgeCapOverride(false)
		c.False(onByConfig.GetCapRetryHedgeSelectionForService("svc"), "override off must beat config on")

		SetRetryHedgeCapOverride(true)
		c.True(offByConfig.GetCapRetryHedgeSelectionForService("svc"), "override on must beat config off")

		retryHedgeCapOverride.Store(0)
		c.False(offByConfig.GetCapRetryHedgeSelectionForService("svc"), "unset override must return control to config")
	})
}
