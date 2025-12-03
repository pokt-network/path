package reputation

import (
	"errors"
	"math/rand"
)

// ErrNoEndpointsAvailable is returned when no endpoints are available for selection.
var ErrNoEndpointsAvailable = errors.New("no endpoints available for selection")

// randIntn is a wrapper for math/rand.Intn to allow mocking in tests.
var randIntn = rand.Intn

// randFloat64 is a wrapper for math/rand.Float64 to allow mocking in tests.
var randFloat64 = rand.Float64

// TierProbation is the tier number for endpoints in the probation zone.
const TierProbation = 4

// TieredSelector selects endpoints using cascade-down tier logic.
// It groups endpoints into tiers based on their reputation scores and
// selects from the highest available tier.
type TieredSelector struct {
	config       TieredSelectionConfig
	minThreshold float64
}

// NewTieredSelector creates a new TieredSelector with the given configuration.
func NewTieredSelector(config TieredSelectionConfig, minThreshold float64) *TieredSelector {
	return &TieredSelector{
		config:       config,
		minThreshold: minThreshold,
	}
}

// SelectEndpoint selects one endpoint using cascade-down tier logic.
// It returns the selected endpoint key and the tier it was selected from (1, 2, or 3).
// Returns ErrNoEndpointsAvailable if no endpoints are available in any tier.
func (s *TieredSelector) SelectEndpoint(endpoints map[EndpointKey]float64) (EndpointKey, int, error) {
	if len(endpoints) == 0 {
		return EndpointKey{}, 0, ErrNoEndpointsAvailable
	}

	// If tiered selection is disabled, do random selection from all endpoints
	if !s.config.Enabled {
		keys := make([]EndpointKey, 0, len(endpoints))
		for key := range endpoints {
			keys = append(keys, key)
		}
		selected := keys[randIntn(len(keys))]
		return selected, 0, nil // tier 0 indicates random selection (no tiering)
	}

	// Group endpoints into tiers
	tier1, tier2, tier3 := s.GroupByTier(endpoints)

	// Cascade selection: try Tier 1 first, then Tier 2, then Tier 3
	if len(tier1) > 0 {
		selected := tier1[randIntn(len(tier1))]
		return selected, 1, nil
	}

	if len(tier2) > 0 {
		selected := tier2[randIntn(len(tier2))]
		return selected, 2, nil
	}

	if len(tier3) > 0 {
		selected := tier3[randIntn(len(tier3))]
		return selected, 3, nil
	}

	return EndpointKey{}, 0, ErrNoEndpointsAvailable
}

// GroupByTier groups endpoints into three tiers based on their scores.
// Tier 1 (Premium): score >= Tier1Threshold
// Tier 2 (Good): score >= Tier2Threshold and < Tier1Threshold
// Tier 3 (Fair): score >= MinThreshold and < Tier2Threshold
// Endpoints below MinThreshold are excluded (should already be filtered).
// Use GroupByTierWithProbation if you need to include probation tier.
func (s *TieredSelector) GroupByTier(endpoints map[EndpointKey]float64) (tier1, tier2, tier3 []EndpointKey) {
	for key, score := range endpoints {
		switch {
		case score >= s.config.Tier1Threshold:
			tier1 = append(tier1, key)
		case score >= s.config.Tier2Threshold:
			tier2 = append(tier2, key)
		case score >= s.minThreshold:
			tier3 = append(tier3, key)
			// Scores below minThreshold are excluded (or go to probation)
		}
	}
	return tier1, tier2, tier3
}

// GroupByTierWithProbation groups endpoints into four tiers based on their scores.
// Tier 1 (Premium): score >= Tier1Threshold
// Tier 2 (Good): score >= Tier2Threshold and < Tier1Threshold
// Tier 3 (Fair): score >= MinThreshold and < Tier2Threshold
// Probation: score >= ProbationThreshold and < MinThreshold
// Endpoints below ProbationThreshold are fully excluded.
func (s *TieredSelector) GroupByTierWithProbation(endpoints map[EndpointKey]float64) (tier1, tier2, tier3, probation []EndpointKey) {
	probationThreshold := s.config.Probation.Threshold
	for key, score := range endpoints {
		switch {
		case score >= s.config.Tier1Threshold:
			tier1 = append(tier1, key)
		case score >= s.config.Tier2Threshold:
			tier2 = append(tier2, key)
		case score >= s.minThreshold:
			tier3 = append(tier3, key)
		case score >= probationThreshold:
			probation = append(probation, key)
			// Scores below probationThreshold are completely excluded
		}
	}
	return tier1, tier2, tier3, probation
}

// TierForScore returns the tier number (1, 2, 3, or 4 for probation) for a given score.
// Returns 0 if the score is below the probation threshold (fully excluded).
func (s *TieredSelector) TierForScore(score float64) int {
	switch {
	case score >= s.config.Tier1Threshold:
		return 1
	case score >= s.config.Tier2Threshold:
		return 2
	case score >= s.minThreshold:
		return 3
	case score >= s.config.Probation.Threshold:
		return TierProbation // Probation zone
	default:
		return 0 // Fully excluded
	}
}

// SelectEndpointWithProbation selects an endpoint using cascade-down tier logic
// with probation support. It tries normal tiers first, then probabilistically
// considers probation endpoints based on the traffic percentage.
// Returns the selected endpoint key and the tier it was selected from.
// Returns ErrNoEndpointsAvailable if no endpoints are available.
func (s *TieredSelector) SelectEndpointWithProbation(endpoints map[EndpointKey]float64) (EndpointKey, int, error) {
	if len(endpoints) == 0 {
		return EndpointKey{}, 0, ErrNoEndpointsAvailable
	}

	// If tiered selection is disabled, do random selection from all endpoints
	if !s.config.Enabled {
		keys := make([]EndpointKey, 0, len(endpoints))
		for key := range endpoints {
			keys = append(keys, key)
		}
		selected := keys[randIntn(len(keys))]
		return selected, 0, nil // tier 0 indicates random selection (no tiering)
	}

	// Group endpoints into tiers including probation
	tier1, tier2, tier3, probation := s.GroupByTierWithProbation(endpoints)

	// Cascade selection: try Tier 1 first, then Tier 2, then Tier 3
	if len(tier1) > 0 {
		selected := tier1[randIntn(len(tier1))]
		return selected, 1, nil
	}

	if len(tier2) > 0 {
		selected := tier2[randIntn(len(tier2))]
		return selected, 2, nil
	}

	if len(tier3) > 0 {
		selected := tier3[randIntn(len(tier3))]
		return selected, 3, nil
	}

	// No normal tiers available, check probation
	if len(probation) > 0 && s.config.Probation.Enabled {
		// Roll dice for probation sampling
		if randFloat64()*100 < s.config.Probation.TrafficPercent {
			selected := probation[randIntn(len(probation))]
			return selected, TierProbation, nil
		}
		// Skipped probation sampling - no endpoints available for this request
	}

	return EndpointKey{}, 0, ErrNoEndpointsAvailable
}

// IsInProbation returns true if the score is in the probation zone.
func (s *TieredSelector) IsInProbation(score float64) bool {
	return score >= s.config.Probation.Threshold && score < s.minThreshold
}

// ProbationConfig returns the probation configuration.
func (s *TieredSelector) ProbationConfig() ProbationConfig {
	return s.config.Probation
}

// Config returns the tiered selection configuration.
func (s *TieredSelector) Config() TieredSelectionConfig {
	return s.config
}

// MinThreshold returns the minimum threshold for Tier 3.
func (s *TieredSelector) MinThreshold() float64 {
	return s.minThreshold
}
