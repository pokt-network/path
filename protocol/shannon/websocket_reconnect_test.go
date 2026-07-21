package shannon

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// opOfAddr extracts the "opN.tech" eTLD+1 from a test endpoint address of the form
// "pokt1<op><k>-https://n<k>.<op>.tech" — the last two dot-separated segments.
func opOfAddr(addr string) string {
	segs := strings.Split(addr, ".")
	if len(segs) < 2 {
		return addr
	}
	return segs[len(segs)-2] + "." + segs[len(segs)-1]
}

// rcEndpoint wraps cfgEndpoint to make IsFallback configurable for tier-2 selection tests.
type rcEndpoint struct {
	*cfgEndpoint
	fallback bool
}

func (e *rcEndpoint) IsFallback() bool { return e.fallback }

func newRCEndpoint(addr string, fallback bool) *rcEndpoint {
	return &rcEndpoint{
		cfgEndpoint: &cfgEndpoint{addr: protocol.EndpointAddr(addr), supplier: addr, sessionID: "sess"},
		fallback:    fallback,
	}
}

// noScore is a scoreOf function that scores every endpoint equally, collapsing
// selectBestReconnectEndpoint's ordering to (non-fallback, then smallest address) — the
// behavior when reputation is unavailable. Used by tests that exercise that ordering.
func noScore(endpoint) float64 { return 0 }

// scoreByAddr builds a scoreOf function from an address→score map (missing → 0).
func scoreByAddr(scores map[string]float64) func(endpoint) float64 {
	return func(ep endpoint) float64 { return scores[string(ep.Addr())] }
}

// Test_selectBestReconnectEndpoint_PrefersNonFallback verifies that a protocol
// (non-fallback) endpoint is chosen over a fallback one for a tier-2 rebind.
func Test_selectBestReconnectEndpoint_PrefersNonFallback(t *testing.T) {
	c := require.New(t)

	fallback := newRCEndpoint("aaa-fallback", true)
	protocolEP := newRCEndpoint("zzz-protocol", false)

	endpoints := map[protocol.EndpointAddr]endpoint{
		fallback.Addr():   fallback,
		protocolEP.Addr(): protocolEP,
	}

	best := selectBestReconnectEndpoint(endpoints, noScore)
	c.Equal(protocolEP.Addr(), best.Addr(), "non-fallback endpoint must win even with a larger address")
}

// Test_selectBestReconnectEndpoint_DeterministicTiebreak verifies that among equally-ranked
// (non-fallback, equal-score) endpoints, selection is deterministic: the lexicographically
// smallest address wins, regardless of map iteration order.
func Test_selectBestReconnectEndpoint_DeterministicTiebreak(t *testing.T) {
	c := require.New(t)

	a := newRCEndpoint("aaa", false)
	b := newRCEndpoint("bbb", false)
	d := newRCEndpoint("ddd", false)

	endpoints := map[protocol.EndpointAddr]endpoint{
		d.Addr(): d,
		b.Addr(): b,
		a.Addr(): a,
	}

	// Run several times to guard against map-iteration-order flakiness.
	for i := 0; i < 20; i++ {
		best := selectBestReconnectEndpoint(endpoints, noScore)
		c.Equal(a.Addr(), best.Addr(), "smallest address must win the tiebreak deterministically")
	}
}

// Test_selectBestReconnectEndpoint_PrefersHigherScore verifies S2a: among non-fallback
// endpoints, the higher WEBSOCKET reputation score wins over the smaller address, so a
// rebind lands on the proven-healthier node rather than the alphabetically-first one.
func Test_selectBestReconnectEndpoint_PrefersHigherScore(t *testing.T) {
	c := require.New(t)

	// "aaa" would win the address tiebreak, but "zzz" has the higher score.
	low := newRCEndpoint("aaa", false)
	high := newRCEndpoint("zzz", false)

	endpoints := map[protocol.EndpointAddr]endpoint{
		low.Addr():  low,
		high.Addr(): high,
	}
	scoreOf := scoreByAddr(map[string]float64{"aaa": 40, "zzz": 95})

	for i := 0; i < 20; i++ {
		best := selectBestReconnectEndpoint(endpoints, scoreOf)
		c.Equal(high.Addr(), best.Addr(), "higher-scored endpoint must win over a smaller address")
	}
}

// Test_selectBestReconnectEndpoint_ScoreNeverBeatsFallbackPreference verifies that a high
// score cannot promote a fallback endpoint above a non-fallback one — fallback status stays
// the primary discriminator (fallbacks are last-resort infra).
func Test_selectBestReconnectEndpoint_ScoreNeverBeatsFallbackPreference(t *testing.T) {
	c := require.New(t)

	protoLowScore := newRCEndpoint("proto", false)
	fallbackHighScore := newRCEndpoint("fb", true)

	endpoints := map[protocol.EndpointAddr]endpoint{
		protoLowScore.Addr():     protoLowScore,
		fallbackHighScore.Addr(): fallbackHighScore,
	}
	// Fallback has the far better score, yet must still lose to the non-fallback endpoint.
	scoreOf := scoreByAddr(map[string]float64{"proto": 35, "fb": 100})

	best := selectBestReconnectEndpoint(endpoints, scoreOf)
	c.Equal(protoLowScore.Addr(), best.Addr(), "non-fallback must win regardless of score")
}

// Test_selectBestReconnectEndpoint_SingleAndEmpty covers the boundary cases.
func Test_selectBestReconnectEndpoint_SingleAndEmpty(t *testing.T) {
	c := require.New(t)

	only := newRCEndpoint("solo", false)
	c.Equal(only.Addr(), selectBestReconnectEndpoint(map[protocol.EndpointAddr]endpoint{only.Addr(): only}, noScore).Addr())

	c.Nil(selectBestReconnectEndpoint(map[protocol.EndpointAddr]endpoint{}, noScore), "empty set returns nil")
}

// buildOperatorEndpoints creates a reconnect-candidate map where operator opX.tech owns
// keyCounts[i] non-fallback endpoints, in the "supplier-https://host" address form so the
// concentration cap can resolve each endpoint's eTLD+1.
func buildOperatorEndpoints(keyCounts []int) (map[protocol.EndpointAddr]endpoint, []string) {
	endpoints := map[protocol.EndpointAddr]endpoint{}
	ops := make([]string, len(keyCounts))
	for i, count := range keyCounts {
		op := fmt.Sprintf("op%d", i)
		ops[i] = op + ".tech"
		for k := 0; k < count; k++ {
			e := newRCEndpoint(fmt.Sprintf("pokt1%s%d-https://n%d.%s.tech", op, k, k, op), false)
			endpoints[e.Addr()] = e
		}
	}
	return endpoints, ops
}

// Test_selectReconnectEndpointWithCap_DisabledMatchesDeterministic verifies that with the
// cap disabled the capped wrapper is identical to the deterministic pick.
func Test_selectReconnectEndpointWithCap_DisabledMatchesDeterministic(t *testing.T) {
	c := require.New(t)
	endpoints, _ := buildOperatorEndpoints([]int{10, 1, 1, 1})

	want := selectBestReconnectEndpoint(endpoints, noScore).Addr()
	for _, disabled := range []float64{0, 1.0, -1, 2} {
		got := selectReconnectEndpointWithCap(endpoints, noScore, disabled, "any-seed")
		c.Equal(want, got.Addr(), "disabled cap (%v) must match deterministic pick", disabled)
	}
}

// Test_selectReconnectEndpointWithCap_Deterministic verifies same-seed reproducibility.
func Test_selectReconnectEndpointWithCap_Deterministic(t *testing.T) {
	c := require.New(t)
	endpoints, _ := buildOperatorEndpoints([]int{10, 1, 1, 1})

	for _, seed := range []protocol.EndpointAddr{"origin-a", "origin-b", "pokt1zzz-https://x.op0.tech"} {
		first := selectReconnectEndpointWithCap(endpoints, noScore, 0.4, seed).Addr()
		for i := 0; i < 50; i++ {
			c.Equal(first, selectReconnectEndpointWithCap(endpoints, noScore, 0.4, seed).Addr(),
				"same seed must reproduce the same rebind target")
		}
	}
}

// Test_selectReconnectEndpointWithCap_SpreadsAcrossOperators verifies the cap spreads
// tied-best rebind targets across operators instead of funneling to one address: across
// many connection origins the 10-key operator's share converges near the 0.4 cap, not 0.77.
func Test_selectReconnectEndpointWithCap_SpreadsAcrossOperators(t *testing.T) {
	c := require.New(t)
	endpoints, ops := buildOperatorEndpoints([]int{10, 1, 1, 1})

	counts := map[string]int{}
	const draws = 20_000
	for i := 0; i < draws; i++ {
		seed := protocol.EndpointAddr(fmt.Sprintf("origin-%d", i))
		ep := selectReconnectEndpointWithCap(endpoints, noScore, 0.4, seed)
		counts[opOfAddr(string(ep.Addr()))]++
	}
	c.InDelta(0.40, float64(counts[ops[0]])/float64(draws), 0.03,
		"dominant operator's rebind share should be capped near 0.40")
}

// Test_selectReconnectEndpointWithCap_RespectsScoreBand verifies the cap only spreads
// among the top-score band — a lower-scored endpoint is never chosen as a rebind target.
func Test_selectReconnectEndpointWithCap_RespectsScoreBand(t *testing.T) {
	c := require.New(t)

	high1 := newRCEndpoint("pokt1a-https://n.alpha.tech", false)
	high2 := newRCEndpoint("pokt1b-https://n.bravo.tech", false)
	low := newRCEndpoint("pokt1c-https://n.charlie.tech", false)
	endpoints := map[protocol.EndpointAddr]endpoint{
		high1.Addr(): high1, high2.Addr(): high2, low.Addr(): low,
	}
	scoreOf := scoreByAddr(map[string]float64{
		string(high1.Addr()): 95, string(high2.Addr()): 95, string(low.Addr()): 40,
	})

	for i := 0; i < 500; i++ {
		ep := selectReconnectEndpointWithCap(endpoints, scoreOf, 0.6, protocol.EndpointAddr(fmt.Sprintf("o%d", i)))
		c.NotEqual(low.Addr(), ep.Addr(), "a below-band (lower-score) endpoint must never be a rebind target")
	}
}

// Test_betterReconnectCandidate verifies the ordering used by selectBestReconnectEndpoint:
// non-fallback > fallback (primary), then higher score, then smaller address.
func Test_betterReconnectCandidate(t *testing.T) {
	c := require.New(t)

	protoSmall := newRCEndpoint("aaa", false)
	protoLarge := newRCEndpoint("bbb", false)
	fallbackSmall := newRCEndpoint("aaa", true)

	// Non-fallback beats fallback regardless of address or score.
	c.True(betterReconnectCandidate(protoLarge, fallbackSmall, noScore), "non-fallback beats fallback")
	c.False(betterReconnectCandidate(fallbackSmall, protoLarge, noScore), "fallback never beats non-fallback")

	// Among non-fallback with equal score, smaller address wins.
	c.True(betterReconnectCandidate(protoSmall, protoLarge, noScore))
	c.False(betterReconnectCandidate(protoLarge, protoSmall, noScore))

	// Among non-fallback, higher score wins over smaller address.
	scoreOf := scoreByAddr(map[string]float64{"aaa": 50, "bbb": 90})
	c.True(betterReconnectCandidate(protoLarge, protoSmall, scoreOf), "higher score beats smaller address")
	c.False(betterReconnectCandidate(protoSmall, protoLarge, scoreOf), "lower score loses despite smaller address")
}
