package shannon

import (
	"errors"
	"fmt"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"

	pathhttp "github.com/buildwithgrove/path/network/http"
	protocolobservations "github.com/buildwithgrove/path/observation/protocol"
)

func TestClassifyRelayError(t *testing.T) {
	logger := polyzero.NewLogger()

	tests := []struct {
		name                    string
		err                     error
		expectedErrorType       protocolobservations.ShannonEndpointErrorType
		expectedSanctionType    protocolobservations.ShannonSanctionType
	}{
		{
			name:                    "nil error returns unspecified",
			err:                     nil,
			expectedErrorType:       protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNSPECIFIED,
			expectedSanctionType:    protocolobservations.ShannonSanctionType_SHANNON_SANCTION_UNSPECIFIED,
		},
		{
			name:                    "HTTP relay error with non-2xx status",
			err:                     fmt.Errorf("%w: %w: %d", errSendHTTPRelay, pathhttp.ErrRelayEndpointHTTPError, 500),
			expectedErrorType:       protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_NON_2XX_STATUS,
			expectedSanctionType:    protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:                    "endpoint config error",
			err:                     errRelayEndpointConfig,
			expectedErrorType:       protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_CONFIG,
			expectedSanctionType:    protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:                    "endpoint timeout error",
			err:                     errRelayEndpointTimeout,
			expectedErrorType:       protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_TIMEOUT,
			expectedSanctionType:    protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:                    "direct HTTP error",
			err:                     pathhttp.ErrRelayEndpointHTTPError,
			expectedErrorType:       protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_BAD_RESPONSE,
			expectedSanctionType:    protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:                    "context canceled error",
			err:                     errContextCanceled,
			expectedErrorType:       protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_REQUEST_CANCELED_BY_PATH,
			expectedSanctionType:    protocolobservations.ShannonSanctionType_SHANNON_SANCTION_DO_NOT_SANCTION,
		},
		{
			name:                    "malformed endpoint payload",
			err:                     errMalformedEndpointPayload,
			expectedErrorType:       protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_BACKEND_SERVICE,
			expectedSanctionType:    protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:                    "unknown error type",
			err:                     errors.New("completely unknown error type"),
			expectedErrorType:       protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNKNOWN,
			expectedSanctionType:    protocolobservations.ShannonSanctionType_SHANNON_SANCTION_UNSPECIFIED,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errorType, sanctionType := classifyRelayError(logger, tc.err)
			
			require.Equal(t, tc.expectedErrorType, errorType,
				"Error type mismatch for error: %v", tc.err)
			require.Equal(t, tc.expectedSanctionType, sanctionType,
				"Sanction type mismatch for error: %v", tc.err)
		})
	}
}

func TestClassifyHttpError(t *testing.T) {
	logger := polyzero.NewLogger()

	tests := []struct {
		name                    string
		err                     error
		expectedErrorType       protocolobservations.ShannonEndpointErrorType
		expectedSanctionType    protocolobservations.ShannonSanctionType
	}{
		{
			name:                    "HTTP relay endpoint error",
			err:                     pathhttp.ErrRelayEndpointHTTPError,
			expectedErrorType:       protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_NON_2XX_STATUS,
			expectedSanctionType:    protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:                    "connection refused error",
			err:                     errors.New("dial tcp: connection refused"),
			expectedErrorType:       protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_CONNECTION_REFUSED,
			expectedSanctionType:    protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:                    "DNS resolution error",
			err:                     errors.New("no such host example.com"),
			expectedErrorType:       protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_DNS_RESOLUTION,
			expectedSanctionType:    protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:                    "TLS handshake error",
			err:                     errors.New("net/http: TLS handshake timeout"),
			expectedErrorType:       protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_TLS_HANDSHAKE,
			expectedSanctionType:    protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		},
		{
			name:                    "unknown HTTP error",
			err:                     errors.New("unknown HTTP error type"),
			expectedErrorType:       protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNKNOWN,
			expectedSanctionType:    protocolobservations.ShannonSanctionType_SHANNON_SANCTION_UNSPECIFIED,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errorType, sanctionType := classifyHttpError(logger, tc.err)
			
			require.Equal(t, tc.expectedErrorType, errorType,
				"Error type mismatch for error: %v", tc.err)
			require.Equal(t, tc.expectedSanctionType, sanctionType,
				"Sanction type mismatch for error: %v", tc.err)
		})
	}
}

// TestErrorClassificationConsistency ensures that both classification functions
// handle HTTP errors consistently
func TestErrorClassificationConsistency(t *testing.T) {
	logger := polyzero.NewLogger()
	
	// Test that HTTP errors are classified consistently by both functions
	httpErr := pathhttp.ErrRelayEndpointHTTPError
	
	// Test direct HTTP error classification
	httpErrorType, httpSanctionType := classifyHttpError(logger, httpErr)
	
	// Test HTTP error wrapped in relay error
	relayErrorType, relaySanctionType := classifyRelayError(logger, httpErr)
	
	// Both should use session sanctions for HTTP errors
	require.Equal(t, protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION, httpSanctionType)
	require.Equal(t, protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION, relaySanctionType)
	
	// Error types should be different but both should indicate HTTP issues
	require.Contains(t, httpErrorType.String(), "HTTP")
	require.Contains(t, relayErrorType.String(), "HTTP")
}