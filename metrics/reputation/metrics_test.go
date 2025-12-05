package reputation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecordSignal(t *testing.T) {
	tests := []struct {
		name           string
		serviceID      string
		signalType     string
		endpointType   string
		endpointDomain string
	}{
		{
			name:           "success signal for eth jsonrpc",
			serviceID:      "eth",
			signalType:     "success",
			endpointType:   EndpointTypeJSONRPC,
			endpointDomain: "example.com",
		},
		{
			name:           "minor error signal for solana websocket",
			serviceID:      "solana",
			signalType:     "minor_error",
			endpointType:   EndpointTypeWebSocket,
			endpointDomain: "rpc.example.com",
		},
		{
			name:           "major error signal for polygon rest",
			serviceID:      "polygon",
			signalType:     "major_error",
			endpointType:   EndpointTypeREST,
			endpointDomain: "api.polygon.com",
		},
		{
			name:           "critical error signal for grpc",
			serviceID:      "cosmos",
			signalType:     "critical_error",
			endpointType:   EndpointTypeGRPC,
			endpointDomain: "grpc.cosmos.com",
		},
		{
			name:           "fatal error signal for unknown endpoint type",
			serviceID:      "avalanche",
			signalType:     "fatal_error",
			endpointType:   EndpointTypeUnknown,
			endpointDomain: "unknown.endpoint.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotPanics(t, func() {
				RecordSignal(tt.serviceID, tt.signalType, tt.endpointType, tt.endpointDomain)
			})
		})
	}
}

func TestRecordEndpointFiltered(t *testing.T) {
	tests := []struct {
		name           string
		serviceID      string
		endpointDomain string
	}{
		{
			name:           "filter eth endpoint",
			serviceID:      "eth",
			endpointDomain: "bad-endpoint.com",
		},
		{
			name:           "filter solana endpoint",
			serviceID:      "solana",
			endpointDomain: "unreliable.solana.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotPanics(t, func() {
				RecordEndpointFiltered(tt.serviceID, tt.endpointDomain)
			})
		})
	}
}

func TestRecordEndpointAllowed(t *testing.T) {
	tests := []struct {
		name           string
		serviceID      string
		endpointDomain string
	}{
		{
			name:           "allow eth endpoint",
			serviceID:      "eth",
			endpointDomain: "good-endpoint.com",
		},
		{
			name:           "allow polygon endpoint",
			serviceID:      "polygon",
			endpointDomain: "reliable.polygon.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotPanics(t, func() {
				RecordEndpointAllowed(tt.serviceID, tt.endpointDomain)
			})
		})
	}
}

func TestRecordScoreObservation(t *testing.T) {
	tests := []struct {
		name      string
		serviceID string
		score     float64
	}{
		{
			name:      "high score",
			serviceID: "eth",
			score:     85.5,
		},
		{
			name:      "low score",
			serviceID: "solana",
			score:     25.3,
		},
		{
			name:      "medium score",
			serviceID: "polygon",
			score:     55.0,
		},
		{
			name:      "perfect score",
			serviceID: "cosmos",
			score:     100.0,
		},
		{
			name:      "zero score",
			serviceID: "avalanche",
			score:     0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotPanics(t, func() {
				RecordScoreObservation(tt.serviceID, tt.score)
			})
		})
	}
}

func TestRecordError(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		errorType string
	}{
		{
			name:      "record signal error",
			operation: "record_signal",
			errorType: "storage_error",
		},
		{
			name:      "get score error",
			operation: "get_score",
			errorType: "not_found",
		},
		{
			name:      "filter error",
			operation: "filter",
			errorType: "invalid_threshold",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotPanics(t, func() {
				RecordError(tt.operation, tt.errorType)
			})
		})
	}
}

func TestSetProbationEndpointsCount(t *testing.T) {
	tests := []struct {
		name      string
		serviceID string
		count     int
	}{
		{
			name:      "zero endpoints in probation",
			serviceID: "eth",
			count:     0,
		},
		{
			name:      "multiple endpoints in probation",
			serviceID: "solana",
			count:     5,
		},
		{
			name:      "single endpoint in probation",
			serviceID: "polygon",
			count:     1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotPanics(t, func() {
				SetProbationEndpointsCount(tt.serviceID, tt.count)
			})
		})
	}
}

func TestRecordProbationTransition(t *testing.T) {
	tests := []struct {
		name           string
		serviceID      string
		endpointDomain string
		transition     string
	}{
		{
			name:           "endpoint entered probation",
			serviceID:      "eth",
			endpointDomain: "failing.endpoint.com",
			transition:     ProbationTransitionEntered,
		},
		{
			name:           "endpoint exited probation",
			serviceID:      "solana",
			endpointDomain: "recovered.endpoint.com",
			transition:     ProbationTransitionExited,
		},
		{
			name:           "endpoint recovered",
			serviceID:      "polygon",
			endpointDomain: "good.endpoint.com",
			transition:     ProbationTransitionRecovered,
		},
		{
			name:           "endpoint demoted",
			serviceID:      "cosmos",
			endpointDomain: "degraded.endpoint.com",
			transition:     ProbationTransitionDemoted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotPanics(t, func() {
				RecordProbationTransition(tt.serviceID, tt.endpointDomain, tt.transition)
			})
		})
	}
}

func TestRecordProbationTraffic(t *testing.T) {
	tests := []struct {
		name           string
		serviceID      string
		endpointDomain string
		success        bool
	}{
		{
			name:           "successful probation traffic",
			serviceID:      "eth",
			endpointDomain: "probation.endpoint.com",
			success:        true,
		},
		{
			name:           "failed probation traffic",
			serviceID:      "solana",
			endpointDomain: "probation.endpoint.com",
			success:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotPanics(t, func() {
				RecordProbationTraffic(tt.serviceID, tt.endpointDomain, tt.success)
			})
		})
	}
}

func TestRecordTierDistribution(t *testing.T) {
	tests := []struct {
		name       string
		serviceID  string
		tier1Count int
		tier2Count int
		tier3Count int
	}{
		{
			name:       "balanced distribution",
			serviceID:  "eth",
			tier1Count: 5,
			tier2Count: 3,
			tier3Count: 2,
		},
		{
			name:       "all in tier 1",
			serviceID:  "solana",
			tier1Count: 10,
			tier2Count: 0,
			tier3Count: 0,
		},
		{
			name:       "no endpoints",
			serviceID:  "polygon",
			tier1Count: 0,
			tier2Count: 0,
			tier3Count: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotPanics(t, func() {
				RecordTierDistribution(tt.serviceID, tt.tier1Count, tt.tier2Count, tt.tier3Count)
			})
		})
	}
}

func TestRecordTierSelection(t *testing.T) {
	tests := []struct {
		name      string
		serviceID string
		tier      int
	}{
		{
			name:      "tier 1 selected",
			serviceID: "eth",
			tier:      1,
		},
		{
			name:      "tier 2 selected",
			serviceID: "solana",
			tier:      2,
		},
		{
			name:      "tier 3 selected",
			serviceID: "polygon",
			tier:      3,
		},
		{
			name:      "no tier available",
			serviceID: "cosmos",
			tier:      0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotPanics(t, func() {
				RecordTierSelection(tt.serviceID, tt.tier)
			})
		})
	}
}

func TestTierToString(t *testing.T) {
	tests := []struct {
		tier     int
		expected string
	}{
		{tier: 0, expected: "0"},
		{tier: 1, expected: "1"},
		{tier: 2, expected: "2"},
		{tier: 3, expected: "3"},
		{tier: 4, expected: "unknown"},
		{tier: 99, expected: "unknown"},
		{tier: -1, expected: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := tierToString(tt.tier)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestEndpointTypeConstants(t *testing.T) {
	// Verify endpoint type constants are defined correctly
	require.Equal(t, "jsonrpc", EndpointTypeJSONRPC)
	require.Equal(t, "rest", EndpointTypeREST)
	require.Equal(t, "websocket", EndpointTypeWebSocket)
	require.Equal(t, "grpc", EndpointTypeGRPC)
	require.Equal(t, "unknown", EndpointTypeUnknown)
}

func TestProbationTransitionConstants(t *testing.T) {
	// Verify probation transition constants are defined correctly
	require.Equal(t, "entered", ProbationTransitionEntered)
	require.Equal(t, "exited", ProbationTransitionExited)
	require.Equal(t, "recovered", ProbationTransitionRecovered)
	require.Equal(t, "demoted", ProbationTransitionDemoted)
}
