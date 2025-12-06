package reputation

import (
	"errors"
	"math/rand"
	"sync"

	"github.com/pokt-network/poktroll/pkg/polylog"
)

// ErrNoEndpointsAvailable is returned when no endpoints are available for selection.
var ErrNoEndpointsAvailable = errors.New("no endpoints available for selection")

// TieredSelector selects endpoints using cascade-down tier logic.
// It groups endpoints into tiers based on their reputation scores and
// selects from the highest available tier.
type TieredSelector struct {
	logger       polylog.Logger
	config       TieredSelectionConfig
	minThreshold float64

	// probationEndpoints tracks which endpoints are currently in probation.
	// An endpoint enters probation when its score falls below the probation threshold.
	// Map key is the endpoint key, value is true if endpoint is in probation.
	probationEndpoints   map[EndpointKey]bool
	probationEndpointsMu sync.RWMutex
}

// NewTieredSelector creates a new TieredSelector with the given configuration.
func NewTieredSelector(config TieredSelectionConfig, minThreshold float64) *TieredSelector {
	return &TieredSelector{
		config:             config,
		minThreshold:       minThreshold,
		probationEndpoints: make(map[EndpointKey]bool),
	}
}

// NewTieredSelectorWithLogger creates a new TieredSelector with the given configuration and logger.
func NewTieredSelectorWithLogger(logger polylog.Logger, config TieredSelectionConfig, minThreshold float64) *TieredSelector {
	return &TieredSelector{
		logger:             logger.With("component", "tiered_selector"),
		config:             config,
		minThreshold:       minThreshold,
		probationEndpoints: make(map[EndpointKey]bool),
	}
}

// SetLogger sets the logger for the TieredSelector.
// This can be used to add logging after creation.
func (s *TieredSelector) SetLogger(logger polylog.Logger) {
	s.logger = logger.With("component", "tiered_selector")
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
		selected := keys[rand.Intn(len(keys))]
		return selected, 0, nil // tier 0 indicates random selection (no tiering)
	}

	// Group endpoints into tiers
	tier1, tier2, tier3 := s.GroupByTier(endpoints)

	// Cascade selection: try Tier 1 first, then Tier 2, then Tier 3
	if len(tier1) > 0 {
		selected := tier1[rand.Intn(len(tier1))]
		return selected, 1, nil
	}

	if len(tier2) > 0 {
		selected := tier2[rand.Intn(len(tier2))]
		return selected, 2, nil
	}

	if len(tier3) > 0 {
		selected := tier3[rand.Intn(len(tier3))]
		return selected, 3, nil
	}

	return EndpointKey{}, 0, ErrNoEndpointsAvailable
}

// GroupByTier groups endpoints into three tiers based on their scores.
// Tier 1 (Premium): score >= Tier1Threshold
// Tier 2 (Good): score >= Tier2Threshold and < Tier1Threshold
// Tier 3 (Fair): score >= MinThreshold and < Tier2Threshold
// Endpoints below MinThreshold are excluded (should already be filtered).
func (s *TieredSelector) GroupByTier(endpoints map[EndpointKey]float64) (tier1, tier2, tier3 []EndpointKey) {
	for key, score := range endpoints {
		switch {
		case score >= s.config.Tier1Threshold:
			tier1 = append(tier1, key)
		case score >= s.config.Tier2Threshold:
			tier2 = append(tier2, key)
		case score >= s.minThreshold:
			tier3 = append(tier3, key)
			// Scores below minThreshold are excluded
		}
	}
	return tier1, tier2, tier3
}

// TierForScore returns the tier number (1, 2, or 3) for a given score.
// Returns 0 if the score is below the minimum threshold.
func (s *TieredSelector) TierForScore(score float64) int {
	switch {
	case score >= s.config.Tier1Threshold:
		return 1
	case score >= s.config.Tier2Threshold:
		return 2
	case score >= s.minThreshold:
		return 3
	default:
		return 0 // Below threshold
	}
}

// Config returns the tiered selection configuration.
func (s *TieredSelector) Config() TieredSelectionConfig {
	return s.config
}

// MinThreshold returns the minimum threshold for Tier 3.
func (s *TieredSelector) MinThreshold() float64 {
	return s.minThreshold
}

// IsInProbation returns true if the endpoint is currently in probation.
func (s *TieredSelector) IsInProbation(key EndpointKey) bool {
	s.probationEndpointsMu.RLock()
	defer s.probationEndpointsMu.RUnlock()
	return s.probationEndpoints[key]
}

// UpdateProbationStatus updates probation tracking based on current scores.
// Returns the list of endpoints currently in probation.
// An endpoint enters probation when: score < probationThreshold AND score >= minThreshold
// An endpoint exits probation when: score >= probationThreshold
func (s *TieredSelector) UpdateProbationStatus(endpoints map[EndpointKey]float64) []EndpointKey {
	if !s.config.Probation.Enabled {
		return nil
	}

	probationThreshold := s.config.Probation.Threshold
	inProbation := make([]EndpointKey, 0)

	s.probationEndpointsMu.Lock()
	defer s.probationEndpointsMu.Unlock()

	// Update probation status for all endpoints
	for key, score := range endpoints {
		wasInProbation := s.probationEndpoints[key]
		// Endpoint is in probation if: score is below probation threshold but above min threshold
		isInProbation := score < probationThreshold && score >= s.minThreshold

		if isInProbation {
			s.probationEndpoints[key] = true
			inProbation = append(inProbation, key)

			// Log probation entry
			if !wasInProbation && s.logger != nil {
				s.logger.Debug().
					Str("endpoint", string(key.EndpointAddr)).
					Str("service_id", string(key.ServiceID)).
					Float64("score", score).
					Float64("probation_threshold", probationThreshold).
					Float64("min_threshold", s.minThreshold).
					Msg("[PROBATION] Endpoint ENTERED probation")
			}
		} else if wasInProbation {
			// Endpoint has recovered or fallen below min threshold
			delete(s.probationEndpoints, key)

			// Log probation exit
			if s.logger != nil {
				exitReason := "recovered"
				if score < s.minThreshold {
					exitReason = "below_min_threshold"
				}
				s.logger.Debug().
					Str("endpoint", string(key.EndpointAddr)).
					Str("service_id", string(key.ServiceID)).
					Float64("score", score).
					Float64("probation_threshold", probationThreshold).
					Float64("min_threshold", s.minThreshold).
					Str("exit_reason", exitReason).
					Msg("[PROBATION] Endpoint EXITED probation")
			}
		}
	}

	return inProbation
}

// ShouldRouteToProbation determines if this request should be routed to a probation endpoint.
// Returns true with probability = traffic_percent / 100.
// For example, if traffic_percent = 10, returns true 10% of the time.
func (s *TieredSelector) ShouldRouteToProbation() bool {
	if !s.config.Probation.Enabled {
		return false
	}

	// Random number between 0 and 99
	r := rand.Intn(100)
	// Return true if random number is less than traffic percent
	// e.g., if traffic_percent = 10, true when r is 0-9 (10% of the time)
	shouldRoute := float64(r) < s.config.Probation.TrafficPercent

	// Log probation routing decision
	if s.logger != nil && shouldRoute {
		s.logger.Debug().
			Int("random_value", r).
			Float64("traffic_percent", s.config.Probation.TrafficPercent).
			Bool("routed_to_probation", shouldRoute).
			Msg("[PROBATION] Routing request to probation endpoint")
	}

	return shouldRoute
}
