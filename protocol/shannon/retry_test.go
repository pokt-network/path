package shannon

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	protocolobservations "github.com/pokt-network/path/observation/protocol"
)

// =============================================================================
// RetryConfig Tests
// =============================================================================

func TestRetryConfig_HydrateDefaults_Disabled(t *testing.T) {
	config := RetryConfig{
		Enabled: false,
	}

	result := config.HydrateDefaults()

	// When disabled, defaults should not be applied
	require.False(t, result.Enabled)
	require.Equal(t, 0, result.MaxRetries)
	require.False(t, result.RetryOn5xx)
	require.False(t, result.RetryOnTimeout)
	require.False(t, result.RetryOnConnection)
}

func TestRetryConfig_HydrateDefaults_Enabled_NoExplicitConfig(t *testing.T) {
	config := RetryConfig{
		Enabled: true,
	}

	result := config.HydrateDefaults()

	// When enabled with no explicit config, defaults should be applied
	require.True(t, result.Enabled)
	require.Equal(t, defaultMaxRetries, result.MaxRetries)
	require.True(t, result.RetryOn5xx)
	require.True(t, result.RetryOnTimeout)
	require.True(t, result.RetryOnConnection)
}

func TestRetryConfig_HydrateDefaults_Enabled_ExplicitConfig(t *testing.T) {
	config := RetryConfig{
		Enabled:           true,
		MaxRetries:        3,
		RetryOn5xx:        false,
		RetryOnTimeout:    true,
		RetryOnConnection: false,
	}

	result := config.HydrateDefaults()

	// When enabled with explicit config, explicit values should be preserved
	require.True(t, result.Enabled)
	require.Equal(t, 3, result.MaxRetries)
	require.False(t, result.RetryOn5xx)
	require.True(t, result.RetryOnTimeout)
	require.False(t, result.RetryOnConnection)
}

func TestRetryConfig_HydrateDefaults_Enabled_ZeroMaxRetries(t *testing.T) {
	config := RetryConfig{
		Enabled:        true,
		MaxRetries:     0, // Not explicitly set
		RetryOnTimeout: true,
	}

	result := config.HydrateDefaults()

	// MaxRetries should get the default value when 0
	require.Equal(t, defaultMaxRetries, result.MaxRetries)
	// Other explicit values should be preserved
	require.True(t, result.RetryOnTimeout)
}

// =============================================================================
// isRetryableError Tests
// =============================================================================

func TestIsRetryableError_Disabled(t *testing.T) {
	config := RetryConfig{
		Enabled: false,
	}

	// No error should be retryable when retry is disabled
	require.False(t, isRetryableError(errRelayEndpointTimeout, config))
	require.False(t, isRetryableError(errRelayEndpointConfig, config))
	require.False(t, isRetryableError(errContextCanceled, config))
}

func TestIsRetryableError_ContextCanceled(t *testing.T) {
	config := RetryConfig{
		Enabled:        true,
		RetryOnTimeout: true,
	}

	// Context cancellation should never be retried
	require.False(t, isRetryableError(errContextCanceled, config))
}

func TestIsRetryableError_ConfigError(t *testing.T) {
	config := RetryConfig{
		Enabled:           true,
		RetryOnConnection: true,
	}

	// Config/fatal errors should never be retried
	require.False(t, isRetryableError(errRelayEndpointConfig, config))
}

func TestIsRetryableError_Timeout(t *testing.T) {
	// With timeout retry enabled
	configEnabled := RetryConfig{
		Enabled:        true,
		RetryOnTimeout: true,
	}
	require.True(t, isRetryableError(errRelayEndpointTimeout, configEnabled))

	// With timeout retry disabled
	configDisabled := RetryConfig{
		Enabled:        true,
		RetryOnTimeout: false,
	}
	require.False(t, isRetryableError(errRelayEndpointTimeout, configDisabled))
}

func TestIsRetryableError_UnknownError(t *testing.T) {
	config := RetryConfig{
		Enabled:           true,
		RetryOn5xx:        true,
		RetryOnTimeout:    true,
		RetryOnConnection: true,
	}

	// Unknown errors should not be retried
	unknownErr := errors.New("unknown error")
	require.False(t, isRetryableError(unknownErr, config))
}

// =============================================================================
// isRetryableErrorType Tests
// =============================================================================

func TestIsRetryableErrorType_Disabled(t *testing.T) {
	config := RetryConfig{
		Enabled: false,
	}

	// No error type should be retryable when retry is disabled
	require.False(t, isRetryableErrorType(protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_TIMEOUT, config))
	require.False(t, isRetryableErrorType(protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_REFUSED, config))
	require.False(t, isRetryableErrorType(protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_5XX, config))
}

func TestIsRetryableErrorType_TimeoutErrors(t *testing.T) {
	timeoutErrors := []protocolobservations.ShannonEndpointErrorType{
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_TIMEOUT,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_IO_TIMEOUT,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONTEXT_DEADLINE_EXCEEDED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_TIMEOUT,
	}

	// With timeout retry enabled
	configEnabled := RetryConfig{
		Enabled:        true,
		RetryOnTimeout: true,
	}
	for _, errType := range timeoutErrors {
		require.True(t, isRetryableErrorType(errType, configEnabled), "Expected %s to be retryable", errType)
	}

	// With timeout retry disabled
	configDisabled := RetryConfig{
		Enabled:        true,
		RetryOnTimeout: false,
	}
	for _, errType := range timeoutErrors {
		require.False(t, isRetryableErrorType(errType, configDisabled), "Expected %s to not be retryable", errType)
	}
}

func TestIsRetryableErrorType_ConnectionErrors(t *testing.T) {
	connectionErrors := []protocolobservations.ShannonEndpointErrorType{
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_REFUSED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_RESET,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_NO_ROUTE_TO_HOST,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_NETWORK_UNREACHABLE,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_BROKEN_PIPE,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_WEBSOCKET_CONNECTION_FAILED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_CONNECTION_REFUSED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_TCP_CONNECTION,
	}

	// With connection retry enabled
	configEnabled := RetryConfig{
		Enabled:           true,
		RetryOnConnection: true,
	}
	for _, errType := range connectionErrors {
		require.True(t, isRetryableErrorType(errType, configEnabled), "Expected %s to be retryable", errType)
	}

	// With connection retry disabled
	configDisabled := RetryConfig{
		Enabled:           true,
		RetryOnConnection: false,
	}
	for _, errType := range connectionErrors {
		require.False(t, isRetryableErrorType(errType, configDisabled), "Expected %s to not be retryable", errType)
	}
}

func TestIsRetryableErrorType_5xxErrors(t *testing.T) {
	// With 5xx retry enabled
	configEnabled := RetryConfig{
		Enabled:    true,
		RetryOn5xx: true,
	}
	require.True(t, isRetryableErrorType(protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_5XX, configEnabled))

	// With 5xx retry disabled
	configDisabled := RetryConfig{
		Enabled:    true,
		RetryOn5xx: false,
	}
	require.False(t, isRetryableErrorType(protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_5XX, configDisabled))
}

func TestIsRetryableErrorType_NonRetryableErrors(t *testing.T) {
	nonRetryableErrors := []protocolobservations.ShannonEndpointErrorType{
		// Client errors (4xx) - never retry
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_4XX,
		// PATH-side cancellation - never retry
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_REQUEST_CANCELED_BY_PATH,
		// Fatal/config errors - never retry
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_DNS_RESOLUTION,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_TLS_HANDSHAKE,
	}

	// Even with all retry options enabled, these should never be retried
	config := RetryConfig{
		Enabled:           true,
		RetryOn5xx:        true,
		RetryOnTimeout:    true,
		RetryOnConnection: true,
	}

	for _, errType := range nonRetryableErrors {
		require.False(t, isRetryableErrorType(errType, config), "Expected %s to NOT be retryable", errType)
	}
}

func TestIsRetryableErrorType_UnknownErrorType(t *testing.T) {
	config := RetryConfig{
		Enabled:           true,
		RetryOn5xx:        true,
		RetryOnTimeout:    true,
		RetryOnConnection: true,
	}

	// Unknown/unspecified error types should not be retried
	require.False(t, isRetryableErrorType(protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNSPECIFIED, config))
}

// =============================================================================
// hasExplicitConfig Tests
// =============================================================================

func TestHasExplicitConfig_AllFalse(t *testing.T) {
	config := RetryConfig{
		Enabled:           true,
		RetryOn5xx:        false,
		RetryOnTimeout:    false,
		RetryOnConnection: false,
	}

	require.False(t, config.hasExplicitConfig())
}

func TestHasExplicitConfig_OneTrue(t *testing.T) {
	tests := []struct {
		name   string
		config RetryConfig
	}{
		{"RetryOn5xx", RetryConfig{Enabled: true, RetryOn5xx: true}},
		{"RetryOnTimeout", RetryConfig{Enabled: true, RetryOnTimeout: true}},
		{"RetryOnConnection", RetryConfig{Enabled: true, RetryOnConnection: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.True(t, tt.config.hasExplicitConfig())
		})
	}
}

func TestHasExplicitConfig_AllTrue(t *testing.T) {
	config := RetryConfig{
		Enabled:           true,
		RetryOn5xx:        true,
		RetryOnTimeout:    true,
		RetryOnConnection: true,
	}

	require.True(t, config.hasExplicitConfig())
}
