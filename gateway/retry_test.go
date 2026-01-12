package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/metrics/devtools"
	"github.com/pokt-network/path/observation"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
	"github.com/pokt-network/path/websockets"
)

// TestShouldRetry tests the retry decision logic
func TestShouldRetry(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		statusCode  int
		retryConfig *ServiceRetryConfig
		expected    bool
	}{
		{
			name:        "nil config returns false",
			err:         errors.New("error"),
			statusCode:  500,
			retryConfig: nil,
			expected:    false,
		},
		{
			name:        "disabled config returns false",
			err:         errors.New("error"),
			statusCode:  500,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(false)},
			expected:    false,
		},
		{
			name:        "nil enabled pointer returns false",
			err:         errors.New("error"),
			statusCode:  500,
			retryConfig: &ServiceRetryConfig{Enabled: nil},
			expected:    false,
		},
		{
			name:        "5xx error with RetryOn5xx enabled",
			err:         errors.New("server error"),
			statusCode:  503,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOn5xx: boolPtr(true)},
			expected:    true,
		},
		{
			name:        "500 error with RetryOn5xx enabled",
			err:         errors.New("internal server error"),
			statusCode:  500,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOn5xx: boolPtr(true)},
			expected:    true,
		},
		{
			name:        "599 error with RetryOn5xx enabled",
			err:         errors.New("server error"),
			statusCode:  599,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOn5xx: boolPtr(true)},
			expected:    true,
		},
		{
			name:        "5xx error with RetryOn5xx disabled",
			err:         errors.New("server error"),
			statusCode:  503,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOn5xx: boolPtr(false)},
			expected:    false,
		},
		{
			name:        "5xx error with RetryOn5xx nil",
			err:         errors.New("server error"),
			statusCode:  503,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOn5xx: nil},
			expected:    false,
		},
		{
			name:        "timeout error with RetryOnTimeout enabled",
			err:         errors.New("request timeout occurred"),
			statusCode:  0,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOnTimeout: boolPtr(true)},
			expected:    true,
		},
		{
			name:        "context.DeadlineExceeded error with RetryOnTimeout enabled",
			err:         context.DeadlineExceeded,
			statusCode:  0,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOnTimeout: boolPtr(true)},
			expected:    true,
		},
		{
			name:        "timeout error (uppercase) with RetryOnTimeout enabled",
			err:         errors.New("Request TIMEOUT occurred"),
			statusCode:  0,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOnTimeout: boolPtr(true)},
			expected:    true,
		},
		{
			name:        "timeout error with RetryOnTimeout disabled",
			err:         errors.New("timeout"),
			statusCode:  0,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOnTimeout: boolPtr(false)},
			expected:    false,
		},
		{
			name:        "timeout error with RetryOnTimeout nil",
			err:         errors.New("timeout"),
			statusCode:  0,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOnTimeout: nil},
			expected:    false,
		},
		{
			name:        "connection refused error with RetryOnConnection enabled",
			err:         errors.New("connection refused"),
			statusCode:  0,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOnConnection: boolPtr(true)},
			expected:    true,
		},
		{
			name:        "dial error with RetryOnConnection enabled",
			err:         errors.New("dial tcp: connection failed"),
			statusCode:  0,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOnConnection: boolPtr(true)},
			expected:    true,
		},
		{
			name:        "network error with RetryOnConnection enabled",
			err:         errors.New("network unreachable"),
			statusCode:  0,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOnConnection: boolPtr(true)},
			expected:    true,
		},
		{
			name:        "connection error (mixed case) with RetryOnConnection enabled",
			err:         errors.New("Connection REFUSED"),
			statusCode:  0,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOnConnection: boolPtr(true)},
			expected:    true,
		},
		{
			name:        "connection error with RetryOnConnection disabled",
			err:         errors.New("connection refused"),
			statusCode:  0,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOnConnection: boolPtr(false)},
			expected:    false,
		},
		{
			name:        "connection error with RetryOnConnection nil",
			err:         errors.New("connection refused"),
			statusCode:  0,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOnConnection: nil},
			expected:    false,
		},
		{
			name:        "4xx error should not retry",
			err:         errors.New("bad request"),
			statusCode:  400,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOn5xx: boolPtr(true)},
			expected:    false,
		},
		{
			name:        "404 error should not retry",
			err:         errors.New("not found"),
			statusCode:  404,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOn5xx: boolPtr(true)},
			expected:    false,
		},
		{
			name:        "2xx success should not retry",
			err:         nil,
			statusCode:  200,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOn5xx: boolPtr(true)},
			expected:    false,
		},
		{
			name:        "no error and no status code should not retry",
			err:         nil,
			statusCode:  0,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOn5xx: boolPtr(true)},
			expected:    false,
		},
		{
			name:       "multiple retry conditions enabled - 5xx triggers",
			err:        errors.New("server error"),
			statusCode: 500,
			retryConfig: &ServiceRetryConfig{
				Enabled:           boolPtr(true),
				RetryOn5xx:        boolPtr(true),
				RetryOnTimeout:    boolPtr(true),
				RetryOnConnection: boolPtr(true),
			},
			expected: true,
		},
		{
			name:       "multiple retry conditions enabled - timeout triggers",
			err:        errors.New("timeout"),
			statusCode: 0,
			retryConfig: &ServiceRetryConfig{
				Enabled:           boolPtr(true),
				RetryOn5xx:        boolPtr(true),
				RetryOnTimeout:    boolPtr(true),
				RetryOnConnection: boolPtr(true),
			},
			expected: true,
		},
		{
			name:       "multiple retry conditions enabled - connection triggers",
			err:        errors.New("connection refused"),
			statusCode: 0,
			retryConfig: &ServiceRetryConfig{
				Enabled:           boolPtr(true),
				RetryOn5xx:        boolPtr(true),
				RetryOnTimeout:    boolPtr(true),
				RetryOnConnection: boolPtr(true),
			},
			expected: true,
		},
		{
			name:       "unknown error with no matching conditions should not retry",
			err:        errors.New("some random error"),
			statusCode: 0,
			retryConfig: &ServiceRetryConfig{
				Enabled:           boolPtr(true),
				RetryOn5xx:        boolPtr(false),
				RetryOnTimeout:    boolPtr(false),
				RetryOnConnection: boolPtr(false),
			},
			expected: false,
		},
		// RetryOnInvalidJSON tests
		{
			name:        "invalid JSON error with RetryOnInvalidJSON enabled",
			err:         errInvalidJSON,
			statusCode:  200,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOnInvalidJSON: boolPtr(true)},
			expected:    true,
		},
		{
			name:        "response is not valid json error with RetryOnInvalidJSON enabled",
			err:         errors.New("response is not valid JSON"),
			statusCode:  200,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOnInvalidJSON: boolPtr(true)},
			expected:    true,
		},
		{
			name:        "invalid json (case insensitive) error with RetryOnInvalidJSON enabled",
			err:         errors.New("Invalid JSON in response body"),
			statusCode:  200,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOnInvalidJSON: boolPtr(true)},
			expected:    true,
		},
		{
			name:        "invalid JSON error with RetryOnInvalidJSON disabled",
			err:         errInvalidJSON,
			statusCode:  200,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOnInvalidJSON: boolPtr(false)},
			expected:    false,
		},
		{
			name:        "invalid JSON error with RetryOnInvalidJSON nil",
			err:         errInvalidJSON,
			statusCode:  200,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOnInvalidJSON: nil},
			expected:    false,
		},
		{
			name:       "multiple retry conditions enabled - invalid JSON triggers",
			err:        errInvalidJSON,
			statusCode: 200,
			retryConfig: &ServiceRetryConfig{
				Enabled:            boolPtr(true),
				RetryOn5xx:         boolPtr(true),
				RetryOnTimeout:     boolPtr(true),
				RetryOnConnection:  boolPtr(true),
				RetryOnInvalidJSON: boolPtr(true),
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := &requestContext{}
			result := rc.shouldRetry(tt.err, tt.statusCode, 100*time.Millisecond, tt.retryConfig, "")
			require.Equal(t, tt.expected, result, "shouldRetry result mismatch for test case: %s", tt.name)
		})
	}
}

// TestGetRetryConfigForService tests the getRetryConfigForService logic
func TestGetRetryConfigForService(t *testing.T) {
	tests := []struct {
		name             string
		protocolNil      bool
		unifiedConfigNil bool
		retryConfig      *ServiceRetryConfig
		expected         *ServiceRetryConfig
	}{
		{
			name:        "nil protocol returns nil",
			protocolNil: true,
			expected:    nil,
		},
		{
			name:             "nil unified config returns nil",
			protocolNil:      false,
			unifiedConfigNil: true,
			expected:         nil,
		},
		{
			name:             "valid config returns retry config",
			protocolNil:      false,
			unifiedConfigNil: false,
			retryConfig: &ServiceRetryConfig{
				Enabled:    boolPtr(true),
				MaxRetries: intPtr(3),
			},
			expected: &ServiceRetryConfig{
				Enabled:    boolPtr(true),
				MaxRetries: intPtr(3),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := &requestContext{
				serviceID: "test-service",
			}

			if !tt.protocolNil {
				// Create a mock protocol
				mockProtocol := &mockProtocolForRetry{
					unifiedConfigNil: tt.unifiedConfigNil,
					retryConfig:      tt.retryConfig,
				}
				rc.protocol = mockProtocol
			}

			result := rc.getRetryConfigForService()

			if tt.expected == nil {
				require.Nil(t, result)
			} else {
				require.NotNil(t, result)
				if tt.expected.Enabled != nil {
					require.NotNil(t, result.Enabled)
					require.Equal(t, *tt.expected.Enabled, *result.Enabled)
				}
				if tt.expected.MaxRetries != nil {
					require.NotNil(t, result.MaxRetries)
					require.Equal(t, *tt.expected.MaxRetries, *result.MaxRetries)
				}
			}
		})
	}
}

// Helper functions
func boolPtr(b bool) *bool {
	return &b
}

func intPtr(i int) *int {
	return &i
}

// mockProtocolForRetry is a minimal mock Protocol implementation for testing retry logic
type mockProtocolForRetry struct {
	unifiedConfigNil bool
	retryConfig      *ServiceRetryConfig
}

func (m *mockProtocolForRetry) GetUnifiedServicesConfig() *UnifiedServicesConfig {
	if m.unifiedConfigNil {
		return nil
	}
	return &UnifiedServicesConfig{
		Services: []ServiceConfig{
			{
				ID:          "test-service",
				RetryConfig: m.retryConfig,
			},
		},
	}
}

// Implement minimal Protocol interface methods (not used in retry tests)
func (m *mockProtocolForRetry) AvailableHTTPEndpoints(ctx context.Context, serviceID protocol.ServiceID, rpcType sharedtypes.RPCType, httpReq *http.Request) (protocol.EndpointAddrList, protocolobservations.Observations, error) {
	return nil, protocolobservations.Observations{}, nil
}

func (m *mockProtocolForRetry) AvailableWebsocketEndpoints(ctx context.Context, serviceID protocol.ServiceID, httpReq *http.Request) (protocol.EndpointAddrList, protocolobservations.Observations, error) {
	return nil, protocolobservations.Observations{}, nil
}

func (m *mockProtocolForRetry) BuildHTTPRequestContextForEndpoint(ctx context.Context, serviceID protocol.ServiceID, endpointAddr protocol.EndpointAddr, rpcType sharedtypes.RPCType, httpReq *http.Request, filterByReputation bool) (ProtocolRequestContext, protocolobservations.Observations, error) {
	return nil, protocolobservations.Observations{}, nil
}

func (m *mockProtocolForRetry) BuildWebsocketRequestContextForEndpoint(ctx context.Context, serviceID protocol.ServiceID, endpointAddr protocol.EndpointAddr, processor websockets.WebsocketMessageProcessor, httpReq *http.Request, w http.ResponseWriter, msgChan chan *observation.RequestResponseObservations) (ProtocolRequestContextWebsocket, <-chan *protocolobservations.Observations, error) {
	return nil, nil, nil
}

func (m *mockProtocolForRetry) SupportedGatewayModes() []protocol.GatewayMode {
	return nil
}

func (m *mockProtocolForRetry) ApplyHTTPObservations(observations *protocolobservations.Observations) error {
	return nil
}

func (m *mockProtocolForRetry) ApplyWebSocketObservations(observations *protocolobservations.Observations) error {
	return nil
}

func (m *mockProtocolForRetry) ConfiguredServiceIDs() map[protocol.ServiceID]struct{} {
	return nil
}

func (m *mockProtocolForRetry) GetTotalServiceEndpointsCount(serviceID protocol.ServiceID, httpReq *http.Request) (int, error) {
	return 0, nil
}

func (m *mockProtocolForRetry) HydrateDisqualifiedEndpointsResponse(serviceID protocol.ServiceID, resp *devtools.DisqualifiedEndpointResponse) {
}

func (m *mockProtocolForRetry) GetConcurrencyConfig() ConcurrencyConfig {
	return ConcurrencyConfig{
		MaxParallelEndpoints: 1,
		MaxConcurrentRelays:  5500,
		MaxBatchPayloads:     5500,
	}
}

func (m *mockProtocolForRetry) CheckWebsocketConnection(ctx context.Context, serviceID protocol.ServiceID, endpointAddr protocol.EndpointAddr) *protocolobservations.Observations {
	return nil
}

func (m *mockProtocolForRetry) IsSessionActive(ctx context.Context, serviceID protocol.ServiceID, sessionID string) bool {
	return true
}

func (m *mockProtocolForRetry) IsSupplierBlacklisted(serviceID protocol.ServiceID, supplierAddr string) bool {
	return false
}

func (m *mockProtocolForRetry) UnblacklistSupplier(serviceID protocol.ServiceID, supplierAddr string) bool {
	return false
}

func (m *mockProtocolForRetry) GetReputationService() reputation.ReputationService {
	return nil
}

func (m *mockProtocolForRetry) GetEndpointsForHealthCheck() func(protocol.ServiceID) ([]EndpointInfo, error) {
	return nil
}

func (m *mockProtocolForRetry) GetHealth(ctx context.Context) error {
	return nil
}

func (m *mockProtocolForRetry) IsAlive() bool {
	return true
}

func (m *mockProtocolForRetry) Name() string {
	return "mockProtocolForRetry"
}

// TestShouldRetryErrorMessageMatching tests that error message matching is case-insensitive
func TestShouldRetryErrorMessageMatching(t *testing.T) {
	tests := []struct {
		name        string
		errorMsg    string
		configField string
		expected    bool
	}{
		// Timeout variations
		{
			name:        "timeout lowercase",
			errorMsg:    "request timeout",
			configField: "timeout",
			expected:    true,
		},
		{
			name:        "timeout uppercase",
			errorMsg:    "REQUEST TIMEOUT",
			configField: "timeout",
			expected:    true,
		},
		{
			name:        "timeout mixed case",
			errorMsg:    "Request TimeOut Occurred",
			configField: "timeout",
			expected:    true,
		},
		{
			name:        "timeout in error message",
			errorMsg:    "operation timeout",
			configField: "timeout",
			expected:    true,
		},
		// Connection variations
		{
			name:        "connection lowercase",
			errorMsg:    "connection refused",
			configField: "connection",
			expected:    true,
		},
		{
			name:        "connection uppercase",
			errorMsg:    "CONNECTION REFUSED",
			configField: "connection",
			expected:    true,
		},
		{
			name:        "dial error",
			errorMsg:    "dial tcp failed",
			configField: "connection",
			expected:    true,
		},
		{
			name:        "network error",
			errorMsg:    "network is unreachable",
			configField: "connection",
			expected:    true,
		},
		// Non-matching errors
		{
			name:        "non-timeout error for timeout config",
			errorMsg:    "some other error",
			configField: "timeout",
			expected:    false,
		},
		{
			name:        "non-connection error for connection config",
			errorMsg:    "some random error",
			configField: "connection",
			expected:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := &requestContext{}
			err := errors.New(tt.errorMsg)

			var config *ServiceRetryConfig
			switch tt.configField {
			case "timeout":
				config = &ServiceRetryConfig{
					Enabled:        boolPtr(true),
					RetryOnTimeout: boolPtr(true),
				}
			case "connection":
				config = &ServiceRetryConfig{
					Enabled:           boolPtr(true),
					RetryOnConnection: boolPtr(true),
				}
			}

			result := rc.shouldRetry(err, 0, 100*time.Millisecond, config, "")
			require.Equal(t, tt.expected, result)
		})
	}
}

// TestShouldRetryWithWrappedErrors tests that wrapped errors are handled correctly
func TestShouldRetryWithWrappedErrors(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		retryConfig *ServiceRetryConfig
		expected    bool
	}{
		{
			name:        "wrapped timeout error",
			err:         errors.New("failed to process: timeout occurred"),
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOnTimeout: boolPtr(true)},
			expected:    true,
		},
		{
			name:        "wrapped connection error",
			err:         errors.New("request failed: connection refused by server"),
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOnConnection: boolPtr(true)},
			expected:    true,
		},
		{
			name:        "wrapped DeadlineExceeded",
			err:         context.DeadlineExceeded,
			retryConfig: &ServiceRetryConfig{Enabled: boolPtr(true), RetryOnTimeout: boolPtr(true)},
			expected:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := &requestContext{}
			result := rc.shouldRetry(tt.err, 0, 100*time.Millisecond, tt.retryConfig, "")
			require.Equal(t, tt.expected, result)
		})
	}
}

// TestShouldRetryStatusCodeBoundaries tests status code boundary conditions
func TestShouldRetryStatusCodeBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		expected   bool
	}{
		{
			name:       "499 - just before 5xx range",
			statusCode: 499,
			expected:   false,
		},
		{
			name:       "500 - start of 5xx range",
			statusCode: 500,
			expected:   true,
		},
		{
			name:       "550 - middle of 5xx range",
			statusCode: 550,
			expected:   true,
		},
		{
			name:       "599 - end of 5xx range",
			statusCode: 599,
			expected:   true,
		},
		{
			name:       "600 - just after 5xx range",
			statusCode: 600,
			expected:   false,
		},
	}

	config := &ServiceRetryConfig{
		Enabled:    boolPtr(true),
		RetryOn5xx: boolPtr(true),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := &requestContext{}
			result := rc.shouldRetry(nil, tt.statusCode, 100*time.Millisecond, config, "")
			require.Equal(t, tt.expected, result)
		})
	}
}

// TestShouldRetryPriorityOrder tests that retry conditions are checked in the correct order
func TestShouldRetryPriorityOrder(t *testing.T) {
	// Test that 5xx is checked before error-based conditions
	rc := &requestContext{}

	// 5xx error should trigger retry even without error
	config := &ServiceRetryConfig{
		Enabled:    boolPtr(true),
		RetryOn5xx: boolPtr(true),
	}
	result := rc.shouldRetry(nil, 503, 100*time.Millisecond, config, "")
	require.True(t, result, "5xx status should trigger retry even with nil error")

	// Error conditions only checked if error is not nil
	config2 := &ServiceRetryConfig{
		Enabled:        boolPtr(true),
		RetryOnTimeout: boolPtr(true),
	}
	result2 := rc.shouldRetry(nil, 200, 100*time.Millisecond, config2, "")
	require.False(t, result2, "timeout retry should not trigger with nil error")

	// Both conditions can trigger independently
	config3 := &ServiceRetryConfig{
		Enabled:        boolPtr(true),
		RetryOn5xx:     boolPtr(true),
		RetryOnTimeout: boolPtr(true),
	}
	result3 := rc.shouldRetry(errors.New("timeout"), 503, 100*time.Millisecond, config3, "")
	require.True(t, result3, "both 5xx and timeout conditions should allow retry")
}

// TestErrorStringMatching tests various error string patterns
func TestErrorStringMatching(t *testing.T) {
	tests := []struct {
		name       string
		errorMsg   string
		expectType string // "timeout", "connection", or "none"
	}{
		// Timeout patterns
		{name: "timeout word", errorMsg: "timeout", expectType: "timeout"},
		{name: "TIMEOUT uppercase", errorMsg: "TIMEOUT", expectType: "timeout"},
		{name: "TimeOut mixed", errorMsg: "TimeOut", expectType: "timeout"},
		{name: "request timeout", errorMsg: "request timeout", expectType: "timeout"},
		{name: "operation timeout", errorMsg: "operation timeout", expectType: "timeout"},
		{name: "timeout occurred", errorMsg: "timeout occurred", expectType: "timeout"},

		// Connection patterns
		{name: "connection word", errorMsg: "connection", expectType: "connection"},
		{name: "CONNECTION uppercase", errorMsg: "CONNECTION", expectType: "connection"},
		{name: "connection refused", errorMsg: "connection refused", expectType: "connection"},
		{name: "dial word", errorMsg: "dial", expectType: "connection"},
		{name: "dial tcp", errorMsg: "dial tcp: connection failed", expectType: "connection"},
		{name: "network word", errorMsg: "network", expectType: "connection"},
		{name: "network unreachable", errorMsg: "network is unreachable", expectType: "connection"},

		// Non-matching patterns
		{name: "generic error", errorMsg: "something went wrong", expectType: "none"},
		{name: "empty string", errorMsg: "", expectType: "none"},
		{name: "unrelated word", errorMsg: "validation failed", expectType: "none"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := &requestContext{}
			err := errors.New(tt.errorMsg)

			// Test timeout matching
			timeoutConfig := &ServiceRetryConfig{
				Enabled:        boolPtr(true),
				RetryOnTimeout: boolPtr(true),
			}
			timeoutMatch := rc.shouldRetry(err, 0, 100*time.Millisecond, timeoutConfig, "")
			if tt.expectType == "timeout" {
				require.True(t, timeoutMatch, "Expected timeout pattern to match for: %s", tt.errorMsg)
			} else {
				require.False(t, timeoutMatch, "Expected timeout pattern not to match for: %s", tt.errorMsg)
			}

			// Test connection matching
			connConfig := &ServiceRetryConfig{
				Enabled:           boolPtr(true),
				RetryOnConnection: boolPtr(true),
			}
			connMatch := rc.shouldRetry(err, 0, 100*time.Millisecond, connConfig, "")
			if tt.expectType == "connection" {
				require.True(t, connMatch, "Expected connection pattern to match for: %s", tt.errorMsg)
			} else {
				require.False(t, connMatch, "Expected connection pattern not to match for: %s", tt.errorMsg)
			}
		})
	}
}

// TestLowercaseConversion ensures error message comparison is truly case-insensitive
func TestLowercaseConversion(t *testing.T) {
	rc := &requestContext{}

	// Create errors with various capitalizations
	errors := []error{
		errors.New("TIMEOUT"),
		errors.New("timeout"),
		errors.New("TimeOut"),
		errors.New("TiMeOuT"),
		errors.New("CONNECTION"),
		errors.New("connection"),
		errors.New("CoNnEcTiOn"),
	}

	timeoutConfig := &ServiceRetryConfig{
		Enabled:        boolPtr(true),
		RetryOnTimeout: boolPtr(true),
	}

	connConfig := &ServiceRetryConfig{
		Enabled:           boolPtr(true),
		RetryOnConnection: boolPtr(true),
	}

	// First 4 errors should match timeout
	for i := 0; i < 4; i++ {
		result := rc.shouldRetry(errors[i], 0, 100*time.Millisecond, timeoutConfig, "")
		require.True(t, result, "Error '%s' should match timeout pattern", errors[i].Error())
	}

	// Last 3 errors should match connection
	for i := 4; i < 7; i++ {
		result := rc.shouldRetry(errors[i], 0, 100*time.Millisecond, connConfig, "")
		require.True(t, result, "Error '%s' should match connection pattern", errors[i].Error())
	}
}

// TestMultipleKeywordsInError tests errors that contain multiple keywords
func TestMultipleKeywordsInError(t *testing.T) {
	rc := &requestContext{}

	// Error with both "timeout" and "connection" - should match both conditions
	mixedErr := errors.New("connection timeout occurred")

	timeoutConfig := &ServiceRetryConfig{
		Enabled:        boolPtr(true),
		RetryOnTimeout: boolPtr(true),
	}

	connConfig := &ServiceRetryConfig{
		Enabled:           boolPtr(true),
		RetryOnConnection: boolPtr(true),
	}

	// Should match timeout condition
	timeoutResult := rc.shouldRetry(mixedErr, 0, 100*time.Millisecond, timeoutConfig, "")
	require.True(t, timeoutResult, "Error with 'timeout' should match timeout condition")

	// Should also match connection condition
	connResult := rc.shouldRetry(mixedErr, 0, 100*time.Millisecond, connConfig, "")
	require.True(t, connResult, "Error with 'connection' should match connection condition")

	// With both enabled, should still return true
	bothConfig := &ServiceRetryConfig{
		Enabled:           boolPtr(true),
		RetryOnTimeout:    boolPtr(true),
		RetryOnConnection: boolPtr(true),
	}
	bothResult := rc.shouldRetry(mixedErr, 0, 100*time.Millisecond, bothConfig, "")
	require.True(t, bothResult, "Error should match when both conditions are enabled")
}

// TestActualErrorStringsFromLogs tests real-world error strings that might appear in logs
func TestActualErrorStringsFromLogs(t *testing.T) {
	rc := &requestContext{}

	realWorldErrors := []struct {
		err         error
		shouldMatch string // "timeout", "connection", or "none"
	}{
		{
			err:         context.DeadlineExceeded,
			shouldMatch: "timeout",
		},
		{
			err:         errors.New("Get \"http://endpoint.com\": dial tcp: connection refused"),
			shouldMatch: "connection",
		},
		{
			err:         errors.New("dial tcp 10.0.0.1:8545: i/o timeout"),
			shouldMatch: "timeout",
		},
		{
			err:         errors.New("read tcp 10.0.0.1:8545->10.0.0.2:12345: connection reset by peer"),
			shouldMatch: "connection",
		},
		{
			err:         errors.New("network is unreachable"),
			shouldMatch: "connection",
		},
		{
			err:         errors.New("no route to host"),
			shouldMatch: "none",
		},
	}

	timeoutConfig := &ServiceRetryConfig{
		Enabled:        boolPtr(true),
		RetryOnTimeout: boolPtr(true),
	}

	connConfig := &ServiceRetryConfig{
		Enabled:           boolPtr(true),
		RetryOnConnection: boolPtr(true),
	}

	for _, tt := range realWorldErrors {
		t.Run(tt.err.Error(), func(t *testing.T) {
			timeoutMatch := rc.shouldRetry(tt.err, 0, 100*time.Millisecond, timeoutConfig, "")
			connMatch := rc.shouldRetry(tt.err, 0, 100*time.Millisecond, connConfig, "")

			switch tt.shouldMatch {
			case "timeout":
				require.True(t, timeoutMatch, "Expected timeout match for: %s", tt.err.Error())
				// Note: some timeout errors may also contain "dial" which matches connection
			case "connection":
				require.True(t, connMatch, "Expected connection match for: %s", tt.err.Error())
			case "none":
				require.False(t, timeoutMatch, "Expected no timeout match for: %s", tt.err.Error())
				require.False(t, connMatch, "Expected no connection match for: %s", tt.err.Error())
			}
		})
	}
}

// TestContextDeadlineExceededSpecifically tests the specific context.DeadlineExceeded error
func TestContextDeadlineExceededSpecifically(t *testing.T) {
	rc := &requestContext{}

	config := &ServiceRetryConfig{
		Enabled:        boolPtr(true),
		RetryOnTimeout: boolPtr(true),
	}

	// Test the actual context.DeadlineExceeded sentinel error
	result := rc.shouldRetry(context.DeadlineExceeded, 0, 100*time.Millisecond, config, "")
	require.True(t, result, "context.DeadlineExceeded should trigger retry")

	// Test error message containing the word "timeout"
	timeoutErr := errors.New("request timeout occurred")
	timeoutResult := rc.shouldRetry(timeoutErr, 0, 100*time.Millisecond, config, "")
	require.True(t, timeoutResult, "Error containing 'timeout' should trigger retry")
}

// TestStatusCodeWithError tests combinations of status codes and errors
func TestStatusCodeWithError(t *testing.T) {
	rc := &requestContext{}

	tests := []struct {
		name        string
		err         error
		statusCode  int
		retryConfig *ServiceRetryConfig
		expected    bool
		description string
	}{
		{
			name:       "5xx with nil error should retry",
			err:        nil,
			statusCode: 500,
			retryConfig: &ServiceRetryConfig{
				Enabled:    boolPtr(true),
				RetryOn5xx: boolPtr(true),
			},
			expected:    true,
			description: "5xx status codes should trigger retry even without an error object",
		},
		{
			name:       "5xx with timeout error - both conditions met",
			err:        errors.New("timeout"),
			statusCode: 503,
			retryConfig: &ServiceRetryConfig{
				Enabled:        boolPtr(true),
				RetryOn5xx:     boolPtr(true),
				RetryOnTimeout: boolPtr(true),
			},
			expected:    true,
			description: "Both 5xx and timeout should trigger retry",
		},
		{
			name:       "4xx with timeout error - only error condition met",
			err:        errors.New("timeout"),
			statusCode: 400,
			retryConfig: &ServiceRetryConfig{
				Enabled:        boolPtr(true),
				RetryOn5xx:     boolPtr(true),
				RetryOnTimeout: boolPtr(true),
			},
			expected:    true,
			description: "Timeout error should trigger retry even with 4xx status",
		},
		{
			name:       "2xx with timeout error - success status but timeout error",
			err:        errors.New("timeout"),
			statusCode: 200,
			retryConfig: &ServiceRetryConfig{
				Enabled:        boolPtr(true),
				RetryOn5xx:     boolPtr(true),
				RetryOnTimeout: boolPtr(true),
			},
			expected:    true,
			description: "Timeout error should trigger retry even with 2xx status",
		},
		{
			name:       "5xx but retry disabled",
			err:        nil,
			statusCode: 500,
			retryConfig: &ServiceRetryConfig{
				Enabled:    boolPtr(true),
				RetryOn5xx: boolPtr(false),
			},
			expected:    false,
			description: "5xx should not retry when RetryOn5xx is disabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := rc.shouldRetry(tt.err, tt.statusCode, 100*time.Millisecond, tt.retryConfig, "")
			require.Equal(t, tt.expected, result, tt.description)
		})
	}
}

// TestStringContainsIsCaseInsensitive validates the case-insensitive string matching
func TestStringContainsIsCaseInsensitive(t *testing.T) {
	// Verify that strings.ToLower + strings.Contains works as expected
	testCases := []struct {
		haystack string
		needle   string
		expected bool
	}{
		{"TIMEOUT", "timeout", true},
		{"timeout", "TIMEOUT", true},
		{"TimeOut", "timeout", true},
		{"Request timeout occurred", "timeout", true},
		{"some error", "timeout", false},
		{"CONNECTION REFUSED", "connection", true},
		{"Dial Failed", "dial", true},
		{"network error", "NETWORK", true},
	}

	for _, tc := range testCases {
		t.Run(tc.haystack+"_contains_"+tc.needle, func(t *testing.T) {
			result := strings.Contains(strings.ToLower(tc.haystack), strings.ToLower(tc.needle))
			require.Equal(t, tc.expected, result)
		})
	}
}

// durationPtr returns a pointer to the given duration
func durationPtr(d time.Duration) *time.Duration {
	return &d
}

// TestMaxRetryLatency tests the time budget feature for retries
func TestMaxRetryLatency(t *testing.T) {
	rc := &requestContext{}

	tests := []struct {
		name            string
		err             error
		statusCode      int
		requestDuration time.Duration
		retryConfig     *ServiceRetryConfig
		expected        bool
		description     string
	}{
		{
			name:            "request exceeds max retry latency - no retry",
			err:             errors.New("server error"),
			statusCode:      500,
			requestDuration: 600 * time.Millisecond,
			retryConfig: &ServiceRetryConfig{
				Enabled:         boolPtr(true),
				RetryOn5xx:      boolPtr(true),
				MaxRetryLatency: durationPtr(500 * time.Millisecond),
			},
			expected:    false,
			description: "Should not retry when request took longer than max retry latency",
		},
		{
			name:            "request within max retry latency - should retry",
			err:             errors.New("server error"),
			statusCode:      500,
			requestDuration: 100 * time.Millisecond,
			retryConfig: &ServiceRetryConfig{
				Enabled:         boolPtr(true),
				RetryOn5xx:      boolPtr(true),
				MaxRetryLatency: durationPtr(500 * time.Millisecond),
			},
			expected:    true,
			description: "Should retry when request took less than max retry latency",
		},
		{
			name:            "request at exact max retry latency boundary - should retry",
			err:             errors.New("server error"),
			statusCode:      500,
			requestDuration: 500 * time.Millisecond,
			retryConfig: &ServiceRetryConfig{
				Enabled:         boolPtr(true),
				RetryOn5xx:      boolPtr(true),
				MaxRetryLatency: durationPtr(500 * time.Millisecond),
			},
			expected:    true,
			description: "Should retry when request took exactly max retry latency (boundary)",
		},
		{
			name:            "nil max retry latency - should retry based on other conditions",
			err:             errors.New("server error"),
			statusCode:      500,
			requestDuration: 10 * time.Second,
			retryConfig: &ServiceRetryConfig{
				Enabled:         boolPtr(true),
				RetryOn5xx:      boolPtr(true),
				MaxRetryLatency: nil,
			},
			expected:    true,
			description: "Should retry when max retry latency is nil (no time budget check)",
		},
		{
			name:            "zero max retry latency - should retry based on other conditions",
			err:             errors.New("server error"),
			statusCode:      500,
			requestDuration: 10 * time.Second,
			retryConfig: &ServiceRetryConfig{
				Enabled:         boolPtr(true),
				RetryOn5xx:      boolPtr(true),
				MaxRetryLatency: durationPtr(0),
			},
			expected:    true,
			description: "Should retry when max retry latency is 0 (disabled)",
		},
		{
			name:            "slow timeout error exceeds budget - no retry",
			err:             errors.New("timeout after waiting"),
			statusCode:      0,
			requestDuration: 2 * time.Second,
			retryConfig: &ServiceRetryConfig{
				Enabled:         boolPtr(true),
				RetryOnTimeout:  boolPtr(true),
				MaxRetryLatency: durationPtr(500 * time.Millisecond),
			},
			expected:    false,
			description: "Should not retry timeout errors when request took too long",
		},
		{
			name:            "fast timeout error within budget - should retry",
			err:             errors.New("timeout"),
			statusCode:      0,
			requestDuration: 200 * time.Millisecond,
			retryConfig: &ServiceRetryConfig{
				Enabled:         boolPtr(true),
				RetryOnTimeout:  boolPtr(true),
				MaxRetryLatency: durationPtr(500 * time.Millisecond),
			},
			expected:    true,
			description: "Should retry fast timeout errors within time budget",
		},
		{
			name:            "connection error after long wait - no retry",
			err:             errors.New("connection refused"),
			statusCode:      0,
			requestDuration: 5 * time.Second,
			retryConfig: &ServiceRetryConfig{
				Enabled:           boolPtr(true),
				RetryOnConnection: boolPtr(true),
				MaxRetryLatency:   durationPtr(500 * time.Millisecond),
			},
			expected:    false,
			description: "Should not retry connection errors when request took too long",
		},
		{
			name:            "fast connection error - should retry",
			err:             errors.New("connection refused"),
			statusCode:      0,
			requestDuration: 50 * time.Millisecond,
			retryConfig: &ServiceRetryConfig{
				Enabled:           boolPtr(true),
				RetryOnConnection: boolPtr(true),
				MaxRetryLatency:   durationPtr(500 * time.Millisecond),
			},
			expected:    true,
			description: "Should retry fast connection errors within time budget",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := rc.shouldRetry(tt.err, tt.statusCode, tt.requestDuration, tt.retryConfig, "")
			require.Equal(t, tt.expected, result, tt.description)
		})
	}
}

// TestMaxRetryLatencyRealWorldScenarios tests realistic scenarios for time budget
func TestMaxRetryLatencyRealWorldScenarios(t *testing.T) {
	rc := &requestContext{}

	// Scenario 1: Fast 5xx failure (network issue) - should retry
	t.Run("fast_5xx_failure", func(t *testing.T) {
		config := &ServiceRetryConfig{
			Enabled:         boolPtr(true),
			RetryOn5xx:      boolPtr(true),
			MaxRetryLatency: durationPtr(500 * time.Millisecond),
		}
		// Server immediately returned 502 after 50ms
		result := rc.shouldRetry(errors.New("bad gateway"), 502, 50*time.Millisecond, config, "")
		require.True(t, result, "Fast 5xx errors should be retried")
	})

	// Scenario 2: Slow timeout (server overloaded) - should NOT retry
	t.Run("slow_timeout", func(t *testing.T) {
		config := &ServiceRetryConfig{
			Enabled:         boolPtr(true),
			RetryOn5xx:      boolPtr(true),
			RetryOnTimeout:  boolPtr(true),
			MaxRetryLatency: durationPtr(500 * time.Millisecond),
		}
		// Request timed out after 10 seconds
		result := rc.shouldRetry(context.DeadlineExceeded, 0, 10*time.Second, config, "")
		require.False(t, result, "Slow timeouts should not be retried to avoid cascading delays")
	})

	// Scenario 3: Archival query failure (expected to be slow) - custom higher budget
	t.Run("archival_query_with_higher_budget", func(t *testing.T) {
		config := &ServiceRetryConfig{
			Enabled:         boolPtr(true),
			RetryOn5xx:      boolPtr(true),
			MaxRetryLatency: durationPtr(5 * time.Second), // Higher budget for archival
		}
		// Archival query failed after 3 seconds
		result := rc.shouldRetry(errors.New("service unavailable"), 503, 3*time.Second, config, "")
		require.True(t, result, "Slow archival queries with higher time budget should be retried")
	})

	// Scenario 4: Multiple fast failures in succession
	t.Run("multiple_fast_failures", func(t *testing.T) {
		config := &ServiceRetryConfig{
			Enabled:         boolPtr(true),
			RetryOn5xx:      boolPtr(true),
			MaxRetryLatency: durationPtr(500 * time.Millisecond),
		}
		// Each attempt failed quickly - should allow retries
		for i := 0; i < 3; i++ {
			result := rc.shouldRetry(errors.New("internal server error"), 500, 100*time.Millisecond, config, "")
			require.True(t, result, "Fast failures should always be retried (attempt %d)", i+1)
		}
	})
}

// TestIsValidJSON tests the JSON validation helper function
func TestIsValidJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected bool
	}{
		{
			name:     "valid JSON object",
			input:    []byte(`{"jsonrpc":"2.0","result":"0x123","id":1}`),
			expected: true,
		},
		{
			name:     "valid JSON array",
			input:    []byte(`[{"jsonrpc":"2.0","result":"0x123","id":1}]`),
			expected: true,
		},
		{
			name:     "valid JSON-RPC error response",
			input:    []byte(`{"jsonrpc":"2.0","error":{"code":-32600,"message":"Invalid Request"},"id":null}`),
			expected: true,
		},
		{
			name:     "valid empty array",
			input:    []byte(`[]`),
			expected: true,
		},
		{
			name:     "valid empty object",
			input:    []byte(`{}`),
			expected: true,
		},
		{
			name:     "empty input is valid (per JSON-RPC batch spec)",
			input:    []byte(``),
			expected: true,
		},
		{
			name:     "nil input is valid",
			input:    nil,
			expected: true,
		},
		{
			name:     "Bad Gateway string is not valid JSON",
			input:    []byte(`Bad Gateway`),
			expected: false,
		},
		{
			name:     "HTML error page is not valid JSON",
			input:    []byte(`<!DOCTYPE html><html><body>Error</body></html>`),
			expected: false,
		},
		{
			name:     "plain text is not valid JSON",
			input:    []byte(`Internal Server Error`),
			expected: false,
		},
		{
			name:     "malformed JSON with missing quote",
			input:    []byte(`{"jsonrpc:"2.0"}`),
			expected: false,
		},
		{
			name:     "JSON with trailing garbage",
			input:    []byte(`{"valid":true}garbage`),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidJSON(tt.input)
			require.Equal(t, tt.expected, result, "isValidJSON result mismatch")
		})
	}
}

// TestValidateJSONResponse tests the JSON response validation function
func TestValidateJSONResponse(t *testing.T) {
	tests := []struct {
		name          string
		rpcType       sharedtypes.RPCType
		responseBytes []byte
		expectError   bool
	}{
		{
			name:          "valid JSON-RPC response",
			rpcType:       sharedtypes.RPCType_JSON_RPC,
			responseBytes: []byte(`{"jsonrpc":"2.0","result":"0x123","id":1}`),
			expectError:   false,
		},
		{
			name:          "valid JSON-RPC batch response",
			rpcType:       sharedtypes.RPCType_JSON_RPC,
			responseBytes: []byte(`[{"jsonrpc":"2.0","result":"0x1","id":1},{"jsonrpc":"2.0","result":"0x2","id":2}]`),
			expectError:   false,
		},
		{
			name:          "Bad Gateway string for JSON-RPC request",
			rpcType:       sharedtypes.RPCType_JSON_RPC,
			responseBytes: []byte(`Bad Gateway`),
			expectError:   true,
		},
		{
			name:          "HTML error page for JSON-RPC request",
			rpcType:       sharedtypes.RPCType_JSON_RPC,
			responseBytes: []byte(`<!DOCTYPE html><html><body>502 Bad Gateway</body></html>`),
			expectError:   true,
		},
		{
			name:          "REST request skips JSON validation (valid JSON)",
			rpcType:       sharedtypes.RPCType_REST,
			responseBytes: []byte(`{"valid":true}`),
			expectError:   false,
		},
		{
			name:          "REST request skips JSON validation (invalid JSON)",
			rpcType:       sharedtypes.RPCType_REST,
			responseBytes: []byte(`Bad Gateway`),
			expectError:   false, // REST doesn't validate JSON
		},
		{
			name:          "WebSocket request skips JSON validation",
			rpcType:       sharedtypes.RPCType_WEBSOCKET,
			responseBytes: []byte(`Bad Gateway`),
			expectError:   false, // WebSocket doesn't validate JSON
		},
		{
			name:          "Unknown RPC type skips JSON validation",
			rpcType:       sharedtypes.RPCType_UNKNOWN_RPC,
			responseBytes: []byte(`Bad Gateway`),
			expectError:   false, // Unknown doesn't validate JSON
		},
		{
			name:          "empty response for JSON-RPC is valid",
			rpcType:       sharedtypes.RPCType_JSON_RPC,
			responseBytes: []byte(``),
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateJSONResponse(tt.rpcType, tt.responseBytes)
			if tt.expectError {
				require.Error(t, err, "expected validation error")
				require.ErrorIs(t, err, errInvalidJSON, "expected errInvalidJSON")
			} else {
				require.NoError(t, err, "expected no validation error")
			}
		})
	}
}

// TestRetryOnInvalidJSONIntegration tests the full retry flow with invalid JSON
func TestRetryOnInvalidJSONIntegration(t *testing.T) {
	rc := &requestContext{}

	t.Run("invalid_json_with_retry_enabled_and_fast_response", func(t *testing.T) {
		config := &ServiceRetryConfig{
			Enabled:            boolPtr(true),
			RetryOnInvalidJSON: boolPtr(true),
			MaxRetryLatency:    durationPtr(500 * time.Millisecond),
		}
		// Response came back quickly (50ms) but with invalid JSON
		result := rc.shouldRetry(errInvalidJSON, 200, 50*time.Millisecond, config, "")
		require.True(t, result, "Fast invalid JSON responses should be retried")
	})

	t.Run("invalid_json_with_retry_enabled_but_slow_response", func(t *testing.T) {
		config := &ServiceRetryConfig{
			Enabled:            boolPtr(true),
			RetryOnInvalidJSON: boolPtr(true),
			MaxRetryLatency:    durationPtr(500 * time.Millisecond),
		}
		// Response came back slowly (1 second) with invalid JSON
		result := rc.shouldRetry(errInvalidJSON, 200, 1*time.Second, config, "")
		require.False(t, result, "Slow invalid JSON responses should NOT be retried (time budget exceeded)")
	})

	t.Run("bad_gateway_string_with_retry_enabled", func(t *testing.T) {
		config := &ServiceRetryConfig{
			Enabled:            boolPtr(true),
			RetryOnInvalidJSON: boolPtr(true),
			MaxRetryLatency:    durationPtr(500 * time.Millisecond),
		}
		// "Bad Gateway" string error (the actual error message from JSON unmarshal failure)
		err := errors.New("response is not valid JSON")
		result := rc.shouldRetry(err, 200, 50*time.Millisecond, config, "")
		require.True(t, result, "Bad Gateway string errors should be retried")
	})
}
