package shannon

import (
	"context"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/metrics"
	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
)

// getServiceRPCTypesForLeaderboard returns the RPC types configured for a service.
// Falls back to JSON_RPC if no RPC types are configured.
func (p *Protocol) getServiceRPCTypesForLeaderboard(serviceID protocol.ServiceID) []sharedtypes.RPCType {
	mapper := gateway.NewRPCTypeMapper()

	// Get configured RPC types for this service
	configuredTypes := p.unifiedServicesConfig.GetServiceRPCTypes(serviceID)
	if len(configuredTypes) == 0 {
		// Default to JSON_RPC if no types configured
		return []sharedtypes.RPCType{sharedtypes.RPCType_JSON_RPC}
	}

	var rpcTypes []sharedtypes.RPCType
	for _, typeStr := range configuredTypes {
		rpcType, err := mapper.ParseRPCType(typeStr)
		if err != nil {
			p.logger.Warn().
				Str("service_id", string(serviceID)).
				Str("rpc_type", typeStr).
				Err(err).
				Msg("Failed to parse RPC type for leaderboard, skipping")
			continue
		}
		rpcTypes = append(rpcTypes, rpcType)
	}

	// If all configured types failed to parse, fall back to JSON_RPC
	if len(rpcTypes) == 0 {
		return []sharedtypes.RPCType{sharedtypes.RPCType_JSON_RPC}
	}

	return rpcTypes
}

// GetEndpointLeaderboardData implements the metrics.LeaderboardDataProvider interface.
// It collects endpoint distribution data grouped by domain, rpc_type, service_id,
// tier_threshold, and session_start_height.
//
// This method iterates over all configured services and their endpoints to build
// a snapshot of the endpoint distribution for the metrics leaderboard.
func (p *Protocol) GetEndpointLeaderboardData(ctx context.Context) ([]metrics.EndpointLeaderboardEntry, error) {
	logger := p.logger.With("method", "GetEndpointLeaderboardData")

	// Check if unified services config is available
	if p.unifiedServicesConfig == nil {
		logger.Debug().Msg("No unified services config available, returning empty leaderboard")
		return nil, nil
	}
	logger.Debug().Int("service_count", len(p.unifiedServicesConfig.Services)).Msg("Building leaderboard data")

	// Use a map to aggregate endpoints by their grouping key
	type groupKey struct {
		Domain             string
		RPCType            string
		ServiceID          string
		TierThreshold      int
		SessionStartHeight int64
	}
	groups := make(map[groupKey]int)

	// Iterate over all configured services
	for _, serviceConfig := range p.unifiedServicesConfig.Services {
		serviceID := serviceConfig.ID

		// Get active sessions for this service (only works in centralized mode)
		// In delegated mode, this will fail since we don't have an HTTP request context
		activeSessions, err := p.getCentralizedGatewayModeActiveSessions(ctx, serviceID)
		if err != nil {
			logger.Debug().
				Str("service_id", string(serviceID)).
				Err(err).
				Msg("Failed to get sessions for service, skipping")
			continue
		}

		if len(activeSessions) == 0 {
			continue
		}

		// Query only for RPC types configured for this service
		rpcTypesToQuery := p.getServiceRPCTypesForLeaderboard(serviceID)

		for _, rpcType := range rpcTypesToQuery {
			// Get endpoints for this RPC type, bypassing reputation filtering
			endpoints, _, uniqueEndpointsErr := p.getUniqueEndpoints(ctx, serviceID, activeSessions, false, rpcType, nil)
			if uniqueEndpointsErr != nil {
				// No endpoints for this RPC type is normal - suppliers may not support all types
				continue
			}

			// Process each endpoint
			for endpointAddr, ep := range endpoints {
				endpointURL := ep.GetURL(rpcType)
				domain, domainErr := shannonmetrics.ExtractDomainOrHost(endpointURL)
				if domainErr != nil {
					domain = shannonmetrics.ErrDomain
				}

				// Get session start height
				var sessionStartHeight int64
				if session := ep.Session(); session != nil && session.Header != nil {
					sessionStartHeight = session.Header.SessionStartBlockHeight
				}

				// Get tier threshold for this endpoint
				tierThreshold := p.getTierThresholdForEndpoint(ctx, serviceID, endpointAddr, rpcType)

				// Create a grouping key
				key := groupKey{
					Domain:             domain,
					RPCType:            metrics.NormalizeRPCType(rpcType.String()),
					ServiceID:          string(serviceID),
					TierThreshold:      tierThreshold,
					SessionStartHeight: sessionStartHeight,
				}

				// Increment count for this group
				groups[key]++
			}
		}
	}

	// Convert groups map to slice of entries
	entries := make([]metrics.EndpointLeaderboardEntry, 0, len(groups))
	for key, count := range groups {
		entries = append(entries, metrics.EndpointLeaderboardEntry{
			Domain:             key.Domain,
			RPCType:            key.RPCType,
			ServiceID:          key.ServiceID,
			TierThreshold:      key.TierThreshold,
			SessionStartHeight: key.SessionStartHeight,
			EndpointCount:      count,
		})
	}

	logger.Debug().Int("total_groups", len(entries)).Msg("Built endpoint leaderboard data")
	return entries, nil
}

// getTierThresholdForEndpoint determines the tier threshold for an endpoint based on its score.
// Returns the minimum score threshold for the tier the endpoint falls into.
// Returns -1 if tier cannot be determined (reputation disabled, no selector, or error getting score).
func (p *Protocol) getTierThresholdForEndpoint(
	ctx context.Context,
	serviceID protocol.ServiceID,
	endpointAddr protocol.EndpointAddr,
	rpcType sharedtypes.RPCType,
) int {
	// If reputation service is not enabled, return -1 (unknown tier)
	if p.reputationService == nil {
		return -1
	}

	// Get the tiered selector for this service (per-service or global)
	selector := p.getTieredSelectorForService(serviceID)
	if selector == nil {
		return -1
	}

	// Get the endpoint's score using the key builder to respect key_granularity
	keyBuilder := p.reputationService.KeyBuilderForService(serviceID)
	key := keyBuilder.BuildKey(serviceID, endpointAddr, rpcType)
	score, err := p.reputationService.GetScore(ctx, key)
	if err != nil {
		// If score not found, use initial score for the service
		// This matches what tiered selection does for new endpoints
		score = reputation.Score{Value: p.reputationService.GetInitialScoreForService(serviceID)}
	}

	// Get the tier for this score
	tier := selector.TierForScore(score.Value)

	// Map tier to threshold
	// Tier 1: score >= Tier1Threshold
	// Tier 2: score >= Tier2Threshold (but < Tier1Threshold)
	// Tier 3: score >= minThreshold (but < Tier2Threshold)
	// Tier 0: score < minThreshold
	config := selector.Config()
	switch tier {
	case 1:
		return int(config.Tier1Threshold)
	case 2:
		return int(config.Tier2Threshold)
	case 3:
		return int(selector.MinThreshold())
	default:
		return 0
	}
}

// GetMeanScoreData implements the metrics.LeaderboardDataProvider interface.
// It calculates the mean reputation score per domain/service_id/rpc_type combination.
func (p *Protocol) GetMeanScoreData(ctx context.Context) ([]metrics.MeanScoreEntry, error) {
	logger := p.logger.With("method", "GetMeanScoreData")

	// Check if unified services config is available
	if p.unifiedServicesConfig == nil {
		logger.Debug().Msg("No unified services config available, returning empty mean scores")
		return nil, nil
	}

	// If reputation service is not enabled, return empty
	if p.reputationService == nil {
		logger.Debug().Msg("Reputation service not enabled, returning empty mean scores")
		return nil, nil
	}

	// Use a map to aggregate scores by domain/service/rpc_type
	type scoreKey struct {
		Domain    string
		ServiceID string
		RPCType   string
	}
	type scoreAggregator struct {
		TotalScore float64
		Count      int
	}
	aggregates := make(map[scoreKey]*scoreAggregator)

	// Iterate over all configured services
	for _, serviceConfig := range p.unifiedServicesConfig.Services {
		serviceID := serviceConfig.ID

		// Get active sessions for this service
		activeSessions, err := p.getCentralizedGatewayModeActiveSessions(ctx, serviceID)
		if err != nil {
			continue
		}

		if len(activeSessions) == 0 {
			continue
		}

		// Query only for RPC types configured for this service
		rpcTypesToQuery := p.getServiceRPCTypesForLeaderboard(serviceID)

		for _, rpcType := range rpcTypesToQuery {
			// Get endpoints for this RPC type, bypassing reputation filtering
			endpoints, _, uniqueEndpointsErr := p.getUniqueEndpoints(ctx, serviceID, activeSessions, false, rpcType, nil)
			if uniqueEndpointsErr != nil {
				continue
			}

			// Process each endpoint
			for endpointAddr, ep := range endpoints {
				endpointURL := ep.GetURL(rpcType)
				domain, domainErr := shannonmetrics.ExtractDomainOrHost(endpointURL)
				if domainErr != nil {
					domain = shannonmetrics.ErrDomain
				}

				// Get the endpoint's score
				keyBuilder := p.reputationService.KeyBuilderForService(serviceID)
				key := keyBuilder.BuildKey(serviceID, endpointAddr, rpcType)
				score, scoreErr := p.reputationService.GetScore(ctx, key)
				if scoreErr != nil {
					// Use initial score for endpoints without scores
					score = reputation.Score{Value: p.reputationService.GetInitialScoreForService(serviceID)}
				}

				// Create aggregation key
				aggKey := scoreKey{
					Domain:    domain,
					ServiceID: string(serviceID),
					RPCType:   metrics.NormalizeRPCType(rpcType.String()),
				}

				// Initialize aggregator if not exists
				if aggregates[aggKey] == nil {
					aggregates[aggKey] = &scoreAggregator{}
				}

				// Add to aggregate
				aggregates[aggKey].TotalScore += score.Value
				aggregates[aggKey].Count++
			}
		}
	}

	// Convert aggregates to entries with mean scores
	entries := make([]metrics.MeanScoreEntry, 0, len(aggregates))
	for key, agg := range aggregates {
		if agg.Count > 0 {
			entries = append(entries, metrics.MeanScoreEntry{
				Domain:    key.Domain,
				ServiceID: key.ServiceID,
				RPCType:   key.RPCType,
				MeanScore: agg.TotalScore / float64(agg.Count),
			})
		}
	}

	logger.Debug().Int("total_entries", len(entries)).Msg("Built mean score data")
	return entries, nil
}

// Ensure Protocol implements LeaderboardDataProvider at compile time
var _ metrics.LeaderboardDataProvider = (*Protocol)(nil)
