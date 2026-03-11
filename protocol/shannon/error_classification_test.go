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
