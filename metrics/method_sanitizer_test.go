package metrics

import (
	"encoding/hex"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/qos/heuristic"
)

func TestSanitize_EmptyAndWhitespace(t *testing.T) {
	cases := []string{"", " ", "   ", "\t", "\n  \t"}
	for _, in := range cases {
		t.Run(fmt.Sprintf("input=%q", in), func(t *testing.T) {
			require.Equal(t, MethodUnknown, SanitizeMethodLabel(NetworkTypeEVM, in))
		})
	}
}

func TestSanitize_AllowlistHit_EVM(t *testing.T) {
	cases := []string{"eth_blockNumber", "eth_call", "eth_getLogs", "net_version", "web3_clientVersion", "debug_traceTransaction"}
	for _, m := range cases {
		t.Run(m, func(t *testing.T) {
			require.Equal(t, m, SanitizeMethodLabel(NetworkTypeEVM, m))
		})
	}
}

func TestSanitize_AllowlistHit_Solana(t *testing.T) {
	cases := []string{"getSlot", "getEpochInfo", "getTransaction", "sendTransaction"}
	for _, m := range cases {
		t.Run(m, func(t *testing.T) {
			require.Equal(t, m, SanitizeMethodLabel(NetworkTypeSolana, m))
		})
	}
}

func TestSanitize_AllowlistHit_CometBFT(t *testing.T) {
	cases := []string{"status", "block", "tx_search", "broadcast_tx_sync"}
	for _, m := range cases {
		t.Run(m, func(t *testing.T) {
			require.Equal(t, m, SanitizeMethodLabel(NetworkTypeCosmos, m))
		})
	}
}

func TestSanitize_AllowlistMiss_EVMPrefix(t *testing.T) {
	cases := []struct {
		in       string
		expected string
	}{
		{"eth_madeUpRandomXYZ", MethodOtherEVM},
		{"net_completelyFake", MethodOtherEVM},
		{"debug_garbageMethod", MethodOtherEVM},
		{"trace_unknown", MethodOtherEVM},
		{"erigon_notReal", MethodOtherEVM},
		{"admin_notListed", MethodOtherEVM},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			require.Equal(t, c.expected, SanitizeMethodLabel(NetworkTypeEVM, c.in))
		})
	}
}

func TestSanitize_AllowlistMiss_SolanaPrefix(t *testing.T) {
	cases := []string{"getFooBar", "doesNotExist", "fakeMethod"}
	for _, m := range cases {
		t.Run(m, func(t *testing.T) {
			require.Equal(t, MethodOtherSolana, SanitizeMethodLabel(NetworkTypeSolana, m))
		})
	}
}

func TestSanitize_AllowlistMiss_CosmosPrefix(t *testing.T) {
	require.Equal(t, MethodOtherCosmos, SanitizeMethodLabel(NetworkTypeCosmos, "made_up_cosmos_method"))
}

func TestSanitize_AllowlistMiss_NoPrefix(t *testing.T) {
	cases := []string{"garbage_attack_string", "totally_random", "x"}
	for _, m := range cases {
		t.Run(m, func(t *testing.T) {
			require.Equal(t, MethodOther, SanitizeMethodLabel(NetworkTypePassthrough, m))
		})
	}
}

func TestSanitize_RESTPath_DropQuery(t *testing.T) {
	require.Equal(t, "/status", SanitizeMethodLabel(NetworkTypeCosmos, "/status?blockchain=foo"))
	require.Equal(t, "/health", SanitizeMethodLabel(NetworkTypeCosmos, "/health?x=1&y=2"))
}

func TestSanitize_RESTPath_NumericSegment(t *testing.T) {
	in := "/cosmos/base/tendermint/v1beta1/blocks/12345"
	want := "/cosmos/base/tendermint/v1beta1/blocks/:var"
	require.Equal(t, want, SanitizeMethodLabel(NetworkTypeCosmos, in))
}

func TestSanitize_RESTPath_HexSegment(t *testing.T) {
	in := "/cosmos/tx/0xabcdef0123456789"
	want := "/cosmos/tx/:var"
	require.Equal(t, want, SanitizeMethodLabel(NetworkTypeCosmos, in))
}

func TestSanitize_RESTPath_Bech32Segment(t *testing.T) {
	cases := []string{
		"/cosmos/auth/v1beta1/accounts/cosmos1abcdef0123456789xyz",
		"/cosmos/staking/v1beta1/delegators/pokt1aaaaaabbbbbbccccccdddd",
		"/cosmos/bank/v1beta1/balances/osmo1qqqqqqqqqqqqqqqqqqqqq",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			out := SanitizeMethodLabel(NetworkTypeCosmos, in)
			require.True(t, strings.HasSuffix(out, ":var"), "expected bech32 segment collapsed; got %q", out)
		})
	}
}

func TestSanitize_RESTPath_LatestStaysVerbatim(t *testing.T) {
	in := "/cosmos/base/tendermint/v1beta1/blocks/latest"
	want := "/cosmos/base/tendermint/v1beta1/blocks/latest"
	require.Equal(t, want, SanitizeMethodLabel(NetworkTypeCosmos, in))
}

func TestSanitize_RESTPath_StaticSegmentsPreserved(t *testing.T) {
	// All segments static — output should equal input.
	in := "/cosmos/base/tendermint/v1beta1/blocks/latest"
	require.Equal(t, in, SanitizeMethodLabel(NetworkTypeCosmos, in))
}

func TestSanitize_LengthCap(t *testing.T) {
	// Long allowlist-miss method → bucketed to MethodOther which is short, cap not stressed.
	// Long REST path with all-static segments → could exceed cap. Force one.
	long := "/" + strings.Repeat("staticpath/", 20) // 220 chars, all static
	out := SanitizeMethodLabel(NetworkTypeCosmos, long)
	require.LessOrEqual(t, len(out), MethodLabelMaxLen, "output must not exceed MethodLabelMaxLen")

	// Long raw method that hits the bucket fallback (still capped).
	rawLong := "eth_" + strings.Repeat("abcdefghij", 20)
	out = SanitizeMethodLabel(NetworkTypeEVM, rawLong)
	require.LessOrEqual(t, len(out), MethodLabelMaxLen)
	require.Equal(t, MethodOtherEVM, out, "long unknown eth_* should bucket")
}

// TestSanitize_CardinalityRegression is the load-bearing test: 10K adversarial
// inputs across all networks, asserting the output set stays bounded. This is
// the regression guard for the actual production bug — a future change that
// accidentally re-leaks user input into the label will fail this test loudly.
func TestSanitize_CardinalityRegression(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	out := make(map[string]struct{}, 1024)

	networks := []string{NetworkTypeEVM, NetworkTypeCosmos, NetworkTypeSolana, NetworkTypePassthrough}

	for i := 0; i < 10_000; i++ {
		nt := networks[rng.Intn(len(networks))]
		raw := adversarialMethod(rng)
		out[SanitizeMethodLabel(nt, raw)] = struct{}{}
	}

	require.Lessf(t, len(out), 500, "cardinality exploded: %d unique outputs (limit 500)", len(out))
	t.Logf("10K adversarial inputs produced %d unique sanitized outputs", len(out))
}

// adversarialMethod produces a raw method-label string that mimics the kinds
// of input causing the production leak: mostly junk, occasionally real
// methods, occasionally REST paths with random IDs.
func adversarialMethod(rng *rand.Rand) string {
	switch rng.Intn(10) {
	case 0:
		return ""
	case 1:
		return "eth_blockNumber"
	case 2:
		return "getSlot"
	case 3, 4:
		// Random hex-ish method (simulates user injecting tx hash as method).
		buf := make([]byte, 32)
		_, _ = rng.Read(buf)
		return "0x" + hex.EncodeToString(buf)
	case 5:
		// REST path with random block height.
		return fmt.Sprintf("/cosmos/base/tendermint/v1beta1/blocks/%d", rng.Int63n(1_000_000_000))
	case 6:
		// REST path with bech32 address.
		buf := make([]byte, 16)
		_, _ = rng.Read(buf)
		return "/cosmos/auth/v1beta1/accounts/cosmos1" + hex.EncodeToString(buf)
	case 7:
		// REST path with arbitrary query string.
		return fmt.Sprintf("/status?height=%d&peer=%d", rng.Int63(), rng.Int63())
	case 8:
		// Random eth_* method name (simulates fuzzed JSON-RPC).
		buf := make([]byte, 12)
		_, _ = rng.Read(buf)
		return "eth_" + hex.EncodeToString(buf)
	default:
		// Total garbage with no recognizable shape.
		buf := make([]byte, 24)
		_, _ = rng.Read(buf)
		return hex.EncodeToString(buf)
	}
}

// TestCometBFTAllowlist_InSyncWithHeuristic guards against drift between the
// metrics package's CometBFT allowlist and the heuristic package's source map.
// The allowlist is derived from heuristic.CometBFTMethodList() at init time, so
// this test mostly documents the relationship — but it would catch any
// accidental override or duplicate definition.
func TestCometBFTAllowlist_InSyncWithHeuristic(t *testing.T) {
	heuristicMethods := heuristic.CometBFTMethodList()
	require.NotEmpty(t, heuristicMethods)
	require.Equal(t, len(heuristicMethods), len(cometBFTAllowlist),
		"cometBFTAllowlist size must match heuristic.CometBFTMethodList()")
	for _, m := range heuristicMethods {
		_, ok := cometBFTAllowlist[m]
		require.Truef(t, ok, "method %q present in heuristic but missing from cometBFTAllowlist", m)
	}
}
