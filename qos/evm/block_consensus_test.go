package evm

import (
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

func TestBlockHeightConsensus_BasicOperation(t *testing.T) {
	logger := polyzero.NewLogger()
	consensus := NewBlockHeightConsensus(logger, 10) // sync allowance of 10

	// Initially should be 0
	assert.Equal(t, uint64(0), consensus.GetPerceivedBlock())

	// Add first observation
	perceived := consensus.AddObservation("endpoint1", 1000)
	assert.Equal(t, uint64(1000), perceived)
	assert.Equal(t, uint64(1000), consensus.GetPerceivedBlock())

	// Add higher observation - should update
	perceived = consensus.AddObservation("endpoint2", 1005)
	assert.Equal(t, uint64(1005), perceived)

	// Add lower observation - should not decrease
	perceived = consensus.AddObservation("endpoint3", 1002)
	assert.Equal(t, uint64(1005), perceived)
}

func TestBlockHeightConsensus_OutlierRejection(t *testing.T) {
	logger := polyzero.NewLogger()
	// sync allowance of 10, multiplier of 3 (default) = max deviation of 30
	consensus := NewBlockHeightConsensus(logger, 10)

	// Add several normal observations to establish median
	consensus.AddObservation("endpoint1", 1000)
	consensus.AddObservation("endpoint2", 1002)
	consensus.AddObservation("endpoint3", 1001)
	consensus.AddObservation("endpoint4", 1003)
	consensus.AddObservation("endpoint5", 1002)

	// Median should be around 1002
	median := consensus.GetMedianBlock()
	assert.True(t, median >= 1001 && median <= 1003, "median should be around 1002, got %d", median)

	// Perceived should be max valid (1003)
	assert.Equal(t, uint64(1003), consensus.GetPerceivedBlock())

	// Now add an outlier - way too high
	// With median ~1002 and max deviation 30, anything above 1032 should be rejected
	perceived := consensus.AddObservation("malicious", 999999999)

	// Perceived should NOT jump to the malicious value
	assert.True(t, perceived < 2000, "outlier should be rejected, got perceived=%d", perceived)

	// The outlier should be ignored in consensus
	assert.Equal(t, perceived, consensus.GetPerceivedBlock())
}

func TestBlockHeightConsensus_GradualIncrease(t *testing.T) {
	logger := polyzero.NewLogger()
	// Use a larger sync allowance (100) to simulate a real chain
	// where endpoints might be spread across ~100 blocks
	consensus := NewBlockHeightConsensus(logger, 100)

	// Simulate multiple endpoints reporting blocks over time
	// In reality, endpoints report roughly the same block with slight variance
	for round := 0; round < 20; round++ {
		baseBlock := uint64(1000 + round*5) // Chain advances 5 blocks per round

		// Multiple endpoints report blocks around the base
		consensus.AddObservation("endpoint1", baseBlock)
		consensus.AddObservation("endpoint2", baseBlock+1)
		consensus.AddObservation("endpoint3", baseBlock+2)
	}

	// Final perceived should be close to the latest blocks (~1095-1100)
	final := consensus.GetPerceivedBlock()
	assert.True(t, final >= 1090, "final perceived %d should be close to 1100", final)
}

func TestBlockHeightConsensus_MixedEndpoints(t *testing.T) {
	logger := polyzero.NewLogger()
	consensus := NewBlockHeightConsensus(logger, 10)

	// Simulate multiple endpoints with slight differences
	// Most are around 1000, but one is slightly ahead
	for i := 0; i < 10; i++ {
		consensus.AddObservation(protocol.EndpointAddr("slow1"), 1000)
		consensus.AddObservation(protocol.EndpointAddr("slow2"), 1001)
		consensus.AddObservation(protocol.EndpointAddr("fast"), 1005)
	}

	// Perceived should be 1005 (the highest valid observation)
	perceived := consensus.GetPerceivedBlock()
	assert.Equal(t, uint64(1005), perceived)

	// Median should be around 1001
	median := consensus.GetMedianBlock()
	assert.True(t, median >= 1000 && median <= 1002, "median should be ~1001, got %d", median)
}

func TestBlockHeightConsensus_Bootstrap(t *testing.T) {
	logger := polyzero.NewLogger()
	consensus := NewBlockHeightConsensus(logger, 10)

	// With very few observations (< 3), should just use max
	consensus.AddObservation("endpoint1", 1000)
	assert.Equal(t, uint64(1000), consensus.GetPerceivedBlock())

	consensus.AddObservation("endpoint2", 5000) // Even if this seems like an outlier
	// With < 3 observations, we don't have enough data for median, so just use max
	assert.Equal(t, uint64(5000), consensus.GetPerceivedBlock())

	// Once we have 3+ observations, consensus kicks in
	consensus.AddObservation("endpoint3", 1001)
	// Now median is established, and 5000 should be rejected as outlier
	perceived := consensus.GetPerceivedBlock()
	assert.True(t, perceived < 2000, "after 3 observations, outlier should be rejected, got %d", perceived)
}

func TestBlockHeightConsensus_ObservationCount(t *testing.T) {
	logger := polyzero.NewLogger()
	consensus := NewBlockHeightConsensus(logger, 10)

	// Add some observations
	consensus.AddObservation("endpoint1", 1000)
	consensus.AddObservation("endpoint2", 1001)
	consensus.AddObservation("endpoint3", 1002)

	count := consensus.GetObservationCount()
	require.Equal(t, 3, count)
}

func TestCalculateMedian(t *testing.T) {
	tests := []struct {
		name     string
		values   []uint64
		expected uint64
	}{
		{"empty", []uint64{}, 0},
		{"single", []uint64{100}, 100},
		{"two values", []uint64{100, 200}, 150},
		{"odd count", []uint64{100, 200, 300}, 200},
		{"even count", []uint64{100, 200, 300, 400}, 250},
		{"same values", []uint64{100, 100, 100}, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := calculateMedian(tt.values)
			assert.Equal(t, tt.expected, result)
		})
	}
}
