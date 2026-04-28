package shannon

import (
	"fmt"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/assert"

	protocolobservations "github.com/pokt-network/path/observation/protocol"
)

func TestClassifyErrorAsSignal_Heuristic(t *testing.T) {
	logger := polyzero.NewLogger()
	latency := 100 * time.Millisecond

	tests := []struct {
		name                 string
		heuristicReason      string
		heuristicDetails     string
		body                 string
		expectedErrorType    protocolobservations.ShannonEndpointErrorType
		expectedSignalReason string
	}{
		{
			name:                 "Heuristic auth error",
			heuristicReason:      "error_indicator_auth_error",
			heuristicDetails:     "Detected error pattern: unauthorized",
			body:                 `{"error":"unauthorized"}`,
			expectedErrorType:    protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_4XX,
			expectedSignalReason: "auth_error",
		},
		{
			name:                 "Heuristic connection error",
			heuristicReason:      "error_indicator_connection_error",
			heuristicDetails:     "Detected error pattern: connection refused",
			body:                 `connection refused`,
			expectedErrorType:    protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_CONNECTION_REFUSED,
			expectedSignalReason: "connection_error",
		},
		{
			name:                 "Heuristic service error",
			heuristicReason:      "html_error_page",
			heuristicDetails:     "Detected HTML error page",
			body:                 `<html><body>Bad Gateway</body></html>`,
			expectedErrorType:    protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_BACKEND_SERVICE,
			expectedSignalReason: "service_error",
		},
		{
			name:                 "Heuristic protocol error",
			heuristicReason:      "jsonrpc_both_result_and_error",
			heuristicDetails:     "JSON-RPC response has both result and error (malformed)",
			body:                 `{"jsonrpc":"2.0","result":"ok","error":"fail"}`,
			expectedErrorType:    protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_PROTOCOL_WIRE_TYPE,
			expectedSignalReason: "protocol_error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Construct the error as it would be from context.go (heuristic-detected backend errors)
			err := fmt.Errorf("raw_payload: %s: heuristic detected %s: %w",
				tt.body, tt.heuristicReason, errHeuristicDetectedBackendError)

			errType, signal := classifyErrorAsSignal(logger, err, latency)

			assert.Equal(t, tt.expectedErrorType, errType)
			assert.Equal(t, tt.expectedSignalReason, signal.Reason)
		})
	}
}

func TestClassifyErrorAsSignal_MalformedPayload(t *testing.T) {
	logger := polyzero.NewLogger()
	latency := 100 * time.Millisecond

	// This simulates the error from context.go:744
	body := "connection refused"
	err := fmt.Errorf("raw_payload: %s: %w", body, errMalformedEndpointPayload)

	errType, signal := classifyErrorAsSignal(logger, err, latency)

	assert.Equal(t, protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_CONNECTION_REFUSED, errType)
	assert.Equal(t, "connection_error", signal.Reason)
}

// Over-servicing rejections must NOT be penalized. The supplier's relay-miner
// is correctly enforcing protocol rate limits; treating this as a fault was
// the bug the fix addresses. These tests guard the invariant by asserting
// SuccessSignal across the three distinct surfaces the rejection can take.
func TestClassifyErrorAsSignal_OverServiced_NoPenalty(t *testing.T) {
	logger := polyzero.NewLogger()
	latency := 100 * time.Millisecond

	cases := []struct {
		name string
		body string
	}{
		{
			name: "poktroll main error string in payload",
			body: "offchain rate limit hit by relayer proxy: foo",
		},
		{
			name: "HA relay-miner JSON body",
			body: `{"error":"session relay limit reached: claimable portion fully consumed"}`,
		},
		{
			name: "HA partial — claimable phrase only",
			body: "claimable portion fully consumed",
		},
	}
	for _, c := range cases {
		t.Run(c.name+" via malformed payload path", func(t *testing.T) {
			// Path: protocol returns errMalformedEndpointPayload wrapping the body.
			// classifyMalformedPayloadAsSignal must see the over-served text
			// and short-circuit to SuccessSignal before any other classification.
			err := fmt.Errorf("raw_payload: %s: %w", c.body, errMalformedEndpointPayload)

			errType, signal := classifyErrorAsSignal(logger, err, latency)

			assert.Equal(t, protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNSPECIFIED, errType,
				"over-serviced should classify as UNSPECIFIED (no-penalty), not as a rate-limit fault")
			// SuccessSignal has Reason="" (default-zero); penalty signals carry "rate_limit" etc.
			assert.NotEqual(t, "rate_limit", signal.Reason)
			assert.GreaterOrEqual(t, signal.GetDefaultImpact(), float64(0),
				"over-serviced must not have negative reputation impact (was -10 MajorError)")
		})

		t.Run(c.name+" via heuristic-detected path", func(t *testing.T) {
			// Path: protocol returns errHeuristicDetectedBackendError, classifier
			// runs through classifyHeuristicErrorAsSignal which dispatches the
			// over-served check inside the rate_limit branch.
			err := fmt.Errorf("raw_payload: %s: heuristic detected error_indicator_rate_limit: %w",
				c.body, errHeuristicDetectedBackendError)

			errType, signal := classifyErrorAsSignal(logger, err, latency)

			assert.Equal(t, protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNSPECIFIED, errType)
			assert.NotEqual(t, "rate_limit", signal.Reason)
			assert.GreaterOrEqual(t, signal.GetDefaultImpact(), float64(0))
		})
	}
}

// Generic backend rate-limit responses (not relay-miner over-servicing) MUST
// continue to be penalized. The fix narrows the no-penalty path to specific
// relay-miner phrases — anything else stays a MajorError.
func TestClassifyErrorAsSignal_GenericRateLimit_StillPenalized(t *testing.T) {
	logger := polyzero.NewLogger()
	latency := 100 * time.Millisecond

	cases := []string{
		"rate limit exceeded",
		"too many requests",
		"throttled",
	}
	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			err := fmt.Errorf("raw_payload: %s: heuristic detected error_indicator_rate_limit: %w",
				body, errHeuristicDetectedBackendError)

			errType, signal := classifyErrorAsSignal(logger, err, latency)

			assert.Equal(t,
				protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_BACKEND_SERVICE,
				errType,
				"generic backend rate-limit must remain a fault (regression guard)")
			assert.Equal(t, "rate_limit", signal.Reason)
			assert.Less(t, signal.GetDefaultImpact(), float64(0),
				"generic backend rate-limit must still penalize reputation")
		})
	}
}
