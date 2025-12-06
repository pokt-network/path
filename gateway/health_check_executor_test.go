package gateway

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"
)

// TestExternalConfigFetching tests that external config is fetched and parsed correctly.
func TestExternalConfigFetching(t *testing.T) {
	// Use the gist URL provided by the user
	externalURL := "https://gist.githubusercontent.com/jorgecuesta/7347e24e09b726118e4201ec945e8588/raw/e5a4da188ba94569aed5499881c58d7715b31612/path_health_checks.yaml"

	// Create a minimal config with external URL
	config := &ActiveHealthChecksConfig{
		Enabled: true,
		External: &ExternalConfigSource{
			URL:     externalURL,
			Timeout: 30 * time.Second,
		},
	}

	// Create a mock reputation service for testing
	executor := &HealthCheckExecutor{
		config: config,
		logger: polyzero.NewLogger(),
	}

	// Initialize the HTTP client
	executor.httpClient = &http.Client{
		Timeout: 30 * time.Second,
	}

	// Fetch external config
	ctx := context.Background()
	executor.refreshExternalConfig(ctx)

	// Check that external configs were loaded
	executor.externalConfigMu.RLock()
	configs := executor.externalConfigs
	configError := executor.externalConfigError
	executor.externalConfigMu.RUnlock()

	require.NoError(t, configError, "Expected no error fetching external config")
	require.NotEmpty(t, configs, "Expected external configs to be loaded")

	// Verify we got the expected services
	serviceIDs := make(map[string]bool)
	for _, cfg := range configs {
		serviceIDs[string(cfg.ServiceID)] = true
	}

	// The gist should contain these services
	expectedServices := []string{"eth", "base", "bsc", "xrplevm", "solana", "stargaze", "shentu"}
	for _, svc := range expectedServices {
		require.True(t, serviceIDs[svc], "Expected service %s to be in external config", svc)
	}

	t.Logf("Successfully loaded %d services from external config", len(configs))

	// Verify eth service has the archival check
	var ethConfig *ServiceHealthCheckConfig
	for i := range configs {
		if configs[i].ServiceID == "eth" {
			ethConfig = &configs[i]
			break
		}
	}
	require.NotNil(t, ethConfig, "Expected eth config to exist")

	// Check eth has the archival check
	hasArchivalCheck := false
	for _, check := range ethConfig.Checks {
		if check.Name == "eth_archival" {
			hasArchivalCheck = true
			require.Equal(t, "critical_error", check.ReputationSignal, "Expected archival check to have critical_error signal")
			require.Contains(t, check.ExpectedResponseContains, "0x314214a541a8e719f516", "Expected archival check to have expected response")
		}
	}
	require.True(t, hasArchivalCheck, "Expected eth config to have archival check")
}

// TestConfigMerging tests that local configs override external configs correctly.
func TestConfigMerging(t *testing.T) {
	executor := &HealthCheckExecutor{
		config: &ActiveHealthChecksConfig{},
		logger: polyzero.NewLogger(),
	}

	// Create external configs
	enabled := true
	external := []ServiceHealthCheckConfig{
		{
			ServiceID:     "eth",
			CheckInterval: 10 * time.Second,
			Enabled:       &enabled,
			Checks: []HealthCheckConfig{
				{Name: "eth_blockNumber", Type: HealthCheckTypeJSONRPC, Method: "POST", Path: "/", ReputationSignal: "minor_error"},
				{Name: "eth_chainId", Type: HealthCheckTypeJSONRPC, Method: "POST", Path: "/", ReputationSignal: "minor_error"},
			},
		},
		{
			ServiceID: "solana",
			Enabled:   &enabled,
			Checks: []HealthCheckConfig{
				{Name: "solana_getHealth", Type: HealthCheckTypeJSONRPC, Method: "POST", Path: "/"},
			},
		},
	}

	// Create local configs - eth_chainId should override external, polygon is local-only
	local := []ServiceHealthCheckConfig{
		{
			ServiceID:     "eth",
			CheckInterval: 5 * time.Second, // Different interval than external
			Checks: []HealthCheckConfig{
				// Override eth_chainId with different signal
				{Name: "eth_chainId", Type: HealthCheckTypeJSONRPC, Method: "POST", Path: "/", ReputationSignal: "major_error"},
				// Add local-only check
				{Name: "eth_gasPrice", Type: HealthCheckTypeJSONRPC, Method: "POST", Path: "/"},
			},
		},
		{
			ServiceID: "polygon", // Local-only service
			Checks: []HealthCheckConfig{
				{Name: "poly_blockNumber", Type: HealthCheckTypeJSONRPC, Method: "POST", Path: "/"},
			},
		},
	}

	// Merge configs
	merged := executor.mergeConfigs(external, local)

	// Verify merged result
	require.Len(t, merged, 3, "Expected 3 services: eth (merged), solana (external-only), polygon (local-only)")

	// Find services by ID
	serviceMap := make(map[string]ServiceHealthCheckConfig)
	for _, cfg := range merged {
		serviceMap[string(cfg.ServiceID)] = cfg
	}

	// Check eth - should have local interval and merged checks
	ethCfg := serviceMap["eth"]
	require.Equal(t, 5*time.Second, ethCfg.CheckInterval, "Expected local interval to override external")
	require.Len(t, ethCfg.Checks, 3, "Expected 3 checks: eth_blockNumber (external), eth_chainId (local override), eth_gasPrice (local-only)")

	// Verify eth_chainId has local signal
	var chainIdCheck *HealthCheckConfig
	for i := range ethCfg.Checks {
		if ethCfg.Checks[i].Name == "eth_chainId" {
			chainIdCheck = &ethCfg.Checks[i]
			break
		}
	}
	require.NotNil(t, chainIdCheck)
	require.Equal(t, "major_error", chainIdCheck.ReputationSignal, "Expected local signal to override external")

	// Check solana - should be external-only
	solanaCfg := serviceMap["solana"]
	require.Len(t, solanaCfg.Checks, 1)
	require.Equal(t, "solana_getHealth", solanaCfg.Checks[0].Name)

	// Check polygon - should be local-only
	polygonCfg := serviceMap["polygon"]
	require.Len(t, polygonCfg.Checks, 1)
	require.Equal(t, "poly_blockNumber", polygonCfg.Checks[0].Name)

	t.Log("Config merging test passed")
}

// TestMapSignalType tests the mapping of signal type strings to reputation signals.
func TestMapSignalType(t *testing.T) {
	executor := &HealthCheckExecutor{
		logger: polyzero.NewLogger(),
	}

	tests := []struct {
		name       string
		signalType string
		reason     string
		latency    time.Duration
		wantType   string
	}{
		{
			name:       "minor_error signal",
			signalType: "minor_error",
			reason:     "test reason",
			latency:    100 * time.Millisecond,
			wantType:   "minor_error",
		},
		{
			name:       "major_error signal",
			signalType: "major_error",
			reason:     "timeout error",
			latency:    500 * time.Millisecond,
			wantType:   "major_error",
		},
		{
			name:       "critical_error signal",
			signalType: "critical_error",
			reason:     "archival check failed",
			latency:    1 * time.Second,
			wantType:   "critical_error",
		},
		{
			name:       "fatal_error signal",
			signalType: "fatal_error",
			reason:     "endpoint unreachable",
			latency:    0,
			wantType:   "fatal_error",
		},
		{
			name:       "recovery_success signal",
			signalType: "recovery_success",
			reason:     "",
			latency:    50 * time.Millisecond,
			wantType:   "recovery_success",
		},
		{
			name:       "unknown signal type defaults to minor_error",
			signalType: "unknown_type",
			reason:     "some error",
			latency:    200 * time.Millisecond,
			wantType:   "minor_error",
		},
		{
			name:       "empty signal type defaults to minor_error",
			signalType: "",
			reason:     "error",
			latency:    0,
			wantType:   "minor_error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signal := executor.mapSignalType(tt.signalType, tt.reason, tt.latency)
			require.NotNil(t, signal, "Signal should not be nil")

			// Check signal has the expected type (SignalType is a string type)
			signalType := string(signal.Type)
			require.Contains(t, signalType, tt.wantType,
				"Expected signal type to contain %s, got %s", tt.wantType, signalType)
		})
	}
}

// TestCategorizeHealthCheckError tests error categorization for metrics.
func TestCategorizeHealthCheckError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: "",
		},
		{
			name:     "timeout error string",
			err:      fmt.Errorf("request timeout occurred"),
			expected: "timeout",
		},
		{
			name:     "connection error",
			err:      fmt.Errorf("connection refused"),
			expected: "connection_error",
		},
		{
			name:     "status code error",
			err:      fmt.Errorf("unexpected status code 500"),
			expected: "unexpected_status",
		},
		{
			name:     "response validation error",
			err:      fmt.Errorf("response does not contain expected value"),
			expected: "response_validation",
		},
		{
			name:     "protocol error",
			err:      fmt.Errorf("protocol negotiation failed"),
			expected: "protocol_error",
		},
		{
			name:     "generic error",
			err:      http.ErrServerClosed,
			expected: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := categorizeHealthCheckError(tt.err)
			require.Equal(t, tt.expected, result)
		})
	}
}

// TestNewHealthCheckExecutor tests executor creation with valid config.
func TestNewHealthCheckExecutor(t *testing.T) {
	config := &ActiveHealthChecksConfig{
		Enabled: true,
		Local: []ServiceHealthCheckConfig{
			{
				ServiceID: "eth",
				Checks: []HealthCheckConfig{
					{
						Name:   "eth_blockNumber",
						Type:   HealthCheckTypeJSONRPC,
						Method: "POST",
						Path:   "/",
					},
				},
			},
		},
	}

	executor := NewHealthCheckExecutor(HealthCheckExecutorConfig{
		Config: config,
		Logger: polyzero.NewLogger(),
	})

	require.NotNil(t, executor)
	require.True(t, executor.ShouldRunChecks())

	// Verify local config is loaded
	ethConfig := executor.GetConfigForService("eth")
	require.NotNil(t, ethConfig)
	require.Len(t, ethConfig.Checks, 1)
	require.Equal(t, "eth_blockNumber", ethConfig.Checks[0].Name)
}

// TestShouldRunChecks tests the shouldRunChecks logic.
func TestShouldRunChecks(t *testing.T) {
	tests := []struct {
		name     string
		config   *ActiveHealthChecksConfig
		expected bool
	}{
		{
			name: "enabled config returns true",
			config: &ActiveHealthChecksConfig{
				Enabled: true,
			},
			expected: true,
		},
		{
			name: "disabled config returns false",
			config: &ActiveHealthChecksConfig{
				Enabled: false,
			},
			expected: false,
		},
		{
			name:     "nil config returns false",
			config:   nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &HealthCheckExecutor{
				config: tt.config,
				logger: polyzero.NewLogger(),
			}
			require.Equal(t, tt.expected, executor.ShouldRunChecks())
		})
	}
}

// TestGetServiceConfigs tests retrieving all service configurations.
func TestGetServiceConfigs(t *testing.T) {
	enabled := true
	executor := &HealthCheckExecutor{
		config: &ActiveHealthChecksConfig{
			Local: []ServiceHealthCheckConfig{
				{ServiceID: "eth", Enabled: &enabled},
				{ServiceID: "solana", Enabled: &enabled},
			},
		},
		logger: polyzero.NewLogger(),
	}

	configs := executor.GetServiceConfigs()
	require.Len(t, configs, 2)

	// Verify service IDs
	serviceIDs := make(map[string]bool)
	for _, cfg := range configs {
		serviceIDs[string(cfg.ServiceID)] = true
	}
	require.True(t, serviceIDs["eth"])
	require.True(t, serviceIDs["solana"])
}
