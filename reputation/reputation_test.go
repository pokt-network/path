package reputation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

func TestEndpointKey_String(t *testing.T) {
	tests := []struct {
		name     string
		key      EndpointKey
		expected string
	}{
		{
			name: "basic key",
			key: EndpointKey{
				ServiceID:    "eth",
				EndpointAddr: "supplier1-https://endpoint.com",
			},
			expected: "eth:supplier1-https://endpoint.com",
		},
		{
			name: "key with special characters in URL",
			key: EndpointKey{
				ServiceID:    "poly",
				EndpointAddr: "pokt1abc-https://relay.example.com:8545/rpc",
			},
			expected: "poly:pokt1abc-https://relay.example.com:8545/rpc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, tt.key.String())
		})
	}
}

func TestNewEndpointKey(t *testing.T) {
	serviceID := protocol.ServiceID("eth")
	endpointAddr := protocol.EndpointAddr("supplier1-https://endpoint.com")

	key := NewEndpointKey(serviceID, endpointAddr)

	require.Equal(t, serviceID, key.ServiceID)
	require.Equal(t, endpointAddr, key.EndpointAddr)
}

func TestScore_IsValid(t *testing.T) {
	tests := []struct {
		name     string
		score    Score
		expected bool
	}{
		{
			name:     "valid score at min",
			score:    Score{Value: MinScore},
			expected: true,
		},
		{
			name:     "valid score at max",
			score:    Score{Value: MaxScore},
			expected: true,
		},
		{
			name:     "valid score in middle",
			score:    Score{Value: 50},
			expected: true,
		},
		{
			name:     "invalid score below min",
			score:    Score{Value: -1},
			expected: false,
		},
		{
			name:     "invalid score above max",
			score:    Score{Value: 101},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, tt.score.IsValid())
		})
	}
}

func TestConfig_HydrateDefaults(t *testing.T) {
	tests := []struct {
		name         string
		input        Config
		expectedInit float64
		expectedMin  float64
		expectedType string
	}{
		{
			name:         "empty config gets all defaults",
			input:        Config{},
			expectedInit: InitialScore,
			expectedMin:  DefaultMinThreshold,
			expectedType: "memory",
		},
		{
			name: "custom values preserved",
			input: Config{
				InitialScore: 90,
				MinThreshold: 40,
				StorageType:  "redis",
			},
			expectedInit: 90,
			expectedMin:  40,
			expectedType: "redis",
		},
		{
			name: "partial config - only initial score set",
			input: Config{
				InitialScore: 75,
			},
			expectedInit: 75,
			expectedMin:  DefaultMinThreshold,
			expectedType: "memory",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.input.HydrateDefaults()

			require.Equal(t, tt.expectedInit, tt.input.InitialScore)
			require.Equal(t, tt.expectedMin, tt.input.MinThreshold)
			require.Equal(t, tt.expectedType, tt.input.StorageType)
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	require.False(t, config.Enabled, "should be disabled by default")
	require.Equal(t, InitialScore, config.InitialScore)
	require.Equal(t, DefaultMinThreshold, config.MinThreshold)
	require.Equal(t, DefaultRecoveryTimeout, config.RecoveryTimeout)
	require.Equal(t, "memory", config.StorageType)

	// Verify SyncConfig defaults are included
	require.Equal(t, DefaultRefreshInterval, config.SyncConfig.RefreshInterval)
	require.Equal(t, DefaultWriteBufferSize, config.SyncConfig.WriteBufferSize)
	require.Equal(t, DefaultFlushInterval, config.SyncConfig.FlushInterval)
}

func TestScoreConstants(t *testing.T) {
	// Verify score constants are sensible
	require.Less(t, MinScore, MaxScore, "MinScore should be less than MaxScore")
	require.GreaterOrEqual(t, InitialScore, MinScore, "InitialScore should be >= MinScore")
	require.LessOrEqual(t, InitialScore, MaxScore, "InitialScore should be <= MaxScore")
	require.GreaterOrEqual(t, DefaultMinThreshold, MinScore, "DefaultMinThreshold should be >= MinScore")
	require.Less(t, DefaultMinThreshold, InitialScore, "DefaultMinThreshold should be < InitialScore")
}

func TestDefaultSyncConfig(t *testing.T) {
	config := DefaultSyncConfig()

	require.Equal(t, DefaultRefreshInterval, config.RefreshInterval)
	require.Equal(t, DefaultWriteBufferSize, config.WriteBufferSize)
	require.Equal(t, DefaultFlushInterval, config.FlushInterval)
}

func TestSyncConfig_HydrateDefaults(t *testing.T) {
	tests := []struct {
		name            string
		input           SyncConfig
		expectedRefresh time.Duration
		expectedBuffer  int
		expectedFlush   time.Duration
	}{
		{
			name:            "empty config gets all defaults",
			input:           SyncConfig{},
			expectedRefresh: DefaultRefreshInterval,
			expectedBuffer:  DefaultWriteBufferSize,
			expectedFlush:   DefaultFlushInterval,
		},
		{
			name: "custom values preserved",
			input: SyncConfig{
				RefreshInterval: 10 * time.Second,
				WriteBufferSize: 500,
				FlushInterval:   200 * time.Millisecond,
			},
			expectedRefresh: 10 * time.Second,
			expectedBuffer:  500,
			expectedFlush:   200 * time.Millisecond,
		},
		{
			name: "partial config - only refresh interval set",
			input: SyncConfig{
				RefreshInterval: 3 * time.Second,
			},
			expectedRefresh: 3 * time.Second,
			expectedBuffer:  DefaultWriteBufferSize,
			expectedFlush:   DefaultFlushInterval,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.input.HydrateDefaults()

			require.Equal(t, tt.expectedRefresh, tt.input.RefreshInterval)
			require.Equal(t, tt.expectedBuffer, tt.input.WriteBufferSize)
			require.Equal(t, tt.expectedFlush, tt.input.FlushInterval)
		})
	}
}

func TestConfig_HydrateDefaults_IncludesSyncConfig(t *testing.T) {
	config := Config{}
	config.HydrateDefaults()

	// Verify SyncConfig was also hydrated
	require.Equal(t, DefaultRefreshInterval, config.SyncConfig.RefreshInterval)
	require.Equal(t, DefaultWriteBufferSize, config.SyncConfig.WriteBufferSize)
	require.Equal(t, DefaultFlushInterval, config.SyncConfig.FlushInterval)
}
