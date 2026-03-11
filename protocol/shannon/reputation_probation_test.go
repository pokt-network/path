package shannon

import (
	"context"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
	reputationstorage "github.com/pokt-network/path/reputation/storage"
)

// TestProbationRouting_IncludesTierFallback verifies that when probation routing
// is active, the result includes BOTH probation endpoints AND the highest-tier
// endpoints. This prevents serving stale data when all probation endpoints fail
// QoS validation (e.g., stale block height).
//
// Scenario: qspider endpoints are in probation (score 30, 39K blocks behind).
// Probation routing picks them, but QoS would reject them as stale.
// With the fix, tier 1 endpoints (nodefleet, score 90) are also included,
// so QoS has healthy fallback options.
func TestProbationRouting_IncludesTierFallback(t *testing.T) {
	ctx := context.Background()
	logger := polyzero.NewLogger()
	serviceID := protocol.ServiceID("base")
	rpcType := sharedtypes.RPCType_JSON_RPC

	// Set up reputation service with memory storage
	repConfig := reputation.Config{
		Enabled:        true,
		InitialScore:   80,
		MinThreshold:   20,
		KeyGranularity: "per-endpoint",
	}
	store := reputationstorage.NewMemoryStorage(10 * time.Minute)
	repSvc := reputation.NewService(repConfig, store)

	// Tiered selection config matching production
	tieredConfig := reputation.TieredSelectionConfig{
		Enabled:        true,
		Tier1Threshold: 80,
		Tier2Threshold: 50,
		Probation: reputation.ProbationConfig{
			Enabled:        true,
			Threshold:      40,
			TrafficPercent: 100, // Force probation routing for test determinism
		},
	}
	selector := reputation.NewTieredSelectorWithLogger(logger, tieredConfig, repConfig.MinThreshold)

	// Create protocol with reputation service and tiered selector
	p := &Protocol{
		reputationService: repSvc,
		tieredSelector:    selector,
	}

	// Define endpoints:
	// - 3 probation endpoints (qspider, score 30 — below probation threshold of 40)
	// - 3 tier 1 endpoints (nodefleet, score 90 — above tier1 threshold of 80)
	probationAddrs := []protocol.EndpointAddr{
		"pokt1qspider1-https://relayminer.portal.qspider.com",
		"pokt1qspider2-https://relayminer02.portal.qspider.com",
		"pokt1qspider3-https://relayminer03.portal.qspider.com",
	}
	tier1Addrs := []protocol.EndpointAddr{
		"pokt1nodefleet1-https://nf.relayminer.nodefleet.net",
		"pokt1nodefleet2-https://pkp.relayminer.nodefleet.net",
		"pokt1nodefleet3-https://nf2.relayminer.nodefleet.net",
	}

	// Build endpoint map
	endpoints := make(map[protocol.EndpointAddr]endpoint)
	for _, addr := range probationAddrs {
		endpoints[addr] = &mockEndpoint{addr: addr}
	}
	for _, addr := range tier1Addrs {
		endpoints[addr] = &mockEndpoint{addr: addr}
	}

	// Set reputation scores — probation endpoints get score 30, tier 1 get score 90
	keyBuilder := repSvc.KeyBuilderForService(serviceID)
	for _, addr := range probationAddrs {
		key := keyBuilder.BuildKey(serviceID, addr, rpcType)
		// Set score to ~30 (below probation threshold of 40, above minThreshold of 20)
		// Start at initial (80), MajorError = -10 each, so 5 errors → 80-50 = 30
		for i := 0; i < 5; i++ {
			_ = repSvc.RecordSignal(ctx, key, reputation.Signal{
				Type:   reputation.SignalTypeMajorError,
				Reason: "stale_block_height",
			})
		}
	}
	for _, addr := range tier1Addrs {
		key := keyBuilder.BuildKey(serviceID, addr, rpcType)
		// Record some successes to keep score high
		for i := 0; i < 5; i++ {
			_ = repSvc.RecordSignal(ctx, key, reputation.Signal{
				Type:   reputation.SignalTypeSuccess,
				Reason: "healthy",
			})
		}
	}

	// Verify scores are in expected ranges
	for _, addr := range probationAddrs {
		key := keyBuilder.BuildKey(serviceID, addr, rpcType)
		score, err := repSvc.GetScore(ctx, key)
		require.NoError(t, err)
		require.Less(t, score.Value, tieredConfig.Probation.Threshold,
			"probation endpoint %s should have score below probation threshold (got %.1f)", addr, score.Value)
		require.GreaterOrEqual(t, score.Value, repConfig.MinThreshold,
			"probation endpoint %s should be above min threshold (got %.1f)", addr, score.Value)
	}
	for _, addr := range tier1Addrs {
		key := keyBuilder.BuildKey(serviceID, addr, rpcType)
		score, err := repSvc.GetScore(ctx, key)
		require.NoError(t, err)
		require.GreaterOrEqual(t, score.Value, tieredConfig.Tier1Threshold,
			"tier1 endpoint %s should have score >= tier1 threshold (got %.1f)", addr, score.Value)
	}

	// Call filterToHighestTier — with TrafficPercent=100, probation routing is always active
	result := p.filterToHighestTier(ctx, serviceID, endpoints, rpcType, logger, "")

	// The result should contain BOTH probation and tier 1 endpoints
	require.GreaterOrEqual(t, len(result), len(probationAddrs)+len(tier1Addrs),
		"result should contain both probation and tier endpoints, got %d (expected >= %d)",
		len(result), len(probationAddrs)+len(tier1Addrs))

	// Verify all probation endpoints are present
	for _, addr := range probationAddrs {
		_, exists := result[addr]
		require.True(t, exists, "probation endpoint %s should be in result", addr)
	}

	// Verify all tier 1 endpoints are present (the fallback)
	for _, addr := range tier1Addrs {
		_, exists := result[addr]
		require.True(t, exists, "tier 1 endpoint %s should be in result (fallback)", addr)
	}
}

// TestProbationRouting_NoProbationEndpoints verifies that when no endpoints
// are in probation, normal tier-based selection works as before.
func TestProbationRouting_NoProbationEndpoints(t *testing.T) {
	ctx := context.Background()
	logger := polyzero.NewLogger()
	serviceID := protocol.ServiceID("base")
	rpcType := sharedtypes.RPCType_JSON_RPC

	repConfig := reputation.Config{
		Enabled:        true,
		InitialScore:   80,
		MinThreshold:   20,
		KeyGranularity: "per-endpoint",
	}
	store := reputationstorage.NewMemoryStorage(10 * time.Minute)
	repSvc := reputation.NewService(repConfig, store)

	tieredConfig := reputation.TieredSelectionConfig{
		Enabled:        true,
		Tier1Threshold: 80,
		Tier2Threshold: 50,
		Probation: reputation.ProbationConfig{
			Enabled:        true,
			Threshold:      40,
			TrafficPercent: 100, // Force probation routing
		},
	}
	selector := reputation.NewTieredSelectorWithLogger(logger, tieredConfig, repConfig.MinThreshold)

	p := &Protocol{
		reputationService: repSvc,
		tieredSelector:    selector,
	}

	// All endpoints are tier 1 (score 90+) — none in probation
	tier1Addrs := []protocol.EndpointAddr{
		"pokt1good1-https://node1.com",
		"pokt1good2-https://node2.com",
		"pokt1good3-https://node3.com",
	}

	endpoints := make(map[protocol.EndpointAddr]endpoint)
	keyBuilder := repSvc.KeyBuilderForService(serviceID)
	for _, addr := range tier1Addrs {
		endpoints[addr] = &mockEndpoint{addr: addr}
		key := keyBuilder.BuildKey(serviceID, addr, rpcType)
		for i := 0; i < 5; i++ {
			_ = repSvc.RecordSignal(ctx, key, reputation.Signal{
				Type:   reputation.SignalTypeSuccess,
				Reason: "healthy",
			})
		}
	}

	result := p.filterToHighestTier(ctx, serviceID, endpoints, rpcType, logger, "")

	// No probation endpoints → probation routing skipped → normal tier selection
	require.Len(t, result, len(tier1Addrs), "should return all tier 1 endpoints")
	for _, addr := range tier1Addrs {
		_, exists := result[addr]
		require.True(t, exists, "tier 1 endpoint %s should be in result", addr)
	}
}

// TestProbationRouting_OnlyProbationEndpoints verifies behavior when ALL
// endpoints are in probation (no tier fallback available).
func TestProbationRouting_OnlyProbationEndpoints(t *testing.T) {
	ctx := context.Background()
	logger := polyzero.NewLogger()
	serviceID := protocol.ServiceID("base")
	rpcType := sharedtypes.RPCType_JSON_RPC

	repConfig := reputation.Config{
		Enabled:        true,
		InitialScore:   80,
		MinThreshold:   20,
		KeyGranularity: "per-endpoint",
	}
	store := reputationstorage.NewMemoryStorage(10 * time.Minute)
	repSvc := reputation.NewService(repConfig, store)

	tieredConfig := reputation.TieredSelectionConfig{
		Enabled:        true,
		Tier1Threshold: 80,
		Tier2Threshold: 50,
		Probation: reputation.ProbationConfig{
			Enabled:        true,
			Threshold:      40,
			TrafficPercent: 100,
		},
	}
	selector := reputation.NewTieredSelectorWithLogger(logger, tieredConfig, repConfig.MinThreshold)

	p := &Protocol{
		reputationService: repSvc,
		tieredSelector:    selector,
	}

	// All endpoints are in probation — score ~30 (between minThreshold 20 and probation 40)
	probationAddrs := []protocol.EndpointAddr{
		"pokt1stale1-https://stale1.com",
		"pokt1stale2-https://stale2.com",
	}

	endpoints := make(map[protocol.EndpointAddr]endpoint)
	keyBuilder := repSvc.KeyBuilderForService(serviceID)
	for _, addr := range probationAddrs {
		endpoints[addr] = &mockEndpoint{addr: addr}
		key := keyBuilder.BuildKey(serviceID, addr, rpcType)
		// 5 major errors: 80 - 50 = 30 (in probation range 20-40)
		for i := 0; i < 5; i++ {
			_ = repSvc.RecordSignal(ctx, key, reputation.Signal{
				Type:   reputation.SignalTypeMajorError,
				Reason: "stale",
			})
		}
	}

	result := p.filterToHighestTier(ctx, serviceID, endpoints, rpcType, logger, "")

	// All endpoints are in probation, tier fallback also returns the same endpoints
	// (they're tier 3). Result should still contain all of them.
	require.Len(t, result, len(probationAddrs),
		"should return probation endpoints (tier fallback has same endpoints)")
	for _, addr := range probationAddrs {
		_, exists := result[addr]
		require.True(t, exists, "probation endpoint %s should be in result", addr)
	}
}
