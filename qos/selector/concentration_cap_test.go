package selector

import (
	"fmt"
	"hash/fnv"
	"os"
	"strings"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// makeOperatorPool builds an endpoint list where operator i (eTLD+1 opN.example) owns
// keyCounts[i] distinct endpoints. Endpoint addresses use the production
// "supplier-https://host" format so shannonmetrics.GetEndpointTLDs resolves the eTLD+1.
func makeOperatorPool(keyCounts []int) (protocol.EndpointAddrList, []string) {
	var eps protocol.EndpointAddrList
	operators := make([]string, len(keyCounts))
	for i, count := range keyCounts {
		// Use a distinct registrable domain per operator: op0.tech, op1.tech, ...
		domain := fmt.Sprintf("op%d.tech", i)
		operators[i] = domain
		for k := 0; k < count; k++ {
			eps = append(eps, protocol.EndpointAddr(
				fmt.Sprintf("pokt1op%dkey%d-https://node%d.%s", i, k, k, domain),
			))
		}
	}
	return eps, operators
}

// operatorShares runs the selector N times and returns each operator's observed
// selection share, keyed by the operator's eTLD+1.
func operatorShares(
	t *testing.T,
	eps protocol.EndpointAddrList,
	maxShare float64,
	draws int,
) map[string]float64 {
	t.Helper()
	logger := polyzero.NewLogger(polyzero.WithOutput(os.Stderr))
	counts := map[string]int{}
	for i := 0; i < draws; i++ {
		sel := SelectWithConcentrationCap(logger, eps, maxShare)
		require.NotEmpty(t, sel, "selector returned empty endpoint")
		require.Contains(t, eps, sel, "selector returned an endpoint not in the pool")
		counts[operatorOf(string(sel))]++
	}
	shares := map[string]float64{}
	for op, c := range counts {
		shares[op] = float64(c) / float64(draws)
	}
	return shares
}

// operatorOf extracts the "opN.tech" eTLD+1 from a test endpoint address of the form
// "pokt1opNkeyK-https://nodeK.opN.tech" — the last two dot-separated segments.
func operatorOf(addr string) string {
	segments := strings.Split(addr, ".")
	if len(segments) < 2 {
		return addr
	}
	return segments[len(segments)-2] + "." + segments[len(segments)-1]
}

func TestSelectWithConcentrationCap_CapsDominantOperator(t *testing.T) {
	// One 10-key operator + three 1-key operators. Flat random would give the big
	// operator 10/13 ≈ 0.77. Cap at 0.4 must pull it down to ~0.40; the three
	// singletons absorb the redistributed remainder (~0.20 each).
	eps, ops := makeOperatorPool([]int{10, 1, 1, 1})
	shares := operatorShares(t, eps, 0.4, 200_000)

	big := ops[0]
	require.InDelta(t, 0.40, shares[big], 0.02,
		"dominant operator should be capped near 0.40, got %.3f", shares[big])
	for _, op := range ops[1:] {
		require.InDelta(t, 0.20, shares[op], 0.02,
			"singleton operator %s should absorb remainder near 0.20, got %.3f", op, shares[op])
	}
}

func TestSelectWithConcentrationCap_NoOpBelowCap(t *testing.T) {
	// Operators 2/2/1/1 keys → max operator share 2/6 ≈ 0.33, below the 0.5 cap.
	// Distribution must stay statistically identical to a flat random pick: each
	// endpoint ≈ 1/6, so each operator's share ≈ keys/6.
	eps, ops := makeOperatorPool([]int{2, 2, 1, 1})
	shares := operatorShares(t, eps, 0.5, 200_000)

	expected := map[string]float64{ops[0]: 2.0 / 6, ops[1]: 2.0 / 6, ops[2]: 1.0 / 6, ops[3]: 1.0 / 6}
	for op, exp := range expected {
		require.InDelta(t, exp, shares[op], 0.02,
			"below-cap distribution should match flat random for %s: want %.3f got %.3f", op, exp, shares[op])
	}
}

func TestSelectWithConcentrationCap_Disabled(t *testing.T) {
	// maxShare <= 0 and >= 1 disable the cap → flat random over endpoints (key-weighted).
	// A 3-key operator + 1-key operator → big operator ≈ 0.75.
	eps, ops := makeOperatorPool([]int{3, 1})
	for _, disabled := range []float64{0, 1.0, -0.5, 1.5} {
		shares := operatorShares(t, eps, disabled, 100_000)
		require.InDelta(t, 0.75, shares[ops[0]], 0.02,
			"disabled cap (%.1f) should be flat/key-weighted, got %.3f", disabled, shares[ops[0]])
	}
}

func TestSelectWithConcentrationCap_FeasibilityFloor(t *testing.T) {
	// 2 operators, cap 0.4 < 1/M (0.5) → infeasible. Result must be uniform-over-
	// operators (~0.5/0.5), NOT the key-weighted 0.75/0.25. No panic, no hang.
	eps, ops := makeOperatorPool([]int{3, 1})
	shares := operatorShares(t, eps, 0.4, 200_000)
	require.InDelta(t, 0.5, shares[ops[0]], 0.02, "infeasible cap → uniform over operators")
	require.InDelta(t, 0.5, shares[ops[1]], 0.02, "infeasible cap → uniform over operators")
}

func TestSelectWithConcentrationCap_SingleAndEmpty(t *testing.T) {
	logger := polyzero.NewLogger(polyzero.WithOutput(os.Stderr))

	// Empty input → empty result, no panic.
	require.Empty(t, SelectWithConcentrationCap(logger, protocol.EndpointAddrList{}, 0.4))

	// Single endpoint → that endpoint, cap irrelevant.
	one := protocol.EndpointAddrList{"pokt1solo-https://node.solo.tech"}
	require.Equal(t, one[0], SelectWithConcentrationCap(logger, one, 0.4))

	// Single operator with many keys → always returns one of its keys.
	eps, _ := makeOperatorPool([]int{5})
	sel := SelectWithConcentrationCap(logger, eps, 0.4)
	require.Contains(t, eps, sel)
}

func TestSelectWithConcentrationCap_UnresolvableStaySeparate(t *testing.T) {
	// Two endpoints with unresolvable eTLD+1 must be treated as two singleton operators,
	// not merged into one bucket (which would fabricate concentration). With a 0.6 cap
	// and two singletons, each should get ~0.5 — not one of them dominating.
	logger := polyzero.NewLogger(polyzero.WithOutput(os.Stderr))
	eps := protocol.EndpointAddrList{"garbage-addr-one", "garbage-addr-two"}
	counts := map[string]int{}
	for i := 0; i < 50_000; i++ {
		counts[string(SelectWithConcentrationCap(logger, eps, 0.6))]++
	}
	require.InDelta(t, 0.5, float64(counts["garbage-addr-one"])/50_000, 0.03,
		"unresolvable endpoints must not be merged")
}

func TestSelectWithConcentrationCapSeeded_Deterministic(t *testing.T) {
	eps, _ := makeOperatorPool([]int{10, 1, 1, 1})

	// Same (endpoints, cap, seed) → same endpoint, every time.
	for _, seed := range []int64{0, 1, 7, 42, 1 << 40} {
		first := SelectWithConcentrationCapSeeded(eps, 0.4, seed)
		for i := 0; i < 50; i++ {
			require.Equal(t, first, SelectWithConcentrationCapSeeded(eps, 0.4, seed),
				"seeded selection must be deterministic for seed %d", seed)
		}
	}
}

func TestSelectWithConcentrationCapSeeded_SpreadRespectsCap(t *testing.T) {
	// Across many distinct seeds the seeded variant must obey the same cap as the random
	// one: the 10-key operator converges near 0.40, not its 0.77 key-share. Seeds are
	// hashed (mirroring the real per-connection seedAddr) to avoid sequential-seed bias.
	eps, ops := makeOperatorPool([]int{10, 1, 1, 1})
	counts := map[string]int{}
	const draws = 200_000
	for i := 0; i < draws; i++ {
		h := fnv.New64a()
		_, _ = fmt.Fprintf(h, "origin-%d", i)
		sel := SelectWithConcentrationCapSeeded(eps, 0.4, int64(h.Sum64()))
		counts[operatorOf(string(sel))]++
	}
	require.InDelta(t, 0.40, float64(counts[ops[0]])/float64(draws), 0.02,
		"seeded spread should cap the dominant operator near 0.40")
}

func TestWaterFillToCap_PreservesMassAndCaps(t *testing.T) {
	// Direct unit test of the water-filling core: mass is preserved and no weight
	// exceeds the cap when feasible.
	weights := []float64{0.7, 0.1, 0.1, 0.1} // one operator over a 0.4 cap
	waterFillToCap(weights, 0.4)

	var total float64
	for _, w := range weights {
		require.LessOrEqual(t, w, 0.4+concentrationCapEpsilon, "no weight may exceed the cap")
		total += w
	}
	require.InDelta(t, 1.0, total, 1e-9, "water-filling must preserve total mass")
	require.InDelta(t, 0.4, weights[0], 1e-9, "over-cap weight clamped to cap")
}
