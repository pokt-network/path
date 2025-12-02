package reputation

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// =============================================================================
// Happy Path Tests
// =============================================================================

func TestTieredSelector_AllTiersPopulated(t *testing.T) {
	selector := NewTieredSelector(TieredSelectionConfig{
		Enabled:        true,
		Tier1Threshold: 70,
		Tier2Threshold: 50,
	}, 30)

	endpoints := map[EndpointKey]float64{
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("tier1-endpoint")): 85, // Tier 1
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("tier2-endpoint")): 60, // Tier 2
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("tier3-endpoint")): 40, // Tier 3
	}

	// Run multiple times to verify tier 1 is always selected
	for i := 0; i < 10; i++ {
		selected, tier, err := selector.SelectEndpoint(endpoints)
		require.NoError(t, err)
		require.Equal(t, 1, tier, "Should always select from Tier 1 when available")
		require.Equal(t, "tier1-endpoint", string(selected.EndpointAddr))
	}
}

func TestTieredSelector_Tier1Empty_SelectsFromTier2(t *testing.T) {
	selector := NewTieredSelector(TieredSelectionConfig{
		Enabled:        true,
		Tier1Threshold: 70,
		Tier2Threshold: 50,
	}, 30)

	endpoints := map[EndpointKey]float64{
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("tier2-endpoint-a")): 60, // Tier 2
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("tier2-endpoint-b")): 55, // Tier 2
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("tier3-endpoint")):   40, // Tier 3
	}

	// Run multiple times to verify tier 2 is always selected (no tier 1 available)
	for i := 0; i < 10; i++ {
		selected, tier, err := selector.SelectEndpoint(endpoints)
		require.NoError(t, err)
		require.Equal(t, 2, tier, "Should select from Tier 2 when Tier 1 is empty")
		require.Contains(t, string(selected.EndpointAddr), "tier2-endpoint")
	}
}

func TestTieredSelector_Tier1And2Empty_SelectsFromTier3(t *testing.T) {
	selector := NewTieredSelector(TieredSelectionConfig{
		Enabled:        true,
		Tier1Threshold: 70,
		Tier2Threshold: 50,
	}, 30)

	endpoints := map[EndpointKey]float64{
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("tier3-endpoint-a")): 45, // Tier 3
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("tier3-endpoint-b")): 35, // Tier 3
	}

	// Run multiple times to verify tier 3 is always selected (no tier 1 or 2 available)
	for i := 0; i < 10; i++ {
		selected, tier, err := selector.SelectEndpoint(endpoints)
		require.NoError(t, err)
		require.Equal(t, 3, tier, "Should select from Tier 3 when Tiers 1 and 2 are empty")
		require.Contains(t, string(selected.EndpointAddr), "tier3-endpoint")
	}
}

func TestTieredSelector_AllTiersEmpty_ReturnsError(t *testing.T) {
	selector := NewTieredSelector(TieredSelectionConfig{
		Enabled:        true,
		Tier1Threshold: 70,
		Tier2Threshold: 50,
	}, 30)

	// All endpoints below min threshold (should have been filtered earlier)
	endpoints := map[EndpointKey]float64{
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("low-score-a")): 25,
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("low-score-b")): 10,
	}

	_, tier, err := selector.SelectEndpoint(endpoints)
	require.ErrorIs(t, err, ErrNoEndpointsAvailable)
	require.Equal(t, 0, tier)
}

func TestTieredSelector_EmptyEndpoints_ReturnsError(t *testing.T) {
	selector := NewTieredSelector(TieredSelectionConfig{
		Enabled:        true,
		Tier1Threshold: 70,
		Tier2Threshold: 50,
	}, 30)

	endpoints := map[EndpointKey]float64{}

	_, tier, err := selector.SelectEndpoint(endpoints)
	require.ErrorIs(t, err, ErrNoEndpointsAvailable)
	require.Equal(t, 0, tier)
}

func TestTieredSelector_Disabled_RandomSelection(t *testing.T) {
	selector := NewTieredSelector(TieredSelectionConfig{
		Enabled:        false, // Disabled
		Tier1Threshold: 70,
		Tier2Threshold: 50,
	}, 30)

	// Mix of tiers
	endpoints := map[EndpointKey]float64{
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("tier1")): 85,
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("tier2")): 60,
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("tier3")): 40,
	}

	// When disabled, tier should be 0 (no tiering)
	selected, tier, err := selector.SelectEndpoint(endpoints)
	require.NoError(t, err)
	require.Equal(t, 0, tier, "Should return tier 0 when tiered selection is disabled")
	require.NotEmpty(t, selected.EndpointAddr)
}

func TestTieredSelector_CustomThresholds(t *testing.T) {
	// Use custom thresholds: 80/60/40
	selector := NewTieredSelector(TieredSelectionConfig{
		Enabled:        true,
		Tier1Threshold: 80,
		Tier2Threshold: 60,
	}, 40)

	endpoints := map[EndpointKey]float64{
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("score-75")): 75, // Tier 2 with custom thresholds
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("score-55")): 55, // Tier 3 with custom thresholds
	}

	selected, tier, err := selector.SelectEndpoint(endpoints)
	require.NoError(t, err)
	require.Equal(t, 2, tier, "Score 75 should be Tier 2 with threshold 80")
	require.Equal(t, "score-75", string(selected.EndpointAddr))
}

// =============================================================================
// GroupByTier Tests
// =============================================================================

func TestTieredSelector_GroupByTier(t *testing.T) {
	selector := NewTieredSelector(TieredSelectionConfig{
		Enabled:        true,
		Tier1Threshold: 70,
		Tier2Threshold: 50,
	}, 30)

	endpoints := map[EndpointKey]float64{
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("t1-a")): 90,
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("t1-b")): 70, // Exactly at threshold
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("t2-a")): 65,
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("t2-b")): 50, // Exactly at threshold
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("t3-a")): 45,
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("t3-b")): 30, // Exactly at min threshold
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("low")):  25, // Below threshold
	}

	tier1, tier2, tier3 := selector.GroupByTier(endpoints)

	require.Len(t, tier1, 2, "Should have 2 endpoints in Tier 1")
	require.Len(t, tier2, 2, "Should have 2 endpoints in Tier 2")
	require.Len(t, tier3, 2, "Should have 2 endpoints in Tier 3")
}

func TestTieredSelector_GroupByTier_BoundaryValues(t *testing.T) {
	selector := NewTieredSelector(TieredSelectionConfig{
		Enabled:        true,
		Tier1Threshold: 70,
		Tier2Threshold: 50,
	}, 30)

	tests := []struct {
		name         string
		score        float64
		expectedTier int
	}{
		{"exactly tier1 threshold", 70, 1},
		{"just above tier1 threshold", 70.1, 1},
		{"just below tier1 threshold", 69.9, 2},
		{"exactly tier2 threshold", 50, 2},
		{"just above tier2 threshold", 50.1, 2},
		{"just below tier2 threshold", 49.9, 3},
		{"exactly min threshold", 30, 3},
		{"just above min threshold", 30.1, 3},
		{"just below min threshold", 29.9, 0},
		{"zero score", 0, 0},
		{"max score", 100, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tier := selector.TierForScore(tt.score)
			require.Equal(t, tt.expectedTier, tier)
		})
	}
}

// =============================================================================
// TierForScore Tests
// =============================================================================

func TestTieredSelector_TierForScore(t *testing.T) {
	selector := NewTieredSelector(TieredSelectionConfig{
		Enabled:        true,
		Tier1Threshold: 70,
		Tier2Threshold: 50,
	}, 30)

	require.Equal(t, 1, selector.TierForScore(100))
	require.Equal(t, 1, selector.TierForScore(85))
	require.Equal(t, 1, selector.TierForScore(70))
	require.Equal(t, 2, selector.TierForScore(69))
	require.Equal(t, 2, selector.TierForScore(60))
	require.Equal(t, 2, selector.TierForScore(50))
	require.Equal(t, 3, selector.TierForScore(49))
	require.Equal(t, 3, selector.TierForScore(40))
	require.Equal(t, 3, selector.TierForScore(30))
	require.Equal(t, 0, selector.TierForScore(29))
	require.Equal(t, 0, selector.TierForScore(0))
}

// =============================================================================
// Config Access Tests
// =============================================================================

func TestTieredSelector_ConfigAccess(t *testing.T) {
	config := TieredSelectionConfig{
		Enabled:        true,
		Tier1Threshold: 75,
		Tier2Threshold: 55,
	}
	selector := NewTieredSelector(config, 35)

	require.Equal(t, config, selector.Config())
	require.Equal(t, float64(35), selector.MinThreshold())
}

// =============================================================================
// Multiple Endpoints in Same Tier Tests
// =============================================================================

func TestTieredSelector_RandomWithinTier(t *testing.T) {
	selector := NewTieredSelector(TieredSelectionConfig{
		Enabled:        true,
		Tier1Threshold: 70,
		Tier2Threshold: 50,
	}, 30)

	// All endpoints in Tier 1
	endpoints := map[EndpointKey]float64{
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("endpoint-a")): 90,
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("endpoint-b")): 85,
		NewEndpointKey(protocol.ServiceID("eth"), protocol.EndpointAddr("endpoint-c")): 80,
	}

	// Track selections over multiple iterations
	selections := make(map[string]int)
	iterations := 100

	for i := 0; i < iterations; i++ {
		selected, tier, err := selector.SelectEndpoint(endpoints)
		require.NoError(t, err)
		require.Equal(t, 1, tier)
		selections[string(selected.EndpointAddr)]++
	}

	// All endpoints should be selected at least once (with high probability)
	// This is a statistical test, so we use a lenient check
	require.GreaterOrEqual(t, len(selections), 2, "Should select from multiple endpoints")
}
