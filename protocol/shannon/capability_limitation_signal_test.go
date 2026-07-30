package shannon

import (
	"fmt"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/assert"

	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/reputation"
)

// A supplier running a pruned (non-archival) node that correctly reports it cannot serve a
// historical request is NOT at fault: lacking history is a capability limitation. The
// protocol-layer classifier must therefore record NO reputation penalty.
//
// This was a measured leak: the gateway exempts these (recordHeuristicErrorToReputation,
// shouldCircuitBreak) but the protocol layer detects FIRST, flattens the structured
// AnalysisResult into an error string, and returns an error rather than a 200 body — so the
// gateway's exemption never ran and the protocol's own MINOR (-3) penalty landed on every
// historical request, tens of thousands of signals per service against a single operator.
//
// The signal must also carry ZERO latency: a capability rejection is answered without
// touching state, so it is one of the fastest responses an endpoint can produce. Crediting
// that latency earns a fast-response bonus and feeds the fake number into the endpoint's
// latency EWMA — an endpoint serving nothing would out-rank one doing real work.
func TestClassifyErrorAsSignal_CapabilityLimitation_NoPenaltyNoLatencyCredit(t *testing.T) {
	logger := polyzero.NewLogger()
	// A real relay latency, which must NOT be credited to the endpoint.
	latency := 12 * time.Millisecond

	cases := []struct {
		name   string
		reason string
		method string
		body   string
	}{
		{
			name:   "geth missing trie node",
			reason: "error_indicator_blockchain_error",
			method: "eth_getBalance",
			body:   `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"missing trie node 0x4a2b (path ) state 0x4a2b is not available"}}`,
		},
		{
			name:   "geth pruned state",
			reason: "error_indicator_blockchain_error",
			method: "eth_getBalance",
			body:   `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"state at block 15537393 is pruned, useful ranges are [22000000, 22100000]"}}`,
		},
		{
			name:   "erigon historical state",
			reason: "error_indicator_blockchain_error",
			method: "eth_call",
			body:   `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"historical state is not available in this node"}}`,
		},
		{
			name:   "bsc not fully indexed",
			reason: "error_indicator_blockchain_error",
			method: "eth_getLogs",
			body:   `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"required historical state and blocks haven't been fully indexed yet"}}`,
		},
		{
			// CometBFT's literal message. The height number sits INSIDE the phrase
			// ("height 100 is not available"), so the pre-existing
			// "height is not available" substring never matched it and these were
			// scored as faults.
			name:   "cometbft pruned height",
			reason: "error_without_jsonrpc_version",
			method: "",
			body:   `{"error":{"code":-32603,"message":"height 100 is not available, lowest height is 21500000"}}`,
		},
		{
			name:   "cosmos pruned block",
			reason: "error_without_jsonrpc_version",
			method: "",
			body:   `{"error":{"code":-32603,"message":"failed to load block: block has been pruned"}}`,
		},
		{
			name:   "tron lite fullnode",
			reason: "non_json_capability_limitation",
			method: "/wallet/getblockbynum",
			body:   `this node is a lite fullnode and does not support this api`,
		},
	}

	for _, c := range cases {
		t.Run(c.name+" via heuristic-detected path", func(t *testing.T) {
			// Exactly the error shape sendRelay builds when the heuristic fires.
			err := fmt.Errorf("raw_payload: %s: heuristic detected %s (method=%s): %w",
				c.body, c.reason, c.method, errHeuristicDetectedBackendError)

			errType, signal := classifyErrorAsSignal(logger, err, latency)

			assert.Equal(t, protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNSPECIFIED, errType,
				"capability limitation must classify as UNSPECIFIED (no fault), not as a blockchain error")
			assert.Equal(t, reputation.SignalTypeSuccess, signal.Type)
			assert.GreaterOrEqual(t, signal.GetDefaultImpact(), float64(0),
				"capability limitation must not have negative reputation impact (was -3 MinorError)")
			assert.Zero(t, signal.Latency,
				"capability limitation must carry no latency credit — a fast rejection is not fast service")
		})

		t.Run(c.name+" via malformed payload path", func(t *testing.T) {
			// Same body surfacing through the malformed-payload error instead.
			err := fmt.Errorf("raw_payload: %s: %w", c.body, errMalformedEndpointPayload)

			errType, signal := classifyErrorAsSignal(logger, err, latency)

			assert.Equal(t, protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNSPECIFIED, errType)
			assert.Equal(t, reputation.SignalTypeSuccess, signal.Type)
			assert.GreaterOrEqual(t, signal.GetDefaultImpact(), float64(0))
			assert.Zero(t, signal.Latency)
		})
	}
}

// Regression guard for the exemption's blast radius: blockchain errors that are NOT
// capability limitations are real supplier faults (a corrupt database, a node that has
// fallen behind or reports itself unhealthy) and MUST still be penalized. The exemption
// keys off archival/capability patterns only — it must not blanket-exempt the
// error_indicator_blockchain_error category it sits in front of.
func TestClassifyErrorAsSignal_NonCapabilityBlockchainError_StillPenalized(t *testing.T) {
	logger := polyzero.NewLogger()
	latency := 12 * time.Millisecond

	bodies := []string{
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"mdbx_panic: fatal database error"}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"node is behind by 4213 slots"}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"node is unhealthy"}}`,
	}
	for _, body := range bodies {
		t.Run(body, func(t *testing.T) {
			err := fmt.Errorf("raw_payload: %s: heuristic detected error_indicator_blockchain_error (method=eth_blockNumber): %w",
				body, errHeuristicDetectedBackendError)

			errType, signal := classifyErrorAsSignal(logger, err, latency)

			assert.Equal(t,
				protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_BACKEND_SERVICE,
				errType,
				"non-capability blockchain errors must remain faults (regression guard)")
			assert.Less(t, signal.GetDefaultImpact(), float64(0),
				"non-capability blockchain errors must still penalize reputation")
		})
	}
}
