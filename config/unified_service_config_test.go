package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
)

func TestUnifiedServicesConfig_Parse(t *testing.T) {
	yamlData := `
latency_profiles:
  fast:
    fast_threshold: 50ms
    normal_threshold: 200ms
    slow_threshold: 500ms
    penalty_threshold: 1s
    severe_threshold: 3s
  slow:
    fast_threshold: 2s
    normal_threshold: 10s
    slow_threshold: 30s
    penalty_threshold: 60s
    severe_threshold: 120s

defaults:
  type: passthrough
  rpc_types:
    - json_rpc
  latency_profile: standard
  reputation_config:
    enabled: true
    initial_score: 80
    min_threshold: 30
  tiered_selection:
    enabled: true
    tier1_threshold: 70
    tier2_threshold: 50
  retry_config:
    enabled: true
    max_retries: 1

services:
  - id: eth
    type: evm
    rpc_types:
      - json_rpc
      - websocket
    latency_profile: fast
    reputation_config:
      initial_score: 85
  - id: base
    type: evm
    latency_profile: fast
  - id: solana
    type: solana
    rpc_types:
      - json_rpc
  - id: cosmoshub
    type: cosmos
    rpc_types:
      - rest
      - comet_bft
  - id: custom-llm
    type: generic
    latency_profile: slow
    fallback:
      enabled: true
      send_all_traffic: false
      endpoints:
        - default_url: "https://llm.backup.io"
`

	var config UnifiedServicesConfig
	err := yaml.Unmarshal([]byte(yamlData), &config)
	require.NoError(t, err)

	// Hydrate defaults
	config.HydrateDefaults()

	// Test parsing
	assert.Len(t, config.Services, 5)
	assert.Len(t, config.LatencyProfiles, 3) // 2 custom + 1 standard (hydrated)

	// Check eth service
	eth := config.GetServiceConfig("eth")
	require.NotNil(t, eth)
	assert.Equal(t, protocol.ServiceID("eth"), eth.ID)
	assert.Equal(t, ServiceTypeEVM, eth.Type)
	assert.Equal(t, []string{"json_rpc", "websocket"}, eth.RPCTypes)
	assert.Equal(t, "fast", eth.LatencyProfile)
	require.NotNil(t, eth.ReputationConfig)
	assert.Equal(t, float64(85), *eth.ReputationConfig.InitialScore)

	// Check base service (inherits most from defaults)
	base := config.GetServiceConfig("base")
	require.NotNil(t, base)
	assert.Equal(t, ServiceTypeEVM, base.Type)
	assert.Equal(t, "fast", base.LatencyProfile)

	// Check solana service
	solana := config.GetServiceConfig("solana")
	require.NotNil(t, solana)
	assert.Equal(t, ServiceTypeSolana, solana.Type)

	// Check custom-llm with fallback
	llm := config.GetServiceConfig("custom-llm")
	require.NotNil(t, llm)
	assert.Equal(t, ServiceTypeGeneric, llm.Type)
	assert.Equal(t, "slow", llm.LatencyProfile)
	require.NotNil(t, llm.Fallback)
	assert.True(t, llm.Fallback.Enabled)
	assert.Len(t, llm.Fallback.Endpoints, 1)

	// Check latency profiles
	fastProfile := config.LatencyProfiles["fast"]
	assert.Equal(t, 50*time.Millisecond, fastProfile.FastThreshold)
	assert.Equal(t, 200*time.Millisecond, fastProfile.NormalThreshold)

	slowProfile := config.LatencyProfiles["slow"]
	assert.Equal(t, 2*time.Second, slowProfile.FastThreshold)
	assert.Equal(t, 10*time.Second, slowProfile.NormalThreshold)
}

func TestUnifiedServicesConfig_Validate(t *testing.T) {
	tests := []struct {
		name        string
		config      UnifiedServicesConfig
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid config",
			config: UnifiedServicesConfig{
				Services: []ServiceConfig{
					{ID: "eth", Type: ServiceTypeEVM},
					{ID: "base", Type: ServiceTypeEVM},
				},
			},
			expectError: false,
		},
		{
			name: "duplicate service ID",
			config: UnifiedServicesConfig{
				Services: []ServiceConfig{
					{ID: "eth", Type: ServiceTypeEVM},
					{ID: "eth", Type: ServiceTypeEVM},
				},
			},
			expectError: true,
			errorMsg:    "duplicate service id 'eth'",
		},
		{
			name: "empty service ID",
			config: UnifiedServicesConfig{
				Services: []ServiceConfig{
					{ID: "", Type: ServiceTypeEVM},
				},
			},
			expectError: true,
			errorMsg:    "id is required",
		},
		{
			name: "invalid service type",
			config: UnifiedServicesConfig{
				Services: []ServiceConfig{
					{ID: "eth", Type: "invalid"},
				},
			},
			expectError: true,
			errorMsg:    "invalid service type",
		},
		{
			name: "unknown latency profile",
			config: UnifiedServicesConfig{
				Services: []ServiceConfig{
					{ID: "eth", LatencyProfile: "nonexistent"},
				},
			},
			expectError: true,
			errorMsg:    "unknown latency_profile 'nonexistent'",
		},
		{
			name: "built-in latency profile is valid",
			config: UnifiedServicesConfig{
				Services: []ServiceConfig{
					{ID: "eth", LatencyProfile: "evm"},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestUnifiedServicesConfig_GetMergedServiceConfig(t *testing.T) {
	config := &UnifiedServicesConfig{
		Defaults: ServiceDefaults{
			Type:           ServiceTypePassthrough,
			RPCTypes:       []string{"json_rpc"},
			LatencyProfile: "standard",
			ReputationConfig: ServiceReputationConfig{
				Enabled:        boolPtr(true),
				InitialScore:   float64Ptr(80),
				MinThreshold:   float64Ptr(30),
				KeyGranularity: reputation.KeyGranularityEndpoint,
			},
			TieredSelection: ServiceTieredSelectionConfig{
				Enabled:        boolPtr(true),
				Tier1Threshold: float64Ptr(70),
				Tier2Threshold: float64Ptr(50),
			},
			RetryConfig: ServiceRetryConfig{
				Enabled:    boolPtr(true),
				MaxRetries: intPtr(1),
			},
		},
		Services: []ServiceConfig{
			{
				ID:             "eth",
				Type:           ServiceTypeEVM,
				RPCTypes:       []string{"json_rpc", "websocket"},
				LatencyProfile: "fast",
				ReputationConfig: &ServiceReputationConfig{
					InitialScore: float64Ptr(85),
				},
			},
			{
				ID:   "base",
				Type: ServiceTypeEVM,
				// Inherits everything else from defaults
			},
		},
	}

	// Test eth service (with overrides)
	ethMerged := config.GetMergedServiceConfig("eth")
	require.NotNil(t, ethMerged)
	assert.Equal(t, ServiceTypeEVM, ethMerged.Type)
	assert.Equal(t, []string{"json_rpc", "websocket"}, ethMerged.RPCTypes)
	assert.Equal(t, "fast", ethMerged.LatencyProfile)
	// Check merged reputation config
	assert.Equal(t, float64(85), *ethMerged.ReputationConfig.InitialScore) // overridden
	assert.Equal(t, float64(30), *ethMerged.ReputationConfig.MinThreshold) // from defaults
	assert.True(t, *ethMerged.ReputationConfig.Enabled)                    // from defaults

	// Test base service (mostly defaults)
	baseMerged := config.GetMergedServiceConfig("base")
	require.NotNil(t, baseMerged)
	assert.Equal(t, ServiceTypeEVM, baseMerged.Type)                        // overridden
	assert.Equal(t, []string{"json_rpc"}, baseMerged.RPCTypes)              // from defaults
	assert.Equal(t, "standard", baseMerged.LatencyProfile)                  // from defaults
	assert.Equal(t, float64(80), *baseMerged.ReputationConfig.InitialScore) // from defaults

	// Test nonexistent service
	nonexistent := config.GetMergedServiceConfig("nonexistent")
	assert.Nil(t, nonexistent)
}

func TestUnifiedServicesConfig_GetLatencyProfile(t *testing.T) {
	config := &UnifiedServicesConfig{
		LatencyProfiles: map[string]LatencyProfileConfig{
			"custom": {
				FastThreshold:    100 * time.Millisecond,
				NormalThreshold:  500 * time.Millisecond,
				SlowThreshold:    1 * time.Second,
				PenaltyThreshold: 2 * time.Second,
				SevereThreshold:  5 * time.Second,
			},
		},
	}

	// Test custom profile
	custom := config.GetLatencyProfile("custom")
	require.NotNil(t, custom)
	assert.Equal(t, 100*time.Millisecond, custom.FastThreshold)

	// Test built-in profile
	evm := config.GetLatencyProfile("evm")
	require.NotNil(t, evm)
	assert.Equal(t, 50*time.Millisecond, evm.FastThreshold)

	// Test nonexistent profile
	nonexistent := config.GetLatencyProfile("nonexistent")
	assert.Nil(t, nonexistent)
}

func TestUnifiedServicesConfig_HydrateDefaults(t *testing.T) {
	config := &UnifiedServicesConfig{}
	config.HydrateDefaults()

	// Check defaults are set
	assert.Equal(t, ServiceTypePassthrough, config.Defaults.Type)
	assert.Equal(t, []string{"json_rpc"}, config.Defaults.RPCTypes)
	assert.Equal(t, "standard", config.Defaults.LatencyProfile)

	// Check reputation defaults
	require.NotNil(t, config.Defaults.ReputationConfig.Enabled)
	assert.True(t, *config.Defaults.ReputationConfig.Enabled)
	assert.Equal(t, reputation.InitialScore, *config.Defaults.ReputationConfig.InitialScore)
	assert.Equal(t, reputation.DefaultMinThreshold, *config.Defaults.ReputationConfig.MinThreshold)

	// Check tiered selection defaults
	require.NotNil(t, config.Defaults.TieredSelection.Enabled)
	assert.True(t, *config.Defaults.TieredSelection.Enabled)
	assert.Equal(t, float64(70), *config.Defaults.TieredSelection.Tier1Threshold)

	// Check retry defaults
	require.NotNil(t, config.Defaults.RetryConfig.Enabled)
	assert.True(t, *config.Defaults.RetryConfig.Enabled)
	assert.Equal(t, 1, *config.Defaults.RetryConfig.MaxRetries)

	// Check observation pipeline defaults
	require.NotNil(t, config.Defaults.ObservationPipeline.Enabled)
	assert.True(t, *config.Defaults.ObservationPipeline.Enabled)
	assert.Equal(t, gateway.DefaultObservationPipelineSampleRate, *config.Defaults.ObservationPipeline.SampleRate)

	// Check health check defaults
	require.NotNil(t, config.Defaults.ActiveHealthChecks.Enabled)
	assert.True(t, *config.Defaults.ActiveHealthChecks.Enabled)
	assert.Equal(t, gateway.DefaultHealthCheckInterval, config.Defaults.ActiveHealthChecks.Interval)

	// Check standard latency profile is added
	standard, exists := config.LatencyProfiles["standard"]
	assert.True(t, exists)
	assert.Equal(t, 500*time.Millisecond, standard.FastThreshold)
}

func TestUnifiedServicesConfig_GetServiceType(t *testing.T) {
	config := &UnifiedServicesConfig{
		Defaults: ServiceDefaults{
			Type: ServiceTypePassthrough,
		},
		Services: []ServiceConfig{
			{ID: "eth", Type: ServiceTypeEVM},
			{ID: "unknown"}, // No type, should get default
		},
	}

	assert.Equal(t, ServiceTypeEVM, config.GetServiceType("eth"))
	assert.Equal(t, ServiceTypePassthrough, config.GetServiceType("unknown"))
	assert.Equal(t, ServiceTypePassthrough, config.GetServiceType("nonexistent"))
}

func TestUnifiedServicesConfig_GetConfiguredServiceIDs(t *testing.T) {
	config := &UnifiedServicesConfig{
		Services: []ServiceConfig{
			{ID: "eth"},
			{ID: "base"},
			{ID: "solana"},
		},
	}

	ids := config.GetConfiguredServiceIDs()
	assert.Len(t, ids, 3)
	assert.Contains(t, ids, protocol.ServiceID("eth"))
	assert.Contains(t, ids, protocol.ServiceID("base"))
	assert.Contains(t, ids, protocol.ServiceID("solana"))
}

func TestServiceType_Validation(t *testing.T) {
	tests := []struct {
		serviceType ServiceType
		valid       bool
	}{
		{ServiceTypeEVM, true},
		{ServiceTypeSolana, true},
		{ServiceTypeCosmos, true},
		{ServiceTypeGeneric, true},
		{ServiceTypePassthrough, true},
		{"invalid", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.serviceType), func(t *testing.T) {
			err := gateway.ValidateServiceType(gateway.ServiceType(tt.serviceType))
			if tt.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestIsBuiltInLatencyProfile(t *testing.T) {
	assert.True(t, gateway.IsBuiltInLatencyProfile("evm"))
	assert.True(t, gateway.IsBuiltInLatencyProfile("solana"))
	assert.True(t, gateway.IsBuiltInLatencyProfile("cosmos"))
	assert.True(t, gateway.IsBuiltInLatencyProfile("llm"))
	assert.True(t, gateway.IsBuiltInLatencyProfile("generic"))
	assert.False(t, gateway.IsBuiltInLatencyProfile("custom"))
	assert.False(t, gateway.IsBuiltInLatencyProfile(""))
}

// Helper functions for creating pointers
func boolPtr(b bool) *bool {
	return &b
}

func float64Ptr(f float64) *float64 {
	return &f
}

func intPtr(i int) *int {
	return &i
}
