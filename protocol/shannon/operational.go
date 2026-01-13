package shannon

import (
	"context"

	"github.com/pokt-network/path/protocol"
)

// GetServiceReadiness returns readiness information for a specific service.
// A service is considered ready if it has active sessions and at least one endpoint.
//
// Returns:
//   - endpointCount: number of available endpoints (after reputation filtering)
//   - hasSession: true if at least one session is available for the service
//   - err: any error encountered while checking readiness
func (p *Protocol) GetServiceReadiness(serviceID protocol.ServiceID) (endpointCount int, hasSession bool, err error) {
	ctx := context.Background()

	// Check if we have sessions for this service
	sessions, err := p.getActiveGatewaySessions(ctx, serviceID, nil)
	if err != nil {
		return 0, false, err
	}

	hasSession = len(sessions) > 0

	if !hasSession {
		return 0, false, nil
	}

	// Get endpoint count (this includes reputation filtering)
	// We use getSessionsUniqueEndpoints to get the actual available endpoints
	// after filtering low-reputation endpoints
	// Note: We don't support Target-Suppliers header here since httpReq may be nil
	endpoints, _, err := p.getUniqueEndpoints(ctx, serviceID, sessions, true, 0, nil) // 0 = UNKNOWN_RPC, gets all types, nil = no supplier filtering
	if err != nil {
		// Not having endpoints isn't necessarily an error - might just be all filtered
		return 0, hasSession, nil
	}

	return len(endpoints), hasSession, nil
}

// GetSanitizedConfig returns a sanitized view of the active configuration.
// All sensitive information (private keys, passwords) is redacted.
func (p *Protocol) GetSanitizedConfig() map[string]interface{} {
	config := make(map[string]interface{})

	// Basic gateway info (no sensitive data)
	config["gateway_mode"] = string(p.gatewayMode)
	config["gateway_address"] = p.gatewayAddr

	// App info (addresses only, not private keys)
	appAddresses := make(map[string][]string)
	for serviceID, addresses := range p.ownedApps {
		appAddresses[string(serviceID)] = addresses
	}
	config["owned_apps"] = appAddresses

	// Reputation config (all public info)
	if p.reputationService != nil {
		repConfig := make(map[string]interface{})
		repConfig["enabled"] = true

		// Add tiered selection info if available
		if p.tieredSelector != nil {
			tierConfig := p.tieredSelector.Config()
			repConfig["tiered_selection"] = map[string]interface{}{
				"enabled":         tierConfig.Enabled,
				"tier1_threshold": tierConfig.Tier1Threshold,
				"tier2_threshold": tierConfig.Tier2Threshold,
				"probation": map[string]interface{}{
					"enabled":             tierConfig.Probation.Enabled,
					"threshold":           tierConfig.Probation.Threshold,
					"traffic_percent":     tierConfig.Probation.TrafficPercent,
					"recovery_multiplier": tierConfig.Probation.RecoveryMultiplier,
				},
			}
		}

		config["reputation_config"] = repConfig
	}

	// Unified services config (merged view)
	if p.unifiedServicesConfig != nil && p.unifiedServicesConfig.HasServices() {
		services := make([]map[string]interface{}, 0)

		for _, svc := range p.unifiedServicesConfig.Services {
			merged := p.unifiedServicesConfig.GetMergedServiceConfig(svc.ID)
			if merged == nil {
				continue
			}

			svcConfig := map[string]interface{}{
				"id":   string(svc.ID),
				"type": string(merged.Type),
			}

			if len(merged.RPCTypes) > 0 {
				svcConfig["rpc_types"] = merged.RPCTypes
			}

			if merged.LatencyProfile != "" {
				svcConfig["latency_profile"] = merged.LatencyProfile
			}

			// Add per-service reputation config if different from global
			if merged.ReputationConfig != nil {
				svcRepConfig := make(map[string]interface{})
				if merged.ReputationConfig.InitialScore != nil {
					svcRepConfig["initial_score"] = *merged.ReputationConfig.InitialScore
				}
				if merged.ReputationConfig.MinThreshold != nil {
					svcRepConfig["min_threshold"] = *merged.ReputationConfig.MinThreshold
				}
				if merged.ReputationConfig.RecoveryTimeout != nil {
					svcRepConfig["recovery_timeout"] = merged.ReputationConfig.RecoveryTimeout.String()
				}
				if len(svcRepConfig) > 0 {
					svcConfig["reputation_config"] = svcRepConfig
				}
			}

			// Add tiered selection if different from global
			if merged.TieredSelection != nil {
				tierConfig := make(map[string]interface{})
				if merged.TieredSelection.Enabled != nil {
					tierConfig["enabled"] = *merged.TieredSelection.Enabled
				}
				if merged.TieredSelection.Tier1Threshold != nil {
					tierConfig["tier1_threshold"] = *merged.TieredSelection.Tier1Threshold
				}
				if merged.TieredSelection.Tier2Threshold != nil {
					tierConfig["tier2_threshold"] = *merged.TieredSelection.Tier2Threshold
				}
				if len(tierConfig) > 0 {
					svcConfig["tiered_selection"] = tierConfig
				}
			}

			// Add retry config
			if merged.RetryConfig != nil {
				retryConfig := make(map[string]interface{})
				if merged.RetryConfig.Enabled != nil {
					retryConfig["enabled"] = *merged.RetryConfig.Enabled
				}
				if merged.RetryConfig.MaxRetries != nil {
					retryConfig["max_retries"] = *merged.RetryConfig.MaxRetries
				}
				if len(retryConfig) > 0 {
					svcConfig["retry_config"] = retryConfig
				}
			}

			// Add fallback info (URLs are public)
			if merged.Fallback != nil && merged.Fallback.Enabled {
				fallbackConfig := map[string]interface{}{
					"enabled":          true,
					"send_all_traffic": merged.Fallback.SendAllTraffic,
					"endpoint_count":   len(merged.Fallback.Endpoints),
				}
				svcConfig["fallback"] = fallbackConfig
			}

			// Add health check status
			if merged.HealthChecks != nil && merged.HealthChecks.Enabled != nil && *merged.HealthChecks.Enabled {
				hcConfig := map[string]interface{}{
					"enabled": true,
				}
				if merged.HealthChecks.Interval > 0 {
					hcConfig["interval"] = merged.HealthChecks.Interval.String()
				}
				if merged.HealthChecks.SyncAllowance != nil {
					hcConfig["sync_allowance"] = *merged.HealthChecks.SyncAllowance
				}
				if len(merged.HealthChecks.Local) > 0 {
					hcConfig["local_check_count"] = len(merged.HealthChecks.Local)
				}
				svcConfig["health_checks"] = hcConfig
			}

			services = append(services, svcConfig)
		}

		config["services"] = services

		// Add latency profiles
		if len(p.unifiedServicesConfig.LatencyProfiles) > 0 {
			profiles := make(map[string]interface{})
			for name, profile := range p.unifiedServicesConfig.LatencyProfiles {
				profiles[name] = map[string]interface{}{
					"fast_threshold":    profile.FastThreshold.String(),
					"normal_threshold":  profile.NormalThreshold.String(),
					"slow_threshold":    profile.SlowThreshold.String(),
					"penalty_threshold": profile.PenaltyThreshold.String(),
					"severe_threshold":  profile.SevereThreshold.String(),
				}
			}
			config["latency_profiles"] = profiles
		}
	}

	// Fallback configuration summary
	if len(p.serviceFallbackMap) > 0 {
		fallbacks := make(map[string]int)
		for serviceID, fb := range p.serviceFallbackMap {
			fallbacks[string(serviceID)] = len(fb.Endpoints)
		}
		config["fallback_endpoints"] = fallbacks
	}

	// Load testing mode (if active)
	if p.loadTestingConfig != nil {
		config["load_testing_mode"] = true
	}

	return config
}
