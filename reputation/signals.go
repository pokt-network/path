package reputation

import (
	"fmt"
	"strconv"
	"time"
)

// SignalType categorizes signals by their impact on reputation.
type SignalType string

const (
	// SignalTypeSuccess indicates a successful request/response.
	SignalTypeSuccess SignalType = "success"

	// SignalTypeMinorError indicates a minor error (validation issues, unknown errors).
	SignalTypeMinorError SignalType = "minor_error"

	// SignalTypeMajorError indicates a major error (timeout, connection issues).
	SignalTypeMajorError SignalType = "major_error"

	// SignalTypeCriticalError indicates a critical error (HTTP 5xx, transport errors).
	SignalTypeCriticalError SignalType = "critical_error"

	// SignalTypeFatalError indicates a fatal error (service misconfiguration).
	// Was previously "permanent sanction" - now just a severe score penalty.
	SignalTypeFatalError SignalType = "fatal_error"

	// SignalTypeRecoverySuccess indicates a successful request from a low-scoring endpoint.
	// This signal has higher impact (+15) than regular success (+1) to allow endpoints
	// to recover faster when they prove they're healthy again.
	// Intended for use by:
	//   - Probation system (PR 7): when sampling traffic to low-scoring endpoints
	//   - Health checks (PR 9): when probing excluded endpoints
	SignalTypeRecoverySuccess SignalType = "recovery_success"

	// SignalTypeSlowResponse indicates a successful but slow response (> PenaltyThreshold).
	// This applies a small penalty to the endpoint's reputation even though the request
	// succeeded, because slow responses indicate degraded performance.
	// Default penalty threshold: 2000ms
	SignalTypeSlowResponse SignalType = "slow_response"

	// SignalTypeVerySlowResponse indicates a successful but very slow response (> SevereThreshold).
	// This applies a moderate penalty to the endpoint's reputation.
	// Default severe threshold: 5000ms
	SignalTypeVerySlowResponse SignalType = "very_slow_response"
)

// Signal represents an event that affects an endpoint's reputation.
type Signal struct {
	// Type categorizes the signal's severity.
	Type SignalType

	// Timestamp when the signal was generated.
	Timestamp time.Time

	// Latency of the request that generated this signal (if applicable).
	Latency time.Duration

	// Reason provides additional context for the signal.
	Reason string

	// Metadata holds additional signal-specific data.
	Metadata map[string]string
}

// NewSuccessSignal creates a signal for a successful request.
func NewSuccessSignal(latency time.Duration) Signal {
	return Signal{
		Type:      SignalTypeSuccess,
		Timestamp: time.Now(),
		Latency:   latency,
	}
}

// NewMinorErrorSignal creates a signal for a minor error.
func NewMinorErrorSignal(reason string) Signal {
	return Signal{
		Type:      SignalTypeMinorError,
		Timestamp: time.Now(),
		Reason:    reason,
	}
}

// NewMajorErrorSignal creates a signal for a major error (timeout, connection issues).
func NewMajorErrorSignal(reason string, latency time.Duration) Signal {
	return Signal{
		Type:      SignalTypeMajorError,
		Timestamp: time.Now(),
		Latency:   latency,
		Reason:    reason,
	}
}

// NewCriticalErrorSignal creates a signal for a critical error (HTTP 5xx).
func NewCriticalErrorSignal(reason string, latency time.Duration) Signal {
	return Signal{
		Type:      SignalTypeCriticalError,
		Timestamp: time.Now(),
		Latency:   latency,
		Reason:    reason,
	}
}

// NewFatalErrorSignal creates a signal for a fatal error.
// This replaces the concept of "permanent sanctions" - endpoints can recover.
func NewFatalErrorSignal(reason string) Signal {
	return Signal{
		Type:      SignalTypeFatalError,
		Timestamp: time.Now(),
		Reason:    reason,
	}
}

// NewRecoverySuccessSignal creates a signal for a successful request from a
// low-scoring endpoint. This has higher impact (+15) than regular success (+1)
// to allow endpoints to recover faster when proving they're healthy.
// Intended for use by Probation system (PR 7) and Health checks (PR 9).
func NewRecoverySuccessSignal(latency time.Duration) Signal {
	return Signal{
		Type:      SignalTypeRecoverySuccess,
		Timestamp: time.Now(),
		Latency:   latency,
	}
}

// NewSlowResponseSignal creates a signal for a successful but slow response.
// This applies a small penalty (-1) even though the request succeeded.
// Use when response latency exceeds PenaltyThreshold (default: 2000ms).
func NewSlowResponseSignal(latency time.Duration) Signal {
	return Signal{
		Type:      SignalTypeSlowResponse,
		Timestamp: time.Now(),
		Latency:   latency,
		Reason:    "slow response",
	}
}

// NewVerySlowResponseSignal creates a signal for a successful but very slow response.
// This applies a moderate penalty (-3) even though the request succeeded.
// Use when response latency exceeds SevereThreshold (default: 5000ms).
func NewVerySlowResponseSignal(latency time.Duration) Signal {
	return Signal{
		Type:      SignalTypeVerySlowResponse,
		Timestamp: time.Now(),
		Latency:   latency,
		Reason:    "very slow response",
	}
}

// scoreImpact defines the default score changes for each signal type.
// This map is unexported to prevent runtime modification.
// Use GetScoreImpact() to retrieve values.
var scoreImpact = map[SignalType]float64{
	SignalTypeSuccess:          +1,  // Small positive impact
	SignalTypeMinorError:       -3,  // Minor penalty
	SignalTypeMajorError:       -10, // Moderate penalty
	SignalTypeCriticalError:    -25, // Severe penalty
	SignalTypeFatalError:       -50, // Maximum penalty (was a permanent ban)
	SignalTypeRecoverySuccess:  +15, // Boosted recovery - allows faster climb back
	SignalTypeSlowResponse:     -1,  // Small penalty for slow responses
	SignalTypeVerySlowResponse: -3,  // Moderate penalty for very slow responses
}

// GetScoreImpact returns the default score impact for a signal type.
// Returns 0 if the signal type is not recognized.
func GetScoreImpact(t SignalType) float64 {
	if impact, ok := scoreImpact[t]; ok {
		return impact
	}
	return 0
}

// GetDefaultImpact returns the default score impact for this signal.
func (s Signal) GetDefaultImpact() float64 {
	return GetScoreImpact(s.Type)
}

// IsPositive returns true if this signal has a positive impact on score.
func (s Signal) IsPositive() bool {
	return s.GetDefaultImpact() > 0
}

// IsNegative returns true if this signal has a negative impact on score.
func (s Signal) IsNegative() bool {
	return s.GetDefaultImpact() < 0
}

// LatencyImpactResult contains the result of latency-aware impact calculation
// along with metadata useful for logging/observability.
type LatencyImpactResult struct {
	FinalImpact     float64
	BaseImpact      float64
	Modifier        float64
	LatencyCategory string // "fast", "normal", "slow", "very_slow", or "skipped"
	Latency         time.Duration
	Config          LatencyConfig
}

// CalculateLatencyAwareImpact calculates the score impact with latency modifiers.
// For success signals, the impact is modified based on response latency:
//   - Fast (< FastThreshold):      base_impact * FastBonus (default: +1 * 2.0 = +2)
//   - Normal (< NormalThreshold):  base_impact * 1.0 (default: +1)
//   - Slow (< SlowThreshold):      base_impact * SlowPenalty (default: +1 * 0.5 = +0.5)
//   - Very slow (>= SlowThreshold): base_impact * VerySlowPenalty (default: +1 * 0.0 = 0)
//
// For error signals, the latency modifier is not applied - errors have fixed impact.
//
// Additionally, if latency exceeds PenaltyThreshold or SevereThreshold,
// an additional penalty signal should be recorded separately.
func (s Signal) CalculateLatencyAwareImpact(config LatencyConfig) float64 {
	result := s.CalculateLatencyAwareImpactWithDetails(config)
	return result.FinalImpact
}

// CalculateLatencyAwareImpactWithDetails calculates the score impact with latency modifiers
// and returns detailed information about the calculation for logging purposes.
// Uses the default (hardcoded) base impact from GetDefaultImpact().
func (s Signal) CalculateLatencyAwareImpactWithDetails(config LatencyConfig) LatencyImpactResult {
	return s.CalculateLatencyAwareImpactWithConfig(config, s.GetDefaultImpact())
}

// CalculateLatencyAwareImpactWithConfig calculates the score impact with latency modifiers
// using a provided base impact value. This allows using configurable signal impacts.
func (s Signal) CalculateLatencyAwareImpactWithConfig(config LatencyConfig, baseImpact float64) LatencyImpactResult {
	// Only apply latency modifiers to positive signals (success, recovery_success)
	if baseImpact <= 0 || s.Latency == 0 || !config.Enabled {
		return LatencyImpactResult{
			FinalImpact:     baseImpact,
			BaseImpact:      baseImpact,
			Modifier:        1.0,
			LatencyCategory: "skipped",
			Latency:         s.Latency,
			Config:          config,
		}
	}

	// Apply latency-based modifier for success signals
	var modifier float64
	var latencyCategory string
	switch {
	case s.Latency < config.FastThreshold:
		// Fast response - bonus multiplier
		modifier = config.FastBonus
		latencyCategory = "fast"
	case s.Latency < config.NormalThreshold:
		// Normal response - standard impact
		modifier = 1.0
		latencyCategory = "normal"
	case s.Latency < config.SlowThreshold:
		// Slow response - reduced impact
		modifier = config.SlowPenalty
		latencyCategory = "slow"
	default:
		// Very slow response - minimal/no impact
		modifier = config.VerySlowPenalty
		latencyCategory = "very_slow"
	}

	return LatencyImpactResult{
		FinalImpact:     baseImpact * modifier,
		BaseImpact:      baseImpact,
		Modifier:        modifier,
		LatencyCategory: latencyCategory,
		Latency:         s.Latency,
		Config:          config,
	}
}

// ClassifyLatency returns the latency signal type based on thresholds.
// Returns nil if no additional penalty signal is needed.
// This is separate from success/error signals - it's an additional penalty.
func ClassifyLatency(latency time.Duration, config LatencyConfig) *SignalType {
	if !config.Enabled || latency == 0 {
		return nil
	}

	if latency >= config.SevereThreshold {
		t := SignalTypeVerySlowResponse
		return &t
	}
	if latency >= config.PenaltyThreshold {
		t := SignalTypeSlowResponse
		return &t
	}
	return nil
}

// WithMultiplier returns a new Signal with a multiplier applied to its impact.
// This is used by probation to boost recovery when endpoints successfully handle requests.
// The multiplier only affects the metadata - the actual impact calculation happens
// in the scoring service which can read the multiplier from metadata.
func (s Signal) WithMultiplier(multiplier float64) Signal {
	if s.Metadata == nil {
		s.Metadata = make(map[string]string)
	}
	s.Metadata["recovery_multiplier"] = fmt.Sprintf("%.2f", multiplier)
	return s
}

// GetRecoveryMultiplier returns the recovery multiplier from signal metadata.
// Returns 1.0 if no multiplier is set (no modification to impact).
// This is used by calculateImpact to boost recovery scoring for probation endpoints.
func (s Signal) GetRecoveryMultiplier() float64 {
	if s.Metadata == nil {
		return 1.0
	}
	if multiplierStr, ok := s.Metadata["recovery_multiplier"]; ok {
		if multiplier, err := strconv.ParseFloat(multiplierStr, 64); err == nil && multiplier > 0 {
			return multiplier
		}
	}
	return 1.0
}
