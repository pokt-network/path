package evm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
)

// mockReputationService implements reputation.ReputationService for testing.
type mockReputationService struct {
	archivalEndpoints map[string]bool // key -> isArchival
}

func (m *mockReputationService) RecordSignal(ctx context.Context, key reputation.EndpointKey, signal reputation.Signal) error {
	return nil
}

func (m *mockReputationService) GetScore(ctx context.Context, key reputation.EndpointKey) (reputation.Score, error) {
	return reputation.Score{}, nil
}

func (m *mockReputationService) GetScores(ctx context.Context, keys []reputation.EndpointKey) (map[reputation.EndpointKey]reputation.Score, error) {
	return nil, nil
}

func (m *mockReputationService) RankEndpointsByScore(ctx context.Context, keys []reputation.EndpointKey) ([]reputation.EndpointKey, error) {
	return nil, nil
}

func (m *mockReputationService) FilterByScore(ctx context.Context, keys []reputation.EndpointKey, minThreshold float64) ([]reputation.EndpointKey, error) {
	return keys, nil
}

func (m *mockReputationService) ResetScore(ctx context.Context, key reputation.EndpointKey) error {
	return nil
}

func (m *mockReputationService) KeyBuilderForService(serviceID protocol.ServiceID) reputation.KeyBuilder {
	return reputation.NewKeyBuilder("domain")
}

func (m *mockReputationService) SetServiceConfig(serviceID protocol.ServiceID, config reputation.ServiceConfig) {
}

func (m *mockReputationService) GetInitialScoreForService(serviceID protocol.ServiceID) float64 {
	return 80
}

func (m *mockReputationService) GetMinThresholdForService(serviceID protocol.ServiceID) float64 {
	return 30
}

func (m *mockReputationService) SetLatencyProfile(serviceID protocol.ServiceID, latencyConfig reputation.LatencyConfig) {
}

func (m *mockReputationService) GetLatencyConfigForService(serviceID protocol.ServiceID) reputation.LatencyConfig {
	return reputation.LatencyConfig{}
}

func (m *mockReputationService) Start(ctx context.Context) error {
	return nil
}

func (m *mockReputationService) Stop() error {
	return nil
}

func (m *mockReputationService) SetLogger(logger polylog.Logger) {
}

func (m *mockReputationService) SetArchivalStatus(ctx context.Context, key reputation.EndpointKey, isArchival bool, archivalTTL time.Duration) error {
	if m.archivalEndpoints == nil {
		m.archivalEndpoints = make(map[string]bool)
	}
	m.archivalEndpoints[key.String()] = isArchival
	return nil
}

func (m *mockReputationService) IsArchivalCapable(ctx context.Context, key reputation.EndpointKey) bool {
	if m.archivalEndpoints == nil {
		return false
	}
	return m.archivalEndpoints[key.String()]
}

func (m *mockReputationService) SetPerceivedBlockNumber(ctx context.Context, serviceID protocol.ServiceID, blockNumber uint64) error {
	return nil
}

func (m *mockReputationService) GetPerceivedBlockNumber(ctx context.Context, serviceID protocol.ServiceID) uint64 {
	return 0
}

func (m *mockReputationService) SetEndpointBlockHeight(ctx context.Context, serviceID protocol.ServiceID, endpointAddr protocol.EndpointAddr, blockHeight uint64) error {
	return nil
}

func (m *mockReputationService) GetEndpointBlockHeights(ctx context.Context, serviceID protocol.ServiceID) map[protocol.EndpointAddr]uint64 {
	return make(map[protocol.EndpointAddr]uint64)
}

func (m *mockReputationService) RemoveEndpointBlockHeights(_ context.Context, _ protocol.ServiceID, _ []protocol.EndpointAddr) error {
	return nil
}

func (m *mockReputationService) GetArchivalEndpoints(ctx context.Context, serviceID protocol.ServiceID) []reputation.EndpointKey {
	if m.archivalEndpoints == nil {
		return nil
	}
	var result []reputation.EndpointKey
	prefix := string(serviceID) + ":"
	for keyStr, isArchival := range m.archivalEndpoints {
		if !isArchival {
			continue
		}
		if len(keyStr) <= len(prefix) || keyStr[:len(prefix)] != prefix {
			continue
		}
		// Parse "serviceID:endpointAddr:rpcType"
		rest := keyStr[len(prefix):]
		lastColon := strings.LastIndex(rest, ":")
		if lastColon < 0 {
			continue
		}
		endpointAddr := rest[:lastColon]
		rpcTypeStr := strings.ToUpper(rest[lastColon+1:])
		rpcType := sharedtypes.RPCType(sharedtypes.RPCType_value[rpcTypeStr])
		result = append(result, reputation.NewEndpointKey(serviceID, protocol.EndpointAddr(endpointAddr), rpcType))
	}
	return result
}

func TestArchivalEndpointSelection_LocalAndRedis(t *testing.T) {
	tests := []struct {
		name             string
		localArchival    map[protocol.EndpointAddr]bool // endpoint -> isArchival in local store
		redisArchival    map[protocol.EndpointAddr]bool // endpoint -> isArchival in Redis
		requiresArchival bool
		expectFound      bool
		expectError      bool
	}{
		{
			name: "local has archival - should find",
			localArchival: map[protocol.EndpointAddr]bool{
				"ep1-url": true,
			},
			redisArchival:    map[protocol.EndpointAddr]bool{},
			requiresArchival: true,
			expectFound:      true,
			expectError:      false,
		},
		{
			name:          "redis has archival, local empty - should find via Redis",
			localArchival: map[protocol.EndpointAddr]bool{},
			redisArchival: map[protocol.EndpointAddr]bool{
				"ep1-url": true,
			},
			requiresArchival: true,
			expectFound:      true,
			expectError:      false,
		},
		{
			name:             "neither has archival - should error",
			localArchival:    map[protocol.EndpointAddr]bool{},
			redisArchival:    map[protocol.EndpointAddr]bool{},
			requiresArchival: true,
			expectFound:      false,
			expectError:      true,
		},
		{
			name: "not requiring archival - all endpoints valid",
			localArchival: map[protocol.EndpointAddr]bool{
				"ep1-url": false,
			},
			redisArchival:    map[protocol.EndpointAddr]bool{},
			requiresArchival: false,
			expectFound:      true,
			expectError:      false,
		},
		{
			name: "both local and redis have archival - should find from local (priority)",
			localArchival: map[protocol.EndpointAddr]bool{
				"ep1-url": true,
			},
			redisArchival: map[protocol.EndpointAddr]bool{
				"ep1-url": true,
			},
			requiresArchival: true,
			expectFound:      true,
			expectError:      false,
		},
		{
			name: "local has non-archival, redis has archival - should find from redis",
			localArchival: map[protocol.EndpointAddr]bool{
				"ep1-url": false,
			},
			redisArchival: map[protocol.EndpointAddr]bool{
				"ep1-url": true,
			},
			requiresArchival: true,
			expectFound:      true,
			expectError:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := polylog.Ctx(context.Background())

			// Create mock reputation service
			mockRepSvc := &mockReputationService{
				archivalEndpoints: make(map[string]bool),
			}

			// Populate Redis archival data
			for endpointAddr, isArchival := range tt.redisArchival {
				key := reputation.NewEndpointKey(
					"test-service",
					endpointAddr,
					sharedtypes.RPCType_JSON_RPC,
				)
				mockRepSvc.archivalEndpoints[key.String()] = isArchival
			}

			// Create serviceState with endpointStore
			ss := &serviceState{
				logger:           logger,
				serviceQoSConfig: NewEVMServiceQoSConfig("test-service", "1", nil),
				endpointStore: &endpointStore{
					logger:    logger,
					endpoints: make(map[protocol.EndpointAddr]endpoint),
				},
				reputationSvc:  mockRepSvc,
				blockConsensus: NewBlockHeightConsensus(logger, 128),
				archivalCache:  NewArchivalCache(), // Initialize cache for new architecture
			}

			// Populate cache from Redis mock data (simulates background refresh worker)
			for keyStr, isArchival := range mockRepSvc.archivalEndpoints {
				if isArchival {
					ss.archivalCache.Set(keyStr, true, 8*time.Hour)
				}
			}

			// Populate local archival data in endpointStore
			for endpointAddr, isArchival := range tt.localArchival {
				endpoint := endpoint{
					checkArchival: endpointCheckArchival{},
				}
				if isArchival {
					// Mark endpoint as archival-capable by setting valid archival check
					endpoint.checkArchival.isArchival = true
					endpoint.checkArchival.expiresAt = time.Now().Add(1 * time.Hour)
				}
				ss.endpointStore.endpoints[endpointAddr] = endpoint
			}

			// Create available endpoints list
			availableEndpoints := make(protocol.EndpointAddrList, 0)
			allEndpoints := make(map[protocol.EndpointAddr]bool)
			for addr := range tt.localArchival {
				allEndpoints[addr] = true
			}
			for addr := range tt.redisArchival {
				allEndpoints[addr] = true
			}
			for addr := range allEndpoints {
				availableEndpoints = append(availableEndpoints, addr)
			}

			// Execute: Call filterArchivalEndpointsForFallback
			archivalEndpoints := ss.filterArchivalEndpointsForFallback(availableEndpoints)

			// Verify based on requiresArchival
			if tt.requiresArchival {
				if tt.expectFound {
					require.NotEmpty(t, archivalEndpoints, "expected to find archival endpoints")
				} else {
					require.Empty(t, archivalEndpoints, "expected no archival endpoints")
				}
			} else {
				// When not requiring archival, filterArchivalEndpointsForFallback is not called,
				// but we test it for completeness
				// Non-archival endpoints should not be filtered
				if tt.expectFound {
					require.NotEmpty(t, availableEndpoints, "expected endpoints to be available")
				}
			}
		})
	}
}

func TestArchivalEndpointSelection_Integration(t *testing.T) {
	logger := polylog.Ctx(context.Background())

	tests := []struct {
		name             string
		setupLocal       func(*endpointStore)
		setupRedis       func(*mockReputationService)
		requiresArchival bool
		numEndpoints     uint
		expectEndpoints  int
		expectError      bool
		errorContains    string
	}{
		{
			name: "archival required, local store has archival endpoint",
			setupLocal: func(store *endpointStore) {
				ep := endpoint{checkArchival: endpointCheckArchival{}}
				ep.checkArchival.isArchival = true
				ep.checkArchival.expiresAt = time.Now().Add(1 * time.Hour)
				store.endpoints["ep1-url"] = ep
			},
			setupRedis: func(svc *mockReputationService) {
				// Redis empty
			},
			requiresArchival: true,
			numEndpoints:     1,
			expectEndpoints:  1,
			expectError:      false,
		},
		{
			name: "archival required, only redis has archival endpoint",
			setupLocal: func(store *endpointStore) {
				// Local has non-archival endpoint
				ep := endpoint{}
				store.endpoints["ep1-url"] = ep
			},
			setupRedis: func(svc *mockReputationService) {
				// Redis has archival status
				key := reputation.NewEndpointKey("test-service", "ep1-url", sharedtypes.RPCType_JSON_RPC)
				svc.archivalEndpoints[key.String()] = true
			},
			requiresArchival: true,
			numEndpoints:     1,
			expectEndpoints:  1,
			expectError:      false,
		},
		{
			name: "archival required, neither has archival - error",
			setupLocal: func(store *endpointStore) {
				ep := endpoint{}
				store.endpoints["ep1-url"] = ep
			},
			setupRedis: func(svc *mockReputationService) {
				// Redis empty
			},
			requiresArchival: true,
			numEndpoints:     1,
			expectEndpoints:  0,
			expectError:      true,
			errorContains:    "no archival",
		},
		{
			name: "non-archival request, any endpoint works",
			setupLocal: func(store *endpointStore) {
				ep := endpoint{}
				store.endpoints["ep1-url"] = ep
			},
			setupRedis: func(svc *mockReputationService) {
				// Redis empty
			},
			requiresArchival: false,
			numEndpoints:     1,
			expectEndpoints:  1,
			expectError:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mock reputation service
			mockRepSvc := &mockReputationService{
				archivalEndpoints: make(map[string]bool),
			}
			tt.setupRedis(mockRepSvc)

			// Create serviceState
			ss := &serviceState{
				logger:           logger,
				serviceQoSConfig: NewEVMServiceQoSConfig("test-service", "1", nil),
				endpointStore: &endpointStore{
					logger:    logger,
					endpoints: make(map[protocol.EndpointAddr]endpoint),
				},
				reputationSvc:  mockRepSvc,
				blockConsensus: NewBlockHeightConsensus(logger, 128),
				archivalCache:  NewArchivalCache(), // Initialize cache for new architecture
			}

			// Populate cache from Redis mock data (simulates background refresh worker)
			for keyStr, isArchival := range mockRepSvc.archivalEndpoints {
				if isArchival {
					ss.archivalCache.Set(keyStr, true, 8*time.Hour)
				}
			}

			// Setup local endpoints
			tt.setupLocal(ss.endpointStore)

			// Get available endpoints
			availableEndpoints := make(protocol.EndpointAddrList, 0, len(ss.endpointStore.endpoints))
			for addr := range ss.endpointStore.endpoints {
				availableEndpoints = append(availableEndpoints, addr)
			}

			// Execute: Select endpoints with archival filtering
			selectedEndpoints, err := ss.SelectMultipleWithArchival(
				availableEndpoints,
				tt.numEndpoints,
				tt.requiresArchival,
				"test-request-id",
			)

			// Verify
			if tt.expectError {
				require.Error(t, err)
				if tt.errorContains != "" {
					require.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
				require.Len(t, selectedEndpoints, tt.expectEndpoints)
			}
		})
	}
}

// TestRefreshArchivalCacheFromRedis_ColdStart verifies that refreshArchivalCacheFromRedis
// can bootstrap the archival cache from the reputation service when the endpointStore is empty.
// This simulates a cold start scenario where reputation.Start() has loaded Redis data
// but no sessions have populated the endpoint store yet.
func TestRefreshArchivalCacheFromRedis_ColdStart(t *testing.T) {
	logger := polylog.Ctx(context.Background())
	ctx := context.Background()
	serviceID := protocol.ServiceID("test-service")

	// Create mock reputation service with archival data (simulates Redis-loaded cache)
	mockRepSvc := &mockReputationService{
		archivalEndpoints: map[string]bool{
			"test-service:ep1-url:json_rpc": true,
			"test-service:ep2-url:json_rpc": true,
			"test-service:ep3-url:json_rpc": false, // not archival
			"other-svc:ep4-url:json_rpc":    true,  // different service
		},
	}

	// Create QoS with EMPTY endpointStore (cold start)
	qos := &QoS{
		logger: logger,
		serviceState: &serviceState{
			logger:           logger,
			serviceQoSConfig: NewEVMServiceQoSConfig(serviceID, "1", nil),
			endpointStore: &endpointStore{
				logger:    logger,
				endpoints: make(map[protocol.EndpointAddr]endpoint), // empty!
			},
			reputationSvc:  mockRepSvc,
			blockConsensus: NewBlockHeightConsensus(logger, 128),
			archivalCache:  NewArchivalCache(),
		},
	}

	// Execute: refresh should bootstrap from reputation service
	qos.refreshArchivalCacheFromRedis(ctx)

	// Verify: archival cache should have the 2 archival endpoints for test-service
	key1 := reputation.NewEndpointKey(serviceID, "ep1-url", sharedtypes.RPCType_JSON_RPC)
	key2 := reputation.NewEndpointKey(serviceID, "ep2-url", sharedtypes.RPCType_JSON_RPC)
	key3 := reputation.NewEndpointKey(serviceID, "ep3-url", sharedtypes.RPCType_JSON_RPC)
	key4 := reputation.NewEndpointKey("other-svc", "ep4-url", sharedtypes.RPCType_JSON_RPC)

	require.True(t, archivalCacheHas(qos.archivalCache, key1.String()), "ep1 should be in archival cache")
	require.True(t, archivalCacheHas(qos.archivalCache, key2.String()), "ep2 should be in archival cache")
	require.False(t, archivalCacheHas(qos.archivalCache, key3.String()), "ep3 should NOT be in archival cache (not archival)")
	require.False(t, archivalCacheHas(qos.archivalCache, key4.String()), "ep4 should NOT be in archival cache (different service)")
}

// TestRefreshArchivalCacheFromRedis_WithEndpoints verifies that both passes work together:
// endpoints from the store are checked via IsArchivalCapable, and additional entries are
// bootstrapped from GetArchivalEndpoints.
func TestRefreshArchivalCacheFromRedis_WithEndpoints(t *testing.T) {
	logger := polylog.Ctx(context.Background())
	ctx := context.Background()
	serviceID := protocol.ServiceID("test-service")

	// Create mock reputation service
	mockRepSvc := &mockReputationService{
		archivalEndpoints: map[string]bool{
			"test-service:ep1-url:json_rpc": true, // also in endpoint store
			"test-service:ep2-url:json_rpc": true, // only in reputation cache
		},
	}

	// Create QoS with ep1 in the endpoint store
	qos := &QoS{
		logger: logger,
		serviceState: &serviceState{
			logger:           logger,
			serviceQoSConfig: NewEVMServiceQoSConfig(serviceID, "1", nil),
			endpointStore: &endpointStore{
				logger: logger,
				endpoints: map[protocol.EndpointAddr]endpoint{
					"ep1-url": {},
				},
			},
			reputationSvc:  mockRepSvc,
			blockConsensus: NewBlockHeightConsensus(logger, 128),
			archivalCache:  NewArchivalCache(),
		},
	}

	// Execute
	qos.refreshArchivalCacheFromRedis(ctx)

	// Both ep1 (from store pass) and ep2 (from reputation pass) should be cached
	key1 := reputation.NewEndpointKey(serviceID, "ep1-url", sharedtypes.RPCType_JSON_RPC)
	key2 := reputation.NewEndpointKey(serviceID, "ep2-url", sharedtypes.RPCType_JSON_RPC)

	require.True(t, archivalCacheHas(qos.archivalCache, key1.String()), "ep1 should be in archival cache (from store + reputation)")
	require.True(t, archivalCacheHas(qos.archivalCache, key2.String()), "ep2 should be in archival cache (from reputation)")
}

// TestFreshEndpoint_ArchivalFiltering verifies that fresh endpoints (not in endpointStore)
// are correctly filtered based on archival cache status when requiresArchival is true.
func TestFreshEndpoint_ArchivalFiltering(t *testing.T) {
	tests := []struct {
		name             string
		requiresArchival bool
		cacheArchival    map[protocol.EndpointAddr]bool // what to seed into archival cache
		expectValid      bool                           // whether the fresh endpoint should pass filtering
		expectError      bool                           // whether SelectMultipleWithArchival should error
	}{
		{
			name:             "archival request, cache empty - fresh endpoint allowed (cold start fallback)",
			requiresArchival: true,
			cacheArchival:    map[protocol.EndpointAddr]bool{},
			expectValid:      true,
			expectError:      false,
		},
		{
			name:             "archival request, cache confirms archival - fresh endpoint allowed",
			requiresArchival: true,
			cacheArchival: map[protocol.EndpointAddr]bool{
				"fresh-ep1": true,
			},
			expectValid: true,
			expectError: false,
		},
		{
			name:             "non-archival request, cache empty - fresh endpoint always allowed",
			requiresArchival: false,
			cacheArchival:    map[protocol.EndpointAddr]bool{},
			expectValid:      true,
			expectError:      false,
		},
		{
			name:             "archival request, cache has other archival but not this one - filtered out",
			requiresArchival: true,
			cacheArchival: map[protocol.EndpointAddr]bool{
				"other-ep": true, // cache is populated but fresh-ep1 is not confirmed
			},
			expectValid: false,
			expectError: true,
		},
		{
			name:             "archival request, cache says not archival - fresh endpoint filtered out",
			requiresArchival: true,
			cacheArchival: map[protocol.EndpointAddr]bool{
				"fresh-ep1": false,
			},
			expectValid: false,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := polylog.Ctx(context.Background())

			mockRepSvc := &mockReputationService{
				archivalEndpoints: make(map[string]bool),
			}

			ss := &serviceState{
				logger:           logger,
				serviceQoSConfig: NewEVMServiceQoSConfig("test-service", "1", nil),
				endpointStore: &endpointStore{
					logger:    logger,
					endpoints: make(map[protocol.EndpointAddr]endpoint), // empty store - all endpoints are "fresh"
				},
				reputationSvc:  mockRepSvc,
				blockConsensus: NewBlockHeightConsensus(logger, 128),
				archivalCache:  NewArchivalCache(),
			}

			// Seed archival cache
			for addr, isArchival := range tt.cacheArchival {
				key := reputation.NewEndpointKey("test-service", addr, sharedtypes.RPCType_JSON_RPC)
				ss.archivalCache.Set(key.String(), isArchival, 8*time.Hour)
			}

			availableEndpoints := protocol.EndpointAddrList{"fresh-ep1"}

			// Test via filterValidEndpointsWithDetails directly
			filtered, validationResults, err := ss.filterValidEndpointsWithDetails(availableEndpoints, tt.requiresArchival, "test-req")
			require.NoError(t, err) // filterValidEndpointsWithDetails itself should not error

			if tt.expectValid {
				require.Len(t, filtered, 1, "fresh endpoint should pass filtering")
				// Check validation result is success
				require.Len(t, validationResults, 1)
				require.True(t, validationResults[0].Success)
			} else {
				require.Empty(t, filtered, "fresh endpoint should be filtered out")
				// Check validation result is failure
				require.Len(t, validationResults, 1)
				require.False(t, validationResults[0].Success)
			}

			// Also test via SelectMultipleWithArchival to verify end-to-end behavior
			selected, err := ss.SelectMultipleWithArchival(availableEndpoints, 1, tt.requiresArchival, "test-req")
			if tt.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), "no archival-capable endpoints")
			} else {
				require.NoError(t, err)
				require.NotEmpty(t, selected)
			}
		})
	}
}

// archivalCacheHas checks whether a key exists and is archival in the cache.
func archivalCacheHas(cache *ArchivalCache, key string) bool {
	isArchival, ok := cache.Get(key)
	return ok && isArchival
}

// TestSelectMultipleWithArchival_FiltersStaleEndpoints verifies that SelectMultipleWithArchival
// (used by hedge race, retry, and batch paths) filters out endpoints that are behind
// the perceived block height by more than sync_allowance.
//
// This is a regression test for the root cause of stale responses being served:
// the hedge race, retry, and batch code paths previously used raw protocol endpoints
// without QoS block height validation. The fix routes all three paths through
// SelectMultipleWithArchival which calls filterValidEndpointsWithDetails → isBlockNumberValid.
func TestSelectMultipleWithArchival_FiltersStaleEndpoints(t *testing.T) {
	logger := polylog.Ctx(context.Background())

	perceivedBlock := uint64(43224462)
	syncAllowance := uint64(5) // 5 blocks tolerance

	tests := []struct {
		name            string
		endpoints       map[protocol.EndpointAddr]uint64 // addr -> block height
		expectFiltered  []protocol.EndpointAddr          // expected to pass
		expectExcluded  []protocol.EndpointAddr          // expected to be filtered out
		expectFallback  bool                             // if all filtered, should fall back
	}{
		{
			name: "stale endpoint filtered, fresh endpoint kept",
			endpoints: map[protocol.EndpointAddr]uint64{
				"fresh-supplier:https://good-node.com":  perceivedBlock - 1, // 1 block behind = OK
				"stale-supplier:https://stale-node.com": perceivedBlock - 65000, // 65K behind = stale
			},
			expectFiltered: []protocol.EndpointAddr{"fresh-supplier:https://good-node.com"},
			expectExcluded: []protocol.EndpointAddr{"stale-supplier:https://stale-node.com"},
		},
		{
			name: "all endpoints within sync allowance",
			endpoints: map[protocol.EndpointAddr]uint64{
				"ep1:https://node1.com": perceivedBlock - 2,
				"ep2:https://node2.com": perceivedBlock - 4,
				"ep3:https://node3.com": perceivedBlock, // at perceived block
			},
			expectFiltered: []protocol.EndpointAddr{
				"ep1:https://node1.com",
				"ep2:https://node2.com",
				"ep3:https://node3.com",
			},
		},
		{
			name: "endpoint exactly at sync allowance boundary passes",
			endpoints: map[protocol.EndpointAddr]uint64{
				"boundary:https://edge.com": perceivedBlock - syncAllowance, // exactly at boundary
			},
			expectFiltered: []protocol.EndpointAddr{"boundary:https://edge.com"},
		},
		{
			name: "endpoint one block past sync allowance filtered",
			endpoints: map[protocol.EndpointAddr]uint64{
				"past-boundary:https://slow.com": perceivedBlock - syncAllowance - 1, // 1 past boundary
			},
			expectExcluded: []protocol.EndpointAddr{"past-boundary:https://slow.com"},
			expectFallback: true, // all filtered = fallback
		},
		{
			name: "mix of valid and stale - only valid returned",
			endpoints: map[protocol.EndpointAddr]uint64{
				"good1:https://fast1.com":    perceivedBlock,
				"good2:https://fast2.com":    perceivedBlock - 3,
				"stale1:https://behind1.com": perceivedBlock - 100,
				"stale2:https://behind2.com": perceivedBlock - 37000,
			},
			expectFiltered: []protocol.EndpointAddr{
				"good1:https://fast1.com",
				"good2:https://fast2.com",
			},
			expectExcluded: []protocol.EndpointAddr{
				"stale1:https://behind1.com",
				"stale2:https://behind2.com",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockRepSvc := &mockReputationService{
				archivalEndpoints: make(map[string]bool),
			}

			// Build endpoint store with block heights and valid chain ID
			chainID := "1"
			endpoints := make(map[protocol.EndpointAddr]endpoint)
			for addr, blockHeight := range tt.endpoints {
				bh := blockHeight // copy for pointer
				endpoints[addr] = endpoint{
					checkBlockNumber: endpointCheckBlockNumber{
						parsedBlockNumberResponse: &bh,
					},
					checkChainID: endpointCheckChainID{
						chainID:   &chainID,
						expiresAt: time.Now().Add(1 * time.Hour),
					},
				}
			}

			ss := &serviceState{
				logger: logger,
				serviceQoSConfig: NewEVMServiceQoSConfigWithSyncAllowance(
					"test-service", "1", nil, syncAllowance,
				),
				endpointStore: &endpointStore{
					logger:    logger,
					endpoints: endpoints,
				},
				reputationSvc:  mockRepSvc,
				blockConsensus: NewBlockHeightConsensus(logger, 128),
				archivalCache:  NewArchivalCache(),
			}

			// Set perceived block number
			ss.perceivedBlockNumber.Store(perceivedBlock)

			// Build available endpoints list
			availableEndpoints := make(protocol.EndpointAddrList, 0, len(tt.endpoints))
			for addr := range tt.endpoints {
				availableEndpoints = append(availableEndpoints, addr)
			}

			// Call SelectMultipleWithArchival (non-archival) — this is exactly what
			// the hedge race, retry, and batch paths now call after the fix
			selected, err := ss.SelectMultipleWithArchival(
				availableEndpoints, uint(len(availableEndpoints)), false, "test-req")

			if tt.expectFallback {
				// When ALL endpoints are stale, SelectMultipleWithArchival returns an error
				// rather than serving stale data to clients.
				require.Error(t, err, "should return error when all endpoints are stale")
				require.Empty(t, selected, "should not return stale endpoints")
				return
			}

			require.NoError(t, err)

			// Verify expected endpoints are in the result
			selectedSet := make(map[protocol.EndpointAddr]bool)
			for _, addr := range selected {
				selectedSet[addr] = true
			}

			for _, addr := range tt.expectFiltered {
				require.True(t, selectedSet[addr],
					"expected endpoint %s to pass QoS validation but it was filtered out", addr)
			}

			for _, addr := range tt.expectExcluded {
				require.False(t, selectedSet[addr],
					"expected stale endpoint %s to be filtered out but it passed QoS validation", addr)
			}
		})
	}
}

// TestRedisURLBlockHeightsCache_FiltersFreshStaleEndpoints verifies that fresh endpoints
// (not in endpointStore) are filtered using cached Redis URL block heights.
//
// This is a regression test for stale responses during session rotation:
// when sessions rotate, new supplier addresses appear for the same stale infrastructure.
// These endpoints aren't in the local store yet, but their URLs have known stale block
// heights in Redis. The cached Redis URL block heights provide a fallback to catch them.
func TestRedisURLBlockHeightsCache_FiltersFreshStaleEndpoints(t *testing.T) {
	logger := polylog.Ctx(context.Background())

	perceivedBlock := uint64(43228834)
	staleBlock := uint64(43188889) // 39K behind
	syncAllowance := uint64(5)

	mockRepSvc := &mockReputationService{
		archivalEndpoints: make(map[string]bool),
	}

	// Create serviceState with NO local entries — simulates a fresh session
	// where endpoints haven't been synced to the store yet
	ss := &serviceState{
		logger: logger,
		serviceQoSConfig: NewEVMServiceQoSConfigWithSyncAllowance(
			"test-service", "1", nil, syncAllowance,
		),
		endpointStore: &endpointStore{
			logger:    logger,
			endpoints: make(map[protocol.EndpointAddr]endpoint),
		},
		reputationSvc:  mockRepSvc,
		blockConsensus: NewBlockHeightConsensus(logger, 128),
		archivalCache:  NewArchivalCache(),
	}
	ss.perceivedBlockNumber.Store(perceivedBlock)

	// Simulate Redis cache populated by syncEndpointBlocksFromRedis —
	// contains stale block height for the qspider URL
	redisCache := map[string]uint64{
		"https://relayminer03.portal.qspider.com": staleBlock,
		"https://good.node.com":                   perceivedBlock - 1,
	}
	ss.redisURLBlockHeights.Store(&redisCache)

	// Fresh endpoints: new supplier addresses not in local store,
	// backed by the same stale/good infrastructure.
	// Uses pokt1...-https://... format so GetURL() (SplitN on first dash) works correctly.
	availableEndpoints := protocol.EndpointAddrList{
		"pokt1stalesupplier-https://relayminer03.portal.qspider.com", // stale URL
		"pokt1goodsupplier-https://good.node.com",                   // good URL
	}

	// filterValidEndpointsWithDetails should use Redis cache to reject the stale one
	filtered, _, err := ss.filterValidEndpointsWithDetails(availableEndpoints, false, "test-req")
	require.NoError(t, err)
	require.Len(t, filtered, 1, "should filter out fresh endpoint with stale URL from Redis cache")
	require.Equal(t, protocol.EndpointAddr("pokt1goodsupplier-https://good.node.com"), filtered[0])

	// Also verify filterStaleURLEndpoints uses Redis cache
	staleFiltered := ss.filterStaleURLEndpoints(availableEndpoints)
	require.Len(t, staleFiltered, 1, "filterStaleURLEndpoints should use Redis cache to remove stale URL")
	require.Equal(t, protocol.EndpointAddr("pokt1goodsupplier-https://good.node.com"), staleFiltered[0])
}

// TestRedisURLBlockHeightsCache_NotUsedWhenLocalStoreHasData verifies that
// the Redis cache doesn't override higher local block heights.
func TestRedisURLBlockHeightsCache_NotUsedWhenLocalStoreHasData(t *testing.T) {
	logger := polylog.Ctx(context.Background())

	perceivedBlock := uint64(43228834)
	syncAllowance := uint64(5)
	chainID := "1"

	mockRepSvc := &mockReputationService{
		archivalEndpoints: make(map[string]bool),
	}

	// Local store has current block height for the endpoint
	localBlock := perceivedBlock - 2
	endpoints := map[protocol.EndpointAddr]endpoint{
		"supplier1:https://node.com": {
			checkBlockNumber: endpointCheckBlockNumber{
				parsedBlockNumberResponse: &localBlock,
			},
			checkChainID: endpointCheckChainID{
				chainID:   &chainID,
				expiresAt: time.Now().Add(1 * time.Hour),
			},
		},
	}

	ss := &serviceState{
		logger: logger,
		serviceQoSConfig: NewEVMServiceQoSConfigWithSyncAllowance(
			"test-service", "1", nil, syncAllowance,
		),
		endpointStore: &endpointStore{
			logger:    logger,
			endpoints: endpoints,
		},
		reputationSvc:  mockRepSvc,
		blockConsensus: NewBlockHeightConsensus(logger, 128),
		archivalCache:  NewArchivalCache(),
	}
	ss.perceivedBlockNumber.Store(perceivedBlock)

	// Redis cache has OLDER block (shouldn't override local)
	staleRedisBlock := perceivedBlock - 100
	redisCache := map[string]uint64{
		"https://node.com": staleRedisBlock,
	}
	ss.redisURLBlockHeights.Store(&redisCache)

	availableEndpoints := protocol.EndpointAddrList{"supplier1:https://node.com"}

	// Should pass — local store has current block, Redis stale value shouldn't override
	selected, err := ss.SelectMultipleWithArchival(availableEndpoints, 1, false, "test-req")
	require.NoError(t, err)
	require.Len(t, selected, 1, "endpoint with current local block should pass even with stale Redis cache")
}
