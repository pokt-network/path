package heuristic

import (
	"fmt"
	"testing"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/assert"
)

// Regression tests for the 2026-08-19 solana incident.
//
// getProgramAccounts returns a top-level array and legitimately returns [] whenever
// its filters match nothing — routine with dataSize/memcmp filters. It was missing
// from emptyArrayValidMethods, so every correct empty response was classified as a
// broken supplier, retried twice, and returned to the client as a 500.
//
// The tell in production was that all five solana operators failed at once at nearly
// identical rates (72-81%) on both gateway builds, while health checks — which never
// call getProgramAccounts — kept succeeding. Independent operators do not fail
// identically at the same second; the common factor was this classifier.
//
// These assert through Analyze, the function the four production call sites use
// (protocol/shannon/context.go, gateway/hedge.go, gateway/health_check_executor.go,
// gateway/http_request_context_handle_request.go), not through ProtocolAnalysis.
func TestAnalyze_SolanaArrayReturningMethods_EmptyArrayIsSuccess(t *testing.T) {
	// Captured verbatim from a mainnet gateway error log.
	emptyArray := []byte(`{"jsonrpc":"2.0","result":[],"id":"a50875a8-38d3-467a-8758-072f0271b49e"}`)

	methods := []struct {
		method string
		why    string
	}{
		{"getProgramAccounts", "array of {pubkey, account}; [] when the filters match nothing"},
		{"getInflationReward", "array of reward objects, entries may be null"},
		{"getSlotLeaders", "array of validator pubkeys"},
		{"getConfirmedBlocksWithLimit", "deprecated alias of getBlocksWithLimit, array of slots"},
	}

	for _, tc := range methods {
		t.Run(tc.method, func(t *testing.T) {
			result := Analyze(emptyArray, 200, sharedtypes.RPCType_JSON_RPC, tc.method)

			assert.False(t, result.ShouldRetry,
				"%s returns %s — an empty array is a valid success, not a supplier fault", tc.method, tc.why)
			assert.Equal(t, "jsonrpc_success", result.Reason)
		})
	}
}

// TestAnalyze_EmptyArrayStillFlaggedForNonArrayMethods proves the fix did not blanket
// disable the detection. These methods never return a top-level array, so "result":[]
// from them really is a broken or canned response and must still be retried.
//
// getMultipleAccounts and getTokenAccountsByOwner are here deliberately: both appeared
// in the production logs alongside getProgramAccounts, but they wrap their array in
// {context, value}, so a bare "result":[] from them is genuinely malformed. Adding them
// to the allowlist would have blinded a true detection.
func TestAnalyze_EmptyArrayStillFlaggedForNonArrayMethods(t *testing.T) {
	emptyArray := []byte(`{"jsonrpc":"2.0","result":[],"id":1}`)

	for _, method := range []string{
		"getSlot",
		"getBalance",
		"getLatestBlockhash",
		"getMultipleAccounts",
		"getTokenAccountsByOwner",
		"eth_blockNumber",
		"eth_getBalance",
	} {
		t.Run(method, func(t *testing.T) {
			result := Analyze(emptyArray, 200, sharedtypes.RPCType_JSON_RPC, method)

			assert.True(t, result.ShouldRetry,
				"%s never returns a top-level array — empty array must stay a detection", method)
			assert.Equal(t, "jsonrpc_invalid_empty_array", result.Reason)
		})
	}
}

// TestAnalyze_PopulatedResultsAreSuccess guards the ordinary path: a non-empty result
// was never affected by the bug, and must not be affected by the fix either.
func TestAnalyze_PopulatedResultsAreSuccess(t *testing.T) {
	cases := map[string][]byte{
		"getProgramAccounts": []byte(`{"jsonrpc":"2.0","result":[{"pubkey":"5tzF...","account":{"lamports":1}}],"id":1}`),
		"getSlot":            []byte(`{"jsonrpc":"2.0","result":361234567,"id":1}`),
	}

	for method, body := range cases {
		t.Run(method, func(t *testing.T) {
			result := Analyze(body, 200, sharedtypes.RPCType_JSON_RPC, method)

			assert.False(t, result.ShouldRetry, "populated %s result must not be flagged", method)
			assert.Equal(t, "jsonrpc_success", result.Reason)
		})
	}
}

// TestEmptyArrayValidMethods_CoversObservedSolanaTraffic pins the allowlist entries the
// incident added, so a future edit that drops one fails here with the reason attached
// rather than silently re-opening the outage.
func TestEmptyArrayValidMethods_CoversObservedSolanaTraffic(t *testing.T) {
	for _, method := range []string{
		"getProgramAccounts",
		"getInflationReward",
		"getSlotLeaders",
		"getConfirmedBlocksWithLimit",
	} {
		assert.True(t, emptyArrayValidMethods[method],
			fmt.Sprintf("%s returns a top-level array; removing it re-opens the 2026-08-19 solana incident", method))
	}
}
