package shannon

import (
	"time"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/reputation"
)

// buildLatencyConfigForService builds a LatencyConfig for a service by choosing between:
// 1. Simple ServiceLatencyConfig (target_ms + penalty_weight)
// 2. Profile-based LatencyProfileConfig (complex thresholds)
//
// Priority order:
// 1. If service.Latency.Enabled is explicitly false, latency scoring is disabled for this service
// 2. If service.Latency has TargetMs > 0, use simple target-based config
// 3. If service has LatencyProfile, use profile-based config
// 4. Otherwise, use global defaults
//
// Returns nil if no configuration should be applied (service uses global defaults).
func buildLatencyConfigForService(
	merged *gateway.ServiceConfig,
	globalLatency reputation.LatencyConfig,
	unifiedConfig *gateway.UnifiedServicesConfig,
) *reputation.LatencyConfig {
	// Priority 1: Check if latency is explicitly disabled for this service
	if merged.Latency != nil && merged.Latency.Enabled != nil && !*merged.Latency.Enabled {
		disabled := globalLatency
		disabled.Enabled = false
		return &disabled
	}

	// Priority 2: Simple target-based config (ServiceLatencyConfig with target_ms)
	if merged.Latency != nil && merged.Latency.TargetMs > 0 {
		return buildSimpleLatencyConfig(merged.Latency, globalLatency)
	}

	// Priority 3: Profile-based config (LatencyProfile with multiple thresholds)
	if merged.LatencyProfile != "" && unifiedConfig != nil {
		latencyProfile := unifiedConfig.GetLatencyProfile(merged.LatencyProfile)
		if latencyProfile != nil {
			return &reputation.LatencyConfig{
				Enabled:          globalLatency.Enabled,
				FastThreshold:    latencyProfile.FastThreshold,
				NormalThreshold:  latencyProfile.NormalThreshold,
				SlowThreshold:    latencyProfile.SlowThreshold,
				PenaltyThreshold: latencyProfile.PenaltyThreshold,
				SevereThreshold:  latencyProfile.SevereThreshold,
				FastBonus:        latencyProfile.FastBonus,
				SlowPenalty:      latencyProfile.SlowPenalty,
				VerySlowPenalty:  latencyProfile.VerySlowPenalty,
			}
		}
	}

	// Priority 4: No service-specific config, use global defaults
	return nil
}

// buildSimpleLatencyConfig converts a simple ServiceLatencyConfig (target_ms + penalty_weight)
// into a full LatencyConfig with thresholds derived from the target.
//
// Simple approach:
//   - Fast: < target_ms → bonus (2x)
//   - Normal: target_ms to 2x target → standard impact (1x)
//   - Slow: 2x to 3x target → reduced impact (0.5x * weight)
//   - Very slow: > 3x target → minimal impact (0.0)
//   - Penalty threshold: 2x target_ms
//   - Severe threshold: 3x target_ms
//
// The penalty_weight scales the overall latency impact:
//   - 0.0 = no latency impact
//   - 0.5 = half impact
//   - 1.0 = full impact (default)
//   - 2.0 = double impact
func buildSimpleLatencyConfig(
	simpleConfig *gateway.ServiceLatencyConfig,
	globalLatency reputation.LatencyConfig,
) *reputation.LatencyConfig {
	targetDuration := time.Duration(simpleConfig.TargetMs) * time.Millisecond
	weight := simpleConfig.PenaltyWeight
	if weight == 0 {
		weight = 1.0 // Default to full weight
	}

	// Check if explicitly enabled/disabled
	enabled := globalLatency.Enabled
	if simpleConfig.Enabled != nil {
		enabled = *simpleConfig.Enabled
	}

	// Build thresholds based on target
	return &reputation.LatencyConfig{
		Enabled:          enabled,
		FastThreshold:    targetDuration,              // Fast: < target
		NormalThreshold:  targetDuration * 2,          // Normal: target to 2x target
		SlowThreshold:    targetDuration * 3,          // Slow: 2x to 3x target
		PenaltyThreshold: targetDuration * 2,          // Penalty: > 2x target
		SevereThreshold:  targetDuration * 3,          // Severe: > 3x target
		FastBonus:        2.0 * weight,                // Fast gets bonus, scaled by weight
		SlowPenalty:      0.5 * weight,                // Slow gets reduced impact, scaled by weight
		VerySlowPenalty:  0.0,                         // Very slow gets no bonus (always 0)
	}
}
