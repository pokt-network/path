package reputation

import (
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

// ScoreImpact defines the default score changes for each signal type.
// These can be overridden by service-specific scoring rules.
var ScoreImpact = map[SignalType]float64{
	SignalTypeSuccess:         +1,  // Small positive impact
	SignalTypeMinorError:      -3,  // Minor penalty
	SignalTypeMajorError:      -10, // Moderate penalty
	SignalTypeCriticalError:   -25, // Severe penalty
	SignalTypeFatalError:      -50, // Maximum penalty (was a permanent ban)
	SignalTypeRecoverySuccess: +15, // Boosted recovery - allows faster climb back
}

// GetDefaultImpact returns the default score impact for a signal type.
func (s Signal) GetDefaultImpact() float64 {
	if impact, ok := ScoreImpact[s.Type]; ok {
		return impact
	}
	return 0
}

// IsPositive returns true if this signal has a positive impact on score.
func (s Signal) IsPositive() bool {
	return s.GetDefaultImpact() > 0
}

// IsNegative returns true if this signal has a negative impact on score.
func (s Signal) IsNegative() bool {
	return s.GetDefaultImpact() < 0
}
