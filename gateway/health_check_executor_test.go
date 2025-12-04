package gateway

import (
	"context"
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
