package heuristic

import (
	"testing"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"
)

// Solana's -32010 is "account index unavailable for this key": the node was started
// without a secondary account index for this program (or with it excluded), so it cannot
// serve getProgramAccounts for it. Observed in production, HTTP 200, signature-valid:
//
//	{"jsonrpc":"2.0","error":{"code":-32010,"message":"<program> excluded from account secondary indexes; this RPC method unavailable for key"},"id":1}
//
// Node configuration, not a fault — another operator serves the same call from an index.
// Before this was recognised the analyzer classified it as a generic JSON-RPC error, the
// request was retried (correct) and the domain was charged a circuit-breaker failure and a
// reputation penalty (wrong) on every poll of a query that one dapp sends continuously.
const solanaAccountIndexExcludedResponse = `{"jsonrpc":"2.0","error":{"code":-32010,"message":"7rAgHPLDc9NryZmNdeEzyDui6D9PHkvTxMjKhNSa7w3a excluded from account secondary indexes; this RPC method unavailable for key"},"id":1}`

func Test_SolanaAccountIndexExcluded_IsRetried(t *testing.T) {
	result := Analyze([]byte(solanaAccountIndexExcludedResponse), 200, sharedtypes.RPCType_JSON_RPC, "getProgramAccounts")

	require.True(t, result.ShouldRetry,
		"an index-excluded key must retry on an endpoint that indexes it; got reason %q", result.Reason)
	require.Equal(t, "excluded from account secondary indexes", result.MatchedPattern)
}

func Test_SolanaAccountIndexExcluded_IsCapabilityLimitationNotArchival(t *testing.T) {
	result := Analyze([]byte(solanaAccountIndexExcludedResponse), 200, sharedtypes.RPCType_JSON_RPC, "getProgramAccounts")

	require.True(t, IsCapabilityLimitationError(result.MatchedPattern),
		"pattern %q must be a capability limitation: no circuit break, no reputation penalty", result.MatchedPattern)
	require.False(t, IsArchivalRelatedError(result.MatchedPattern),
		"an index exclusion is not an archival condition and must not route through the archival filters")
}

// The structured AnalysisResult is lost on the hedge_failed path, where only the error
// string survives; that fallback must recognise the wording too.
func Test_SolanaAccountIndexExcluded_SubstringFallback(t *testing.T) {
	require.True(t, ErrorContainsArchivalPattern(
		`relay failed: {"code":-32010,"message":"Tokenkeg excluded from account secondary indexes; this RPC method unavailable for key"}`))
}
