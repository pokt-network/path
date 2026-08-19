package heuristic

import (
	"testing"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"
)

// Geth's path-based state scheme (PBSS) reports unavailable historical state with
// wording none of the hash-based-scheme patterns match:
//
//	{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"metadata is not found, 12114132"}}
//
// Reported from production against eth_getBalance at block 0x13570a9 (20,279,465).
// Before this was recognised the analyzer classified it as jsonrpc_valid_error and
// returned ShouldRetry=false, so the request was never re-tried on an endpoint that
// actually retains the state and the client received the -32000 verbatim.
const pbssPrunedStateResponse = `{"jsonrpc":"2.0","id":338498041,"error":{"code":-32000,"message":"metadata is not found, 12114132"}}`

func Test_PBSSPrunedState_IsRetried(t *testing.T) {
	result := Analyze([]byte(pbssPrunedStateResponse), 200, sharedtypes.RPCType_JSON_RPC, "eth_getBalance")

	require.True(t, result.ShouldRetry,
		"PBSS pruned-state error must retry on a different endpoint; got reason %q", result.Reason)
	require.Equal(t, "metadata is not found", result.MatchedPattern)
}

func Test_PBSSPrunedState_IsCapabilityLimitation(t *testing.T) {
	result := Analyze([]byte(pbssPrunedStateResponse), 200, sharedtypes.RPCType_JSON_RPC, "eth_getBalance")

	// Retrying is only half the requirement. A node that honestly reports it does
	// not retain historical state is capability-limited, not broken: circuit-breaking
	// its whole domain for a capability mismatch is the death-spiral this guards.
	require.True(t, IsArchivalRelatedError(result.MatchedPattern),
		"pattern %q must be archival-related", result.MatchedPattern)
	require.True(t, IsCapabilityLimitationError(result.MatchedPattern),
		"pattern %q must be a capability limitation", result.MatchedPattern)
}

// The structured AnalysisResult is lost on the hedge_failed path, where only the
// error string survives; that fallback must recognise the wording too.
func Test_PBSSPrunedState_SubstringFallback(t *testing.T) {
	require.True(t, ErrorContainsArchivalPattern(
		`relay failed: {"code":-32000,"message":"Metadata is not found, 12114132"}`))
}
