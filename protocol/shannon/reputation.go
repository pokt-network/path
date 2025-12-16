package shannon

import (
	"context"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	reputationmetrics "github.com/pokt-network/path/metrics/reputation"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
)

// NOTE: Error classification has been moved to error_classification.go
// The old mapErrorToSignal, mapSessionSanctionError, and mapNonSanctionedError functions
// have been replaced by classifyErrorAsSignal() which directly maps errors to reputation signals
// without the intermediate "sanction" concept.
//
// See ERROR_CLASSIFICATION.md for detailed documentation of all error categories.
//
// The new classification is called from context.go and websocket_context.go where errors occur.

// filterByReputation filters endpoints based on their reputation score.
// Returns only endpoints with scores above the configured minimum threshold.
// Endpoints without a score (new endpoints) are assumed to have the initial score.
func (p *Protocol) filterByReputation(
	ctx context.Context,
	serviceID protocol.ServiceID,
	endpoints map[protocol.EndpointAddr]endpoint,
	rpcType sharedtypes.RPCType,
	logger polylog.Logger,
) map[protocol.EndpointAddr]endpoint {
	if p.reputationService == nil {
		return endpoints
	}

	keyBuilder := p.reputationService.KeyBuilderForService(serviceID)

	// Build endpoint keys for batch lookup
	keys := make([]reputation.EndpointKey, 0, len(endpoints))
	for addr := range endpoints {
		keys = append(keys, keyBuilder.BuildKey(serviceID, addr, rpcType))
	}

	// Get scores for all endpoints in a single call
	scores, err := p.reputationService.GetScores(ctx, keys)
	if err != nil {
		logger.Warn().Err(err).Msg("Failed to get reputation scores, allowing all endpoints")
		reputationmetrics.RecordError("get_scores", "storage_error")
		return endpoints
	}

	// Filter endpoints below threshold
	filtered := make(map[protocol.EndpointAddr]endpoint, len(endpoints))
	for addr, ep := range endpoints {
		key := keyBuilder.BuildKey(serviceID, addr, rpcType)
		score, exists := scores[key]

		// Extract domain for metrics
		endpointDomain := extractEndpointDomain(ep.PublicURL(), logger)

		// If score doesn't exist, the endpoint is new and gets initial score (which is above threshold)
		if !exists {
			filtered[addr] = ep
			reputationmetrics.RecordEndpointAllowed(string(serviceID), endpointDomain)
			continue
		}

		// Record score observation for histogram
		reputationmetrics.RecordScoreObservation(string(serviceID), score.Value)

		// Check if score is above the configured minimum threshold (use per-service threshold)
		minThreshold := p.getMinThresholdForService(serviceID)
		if score.Value >= minThreshold {
			filtered[addr] = ep
			reputationmetrics.RecordEndpointAllowed(string(serviceID), endpointDomain)
		} else {
			logger.Debug().
				Str("endpoint", string(addr)).
				Float64("score", score.Value).
				Float64("threshold", minThreshold).
				Msg("Filtering out low-reputation endpoint")
			reputationmetrics.RecordEndpointFiltered(string(serviceID), endpointDomain)
		}
	}

	return filtered
}

// extractEndpointDomain extracts the domain from an endpoint URL for metrics labeling.
func extractEndpointDomain(url string, logger polylog.Logger) string {
	domain, err := shannonmetrics.ExtractDomainOrHost(url)
	if err != nil {
		logger.Debug().Err(err).Str("url", url).Msg("Could not extract domain from endpoint URL")
		return shannonmetrics.ErrDomain
	}
	return domain
}

// getEndpointScores retrieves reputation scores for all endpoints and returns them
// as a map suitable for the TieredSelector.
func (p *Protocol) getEndpointScores(
	ctx context.Context,
	serviceID protocol.ServiceID,
	endpoints map[protocol.EndpointAddr]endpoint,
	rpcType sharedtypes.RPCType,
	_ polylog.Logger, // logger reserved for future debug logging
) (map[reputation.EndpointKey]float64, error) {
	// Build endpoint keys for batch lookup
	keys := make([]reputation.EndpointKey, 0, len(endpoints))
	for addr := range endpoints {
		keys = append(keys, reputation.NewEndpointKey(serviceID, addr, rpcType))
	}

	// Get scores from reputation service
	scores, err := p.reputationService.GetScores(ctx, keys)
	if err != nil {
		reputationmetrics.RecordError("get_scores", "storage_error")
		return nil, err
	}

	// Convert to score values map
	result := make(map[reputation.EndpointKey]float64, len(endpoints))
	for addr := range endpoints {
		key := reputation.NewEndpointKey(serviceID, addr, rpcType)
		if score, exists := scores[key]; exists {
			result[key] = score.Value
		} else {
			// New endpoints get initial score
			result[key] = reputation.InitialScore
		}
	}

	return result, nil
}

// getReputationMinThreshold returns the configured minimum reputation threshold.
// Falls back to default if tiered selector is not configured.
func (p *Protocol) getReputationMinThreshold() float64 {
	if p.tieredSelector != nil {
		return p.tieredSelector.MinThreshold()
	}
	return reputation.DefaultMinThreshold
}

// getTieredSelectorForService returns the tiered selector for a specific service.
// If a per-service selector is configured, it is returned; otherwise the global selector is used.
func (p *Protocol) getTieredSelectorForService(serviceID protocol.ServiceID) *reputation.TieredSelector {
	// Check for per-service selector
	if p.serviceTieredSelectors != nil {
		if selector, ok := p.serviceTieredSelectors[serviceID]; ok {
			return selector
		}
	}
	// Fall back to global selector
	return p.tieredSelector
}

// getMinThresholdForService returns the minimum reputation threshold for a service.
// Uses per-service configuration if available, otherwise falls back to global.
func (p *Protocol) getMinThresholdForService(serviceID protocol.ServiceID) float64 {
	// Check for per-service selector (has its own min threshold)
	if p.serviceTieredSelectors != nil {
		if selector, ok := p.serviceTieredSelectors[serviceID]; ok {
			return selector.MinThreshold()
		}
	}
	// Fall back to global
	return p.getReputationMinThreshold()
}

// filterToHighestTier filters endpoints to only return those from the highest available tier.
// This implements the cascade-down selection: if Tier 1 has endpoints, only return Tier 1.
// If Tier 1 is empty, return Tier 2. If both are empty, return Tier 3.
// This allows the QoS layer to still do its validation and selection, but only within the best tier.
//
// If probation is enabled, this function also:
// - Updates probation status for all endpoints
// - Randomly routes a percentage of traffic to probation endpoints
// - Records probation metrics
func (p *Protocol) filterToHighestTier(
	ctx context.Context,
	serviceID protocol.ServiceID,
	endpoints map[protocol.EndpointAddr]endpoint,
	rpcType sharedtypes.RPCType,
	logger polylog.Logger,
) map[protocol.EndpointAddr]endpoint {
	if len(endpoints) == 0 {
		return endpoints
	}

	// Get the tiered selector for this service (may be per-service or global)
	selector := p.getTieredSelectorForService(serviceID)
	if selector == nil {
		// No selector configured, return all endpoints
		return endpoints
	}

	// Get scores for all endpoints
	endpointScores, err := p.getEndpointScores(ctx, serviceID, endpoints, rpcType, logger)
	if err != nil {
		logger.Warn().Err(err).Msg("Failed to get endpoint scores for tiered filtering, returning all endpoints")
		return endpoints
	}

	// Track probation transitions before updating status
	var transitionEvents []struct {
		key        reputation.EndpointKey
		transition string
	}

	if selector.Config().Probation.Enabled {
		probationThreshold := selector.Config().Probation.Threshold
		for key, score := range endpointScores {
			wasInProbation := selector.IsInProbation(key)
			isInProbation := score < probationThreshold && score >= selector.MinThreshold()

			// Record transition events
			if isInProbation && !wasInProbation {
				transitionEvents = append(transitionEvents, struct {
					key        reputation.EndpointKey
					transition string
				}{key, reputationmetrics.ProbationTransitionEntered})
			} else if !isInProbation && wasInProbation {
				transitionEvents = append(transitionEvents, struct {
					key        reputation.EndpointKey
					transition string
				}{key, reputationmetrics.ProbationTransitionExited})
			}
		}
	}

	// Update probation status and get list of endpoints currently in probation
	probationEndpoints := selector.UpdateProbationStatus(endpointScores)
	probationCount := len(probationEndpoints)

	// Record probation metrics if probation is enabled
	if selector.Config().Probation.Enabled {
		reputationmetrics.SetProbationEndpointsCount(string(serviceID), probationCount)

		// Record transition events
		for _, event := range transitionEvents {
			domain := extractEndpointDomain(string(event.key.EndpointAddr), logger)
			reputationmetrics.RecordProbationTransition(string(serviceID), domain, event.transition)
		}
	}

	// Check if this request should be routed to probation endpoints
	shouldRouteToProbation := selector.ShouldRouteToProbation()

	// If probation routing is active and we have probation endpoints, route to them
	if shouldRouteToProbation && probationCount > 0 {
		logger.Info().
			Int("probation_count", probationCount).
			Float64("traffic_percent", selector.Config().Probation.TrafficPercent).
			Msg("Routing request to probation endpoints for recovery")

		// Build result map with only probation endpoints
		result := make(map[protocol.EndpointAddr]endpoint, probationCount)
		for _, key := range probationEndpoints {
			if ep, exists := endpoints[key.EndpointAddr]; exists {
				result[key.EndpointAddr] = ep
			}
		}

		return result
	}

	// Normal tier-based routing (non-probation)
	// Group endpoints by tier using the service-specific selector
	tier1, tier2, tier3 := selector.GroupByTier(endpointScores)
	tier1Count, tier2Count, tier3Count := len(tier1), len(tier2), len(tier3)

	// Record tier distribution metrics (gauge showing current state)
	reputationmetrics.RecordTierDistribution(string(serviceID), tier1Count, tier2Count, tier3Count)

	// Log detailed tier distribution for observability
	logger.Info().
		Int("tier1_count", tier1Count).
		Int("tier2_count", tier2Count).
		Int("tier3_count", tier3Count).
		Int("probation_count", probationCount).
		Int("total_endpoints", len(endpoints)).
		Float64("tier1_threshold", selector.Config().Tier1Threshold).
		Float64("tier2_threshold", selector.Config().Tier2Threshold).
		Float64("min_threshold", selector.MinThreshold()).
		Float64("probation_threshold", selector.Config().Probation.Threshold).
		Bool("probation_enabled", selector.Config().Probation.Enabled).
		Msg("Tiered selection: endpoint distribution across tiers")

	// Determine which tier to use and record metric
	var selectedTier int
	var selectedKeys []reputation.EndpointKey

	switch {
	case tier1Count > 0:
		selectedTier = 1
		selectedKeys = tier1
	case tier2Count > 0:
		selectedTier = 2
		selectedKeys = tier2
	case tier3Count > 0:
		selectedTier = 3
		selectedKeys = tier3
	default:
		// No endpoints in any tier (all below threshold) - return empty
		logger.Warn().Msg("No endpoints available in any tier after tiered filtering")
		reputationmetrics.RecordTierSelection(string(serviceID), 0)
		return make(map[protocol.EndpointAddr]endpoint)
	}

	// Record the tier selection metric (counter for selections)
	reputationmetrics.RecordTierSelection(string(serviceID), selectedTier)

	// Build result map with only endpoints from the selected tier
	result := make(map[protocol.EndpointAddr]endpoint, len(selectedKeys))
	for _, key := range selectedKeys {
		if ep, exists := endpoints[key.EndpointAddr]; exists {
			result[key.EndpointAddr] = ep
		}
	}

	logger.Info().
		Int("selected_tier", selectedTier).
		Int("endpoints_in_selected_tier", len(result)).
		Int("tier1_available", tier1Count).
		Int("tier2_available", tier2Count).
		Int("tier3_available", tier3Count).
		Int("probation_available", probationCount).
		Msg("Tiered selection: returning endpoints from highest available tier")

	return result
}
