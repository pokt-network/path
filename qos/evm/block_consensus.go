package evm

import (
	"sort"
	"sync"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/protocol"
)

const (
	// defaultObservationWindow is how long to keep block observations.
	// Observations older than this are pruned.
	defaultObservationWindow = 2 * time.Minute

	// defaultMaxObservations is the maximum number of observations to keep.
	// Prevents unbounded memory growth with many endpoints.
	defaultMaxObservations = 1000

	// defaultMinObservationsForMedian is the minimum observations needed
	// before using median-based consensus. Below this, we use max.
	defaultMinObservationsForMedian = 3

	// defaultMaxDeviationMultiplier is how many times the sync_allowance
	// we allow above the median before rejecting as outlier.
	// e.g., with sync_allowance=10 and multiplier=3, we accept up to median+30.
	defaultMaxDeviationMultiplier = 3
)

// blockObservation represents a single block height observation from an endpoint.
type blockObservation struct {
	blockHeight  uint64
	observedAt   time.Time
	endpointAddr protocol.EndpointAddr
}

// BlockHeightConsensus calculates a robust perceived block height using
// median-anchored consensus. This protects against malicious or misconfigured
// endpoints reporting extremely high block numbers.
//
// Algorithm:
//  1. Track recent block observations in a sliding window
//  2. Calculate median of all observations
//  3. Filter out outliers: blocks > median + (syncAllowance * multiplier)
//  4. Return max of filtered observations
//
// This is self-adjusting and requires no manual configuration beyond
// the existing sync_allowance setting.
type BlockHeightConsensus struct {
	mu sync.RWMutex

	logger polylog.Logger

	// observations is a sliding window of recent block observations
	observations []blockObservation

	// observationWindow is how long to keep observations
	observationWindow time.Duration

	// maxObservations caps memory usage
	maxObservations int

	// syncAllowance is the configured sync allowance for the service
	// Used to determine outlier threshold
	syncAllowance uint64

	// maxDeviationMultiplier determines how far above median is acceptable
	maxDeviationMultiplier uint64

	// cachedPerceived is the last calculated perceived block
	// Updated on each AddObservation call
	cachedPerceived uint64
}

// NewBlockHeightConsensus creates a new consensus calculator.
func NewBlockHeightConsensus(logger polylog.Logger, syncAllowance uint64) *BlockHeightConsensus {
	if syncAllowance == 0 {
		syncAllowance = defaultEVMBlockNumberSyncAllowance
	}

	return &BlockHeightConsensus{
		logger:                 logger,
		observations:           make([]blockObservation, 0, 100),
		observationWindow:      defaultObservationWindow,
		maxObservations:        defaultMaxObservations,
		syncAllowance:          syncAllowance,
		maxDeviationMultiplier: defaultMaxDeviationMultiplier,
	}
}

// AddObservation adds a new block height observation and recalculates consensus.
// Returns the new perceived block height.
func (c *BlockHeightConsensus) AddObservation(endpointAddr protocol.EndpointAddr, blockHeight uint64) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()

	// Add new observation
	c.observations = append(c.observations, blockObservation{
		blockHeight:  blockHeight,
		observedAt:   now,
		endpointAddr: endpointAddr,
	})

	// Prune old observations and enforce max size
	c.pruneObservationsLocked(now)

	// Recalculate perceived block
	c.cachedPerceived = c.calculatePerceivedLocked()

	return c.cachedPerceived
}

// GetPerceivedBlock returns the current perceived block height.
// This is a cached value updated on each AddObservation call.
func (c *BlockHeightConsensus) GetPerceivedBlock() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cachedPerceived
}

// GetObservationCount returns the current number of observations in the window.
// Useful for debugging and monitoring.
func (c *BlockHeightConsensus) GetObservationCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.observations)
}

// GetMedianBlock returns the current median block height.
// Returns 0 if no observations exist.
func (c *BlockHeightConsensus) GetMedianBlock() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.observations) == 0 {
		return 0
	}

	heights := c.extractHeightsLocked()
	return calculateMedian(heights)
}

// pruneObservationsLocked removes old observations and enforces max size.
// Must be called with lock held.
func (c *BlockHeightConsensus) pruneObservationsLocked(now time.Time) {
	cutoff := now.Add(-c.observationWindow)

	// Filter to recent observations only
	valid := c.observations[:0] // Reuse underlying array
	for _, obs := range c.observations {
		if obs.observedAt.After(cutoff) {
			valid = append(valid, obs)
		}
	}
	c.observations = valid

	// Enforce max size by removing oldest if needed
	if len(c.observations) > c.maxObservations {
		// Sort by time and keep most recent
		sort.Slice(c.observations, func(i, j int) bool {
			return c.observations[i].observedAt.After(c.observations[j].observedAt)
		})
		c.observations = c.observations[:c.maxObservations]
	}
}

// calculatePerceivedLocked calculates the perceived block using median-anchored consensus.
// Must be called with lock held.
func (c *BlockHeightConsensus) calculatePerceivedLocked() uint64 {
	if len(c.observations) == 0 {
		return 0
	}

	heights := c.extractHeightsLocked()

	// If we don't have enough observations, just use max
	// This handles bootstrap case
	if len(heights) < defaultMinObservationsForMedian {
		return heights[len(heights)-1] // Already sorted, last is max
	}

	median := calculateMedian(heights)

	// Calculate outlier threshold: median + (syncAllowance * multiplier)
	// This allows for some natural variance but rejects extreme outliers
	maxAllowed := median + (c.syncAllowance * c.maxDeviationMultiplier)

	// Find the maximum block that's within acceptable range
	var maxValid uint64
	var outlierCount int
	for _, h := range heights {
		if h <= maxAllowed {
			if h > maxValid {
				maxValid = h
			}
		} else {
			outlierCount++
		}
	}

	// Log if we rejected outliers
	if outlierCount > 0 {
		c.logger.Warn().
			Uint64("median", median).
			Uint64("max_allowed", maxAllowed).
			Uint64("perceived", maxValid).
			Int("outliers_rejected", outlierCount).
			Msg("Rejected block height outliers in consensus calculation")
	}

	// If all observations were outliers (shouldn't happen), fall back to median
	if maxValid == 0 {
		return median
	}

	return maxValid
}

// extractHeightsLocked extracts and sorts block heights from observations.
// Must be called with lock held.
func (c *BlockHeightConsensus) extractHeightsLocked() []uint64 {
	heights := make([]uint64, len(c.observations))
	for i, obs := range c.observations {
		heights[i] = obs.blockHeight
	}
	sort.Slice(heights, func(i, j int) bool { return heights[i] < heights[j] })
	return heights
}

// calculateMedian returns the median of a sorted slice of uint64.
func calculateMedian(sorted []uint64) uint64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 0 {
		// Even number: average of two middle values
		return (sorted[n/2-1] + sorted[n/2]) / 2
	}
	// Odd number: middle value
	return sorted[n/2]
}
