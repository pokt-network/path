package shannon

import (
	"errors"

	protocolobservations "github.com/pokt-network/path/observation/protocol"
)

// Default retry configuration values
const (
	defaultMaxRetries        = 1
	defaultRetryOn5xx        = true
	defaultRetryOnTimeout    = true
	defaultRetryOnConnection = true
)

// RetryConfig holds configuration for automatic request retry.
// When enabled, failed requests will be retried on different endpoints.
type RetryConfig struct {
	// Enabled enables or disables automatic retry.
	// Default: false (disabled)
	Enabled bool `yaml:"enabled"`

	// MaxRetries is the maximum number of retry attempts after the initial request fails.
	// Total attempts = 1 (initial) + MaxRetries.
	// Default: 1
	MaxRetries int `yaml:"max_retries"`

	// RetryOn5xx enables retry on HTTP 5xx server errors.
	// Default: true
	RetryOn5xx bool `yaml:"retry_on_5xx"`

	// RetryOnTimeout enables retry on timeout errors.
	// Default: true
	RetryOnTimeout bool `yaml:"retry_on_timeout"`

	// RetryOnConnection enables retry on connection errors (refused, reset, unreachable).
	// Default: true
	RetryOnConnection bool `yaml:"retry_on_connection"`
}

// HydrateDefaults applies default values to RetryConfig.
func (rc *RetryConfig) HydrateDefaults() RetryConfig {
	// Only apply defaults if retry is enabled
	if !rc.Enabled {
		return *rc
	}

	if rc.MaxRetries == 0 {
		rc.MaxRetries = defaultMaxRetries
	}

	// Set default retry behavior flags (only if enabled and unset)
	// Note: YAML unmarshaling sets false for missing bool fields,
	// so we default to true for all retry types when enabled
	if !rc.hasExplicitConfig() {
		rc.RetryOn5xx = defaultRetryOn5xx
		rc.RetryOnTimeout = defaultRetryOnTimeout
		rc.RetryOnConnection = defaultRetryOnConnection
	}

	return *rc
}

// hasExplicitConfig returns true if any retry type has been explicitly configured.
// Used to determine if defaults should be applied.
func (rc *RetryConfig) hasExplicitConfig() bool {
	return rc.RetryOn5xx || rc.RetryOnTimeout || rc.RetryOnConnection
}

// isRetryableError determines if an error should trigger a retry attempt.
// Returns true if the error type is configured as retryable.
func isRetryableError(err error, config RetryConfig) bool {
	if !config.Enabled {
		return false
	}

	// Check for context cancellation (never retry)
	if errors.Is(err, errContextCanceled) {
		return false
	}

	// Check for config/fatal errors (TLS, DNS - never retry)
	if errors.Is(err, errRelayEndpointConfig) {
		return false
	}

	// Check for timeout errors
	if config.RetryOnTimeout && errors.Is(err, errRelayEndpointTimeout) {
		return true
	}

	return false
}

// isRetryableErrorType determines if an error type from observations should trigger a retry.
// This is used after error classification to determine retry behavior.
func isRetryableErrorType(errorType protocolobservations.ShannonEndpointErrorType, config RetryConfig) bool {
	if !config.Enabled {
		return false
	}

	switch errorType {
	// Timeout errors
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_TIMEOUT,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_IO_TIMEOUT,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONTEXT_DEADLINE_EXCEEDED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_TIMEOUT:
		return config.RetryOnTimeout

	// Connection errors
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_REFUSED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_RESET,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_NO_ROUTE_TO_HOST,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_NETWORK_UNREACHABLE,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_BROKEN_PIPE,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_WEBSOCKET_CONNECTION_FAILED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_CONNECTION_REFUSED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_TCP_CONNECTION:
		return config.RetryOnConnection

	// 5xx errors
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_5XX:
		return config.RetryOn5xx

	// Client errors (4xx) - never retry
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_4XX:
		return false

	// PATH-side cancellation - never retry
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_REQUEST_CANCELED_BY_PATH:
		return false

	// Fatal/config errors - never retry
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_DNS_RESOLUTION,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_TLS_HANDSHAKE:
		return false

	default:
		// Default: don't retry unknown error types
		return false
	}
}
