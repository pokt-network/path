package shannon

import (
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"

	protocolobservations "github.com/pokt-network/path/observation/protocol"
)

// TestClassifyMalformedEndpointPayload_RelayMinerErrors tests that RelayMiner errors
// from poktroll are properly classified based on their error messages.
func TestClassifyMalformedEndpointPayload_RelayMinerErrors(t *testing.T) {
	logger := polyzero.NewLogger()

	tests := []struct {
		name             string
		payloadContent   string
		expectedErrType  protocolobservations.ShannonEndpointErrorType
		expectedSanction protocolobservations.ShannonSanctionType
	}{
		{
			name:             "RelayMiner timeout (code 10)",
			payloadContent:   "relayer proxy request timed out",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_RELAY_MINER_TIMEOUT,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:             "RelayMiner invalid session (code 1)",
			payloadContent:   "invalid session in relayer request",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_RELAY_MINER_INVALID_SESSION,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:             "RelayMiner services configs undefined (code 2)",
			payloadContent:   "services configurations are undefined",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_RELAY_MINER_SERVICE_CONFIG_UNDEFINED,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_PERMANENT,
		},
		{
			name:             "RelayMiner service endpoint not handled (code 3)",
			payloadContent:   "service endpoint not handled by relayer proxy",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_SERVICE_NOT_CONFIGURED,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_PERMANENT,
		},
		{
			name:             "RelayMiner unsupported transport type (code 4)",
			payloadContent:   "unsupported proxy transport type",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_RELAY_MINER_UNSUPPORTED_TRANSPORT,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_PERMANENT,
		},
		{
			name:             "RelayMiner internal error (code 5)",
			payloadContent:   "internal error",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_RELAY_MINER_INTERNAL_ERROR,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:             "RelayMiner unknown session (code 6)",
			payloadContent:   "relayer proxy encountered unknown session",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_RELAY_MINER_UNKNOWN_SESSION,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:             "RelayMiner rate limited (code 7)",
			payloadContent:   "offchain rate limit hit by relayer proxy",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_RELAY_MINER_RATE_LIMITED,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:             "RelayMiner relay cost calculation failed (code 8)",
			payloadContent:   "failed to calculate relay cost",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_RELAY_MINER_RELAY_COST_CALCULATION,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:             "RelayMiner suppliers not reachable (code 9)",
			payloadContent:   "supplier(s) not reachable",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_SUPPLIERS_NOT_REACHABLE,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:             "RelayMiner max body exceeded (code 11)",
			payloadContent:   "body size exceeds maximum allowed",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_RESPONSE_SIZE_EXCEEDED,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_UNSPECIFIED,
		},
		{
			name:             "RelayMiner response limit exceeded (code 12)",
			payloadContent:   "response limit exceed",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_RESPONSE_SIZE_EXCEEDED,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_UNSPECIFIED,
		},
		{
			name:             "RelayMiner request limit exceeded (code 13)",
			payloadContent:   "request limit exceed",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_RELAY_MINER_REQUEST_LIMIT_EXCEEDED,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_DO_NOT_SANCTION,
		},
		{
			name:             "RelayMiner unmarshal relay request failed (code 14)",
			payloadContent:   "failed to unmarshal relay request",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_RELAY_MINER_UNMARSHAL_REQUEST,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errType, sanction := classifyMalformedEndpointPayload(logger, tt.payloadContent)
			require.Equal(t, tt.expectedErrType, errType, "unexpected error type for payload: %s", tt.payloadContent)
			require.Equal(t, tt.expectedSanction, sanction, "unexpected sanction type for payload: %s", tt.payloadContent)
		})
	}
}

// TestClassifyMalformedEndpointPayload_ExistingErrors tests that existing error patterns
// are still properly classified after adding RelayMiner-specific patterns.
func TestClassifyMalformedEndpointPayload_ExistingErrors(t *testing.T) {
	logger := polyzero.NewLogger()

	tests := []struct {
		name             string
		payloadContent   string
		expectedErrType  protocolobservations.ShannonEndpointErrorType
		expectedSanction protocolobservations.ShannonSanctionType
	}{
		{
			name:             "Connection refused",
			payloadContent:   "dial tcp 127.0.0.1:8080: connection refused",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_CONNECTION_REFUSED,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:             "Unexpected EOF",
			payloadContent:   "unexpected EOF",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_UNEXPECTED_EOF,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:             "DNS resolution error",
			payloadContent:   "dial tcp: lookup foo.example.com: no such host",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_DNS_RESOLUTION,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:             "TLS handshake error",
			payloadContent:   "tls: handshake failure",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_TLS_HANDSHAKE,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:             "Server closed connection",
			payloadContent:   "server closed idle connection",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_SERVER_CLOSED_CONNECTION,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_UNSPECIFIED,
		},
		{
			name:             "Unknown error falls through",
			payloadContent:   "some completely unknown error message",
			expectedErrType:  protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_UNKNOWN,
			expectedSanction: protocolobservations.ShannonSanctionType_SHANNON_SANCTION_DO_NOT_SANCTION,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errType, sanction := classifyMalformedEndpointPayload(logger, tt.payloadContent)
			require.Equal(t, tt.expectedErrType, errType, "unexpected error type for payload: %s", tt.payloadContent)
			require.Equal(t, tt.expectedSanction, sanction, "unexpected sanction type for payload: %s", tt.payloadContent)
		})
	}
}

// TestRelayMinerTimeoutIsRetryable verifies that the RelayMiner timeout error
// is properly classified as retryable when retry_on_timeout is enabled.
func TestRelayMinerTimeoutIsRetryable(t *testing.T) {
	// First, verify the classification
	logger := polyzero.NewLogger()
	errType, _ := classifyMalformedEndpointPayload(logger, "relayer proxy request timed out")
	require.Equal(t, protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_RELAY_MINER_TIMEOUT, errType)

	// Then, verify it's retryable
	config := RetryConfig{
		Enabled:        true,
		RetryOnTimeout: true,
	}
	require.True(t, isRetryableErrorType(errType, config), "RelayMiner timeout should be retryable")

	// And verify it's not retryable when timeout retry is disabled
	configDisabled := RetryConfig{
		Enabled:        true,
		RetryOnTimeout: false,
	}
	require.False(t, isRetryableErrorType(errType, configDisabled), "RelayMiner timeout should not be retryable when timeout retry is disabled")
}
