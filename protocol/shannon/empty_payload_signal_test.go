package shannon

import (
	"fmt"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/reputation"
)

// Regression tests for the empty-payload severity gap found 2026-08-19.
//
// A supplier returning a signature-valid RelayResponse whose body is empty passed
// every check PATH performs — signature verification, ValidateBasic, and unmarshal.
// Only a content heuristic caught it, and that heuristic's signal was MINOR (-3),
// the same weight as a passing blockchain_error. One endpoint produced ~800 empty
// responses in a two-minute sample while holding a reputation score of 100: at a
// small fraction of total volume, successes outrun -3 indefinitely.
//
// An empty body on a body-bearing 2xx has no valid reading for any RPC type PATH
// forwards, and the relay is signed and settleable regardless of content — so it is
// a protocol violation and belongs at CRITICAL, alongside protocol_error.
//
// These assert through classifyErrorAsSignal, the function the relay path calls,
// rather than on the classifier branch directly.
func TestClassifyErrorAsSignal_EmptyResponseIsCritical(t *testing.T) {
	logger := polyzero.NewLogger()

	// Shaped exactly as the production error is built at context.go:843.
	err := fmt.Errorf("raw_payload: %s: heuristic detected %s (method=%s): %w",
		"", "empty_response", "getTokenAccountsByOwner", errHeuristicDetectedBackendError)

	_, signal := classifyErrorAsSignal(logger, err, 250*time.Millisecond)

	require.Equal(t, reputation.SignalTypeCriticalError, signal.Type,
		"an empty body on a body-bearing 2xx is a protocol violation, not a transient fault")
	require.Equal(t, "empty_response", signal.Reason)
}

// TestClassifyErrorAsSignal_SmallNoResultStaysMinor pins the deliberate split. A short
// response missing a "result" field is ambiguous — a truncated read or a terse upstream
// error — unlike a zero-length body, which has no valid reading. Raising both together
// would have been the easier edit and the wrong one.
func TestClassifyErrorAsSignal_SmallNoResultStaysMinor(t *testing.T) {
	logger := polyzero.NewLogger()

	err := fmt.Errorf("raw_payload: %s: heuristic detected %s (method=%s): %w",
		`{"jsonrpc":"2.0"}`, "small_no_result", "getSlot", errHeuristicDetectedBackendError)

	_, signal := classifyErrorAsSignal(logger, err, 250*time.Millisecond)

	require.Equal(t, reputation.SignalTypeMinorError, signal.Type,
		"small_no_result is ambiguous and must not inherit the empty-body severity")
	require.Equal(t, "small_no_result", signal.Reason)
}

// TestClassifyErrorAsSignal_ReasonSuffixDoesNotDefeatMatching is the pin for the root
// cause. context.go builds the error as "heuristic detected %s (method=%s)", so the
// reason reaches the classifier as "empty_response (method=getSlot)". Every
// exact-equality case in classifyHeuristicErrorAsSignal therefore never matched for a
// JSON-RPC request and fell through to unknown_payload_error at MINOR. Only the
// HasPrefix("error_indicator_...") cases survived, which is why the gap stayed
// invisible — the surviving cases covered the common errors.
//
// Asserted per-reason with and without the suffix: the two must agree.
func TestClassifyErrorAsSignal_ReasonSuffixDoesNotDefeatMatching(t *testing.T) {
	logger := polyzero.NewLogger()

	cases := []struct {
		reason     string
		wantType   reputation.SignalType
		wantReason string
	}{
		{"empty_response", reputation.SignalTypeCriticalError, "empty_response"},
		{"small_no_result", reputation.SignalTypeMinorError, "small_no_result"},
		{"html_error_page", reputation.SignalTypeCriticalError, "service_error"},
		{"bad_gateway", reputation.SignalTypeCriticalError, "service_error"},
	}

	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			withMethod := fmt.Errorf("raw_payload: %s: heuristic detected %s (method=%s): %w",
				"", tc.reason, "getSlot", errHeuristicDetectedBackendError)
			bare := fmt.Errorf("raw_payload: %s: heuristic detected %s: %w",
				"", tc.reason, errHeuristicDetectedBackendError)

			_, gotWith := classifyErrorAsSignal(logger, withMethod, 250*time.Millisecond)
			_, gotBare := classifyErrorAsSignal(logger, bare, 250*time.Millisecond)

			require.Equal(t, tc.wantType, gotWith.Type,
				"reason %q carrying a (method=...) suffix must classify the same as without it", tc.reason)
			require.Equal(t, tc.wantReason, gotWith.Reason)
			require.Equal(t, gotBare.Type, gotWith.Type, "suffix changed the signal type")
			require.Equal(t, gotBare.Reason, gotWith.Reason, "suffix changed the signal reason")
		})
	}
}
