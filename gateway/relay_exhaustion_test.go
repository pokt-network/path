package gateway

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/qos/heuristic"
)

// Test_relayExhaustionCategory verifies the classification feeding relay_exhausted_total,
// especially that backend-reported historical/pruned state is isolated as "capability".
func Test_relayExhaustionCategory(t *testing.T) {
	hr := func(pattern string) *heuristic.AnalysisResult {
		return &heuristic.AnalysisResult{MatchedPattern: pattern}
	}

	// MatchedPattern holds the short errorPatterns key that matched (e.g. "historical state"),
	// NOT the full backend message — that is what IsCapabilityLimitationError switches on.
	tests := []struct {
		name string
		hr   *heuristic.AnalysisResult
		err  error
		want string
	}{
		{"capability: historical state", hr("historical state"), nil, "capability"},
		{"capability: pruned state", hr("is pruned"), nil, "capability"},
		{"capability: state not available", hr("state not available"), nil, "capability"},
		{"capability: missing trie node", hr("missing trie node"), nil, "capability"},
		{"heuristic: non-capability pattern", hr("execution reverted"), nil, "heuristic"},
		{"transport: error, no heuristic", nil, errors.New("dial tcp: connection refused"), "transport"},
		{"status: no heuristic, no error", nil, nil, "status"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, relayExhaustionCategory(tc.hr, tc.err))
		})
	}
}
