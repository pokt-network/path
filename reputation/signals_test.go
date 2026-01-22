package reputation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewSuccessSignal(t *testing.T) {
	latency := 150 * time.Millisecond
	signal := NewSuccessSignal(latency)

	require.Equal(t, SignalTypeSuccess, signal.Type)
	require.Equal(t, latency, signal.Latency)
	require.WithinDuration(t, time.Now(), signal.Timestamp, time.Second)
}

func TestNewMinorErrorSignal(t *testing.T) {
	reason := "validation failed"
	signal := NewMinorErrorSignal(reason)

	require.Equal(t, SignalTypeMinorError, signal.Type)
	require.Equal(t, reason, signal.Reason)
	require.WithinDuration(t, time.Now(), signal.Timestamp, time.Second)
}

func TestNewMajorErrorSignal(t *testing.T) {
	reason := "connection timeout"
	latency := 5 * time.Second
	signal := NewMajorErrorSignal(reason, latency)

	require.Equal(t, SignalTypeMajorError, signal.Type)
	require.Equal(t, reason, signal.Reason)
	require.Equal(t, latency, signal.Latency)
}

func TestNewCriticalErrorSignal(t *testing.T) {
	reason := "HTTP 503"
	latency := 200 * time.Millisecond
	signal := NewCriticalErrorSignal(reason, latency)

	require.Equal(t, SignalTypeCriticalError, signal.Type)
	require.Equal(t, reason, signal.Reason)
	require.Equal(t, latency, signal.Latency)
}

func TestNewFatalErrorSignal(t *testing.T) {
	reason := "service not configured"
	signal := NewFatalErrorSignal(reason)

	require.Equal(t, SignalTypeFatalError, signal.Type)
	require.Equal(t, reason, signal.Reason)
}

func TestNewRecoverySuccessSignal(t *testing.T) {
	latency := 100 * time.Millisecond
	signal := NewRecoverySuccessSignal(latency)

	require.Equal(t, SignalTypeRecoverySuccess, signal.Type)
	require.Equal(t, latency, signal.Latency)
	require.WithinDuration(t, time.Now(), signal.Timestamp, time.Second)

	// Recovery success should have higher impact than regular success
	require.Greater(t, signal.GetDefaultImpact(), GetScoreImpact(SignalTypeSuccess),
		"recovery success should have higher impact than regular success")
}

func TestSignal_GetDefaultImpact(t *testing.T) {
	tests := []struct {
		name           string
		signalType     SignalType
		expectedImpact float64
	}{
		{
			name:           "success has positive impact",
			signalType:     SignalTypeSuccess,
			expectedImpact: +1,
		},
		{
			name:           "minor error has small negative impact",
			signalType:     SignalTypeMinorError,
			expectedImpact: -3,
		},
		{
			name:           "major error has moderate negative impact",
			signalType:     SignalTypeMajorError,
			expectedImpact: -10,
		},
		{
			name:           "critical error has severe negative impact",
			signalType:     SignalTypeCriticalError,
			expectedImpact: -25,
		},
		{
			name:           "fatal error has maximum negative impact",
			signalType:     SignalTypeFatalError,
			expectedImpact: -50,
		},
		{
			name:           "recovery success has moderate positive impact",
			signalType:     SignalTypeRecoverySuccess,
			expectedImpact: +5, // Reduced from +15 to require sustained good behavior
		},
		{
			name:           "unknown type has zero impact",
			signalType:     SignalType("unknown"),
			expectedImpact: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signal := Signal{Type: tt.signalType}
			require.Equal(t, tt.expectedImpact, signal.GetDefaultImpact())
		})
	}
}

func TestSignal_IsPositive(t *testing.T) {
	positiveSignal := Signal{Type: SignalTypeSuccess}
	require.True(t, positiveSignal.IsPositive())
	require.False(t, positiveSignal.IsNegative())

	negativeSignal := Signal{Type: SignalTypeMajorError}
	require.False(t, negativeSignal.IsPositive())
	require.True(t, negativeSignal.IsNegative())

	neutralSignal := Signal{Type: SignalType("unknown")}
	require.False(t, neutralSignal.IsPositive())
	require.False(t, neutralSignal.IsNegative())
}

func TestGetScoreImpact_Values(t *testing.T) {
	// Verify impact values make sense relative to each other
	successImpact := GetScoreImpact(SignalTypeSuccess)
	recoveryImpact := GetScoreImpact(SignalTypeRecoverySuccess)
	minorImpact := GetScoreImpact(SignalTypeMinorError)
	majorImpact := GetScoreImpact(SignalTypeMajorError)
	criticalImpact := GetScoreImpact(SignalTypeCriticalError)
	fatalImpact := GetScoreImpact(SignalTypeFatalError)

	// Success should be positive
	require.Greater(t, successImpact, float64(0), "success should have positive impact")

	// Recovery success should be positive and greater than regular success
	require.Greater(t, recoveryImpact, float64(0), "recovery success should have positive impact")
	require.Greater(t, recoveryImpact, successImpact, "recovery success should be greater than regular success")

	// Errors should be negative and increasingly severe
	require.Less(t, minorImpact, float64(0), "minor error should have negative impact")
	require.Less(t, majorImpact, minorImpact, "major error should be worse than minor")
	require.Less(t, criticalImpact, majorImpact, "critical error should be worse than major")
	require.Less(t, fatalImpact, criticalImpact, "fatal error should be worse than critical")
}

func TestGetScoreImpact_UnknownType(t *testing.T) {
	// Unknown signal types should return 0
	require.Equal(t, float64(0), GetScoreImpact(SignalType("unknown")))
	require.Equal(t, float64(0), GetScoreImpact(SignalType("")))
}
