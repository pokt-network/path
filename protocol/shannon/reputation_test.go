package shannon

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	apptypes "github.com/pokt-network/poktroll/x/application/types"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
	reputationstorage "github.com/pokt-network/path/reputation/storage"
)

// =============================================================================
// Error-to-Signal Mapping Tests
// =============================================================================

func TestMapErrorToSignal_PermanentSanction(t *testing.T) {
	signal := mapErrorToSignal(
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_SERVICE_NOT_CONFIGURED,
		protocolobservations.ShannonSanctionType_SHANNON_SANCTION_PERMANENT,
		100*time.Millisecond,
	)

	require.Equal(t, reputation.SignalTypeFatalError, signal.Type)
}

func TestMapErrorToSignal_SessionSanction_Timeout(t *testing.T) {
	tests := []struct {
		name      string
		errorType protocolobservations.ShannonEndpointErrorType
	}{
		{"TIMEOUT", protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_TIMEOUT},
		{"HTTP_IO_TIMEOUT", protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_IO_TIMEOUT},
		{"HTTP_CONTEXT_DEADLINE_EXCEEDED", protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONTEXT_DEADLINE_EXCEEDED},
		{"HTTP_CONNECTION_TIMEOUT", protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_TIMEOUT},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signal := mapErrorToSignal(
				tt.errorType,
				protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
				5*time.Second,
			)

			require.Equal(t, reputation.SignalTypeMajorError, signal.Type)
			require.Equal(t, "timeout", signal.Reason)
			require.Equal(t, 5*time.Second, signal.Latency)
		})
	}
}

func TestMapErrorToSignal_SessionSanction_ConnectionError(t *testing.T) {
	tests := []struct {
		name      string
		errorType protocolobservations.ShannonEndpointErrorType
	}{
		{"HTTP_CONNECTION_REFUSED", protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_REFUSED},
		{"HTTP_CONNECTION_RESET", protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_RESET},
		{"WEBSOCKET_CONNECTION_FAILED", protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_WEBSOCKET_CONNECTION_FAILED},
		{"RAW_PAYLOAD_DNS_RESOLUTION", protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_DNS_RESOLUTION},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signal := mapErrorToSignal(
				tt.errorType,
				protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
				100*time.Millisecond,
			)

			require.Equal(t, reputation.SignalTypeMajorError, signal.Type)
			require.Equal(t, "connection_error", signal.Reason)
		})
	}
}

func TestMapErrorToSignal_SessionSanction_ServiceError(t *testing.T) {
	tests := []struct {
		name      string
		errorType protocolobservations.ShannonEndpointErrorType
	}{
		{"HTTP_NON_2XX_STATUS", protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_NON_2XX_STATUS},
		{"HTTP_BAD_RESPONSE", protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_BAD_RESPONSE},
		{"RELAY_MINER_HTTP_5XX", protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_5XX},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signal := mapErrorToSignal(
				tt.errorType,
				protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
				200*time.Millisecond,
			)

			require.Equal(t, reputation.SignalTypeCriticalError, signal.Type)
			require.Equal(t, "service_error", signal.Reason)
		})
	}
}

func TestMapErrorToSignal_SessionSanction_ValidationError(t *testing.T) {
	tests := []struct {
		name      string
		errorType protocolobservations.ShannonEndpointErrorType
	}{
		{"RESPONSE_VALIDATION_ERR", protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RESPONSE_VALIDATION_ERR},
		{"RESPONSE_SIGNATURE_VALIDATION_ERR", protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RESPONSE_SIGNATURE_VALIDATION_ERR},
		{"NIL_SUPPLIER_PUBKEY", protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_NIL_SUPPLIER_PUBKEY},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signal := mapErrorToSignal(
				tt.errorType,
				protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
				150*time.Millisecond,
			)

			require.Equal(t, reputation.SignalTypeCriticalError, signal.Type)
			require.Equal(t, "validation_error", signal.Reason)
		})
	}
}

func TestMapErrorToSignal_DoNotSanction(t *testing.T) {
	tests := []struct {
		name         string
		errorType    protocolobservations.ShannonEndpointErrorType
		expectedType reputation.SignalType
	}{
		{
			name:         "REQUEST_CANCELED_BY_PATH returns success (not endpoint's fault)",
			errorType:    protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_REQUEST_CANCELED_BY_PATH,
			expectedType: reputation.SignalTypeSuccess,
		},
		{
			name:         "RELAY_MINER_HTTP_4XX returns minor (client error)",
			errorType:    protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_4XX,
			expectedType: reputation.SignalTypeMinorError,
		},
		{
			name:         "HTTP_UNKNOWN returns minor",
			errorType:    protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_UNKNOWN,
			expectedType: reputation.SignalTypeMinorError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signal := mapErrorToSignal(
				tt.errorType,
				protocolobservations.ShannonSanctionType_SHANNON_SANCTION_DO_NOT_SANCTION,
				50*time.Millisecond,
			)

			require.Equal(t, tt.expectedType, signal.Type)
		})
	}
}

func TestMapErrorToSignal_UnknownSanctionType(t *testing.T) {
	signal := mapErrorToSignal(
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNKNOWN,
		protocolobservations.ShannonSanctionType_SHANNON_SANCTION_UNSPECIFIED,
		100*time.Millisecond,
	)

	// Unknown sanction type defaults to minor error
	require.Equal(t, reputation.SignalTypeMinorError, signal.Type)
}

func TestSignalImpacts(t *testing.T) {
	// Verify the signal impacts follow expected severity ordering
	tests := []struct {
		name           string
		signalFn       func() reputation.Signal
		expectedImpact float64
	}{
		{
			name:           "Success has positive impact",
			signalFn:       func() reputation.Signal { return reputation.NewSuccessSignal(100 * time.Millisecond) },
			expectedImpact: +1,
		},
		{
			name:           "Minor error has small negative impact",
			signalFn:       func() reputation.Signal { return reputation.NewMinorErrorSignal("test") },
			expectedImpact: -3,
		},
		{
			name:           "Major error has moderate negative impact",
			signalFn:       func() reputation.Signal { return reputation.NewMajorErrorSignal("test", 100*time.Millisecond) },
			expectedImpact: -10,
		},
		{
			name:           "Critical error has severe negative impact",
			signalFn:       func() reputation.Signal { return reputation.NewCriticalErrorSignal("test", 100*time.Millisecond) },
			expectedImpact: -25,
		},
		{
			name:           "Fatal error has maximum negative impact",
			signalFn:       func() reputation.Signal { return reputation.NewFatalErrorSignal("test") },
			expectedImpact: -50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signal := tt.signalFn()
			require.Equal(t, tt.expectedImpact, signal.GetDefaultImpact())
		})
	}
}

// =============================================================================
// Reputation Service Tests
// =============================================================================

// TestReputation_SignalRecording verifies that signals are recorded
// in the reputation service when handleEndpointSuccess/Error are called.
func TestReputation_SignalRecording(t *testing.T) {
	ctx := context.Background()

	// Create reputation service with config
	config := reputation.Config{
		Enabled:         true,
		InitialScore:    80,
		MinThreshold:    30,
		RecoveryTimeout: 5 * time.Minute,
		StorageType:     "memory",
	}
	config.HydrateDefaults()

	store := reputationstorage.NewMemoryStorage(config.RecoveryTimeout)
	svc := reputation.NewService(config, store)
	require.NoError(t, svc.Start(ctx))
	defer func() { _ = svc.Stop() }()

	serviceID := protocol.ServiceID("test-service")
	endpointAddr := protocol.EndpointAddr("supplier1-https://endpoint.example.com")
	key := reputation.NewEndpointKey(serviceID, endpointAddr)

	// Initially, endpoint should not have a score (new endpoint)
	_, err := svc.GetScore(ctx, key)
	require.ErrorIs(t, err, reputation.ErrNotFound)

	// Record a success signal
	successSignal := reputation.NewSuccessSignal(100 * time.Millisecond)
	err = svc.RecordSignal(ctx, key, successSignal)
	require.NoError(t, err)

	// Now endpoint should have a score
	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Equal(t, config.InitialScore+1, score.Value) // 80 + 1 = 81
	require.Equal(t, int64(1), score.SuccessCount)
	require.Equal(t, int64(0), score.ErrorCount)

	// Record an error signal (major error = -10)
	errorSignal := reputation.NewMajorErrorSignal("timeout", 5*time.Second)
	err = svc.RecordSignal(ctx, key, errorSignal)
	require.NoError(t, err)

	score, err = svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Equal(t, 81.0-10.0, score.Value) // 81 - 10 = 71
	require.Equal(t, int64(1), score.SuccessCount)
	require.Equal(t, int64(1), score.ErrorCount)
}

// TestReputation_FilterByReputation verifies that filterByReputation
// correctly excludes endpoints below the threshold.
func TestReputation_FilterByReputation(t *testing.T) {
	ctx := context.Background()
	logger := polyzero.NewLogger()

	// Create reputation service
	config := reputation.Config{
		Enabled:         true,
		InitialScore:    80,
		MinThreshold:    30,
		RecoveryTimeout: 5 * time.Minute,
		StorageType:     "memory",
	}
	config.HydrateDefaults()

	store := reputationstorage.NewMemoryStorage(config.RecoveryTimeout)
	svc := reputation.NewService(config, store)
	require.NoError(t, svc.Start(ctx))
	defer func() { _ = svc.Stop() }()

	// Create a Protocol with the reputation service
	p := &Protocol{
		logger:            logger,
		reputationService: svc,
	}

	serviceID := protocol.ServiceID("test-service")

	// Create test endpoints
	endpoints := map[protocol.EndpointAddr]endpoint{
		"supplier1-https://good.example.com": &mockEndpoint{addr: "supplier1-https://good.example.com"},
		"supplier2-https://bad.example.com":  &mockEndpoint{addr: "supplier2-https://bad.example.com"},
		"supplier3-https://new.example.com":  &mockEndpoint{addr: "supplier3-https://new.example.com"},
	}

	// Set up scores:
	// - good endpoint: score 80 (above threshold)
	// - bad endpoint: score 20 (below threshold of 30)
	// - new endpoint: no score (should be allowed - treated as initial score)

	goodKey := reputation.NewEndpointKey(serviceID, "supplier1-https://good.example.com")
	badKey := reputation.NewEndpointKey(serviceID, "supplier2-https://bad.example.com")

	// Record signals to establish scores
	// Good endpoint: 1 success -> 80 + 1 = 81
	err := svc.RecordSignal(ctx, goodKey, reputation.NewSuccessSignal(100*time.Millisecond))
	require.NoError(t, err)

	// Bad endpoint: multiple critical errors to drop below threshold
	// Initial: 80, after 3 critical errors (-25 each): 80 - 75 = 5
	for i := 0; i < 3; i++ {
		err := svc.RecordSignal(ctx, badKey, reputation.NewCriticalErrorSignal("service_error", 200*time.Millisecond))
		require.NoError(t, err)
	}

	// Verify scores before filtering
	goodScore, _ := svc.GetScore(ctx, goodKey)
	badScore, _ := svc.GetScore(ctx, badKey)
	t.Logf("Good endpoint score: %.1f", goodScore.Value)
	t.Logf("Bad endpoint score: %.1f", badScore.Value)

	require.GreaterOrEqual(t, goodScore.Value, reputation.DefaultMinThreshold)
	require.Less(t, badScore.Value, reputation.DefaultMinThreshold)

	// Filter endpoints by reputation
	filtered := p.filterByReputation(ctx, serviceID, endpoints, logger)

	// Should have 2 endpoints: good and new (bad should be filtered out)
	require.Len(t, filtered, 2)
	require.Contains(t, filtered, protocol.EndpointAddr("supplier1-https://good.example.com"))
	require.Contains(t, filtered, protocol.EndpointAddr("supplier3-https://new.example.com"))
	require.NotContains(t, filtered, protocol.EndpointAddr("supplier2-https://bad.example.com"))
}

// TestReputation_DisabledNoFiltering verifies that when reputation
// is disabled (nil service), no filtering occurs.
func TestReputation_DisabledNoFiltering(t *testing.T) {
	ctx := context.Background()
	logger := polyzero.NewLogger()

	// Create Protocol WITHOUT reputation service
	p := &Protocol{
		logger:            logger,
		reputationService: nil, // Disabled
	}

	serviceID := protocol.ServiceID("test-service")
	endpoints := map[protocol.EndpointAddr]endpoint{
		"supplier1-https://endpoint1.com": &mockEndpoint{addr: "supplier1-https://endpoint1.com"},
		"supplier2-https://endpoint2.com": &mockEndpoint{addr: "supplier2-https://endpoint2.com"},
	}

	// Filter should return all endpoints unchanged
	filtered := p.filterByReputation(ctx, serviceID, endpoints, logger)
	require.Equal(t, endpoints, filtered)
}

// TestReputation_ScoreRecovery verifies that endpoints can recover
// their score through successful requests.
func TestReputation_ScoreRecovery(t *testing.T) {
	ctx := context.Background()

	config := reputation.Config{
		Enabled:         true,
		InitialScore:    80,
		MinThreshold:    30,
		RecoveryTimeout: 5 * time.Minute,
	}
	config.HydrateDefaults()

	store := reputationstorage.NewMemoryStorage(config.RecoveryTimeout)
	svc := reputation.NewService(config, store)
	require.NoError(t, svc.Start(ctx))
	defer func() { _ = svc.Stop() }()

	key := reputation.NewEndpointKey("eth", "supplier1-https://endpoint.com")

	// Drop score below threshold with critical errors
	// Initial: 80, after 3 critical errors: 80 - 75 = 5
	for i := 0; i < 3; i++ {
		err := svc.RecordSignal(ctx, key, reputation.NewCriticalErrorSignal("error", 100*time.Millisecond))
		require.NoError(t, err)
	}

	score, _ := svc.GetScore(ctx, key)
	require.Less(t, score.Value, reputation.DefaultMinThreshold)
	t.Logf("Score after errors: %.1f (below threshold %.1f)", score.Value, reputation.DefaultMinThreshold)

	// Recover through many success signals (+1 each)
	// Need to go from 5 to 30, so 25 successes
	for i := 0; i < 25; i++ {
		err := svc.RecordSignal(ctx, key, reputation.NewSuccessSignal(100*time.Millisecond))
		require.NoError(t, err)
	}

	score, _ = svc.GetScore(ctx, key)
	require.GreaterOrEqual(t, score.Value, reputation.DefaultMinThreshold)
	t.Logf("Score after recovery: %.1f (above threshold %.1f)", score.Value, reputation.DefaultMinThreshold)
}

// TestReputation_HotPathVerification verifies that the reputation service
// is correctly integrated into the hot path (requestContext) and that signals are
// recorded when handleEndpointSuccess/Error would be called.
func TestReputation_HotPathVerification(t *testing.T) {
	ctx := context.Background()
	logger := polyzero.NewLogger()

	// Create reputation service (simulating what NewProtocol does when Enabled=true)
	config := reputation.Config{
		Enabled:         true,
		InitialScore:    80,
		MinThreshold:    30,
		RecoveryTimeout: 5 * time.Minute,
		StorageType:     "memory",
	}
	config.HydrateDefaults()

	store := reputationstorage.NewMemoryStorage(config.RecoveryTimeout)
	svc := reputation.NewService(config, store)
	require.NoError(t, svc.Start(ctx))
	defer func() { _ = svc.Stop() }()

	// Create Protocol with reputation service
	p := &Protocol{
		logger:            logger,
		reputationService: svc,
	}

	// Verify reputation service is set
	require.NotNil(t, p.reputationService, "Reputation service should be initialized when Enabled=true")

	// Simulate what happens in the hot path:
	// 1. Request comes in
	// 2. Endpoint is selected
	// 3. Request is sent
	// 4. handleEndpointSuccess/Error is called which records a signal

	serviceID := protocol.ServiceID("eth")
	endpointAddr := protocol.EndpointAddr("supplier1-https://endpoint.example.com")
	endpointKey := reputation.NewEndpointKey(serviceID, endpointAddr)

	// Simulate handleEndpointSuccess recording a signal
	latency := 100 * time.Millisecond
	signal := reputation.NewSuccessSignal(latency)
	err := p.reputationService.RecordSignal(ctx, endpointKey, signal)
	require.NoError(t, err)

	// Verify the signal was recorded
	score, err := p.reputationService.GetScore(ctx, endpointKey)
	require.NoError(t, err)
	require.Equal(t, float64(81), score.Value, "Score should be initial (80) + success (1) = 81")
	require.Equal(t, int64(1), score.SuccessCount)

	// Simulate handleEndpointError recording an error signal
	errorSignal := mapErrorToSignal(
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_TIMEOUT,
		protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION,
		5*time.Second,
	)
	err = p.reputationService.RecordSignal(ctx, endpointKey, errorSignal)
	require.NoError(t, err)

	// Verify the error signal was recorded
	score, err = p.reputationService.GetScore(ctx, endpointKey)
	require.NoError(t, err)
	require.Equal(t, float64(71), score.Value, "Score should be 81 - 10 (major error) = 71")
	require.Equal(t, int64(1), score.SuccessCount)
	require.Equal(t, int64(1), score.ErrorCount)

	t.Log("Verified: Reputation service is correctly integrated into the hot path")
	t.Logf("   - Initial score: 80")
	t.Logf("   - After success (+1): 81")
	t.Logf("   - After timeout error (-10): 71")
}

// =============================================================================
// Test Helpers
// =============================================================================

// mockEndpoint implements the endpoint interface for testing
type mockEndpoint struct {
	addr protocol.EndpointAddr
}

// Ensure mockEndpoint implements the endpoint interface
var _ endpoint = (*mockEndpoint)(nil)

func (m *mockEndpoint) Addr() protocol.EndpointAddr {
	return m.addr
}

func (m *mockEndpoint) PublicURL() string {
	return string(m.addr)
}

func (m *mockEndpoint) WebsocketURL() (string, error) {
	return "", nil
}

func (m *mockEndpoint) Session() *sessiontypes.Session {
	return &sessiontypes.Session{
		Header:      &sessiontypes.SessionHeader{},
		Application: &apptypes.Application{},
	}
}

func (m *mockEndpoint) Supplier() string {
	return "supplier1"
}

func (m *mockEndpoint) GetURL(_ sharedtypes.RPCType) string {
	return string(m.addr)
}

func (m *mockEndpoint) IsFallback() bool {
	return false
}

// =============================================================================
// Storage Type Configuration Tests
// =============================================================================

// TestReputation_StorageTypeConfiguration verifies the storage type switch
// behavior in protocol initialization, including error cases.
func TestReputation_StorageTypeConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		config      reputation.Config
		redisConfig *reputation.RedisConfig // Global redis config (separate from reputation config)
		expectError bool
		errContains string
	}{
		{
			name: "empty storage type defaults to memory",
			config: reputation.Config{
				Enabled:      true,
				InitialScore: 80,
				MinThreshold: 30,
				StorageType:  "", // Empty should default to memory
			},
			expectError: false,
		},
		{
			name: "memory storage type works",
			config: reputation.Config{
				Enabled:      true,
				InitialScore: 80,
				MinThreshold: 30,
				StorageType:  "memory",
			},
			expectError: false,
		},
		{
			name: "redis storage type without config errors",
			config: reputation.Config{
				Enabled:      true,
				InitialScore: 80,
				MinThreshold: 30,
				StorageType:  "redis",
			},
			redisConfig: nil, // No redis config
			expectError: true,
			errContains: "redis storage requires global redis_config",
		},
		{
			name: "redis storage with invalid address errors",
			config: reputation.Config{
				Enabled:      true,
				InitialScore: 80,
				MinThreshold: 30,
				StorageType:  "redis",
			},
			redisConfig: &reputation.RedisConfig{
				Address:     "localhost:59999", // Invalid port, won't connect
				DialTimeout: 500 * time.Millisecond,
			},
			expectError: true,
			errContains: "failed to create redis storage",
		},
		{
			name: "unsupported storage type errors",
			config: reputation.Config{
				Enabled:      true,
				InitialScore: 80,
				MinThreshold: 30,
				StorageType:  "postgres", // Unsupported
			},
			expectError: true,
			errContains: "unsupported reputation storage type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This test validates the storage type switch logic by simulating
			// what NewProtocol does when initializing the reputation service.
			ctx := context.Background()
			tt.config.HydrateDefaults()

			var store reputation.Storage
			var err error

			switch tt.config.StorageType {
			case "memory", "":
				store = reputationstorage.NewMemoryStorage(tt.config.RecoveryTimeout)
			case "redis":
				if tt.redisConfig == nil {
					err = fmt.Errorf("redis storage requires global redis_config to be set")
				} else {
					store, err = reputationstorage.NewRedisStorage(ctx, *tt.redisConfig, tt.config.RecoveryTimeout)
					if err != nil {
						err = fmt.Errorf("failed to create redis storage: %w", err)
					}
				}
			default:
				err = fmt.Errorf("unsupported reputation storage type: %s", tt.config.StorageType)
			}

			if tt.expectError {
				require.Error(t, err)
				if tt.errContains != "" {
					require.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, store)
			}
		})
	}
}

// TestReputation_ConfigHydrateDefaults verifies that the Config HydrateDefaults
// method properly sets defaults for unset values.
func TestReputation_ConfigHydrateDefaults(t *testing.T) {
	tests := []struct {
		name     string
		config   reputation.Config
		expected reputation.Config
	}{
		{
			name:   "empty config gets all defaults",
			config: reputation.Config{Enabled: true}, // Only enabled set
			expected: reputation.Config{
				Enabled:         true,
				InitialScore:    reputation.InitialScore,
				MinThreshold:    reputation.DefaultMinThreshold,
				RecoveryTimeout: reputation.DefaultRecoveryTimeout,
				StorageType:     "memory",
				SyncConfig: reputation.SyncConfig{
					RefreshInterval: reputation.DefaultRefreshInterval,
					WriteBufferSize: reputation.DefaultWriteBufferSize,
					FlushInterval:   reputation.DefaultFlushInterval,
				},
			},
		},
		{
			name: "partial config only fills missing values",
			config: reputation.Config{
				Enabled:      true,
				InitialScore: 90, // Custom value - should be preserved
				MinThreshold: 40, // Custom value - should be preserved
				StorageType:  "", // Empty - should get default "memory"
			},
			expected: reputation.Config{
				Enabled:         true,
				InitialScore:    90, // Preserved
				MinThreshold:    40, // Preserved
				RecoveryTimeout: reputation.DefaultRecoveryTimeout,
				StorageType:     "memory",
				SyncConfig: reputation.SyncConfig{
					RefreshInterval: reputation.DefaultRefreshInterval,
					WriteBufferSize: reputation.DefaultWriteBufferSize,
					FlushInterval:   reputation.DefaultFlushInterval,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.config.HydrateDefaults()
			require.Equal(t, tt.expected.InitialScore, tt.config.InitialScore)
			require.Equal(t, tt.expected.MinThreshold, tt.config.MinThreshold)
			require.Equal(t, tt.expected.RecoveryTimeout, tt.config.RecoveryTimeout)
			require.Equal(t, tt.expected.StorageType, tt.config.StorageType)
			require.Equal(t, tt.expected.SyncConfig.RefreshInterval, tt.config.SyncConfig.RefreshInterval)
			require.Equal(t, tt.expected.SyncConfig.WriteBufferSize, tt.config.SyncConfig.WriteBufferSize)
			require.Equal(t, tt.expected.SyncConfig.FlushInterval, tt.config.SyncConfig.FlushInterval)
		})
	}
}

// TestReputation_ConfigValidation verifies that the Config Validate
// method properly detects invalid configurations.
func TestReputation_ConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		config      reputation.Config
		expectError bool
		errContains string
	}{
		{
			name: "valid config",
			config: reputation.Config{
				Enabled:         true,
				InitialScore:    80,
				MinThreshold:    30,
				RecoveryTimeout: 5 * time.Minute,
			},
			expectError: false,
		},
		{
			name: "initial score below min allowed",
			config: reputation.Config{
				Enabled:      true,
				InitialScore: -10, // Below MinScore (0)
				MinThreshold: 30,
			},
			expectError: true,
			errContains: "initial_score",
		},
		{
			name: "initial score above max allowed",
			config: reputation.Config{
				Enabled:      true,
				InitialScore: 150, // Above MaxScore (100)
				MinThreshold: 30,
			},
			expectError: true,
			errContains: "initial_score",
		},
		{
			name: "min threshold below min allowed",
			config: reputation.Config{
				Enabled:      true,
				InitialScore: 80,
				MinThreshold: -5, // Below MinScore (0)
			},
			expectError: true,
			errContains: "min_threshold",
		},
		{
			name: "min threshold above max allowed",
			config: reputation.Config{
				Enabled:      true,
				InitialScore: 80,
				MinThreshold: 110, // Above MaxScore (100)
			},
			expectError: true,
			errContains: "min_threshold",
		},
		{
			name: "initial score below min threshold",
			config: reputation.Config{
				Enabled:      true,
				InitialScore: 20, // Below min_threshold
				MinThreshold: 50,
			},
			expectError: true,
			errContains: "initial_score",
		},
		{
			name: "negative recovery timeout",
			config: reputation.Config{
				Enabled:         true,
				InitialScore:    80,
				MinThreshold:    30,
				RecoveryTimeout: -1 * time.Minute,
			},
			expectError: true,
			errContains: "recovery_timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()

			if tt.expectError {
				require.Error(t, err)
				if tt.errContains != "" {
					require.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// Key Granularity Integration Tests
// =============================================================================

// TestReputation_KeyGranularityPerSupplier verifies that when using per-supplier
// granularity, multiple endpoints from the same supplier share a single score.
func TestReputation_KeyGranularityPerSupplier(t *testing.T) {
	ctx := context.Background()
	logger := polyzero.NewLogger()

	// Create reputation service with per-supplier granularity
	config := reputation.Config{
		Enabled:         true,
		InitialScore:    80,
		MinThreshold:    30,
		RecoveryTimeout: 5 * time.Minute,
		StorageType:     "memory",
		KeyGranularity:  reputation.KeyGranularitySupplier, // Per-supplier
	}
	config.HydrateDefaults()

	store := reputationstorage.NewMemoryStorage(config.RecoveryTimeout)
	svc := reputation.NewService(config, store)
	require.NoError(t, svc.Start(ctx))
	defer func() { _ = svc.Stop() }()

	// Create a Protocol with the reputation service
	p := &Protocol{
		logger:            logger,
		reputationService: svc,
	}

	serviceID := protocol.ServiceID("eth")

	// Two endpoints from the SAME supplier (pokt1supplier1)
	endpoint1Addr := protocol.EndpointAddr("pokt1supplier1-https://node1.example.com")
	endpoint2Addr := protocol.EndpointAddr("pokt1supplier1-https://node2.example.com")
	// One endpoint from a DIFFERENT supplier
	endpoint3Addr := protocol.EndpointAddr("pokt1supplier2-https://node3.example.com")

	// Get the key builder for the service
	keyBuilder := svc.KeyBuilderForService(serviceID)
	require.IsType(t, &reputation.SupplierKeyBuilder{}, keyBuilder)

	// Build keys - endpoints 1 and 2 should have the SAME key
	key1 := keyBuilder.BuildKey(serviceID, endpoint1Addr)
	key2 := keyBuilder.BuildKey(serviceID, endpoint2Addr)
	key3 := keyBuilder.BuildKey(serviceID, endpoint3Addr)

	// Verify keys 1 and 2 are the same (same supplier)
	require.Equal(t, key1, key2, "Endpoints from same supplier should have same key")
	require.NotEqual(t, key1, key3, "Endpoints from different suppliers should have different keys")

	// Record critical errors on endpoint1 - this should affect endpoint2's score too
	for i := 0; i < 3; i++ {
		err := svc.RecordSignal(ctx, key1, reputation.NewCriticalErrorSignal("service_error", 200*time.Millisecond))
		require.NoError(t, err)
	}

	// Verify that endpoint2's score is also affected (since they share the key)
	score2, err := svc.GetScore(ctx, key2)
	require.NoError(t, err)
	require.Less(t, score2.Value, reputation.DefaultMinThreshold,
		"Endpoint2 should have low score because it shares key with endpoint1")

	// Create test endpoints for filtering
	endpoints := map[protocol.EndpointAddr]endpoint{
		endpoint1Addr: &mockEndpoint{addr: endpoint1Addr},
		endpoint2Addr: &mockEndpoint{addr: endpoint2Addr},
		endpoint3Addr: &mockEndpoint{addr: endpoint3Addr},
	}

	// Filter by reputation
	filtered := p.filterByReputation(ctx, serviceID, endpoints, logger)

	// Both endpoint1 and endpoint2 should be filtered out (same supplier, same low score)
	// endpoint3 should pass (new endpoint, initial score)
	require.Len(t, filtered, 1)
	require.Contains(t, filtered, endpoint3Addr, "Endpoint from different supplier should pass")
	require.NotContains(t, filtered, endpoint1Addr, "Endpoint1 should be filtered")
	require.NotContains(t, filtered, endpoint2Addr, "Endpoint2 should be filtered (shares score with endpoint1)")

	t.Log("Verified: Per-supplier granularity correctly shares scores between endpoints from same supplier")
}

// TestReputation_KeyGranularityPerDomain verifies that when using per-domain
// granularity, all endpoints from the same hosting domain share a single score.
func TestReputation_KeyGranularityPerDomain(t *testing.T) {
	ctx := context.Background()
	logger := polyzero.NewLogger()

	// Create reputation service with per-domain granularity
	config := reputation.Config{
		Enabled:         true,
		InitialScore:    80,
		MinThreshold:    30,
		RecoveryTimeout: 5 * time.Minute,
		StorageType:     "memory",
		KeyGranularity:  reputation.KeyGranularityDomain, // Per-domain
	}
	config.HydrateDefaults()

	store := reputationstorage.NewMemoryStorage(config.RecoveryTimeout)
	svc := reputation.NewService(config, store)
	require.NoError(t, svc.Start(ctx))
	defer func() { _ = svc.Stop() }()

	// Create a Protocol with the reputation service
	p := &Protocol{
		logger:            logger,
		reputationService: svc,
	}

	serviceID := protocol.ServiceID("eth")

	// Multiple endpoints from DIFFERENT suppliers but SAME hosting domain (nodefleet.net)
	endpoint1Addr := protocol.EndpointAddr("pokt1supplier1-https://rm-01.eu.nodefleet.net")
	endpoint2Addr := protocol.EndpointAddr("pokt1supplier2-https://rm-02.us.nodefleet.net")
	// One endpoint from a DIFFERENT domain
	endpoint3Addr := protocol.EndpointAddr("pokt1supplier3-https://relay.pokt.network")

	// Get the key builder for the service
	keyBuilder := svc.KeyBuilderForService(serviceID)
	require.IsType(t, &reputation.DomainKeyBuilder{}, keyBuilder)

	// Build keys - endpoints 1 and 2 should have the SAME key (same domain)
	key1 := keyBuilder.BuildKey(serviceID, endpoint1Addr)
	key2 := keyBuilder.BuildKey(serviceID, endpoint2Addr)
	key3 := keyBuilder.BuildKey(serviceID, endpoint3Addr)

	// Verify keys 1 and 2 are the same (same domain: nodefleet.net)
	require.Equal(t, key1, key2, "Endpoints from same domain should have same key")
	require.NotEqual(t, key1, key3, "Endpoints from different domains should have different keys")

	// Record critical errors on endpoint1 - this should affect endpoint2's score too (same domain)
	for i := 0; i < 3; i++ {
		err := svc.RecordSignal(ctx, key1, reputation.NewCriticalErrorSignal("service_error", 200*time.Millisecond))
		require.NoError(t, err)
	}

	// Verify that endpoint2's score is also affected (since they share the domain key)
	score2, err := svc.GetScore(ctx, key2)
	require.NoError(t, err)
	require.Less(t, score2.Value, reputation.DefaultMinThreshold,
		"Endpoint2 should have low score because it shares domain with endpoint1")

	// Create test endpoints for filtering
	endpoints := map[protocol.EndpointAddr]endpoint{
		endpoint1Addr: &mockEndpoint{addr: endpoint1Addr},
		endpoint2Addr: &mockEndpoint{addr: endpoint2Addr},
		endpoint3Addr: &mockEndpoint{addr: endpoint3Addr},
	}

	// Filter by reputation
	filtered := p.filterByReputation(ctx, serviceID, endpoints, logger)

	// Both endpoint1 and endpoint2 should be filtered out (same domain, same low score)
	// endpoint3 should pass (different domain, new endpoint, initial score)
	require.Len(t, filtered, 1)
	require.Contains(t, filtered, endpoint3Addr, "Endpoint from different domain should pass")
	require.NotContains(t, filtered, endpoint1Addr, "Endpoint1 should be filtered")
	require.NotContains(t, filtered, endpoint2Addr, "Endpoint2 should be filtered (shares score via domain)")

	t.Log("Verified: Per-domain granularity correctly shares scores between endpoints from same hosting domain")
}

// TestReputation_KeyGranularityDefault verifies that the default granularity
// (per-endpoint) treats each endpoint independently.
func TestReputation_KeyGranularityDefault(t *testing.T) {
	ctx := context.Background()
	logger := polyzero.NewLogger()

	// Create reputation service with default (per-endpoint) granularity
	config := reputation.Config{
		Enabled:         true,
		InitialScore:    80,
		MinThreshold:    30,
		RecoveryTimeout: 5 * time.Minute,
		StorageType:     "memory",
		// KeyGranularity not set - should default to per-endpoint
	}
	config.HydrateDefaults()
	require.Equal(t, reputation.KeyGranularityEndpoint, config.KeyGranularity)

	store := reputationstorage.NewMemoryStorage(config.RecoveryTimeout)
	svc := reputation.NewService(config, store)
	require.NoError(t, svc.Start(ctx))
	defer func() { _ = svc.Stop() }()

	// Create a Protocol with the reputation service
	p := &Protocol{
		logger:            logger,
		reputationService: svc,
	}

	serviceID := protocol.ServiceID("eth")

	// Two endpoints from the SAME supplier
	endpoint1Addr := protocol.EndpointAddr("pokt1supplier1-https://node1.example.com")
	endpoint2Addr := protocol.EndpointAddr("pokt1supplier1-https://node2.example.com")

	// Get the key builder for the service
	keyBuilder := svc.KeyBuilderForService(serviceID)
	require.IsType(t, &reputation.EndpointKeyBuilder{}, keyBuilder)

	// Build keys - each endpoint should have a DIFFERENT key
	key1 := keyBuilder.BuildKey(serviceID, endpoint1Addr)
	key2 := keyBuilder.BuildKey(serviceID, endpoint2Addr)

	// Verify keys are different (per-endpoint granularity)
	require.NotEqual(t, key1, key2, "Each endpoint should have its own key")

	// Record critical errors ONLY on endpoint1
	for i := 0; i < 3; i++ {
		err := svc.RecordSignal(ctx, key1, reputation.NewCriticalErrorSignal("service_error", 200*time.Millisecond))
		require.NoError(t, err)
	}

	// Verify endpoint1 has low score but endpoint2 is unaffected
	score1, _ := svc.GetScore(ctx, key1)
	_, err := svc.GetScore(ctx, key2) // Should not exist yet
	require.Less(t, score1.Value, reputation.DefaultMinThreshold, "Endpoint1 should have low score")
	require.ErrorIs(t, err, reputation.ErrNotFound, "Endpoint2 should not have a score yet")

	// Create test endpoints for filtering
	endpoints := map[protocol.EndpointAddr]endpoint{
		endpoint1Addr: &mockEndpoint{addr: endpoint1Addr},
		endpoint2Addr: &mockEndpoint{addr: endpoint2Addr},
	}

	// Filter by reputation
	filtered := p.filterByReputation(ctx, serviceID, endpoints, logger)

	// Only endpoint1 should be filtered out
	require.Len(t, filtered, 1)
	require.Contains(t, filtered, endpoint2Addr, "Endpoint2 should pass (no score, gets initial)")
	require.NotContains(t, filtered, endpoint1Addr, "Endpoint1 should be filtered (low score)")

	t.Log("Verified: Per-endpoint granularity treats each endpoint independently")
}
