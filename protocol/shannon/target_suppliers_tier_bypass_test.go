package shannon

import (
	"context"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
	reputationstorage "github.com/pokt-network/path/reputation/storage"
)

// TestFilterToHighestTier_EmptyWhenAllBelowThreshold pins the root cause behind the
// Target-Suppliers bypass: when no endpoint reaches any tier, filterToHighestTier returns an
// EMPTY map rather than falling back to the input set.
//
// This is correct for normal selection — serving traffic to endpoints reputation has
// disqualified is worse than failing — but it is why a Target-Suppliers pin could not reach a
// cooled-down supplier: the pin bypasses the threshold/cooldown filter, then this ran anyway
// and emptied the pool. Real case (mantle/nodefleet, 2026-07-31): 40 of 50 endpoints at score 0,
// and `Target-Suppliers: <any of them>` returned "no valid endpoints available for service".
func TestFilterToHighestTier_EmptyWhenAllBelowThreshold(t *testing.T) {
	ctx := context.Background()
	logger := polyzero.NewLogger()
	serviceID := protocol.ServiceID("mantle")
	rpcType := sharedtypes.RPCType_JSON_RPC

	repConfig := reputation.Config{
		Enabled:        true,
		InitialScore:   80,
		MinThreshold:   20,
		KeyGranularity: "per-endpoint",
	}
	repSvc := reputation.NewService(repConfig, reputationstorage.NewMemoryStorage(10*time.Minute))

	// Probation disabled so the normal (non-probation) tier path is exercised.
	tieredConfig := reputation.TieredSelectionConfig{
		Enabled:        true,
		Tier1Threshold: 80,
		Tier2Threshold: 50,
		Probation:      reputation.ProbationConfig{Enabled: false},
	}
	p := &Protocol{
		reputationService: repSvc,
		tieredSelector:    reputation.NewTieredSelectorWithLogger(logger, tieredConfig, repConfig.MinThreshold),
	}

	addrs := []protocol.EndpointAddr{
		"pokt1a-https://dopokt1.relayminer.example.net",
		"pokt1b-https://nr.relayminer.example.net",
	}
	endpoints := make(map[protocol.EndpointAddr]endpoint, len(addrs))
	for _, addr := range addrs {
		endpoints[addr] = &mockEndpoint{addr: addr}
	}

	// Drive every endpoint below MinThreshold using major errors, which do not trip the
	// critical-strike cooldown — this isolates the threshold path from the cooldown path.
	keyBuilder := repSvc.KeyBuilderForService(serviceID)
	for _, addr := range addrs {
		key := keyBuilder.BuildKey(serviceID, addr, rpcType)
		for range 8 {
			_ = repSvc.RecordSignal(ctx, key, reputation.Signal{
				Type:   reputation.SignalTypeMajorError,
				Reason: "health_check_critical_error",
			})
		}
		score, err := repSvc.GetScore(ctx, key)
		require.NoError(t, err)
		require.Less(t, score.Value, repConfig.MinThreshold,
			"endpoint %s must be below MinThreshold for this test to mean anything (got %.1f)", addr, score.Value)
	}

	result := p.filterToHighestTier(ctx, serviceID, endpoints, rpcType, logger, "")
	require.Empty(t, result, "filterToHighestTier must return an empty map when no endpoint reaches a tier")

	// The requestedEndpointAddr escape hatch also requires score >= MinThreshold, so it does
	// NOT rescue a score-0 endpoint either. Skipping the whole stage is the only way through.
	result = p.filterToHighestTier(ctx, serviceID, endpoints, rpcType, logger, addrs[0])
	require.Empty(t, result, "requestedEndpointAddr must not rescue an endpoint below MinThreshold")
}

// TestShouldApplyTieredSelection covers the gate that decides whether the behavior pinned above
// runs at all. The Target-Suppliers case is the fix: a supplier pin must bypass tiered selection
// for the same reason it already bypasses the threshold/cooldown filter.
func TestShouldApplyTieredSelection(t *testing.T) {
	logger := polyzero.NewLogger()
	enabledSelector := reputation.NewTieredSelectorWithLogger(
		logger,
		reputation.TieredSelectionConfig{Enabled: true, Tier1Threshold: 80, Tier2Threshold: 50},
		20,
	)
	disabledSelector := reputation.NewTieredSelectorWithLogger(
		logger,
		reputation.TieredSelectionConfig{Enabled: false},
		20,
	)

	tests := []struct {
		name               string
		selector           *reputation.TieredSelector
		filterByReputation bool
		allowedSuppliers   []string
		rpcType            sharedtypes.RPCType
		want               bool
	}{
		{
			name:               "applies for a normal JSON-RPC request",
			selector:           enabledSelector,
			filterByReputation: true,
			rpcType:            sharedtypes.RPCType_JSON_RPC,
			want:               true,
		},
		{
			name:               "skipped when a Target-Suppliers pin is active",
			selector:           enabledSelector,
			filterByReputation: true,
			allowedSuppliers:   []string{"pokt1abc"},
			rpcType:            sharedtypes.RPCType_JSON_RPC,
			want:               false,
		},
		{
			name:               "skipped for health checks / leaderboard gathering",
			selector:           enabledSelector,
			filterByReputation: false,
			rpcType:            sharedtypes.RPCType_JSON_RPC,
			want:               false,
		},
		{
			name:               "skipped for WebSocket (S1)",
			selector:           enabledSelector,
			filterByReputation: true,
			rpcType:            sharedtypes.RPCType_WEBSOCKET,
			want:               false,
		},
		{
			name:               "skipped when tiered selection is disabled by config",
			selector:           disabledSelector,
			filterByReputation: true,
			rpcType:            sharedtypes.RPCType_JSON_RPC,
			want:               false,
		},
		{
			name:               "skipped when no selector is configured",
			selector:           nil,
			filterByReputation: true,
			rpcType:            sharedtypes.RPCType_JSON_RPC,
			want:               false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Protocol{tieredSelector: tt.selector}
			got := p.shouldApplyTieredSelection(tt.filterByReputation, tt.allowedSuppliers, tt.rpcType)
			require.Equal(t, tt.want, got)
		})
	}
}
