package shannon

import (
	"context"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/metrics"
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

// filterByReputation filters endpoints based on their reputation score and cooldown status.
// Returns only endpoints with scores above the configured minimum threshold and not in cooldown.
// Endpoints without a score (new endpoints) are assumed to have the initial score.
func (p *Protocol) filterByReputation(
	ctx context.Context,
	serviceID protocol.ServiceID,
	endpoints map[protocol.EndpointAddr]endpoint,
	rpcType sharedtypes.RPCType,
	logger polylog.Logger,
	requestedEndpointAddr protocol.EndpointAddr,
) map[protocol.EndpointAddr]endpoint {
	if p.reputationService == nil {
		return endpoints
	}

	keyBuilder := p.reputationService.KeyBuilderForService(serviceID)

	// Build keys + remember each endpoint in a single pass. The second loop
	// walks this slice instead of re-iterating the map and re-calling BuildKey
	// (which allocates for non-default key granularities — domain mode parses
	// URLs and extracts hostnames).
	type addrKey struct {
		addr protocol.EndpointAddr
		ep   endpoint
		key  reputation.EndpointKey
	}
	cached := make([]addrKey, 0, len(endpoints))
	keys := make([]reputation.EndpointKey, 0, len(endpoints))
	for addr, ep := range endpoints {
		k := keyBuilder.BuildKey(serviceID, addr, rpcType)
		cached = append(cached, addrKey{addr: addr, ep: ep, key: k})
		keys = append(keys, k)
	}

	// Get scores for all endpoints in a single call
	scores, err := p.reputationService.GetScores(ctx, keys)
	if err != nil {
		logger.Warn().Err(err).Msg("Failed to get reputation scores, allowing all endpoints")
		return endpoints
	}

	// Hoist out of the loop; neither the threshold nor the rpc_type label
	// changes per endpoint.
	minThreshold := p.getMinThresholdForService(serviceID)
	rpcTypeLabel := metrics.NormalizeRPCType(rpcType.String())
	serviceIDLabel := string(serviceID)

	// Filter endpoints below threshold or in cooldown
	filtered := make(map[protocol.EndpointAddr]endpoint, len(endpoints))
	for _, ak := range cached {
		score, exists := scores[ak.key]

		// If score doesn't exist, the endpoint is new and gets initial score (which is above threshold)
		if !exists {
			filtered[ak.addr] = ak.ep
			continue
		}

		// Check if endpoint is in cooldown from strike system
		if score.IsInCooldown() {
			// CRITICAL: If this is the pre-selected endpoint, we MUST include it to avoid
			// the "Selected endpoint is not available" error. This handles the race condition
			// where an endpoint was selected but then entered cooldown.
			if ak.addr == requestedEndpointAddr {
				filtered[ak.addr] = ak.ep
				logger.Debug().
					Str("endpoint", string(ak.addr)).
					Int("strikes", score.CriticalStrikes).
					Dur("cooldown_remaining", score.CooldownRemaining()).
					Msg("Including requested endpoint even though it is in cooldown")
				continue
			}

			logger.Debug().
				Str("endpoint", string(ak.addr)).
				Int("strikes", score.CriticalStrikes).
				Dur("cooldown_remaining", score.CooldownRemaining()).
				Msg("Filtering out endpoint in cooldown (strike system)")
			metrics.RecordReputationDisqualified(serviceIDLabel, rpcTypeLabel, metrics.ReputationDisqualifyReasonCooldown)
			continue
		}

		// Check if score is above the configured minimum threshold (use per-service threshold)
		if score.Value >= minThreshold || ak.addr == requestedEndpointAddr {
			filtered[ak.addr] = ak.ep
			if score.Value < minThreshold && ak.addr == requestedEndpointAddr {
				logger.Debug().
					Str("endpoint", string(ak.addr)).
					Float64("score", score.Value).
					Float64("threshold", minThreshold).
					Msg("Including requested endpoint even though it is below threshold")
			}
		} else {
			logger.Debug().
				Str("endpoint", string(ak.addr)).
				Float64("score", score.Value).
				Float64("threshold", minThreshold).
				Msg("Filtering out low-reputation endpoint")
			metrics.RecordReputationDisqualified(serviceIDLabel, rpcTypeLabel, metrics.ReputationDisqualifyReasonBelowThreshold)
		}
	}

	// Pool-collapse guard: if reputation filtered out EVERY endpoint (all in cooldown or
	// below threshold), do NOT return empty. An empty result drops the caller to a
	// reputation-BLIND fallback (random / least-stale) that ignores which endpoints are
	// actually least-bad — observed making a fully-degraded service worse (e.g. poly-zkevm
	// went 50%→100% error when every endpoint was cooled and selection fell to random). A
	// hard bench should relatively deprioritize an endpoint, never remove the last ones
	// standing when there is no better alternative. Keep the least-bad TIER (the endpoints at
	// the maximum score) so selection stays reputation-aware over the degraded pool.
	if len(filtered) == 0 && len(cached) > 0 {
		scoreOf := func(ak addrKey) float64 {
			if sc, ok := scores[ak.key]; ok {
				return sc.Value
			}
			return reputation.InitialScore
		}
		maxScore := scoreOf(cached[0])
		for _, ak := range cached[1:] {
			if v := scoreOf(ak); v > maxScore {
				maxScore = v
			}
		}
		for _, ak := range cached {
			if scoreOf(ak) >= maxScore-1e-9 {
				filtered[ak.addr] = ak.ep
			}
		}
		logger.Warn().
			Int("total_endpoints", len(cached)).
			Int("kept_least_bad", len(filtered)).
			Float64("max_score", maxScore).
			Msg("Reputation filtered ALL endpoints; keeping the least-bad tier to avoid reputation-blind fallback")
		metrics.RecordReputationPoolCollapseGuard(serviceIDLabel, rpcTypeLabel)
	}

	return filtered
}

// endpointScoresForKeys fetches reputation scores for the given keys and projects
// them to the value map the TieredSelector needs. New endpoints (no stored score)
// get the initial score. Callers build the keys once and reuse them, avoiding a
// second BuildKey pass per endpoint.
func (p *Protocol) endpointScoresForKeys(
	ctx context.Context,
	keys []reputation.EndpointKey,
) (map[reputation.EndpointKey]float64, error) {
	scores, err := p.reputationService.GetScores(ctx, keys)
	if err != nil {
		return nil, err
	}

	result := make(map[reputation.EndpointKey]float64, len(keys))
	for _, key := range keys {
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
// Falls back to default if the tiered selector is not configured.
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
	requestedEndpointAddr protocol.EndpointAddr,
) map[protocol.EndpointAddr]endpoint {
	if len(endpoints) == 0 {
		return endpoints
	}

	// Get the tiered selector for this service (maybe per-service or global)
	selector := p.getTieredSelectorForService(serviceID)
	if selector == nil {
		// No selector configured, return all endpoints
		return endpoints
	}

	keyBuilder := p.reputationService.KeyBuilderForService(serviceID)

	// Build reputation keys once, together with the aggregated-key -> full-address
	// mapping. With per-domain/supplier granularity the reputation keys contain
	// domains (e.g., "dopokt.com") while we must return endpoints by their full
	// addresses (e.g., "pokt1abc-https://relay.dopokt.com"), and several addresses
	// can share one key. Reusing this single pass for both score lookup and result
	// reconstruction means BuildKey runs once per endpoint instead of twice.
	keys := make([]reputation.EndpointKey, 0, len(endpoints))
	keyToEndpoints := make(map[protocol.EndpointAddr][]protocol.EndpointAddr, len(endpoints))
	for addr := range endpoints {
		key := keyBuilder.BuildKey(serviceID, addr, rpcType)
		keys = append(keys, key)
		keyToEndpoints[key.EndpointAddr] = append(keyToEndpoints[key.EndpointAddr], addr)
	}

	// Fetch scores once and project to the value map the tiered selector needs.
	endpointScores, err := p.endpointScoresForKeys(ctx, keys)
	if err != nil {
		logger.Warn().Err(err).Msg("Failed to get endpoint scores for tiered filtering, returning all endpoints")
		return endpoints
	}

	// Update probation status and get a list of endpoints currently in probation
	probationEndpoints := selector.UpdateProbationStatus(endpointScores)
	probationCount := len(probationEndpoints)

	// Check if this request should be routed to probation endpoints
	shouldRouteToProbation := selector.ShouldRouteToProbation()

	// If probation routing is active, and we have probation endpoints, include them
	// alongside the highest-tier endpoints. This allows QoS to validate both:
	// - Probation endpoints that pass validation get traffic (recovery works)
	// - If all probation endpoints fail validation (e.g., stale block height),
	//   QoS falls back to the healthy tier endpoints instead of serving stale data
	if shouldRouteToProbation && probationCount > 0 {
		logger.Debug().
			Int("probation_count", probationCount).
			Float64("traffic_percent", selector.Config().Probation.TrafficPercent).
			Msg("Routing request to probation endpoints for recovery (with tier fallback)")

		// Start with probation endpoints
		result := make(map[protocol.EndpointAddr]endpoint)
		for _, key := range probationEndpoints {
			for _, fullAddr := range keyToEndpoints[key.EndpointAddr] {
				if ep, exists := endpoints[fullAddr]; exists {
					result[fullAddr] = ep
				}
			}
		}

		// Also include highest-tier endpoints as fallback.
		// QoS validation will prefer healthy endpoints over stale ones,
		// so probation endpoints only get traffic if they pass validation.
		tier1, tier2, tier3 := selector.GroupByTier(endpointScores)
		var fallbackKeys []reputation.EndpointKey
		switch {
		case len(tier1) > 0:
			fallbackKeys = tier1
		case len(tier2) > 0:
			fallbackKeys = tier2
		case len(tier3) > 0:
			fallbackKeys = tier3
		}
		for _, key := range fallbackKeys {
			for _, fullAddr := range keyToEndpoints[key.EndpointAddr] {
				if ep, exists := endpoints[fullAddr]; exists {
					result[fullAddr] = ep
				}
			}
		}

		// If a specific endpoint was requested and it's NOT in the result, but IS above threshold,
		// include it anyway to avoid "Selected endpoint is not available" error.
		if requestedEndpointAddr != "" {
			if _, ok := result[requestedEndpointAddr]; !ok {
				if ep, exists := endpoints[requestedEndpointAddr]; exists {
					key := keyBuilder.BuildKey(serviceID, requestedEndpointAddr, rpcType)
					if score, ok := endpointScores[key]; ok && score >= selector.MinThreshold() {
						result[requestedEndpointAddr] = ep
						logger.Debug().Str("endpoint", string(requestedEndpointAddr)).Msg("Including requested endpoint even though probation is active")
					}
				}
			}
		}

		return result
	}

	// Normal tier-based routing (non-probation)
	// Group endpoints by tier using the service-specific selector
	tier1, tier2, tier3 := selector.GroupByTier(endpointScores)
	tier1Count, tier2Count, tier3Count := len(tier1), len(tier2), len(tier3)

	// Log detailed tier distribution for observability
	logger.Debug().
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
		return make(map[protocol.EndpointAddr]endpoint)
	}

	// Build result map with only endpoints from the selected tier
	// Use keyToEndpoints mapping to get full endpoint addresses from domain/supplier keys
	result := make(map[protocol.EndpointAddr]endpoint)
	for _, key := range selectedKeys {
		for _, fullAddr := range keyToEndpoints[key.EndpointAddr] {
			if ep, exists := endpoints[fullAddr]; exists {
				result[fullAddr] = ep
			}
		}
	}

	// CRITICAL: If a specific endpoint was requested and it's NOT in the highest tier,
	// but it IS above the minimum threshold, we MUST include it.
	// This prevents the "Selected endpoint is not available" error which occurs when:
	// 1. AvailableHTTPEndpoints() returned the endpoint (it was in the highest tier then)
	// 2. Gateway selected it
	// 3. BuildHTTPRequestContextForEndpoint() is called, but now it's no longer in the highest tier
	if requestedEndpointAddr != "" {
		if _, ok := result[requestedEndpointAddr]; !ok {
			if ep, exists := endpoints[requestedEndpointAddr]; exists {
				key := keyBuilder.BuildKey(serviceID, requestedEndpointAddr, rpcType)
				if score, ok := endpointScores[key]; ok && score >= selector.MinThreshold() {
					result[requestedEndpointAddr] = ep
					logger.Debug().
						Str("endpoint", string(requestedEndpointAddr)).
						Int("selected_tier", selectedTier).
						Msg("Including requested endpoint even though it's not in the highest tier")
				}
			}
		}
	}

	logger.Debug().
		Int("selected_tier", selectedTier).
		Int("endpoints_in_selected_tier", len(result)).
		Int("tier1_available", tier1Count).
		Int("tier2_available", tier2Count).
		Int("tier3_available", tier3Count).
		Int("probation_available", probationCount).
		Msg("Tiered selection: returning endpoints from highest available tier")

	return result
}
