package gateway

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
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

// TestConfigMergingSyncAllowance tests that sync_allowance is properly merged from external to local configs.
func TestConfigMergingSyncAllowance(t *testing.T) {
	executor := &HealthCheckExecutor{
		config: &ActiveHealthChecksConfig{},
		logger: polyzero.NewLogger(),
	}

	enabled := true
	externalSyncAllowance := 50
	localSyncAllowance := 100

	// External config with sync_allowance
	external := []ServiceHealthCheckConfig{
		{
			ServiceID:     "arb-one",
			CheckInterval: 10 * time.Second,
			Enabled:       &enabled,
			SyncAllowance: &externalSyncAllowance,
			Checks: []HealthCheckConfig{
				{Name: "eth_blockNumber", Type: HealthCheckTypeJSONRPC, Method: "POST", Path: "/"},
			},
		},
		{
			ServiceID:     "eth",
			CheckInterval: 10 * time.Second,
			Enabled:       &enabled,
			SyncAllowance: &externalSyncAllowance,
			Checks: []HealthCheckConfig{
				{Name: "eth_blockNumber", Type: HealthCheckTypeJSONRPC, Method: "POST", Path: "/"},
			},
		},
	}

	// Local config overrides sync_allowance for arb-one, eth has no local override
	local := []ServiceHealthCheckConfig{
		{
			ServiceID:     "arb-one",
			SyncAllowance: &localSyncAllowance, // Override with different value
			Checks: []HealthCheckConfig{
				{Name: "eth_chainId", Type: HealthCheckTypeJSONRPC, Method: "POST", Path: "/"},
			},
		},
	}

	// Merge configs
	merged := executor.mergeConfigs(external, local)

	// Find services by ID
	serviceMap := make(map[string]ServiceHealthCheckConfig)
	for _, cfg := range merged {
		serviceMap[string(cfg.ServiceID)] = cfg
	}

	// Check arb-one - should have local sync_allowance
	arbOneCfg := serviceMap["arb-one"]
	require.NotNil(t, arbOneCfg.SyncAllowance, "Expected arb-one to have sync_allowance")
	require.Equal(t, localSyncAllowance, *arbOneCfg.SyncAllowance, "Expected local sync_allowance (100) to override external (50)")

	// Check eth - should have external sync_allowance (no local override)
	ethCfg := serviceMap["eth"]
	require.NotNil(t, ethCfg.SyncAllowance, "Expected eth to have sync_allowance from external")
	require.Equal(t, externalSyncAllowance, *ethCfg.SyncAllowance, "Expected external sync_allowance (50) when no local override")

	t.Log("SyncAllowance merging test passed")
}

// mockQoSServiceWithSyncAllowance is a minimal mock that tracks SetSyncAllowance calls.
type mockQoSServiceWithSyncAllowance struct {
	QoSService // embed interface — only SetSyncAllowance is used
	syncAllowance uint64
	called        bool
}

func (m *mockQoSServiceWithSyncAllowance) SetSyncAllowance(v uint64) {
	m.syncAllowance = v
	m.called = true
}

// TestSetQoSInstancesAppliesSyncAllowanceFromExternalConfigs verifies that
// SetQoSInstances propagates sync_allowance from already-loaded external configs.
// This reproduces a bug where InitExternalConfig runs before SetQoSInstances,
// so the initial SetSyncAllowance calls find an empty qosInstances map.
func TestSetQoSInstancesAppliesSyncAllowanceFromExternalConfigs(t *testing.T) {
	syncAllowance20 := 20
	syncAllowance1200 := 1200

	executor := &HealthCheckExecutor{
		config:       &ActiveHealthChecksConfig{},
		logger:       polyzero.NewLogger(),
		qosInstances: make(map[protocol.ServiceID]QoSService),
	}

	// Simulate external configs already loaded (as if InitExternalConfig ran first)
	executor.externalConfigs = []ServiceHealthCheckConfig{
		{
			ServiceID:     "base",
			SyncAllowance: &syncAllowance20,
		},
		{
			ServiceID:     "arb-one",
			SyncAllowance: &syncAllowance1200,
		},
	}

	// Create mock QoS instances (as if cmd/qos.go created them with syncAllowance=0)
	baseMock := &mockQoSServiceWithSyncAllowance{}
	arbMock := &mockQoSServiceWithSyncAllowance{}

	// This is the call that happens in cmd/main.go AFTER InitExternalConfig
	executor.SetQoSInstances(map[protocol.ServiceID]QoSService{
		"base":    baseMock,
		"arb-one": arbMock,
	})

	// Verify sync_allowance was applied from external configs
	require.True(t, baseMock.called, "SetSyncAllowance should have been called on base")
	require.Equal(t, uint64(20), baseMock.syncAllowance, "base should have sync_allowance=20 from external config")

	require.True(t, arbMock.called, "SetSyncAllowance should have been called on arb-one")
	require.Equal(t, uint64(1200), arbMock.syncAllowance, "arb-one should have sync_allowance=1200 from external config")
}

// TestSetQoSInstancesNoExternalConfigs verifies SetQoSInstances works when no external configs are loaded.
func TestSetQoSInstancesNoExternalConfigs(t *testing.T) {
	executor := &HealthCheckExecutor{
		config:       &ActiveHealthChecksConfig{},
		logger:       polyzero.NewLogger(),
		qosInstances: make(map[protocol.ServiceID]QoSService),
	}

	// No external configs loaded
	baseMock := &mockQoSServiceWithSyncAllowance{}
	executor.SetQoSInstances(map[protocol.ServiceID]QoSService{
		"base": baseMock,
	})

	require.False(t, baseMock.called, "SetSyncAllowance should NOT have been called when no external configs")
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

// recordedSignalReputationSvc captures every RecordSignal call so tests can
// assert whether the health-check path skipped or applied a penalty. Other
// ReputationService methods are inherited from the embedded nil interface and
// must NOT be called by the code under test (they will panic, surfacing any
// accidental dependency).
type recordedSignalReputationSvc struct {
	reputation.ReputationService

	mu      sync.Mutex
	signals []reputation.Signal
}

func (m *recordedSignalReputationSvc) RecordSignal(_ context.Context, _ reputation.EndpointKey, signal reputation.Signal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.signals = append(m.signals, signal)
	return nil
}

func (m *recordedSignalReputationSvc) KeyBuilderForService(_ protocol.ServiceID) reputation.KeyBuilder {
	return reputation.NewKeyBuilder(reputation.KeyGranularityEndpoint)
}

func (m *recordedSignalReputationSvc) RecordedSignals() []reputation.Signal {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]reputation.Signal, len(m.signals))
	copy(out, m.signals)
	return out
}

// TestRecordCheckResult_OverServicedSkipsPenalty pins the invariant that the
// health-check executor must not penalize a supplier when the relay-miner has
// signaled the application's per-session stake budget is exhausted. Without
// this skip the executor was draining easy2stake's BSC supplier set to score=0
// in production despite the request-path no-penalty fix.
func TestRecordCheckResult_OverServicedSkipsPenalty(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{
			name: "poktroll main exact phrase",
			err:  fmt.Errorf("relay miner returned error: offchain rate limit hit by relayer proxy"),
		},
		{
			name: "HA relay-miner JSON body via wrapped error",
			err:  fmt.Errorf("non-2xx: 429 — body: {\"error\":\"session relay limit reached: claimable portion fully consumed\"}"),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := &recordedSignalReputationSvc{}
			executor := &HealthCheckExecutor{
				logger:        polyzero.NewLogger(),
				reputationSvc: rep,
			}

			executor.recordCheckResult(
				context.Background(),
				protocol.ServiceID("bsc"),
				protocol.EndpointAddr("pokt1abc-https://relay.example.com"),
				HealthCheckConfig{Name: "block-number", Type: "json_rpc"},
				c.err,
				100*time.Millisecond,
			)

			require.Empty(t, rep.RecordedSignals(),
				"over-serviced check failures must not record a reputation signal")
		})
	}
}

// TestRecordCheckResult_NonOverServicedFailureRecordsPenalty pins that generic
// check failures still penalize. The over-serviced skip must be narrowly scoped
// to relay-miner phrases — anything else stays a fault.
func TestRecordCheckResult_NonOverServicedFailureRecordsPenalty(t *testing.T) {
	rep := &recordedSignalReputationSvc{}
	executor := &HealthCheckExecutor{
		logger:        polyzero.NewLogger(),
		reputationSvc: rep,
	}

	executor.recordCheckResult(
		context.Background(),
		protocol.ServiceID("bsc"),
		protocol.EndpointAddr("pokt1abc-https://relay.example.com"),
		HealthCheckConfig{Name: "block-number", Type: "json_rpc", ReputationSignal: "major_error"},
		fmt.Errorf("connection refused"),
		100*time.Millisecond,
	)

	signals := rep.RecordedSignals()
	require.Len(t, signals, 1, "regular check failure must record exactly one penalty signal")
	require.Equal(t, reputation.SignalTypeMajorError, signals[0].Type,
		"configured major_error severity must propagate; over-served skip must not catch this")
}

// TestRecordCheckResult_SuccessRecordsRecoverySignal pins the success path.
func TestRecordCheckResult_SuccessRecordsRecoverySignal(t *testing.T) {
	rep := &recordedSignalReputationSvc{}
	executor := &HealthCheckExecutor{
		logger:        polyzero.NewLogger(),
		reputationSvc: rep,
	}

	executor.recordCheckResult(
		context.Background(),
		protocol.ServiceID("bsc"),
		protocol.EndpointAddr("pokt1abc-https://relay.example.com"),
		HealthCheckConfig{Name: "block-number", Type: "json_rpc"},
		nil,
		50*time.Millisecond,
	)

	signals := rep.RecordedSignals()
	require.Len(t, signals, 1)
	require.Equal(t, reputation.SignalTypeRecoverySuccess, signals[0].Type)
}

// Ensure the sharedtypes import is referenced — used elsewhere in the package
// but kept here so future tests that need RPCType can copy this file as a
// template without hunting for the import.
var _ = sharedtypes.RPCType_JSON_RPC

// ---------------------------------------------------------------------------
// Backend-URL health check dedup
// ---------------------------------------------------------------------------

// TestGroupEndpointsByURL pins the grouping used for backend-URL dedup: endpoints
// that share an identical backend URL are collapsed into one group (one relay per
// URL), endpoints with no HTTP URL each stay a singleton (never deduped), and both
// groups and members come back in a stable order so rotation is deterministic.
func TestGroupEndpointsByURL(t *testing.T) {
	eps := []EndpointInfo{
		{Addr: "sB-https://r2.example.com", HTTPURL: "https://r2.example.com"},
		{Addr: "sA-https://r1.example.com", HTTPURL: "https://r1.example.com"},
		{Addr: "sC-https://r1.example.com", HTTPURL: "https://r1.example.com"},
		{Addr: "sD-https://r1.example.com", HTTPURL: "https://r1.example.com"},
		{Addr: "wsOnly", HTTPURL: "", WebSocketURL: "wss://ws.example.com"},
	}

	groups := groupEndpointsByURL(eps)

	// r1 (3 members) + r2 (1 member) + ws-only singleton = 3 groups.
	require.Len(t, groups, 3)

	// First group is r1 (sorted URL order), members sorted by Addr.
	require.Len(t, groups[0], 3)
	require.Equal(t, protocol.EndpointAddr("sA-https://r1.example.com"), groups[0][0].Addr)
	require.Equal(t, protocol.EndpointAddr("sC-https://r1.example.com"), groups[0][1].Addr)
	require.Equal(t, protocol.EndpointAddr("sD-https://r1.example.com"), groups[0][2].Addr)

	// Second group is r2 (single member).
	require.Len(t, groups[1], 1)
	require.Equal(t, protocol.EndpointAddr("sB-https://r2.example.com"), groups[1][0].Addr)

	// Third group is the WS-only singleton — never merged with anything.
	require.Len(t, groups[2], 1)
	require.Equal(t, protocol.EndpointAddr("wsOnly"), groups[2][0].Addr)
}

// TestGroupEndpointsByURL_Stable pins that grouping is deterministic across calls,
// so representative rotation actually round-robins the same ordering each cycle.
func TestGroupEndpointsByURL_Stable(t *testing.T) {
	eps := []EndpointInfo{
		{Addr: "s3-https://b.example.com", HTTPURL: "https://b.example.com"},
		{Addr: "s1-https://a.example.com", HTTPURL: "https://a.example.com"},
		{Addr: "s2-https://a.example.com", HTTPURL: "https://a.example.com"},
	}
	first := groupEndpointsByURL(eps)
	for i := 0; i < 5; i++ {
		require.Equal(t, first, groupEndpointsByURL(eps))
	}
}

// TestRepresentativeIndex_RoundRobin pins that rotation gives every member of a
// same-URL group a directly-probed turn within len(group) consecutive cycles.
func TestRepresentativeIndex_RoundRobin(t *testing.T) {
	require.Equal(t, 0, representativeIndex(7, 1), "singleton group always uses index 0")
	require.Equal(t, 0, representativeIndex(7, 0), "empty group must not divide-by-zero")

	const size = 4
	seen := make(map[int]struct{})
	for cycle := uint64(1); cycle <= size; cycle++ {
		seen[representativeIndex(cycle, size)] = struct{}{}
	}
	require.Len(t, seen, size, "every member must be representative once per %d cycles", size)
}

// TestBackendDedupEnabled pins the default-on behavior and explicit override.
func TestBackendDedupEnabled(t *testing.T) {
	tru, fls := true, false
	require.True(t, (&HealthCheckExecutor{}).backendDedupEnabled(), "nil config defaults to enabled")
	require.True(t, (&HealthCheckExecutor{config: &ActiveHealthChecksConfig{}}).backendDedupEnabled(), "nil flag defaults to enabled")
	require.True(t, (&HealthCheckExecutor{config: &ActiveHealthChecksConfig{BackendDedup: &tru}}).backendDedupEnabled())
	require.False(t, (&HealthCheckExecutor{config: &ActiveHealthChecksConfig{BackendDedup: &fls}}).backendDedupEnabled())
}

// TestHydrateDefaults_BackendDedup pins that dedup ships on by default.
func TestHydrateDefaults_BackendDedup(t *testing.T) {
	cfg := &ActiveHealthChecksConfig{}
	cfg.HydrateDefaults(true)
	require.NotNil(t, cfg.BackendDedup)
	require.True(t, *cfg.BackendDedup)

	// Explicit false must be preserved, not overwritten by the default.
	fls := false
	cfg2 := &ActiveHealthChecksConfig{BackendDedup: &fls}
	cfg2.HydrateDefaults(true)
	require.NotNil(t, cfg2.BackendDedup)
	require.False(t, *cfg2.BackendDedup)
}

// TestFanOutcomeToSibling_RecordsSignals pins that fanning replays one reputation
// signal per HTTP outcome onto the sibling — a passing check recovers it, a failing
// check penalizes it — mirroring what the representative recorded, without a relay.
func TestFanOutcomeToSibling_RecordsSignals(t *testing.T) {
	rep := &recordedSignalReputationSvc{}
	executor := &HealthCheckExecutor{
		logger:        polyzero.NewLogger(),
		reputationSvc: rep,
		// observationQueue nil => processObservationSync is a no-op; archival not resolved
		// => no SetArchivalStatus call. Isolates the reputation-signal fan.
	}

	outcomes := []checkOutcome{
		{
			check:   HealthCheckConfig{Name: "block-number", Type: "json_rpc"},
			err:     nil, // pass
			latency: 40 * time.Millisecond,
		},
		{
			check:   HealthCheckConfig{Name: "chain-id", Type: "json_rpc", ReputationSignal: "critical_error"},
			err:     fmt.Errorf("wrong chain id"),
			latency: 60 * time.Millisecond,
		},
	}

	executor.fanOutcomeToSibling(
		context.Background(),
		protocol.ServiceID("bsc"),
		protocol.EndpointAddr("pokt1sibling-https://r1.example.com"),
		outcomes,
	)

	signals := rep.RecordedSignals()
	require.Len(t, signals, 2, "one fanned reputation signal per HTTP outcome")
	require.Equal(t, reputation.SignalTypeRecoverySuccess, signals[0].Type, "passing check recovers the sibling")
	require.Equal(t, reputation.SignalTypeCriticalError, signals[1].Type, "failing check penalizes the sibling at configured severity")
}

// TestFanOutcomeToSibling_OverServicedNotPenalized pins that a representative's
// over-serviced (stake-exhausted) result, when fanned, still skips the sibling
// penalty — same no-penalty rule as a direct relay.
func TestFanOutcomeToSibling_OverServicedNotPenalized(t *testing.T) {
	rep := &recordedSignalReputationSvc{}
	executor := &HealthCheckExecutor{
		logger:        polyzero.NewLogger(),
		reputationSvc: rep,
	}

	outcomes := []checkOutcome{{
		check:   HealthCheckConfig{Name: "block-number", Type: "json_rpc", ReputationSignal: "major_error"},
		err:     fmt.Errorf("relay miner returned error: offchain rate limit hit by relayer proxy"),
		latency: 20 * time.Millisecond,
	}}

	executor.fanOutcomeToSibling(
		context.Background(),
		protocol.ServiceID("bsc"),
		protocol.EndpointAddr("pokt1sibling-https://r1.example.com"),
		outcomes,
	)

	require.Empty(t, rep.RecordedSignals(),
		"fanned over-serviced failures must not penalize the sibling")
}
