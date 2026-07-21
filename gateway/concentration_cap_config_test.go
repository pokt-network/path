package gateway

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

func TestGetMaxOperatorShareForService(t *testing.T) {
	f := func(v float64) *float64 { return &v }

	tests := []struct {
		name    string
		cfg     *UnifiedServicesConfig
		service protocol.ServiceID
		want    float64
	}{
		{
			name:    "unset anywhere → global default",
			cfg:     &UnifiedServicesConfig{Services: []ServiceConfig{{ID: "eth"}}},
			service: "eth",
			want:    DefaultMaxOperatorShare,
		},
		{
			name:    "unknown service → global default",
			cfg:     &UnifiedServicesConfig{},
			service: "missing",
			want:    DefaultMaxOperatorShare,
		},
		{
			name: "defaults set, no per-service → defaults",
			cfg: &UnifiedServicesConfig{
				Defaults: ServiceDefaults{MaxOperatorTrafficShare: f(0.6)},
				Services: []ServiceConfig{{ID: "eth"}},
			},
			service: "eth",
			want:    0.6,
		},
		{
			name: "per-service overrides defaults",
			cfg: &UnifiedServicesConfig{
				Defaults: ServiceDefaults{MaxOperatorTrafficShare: f(0.6)},
				Services: []ServiceConfig{{ID: "eth", MaxOperatorTrafficShare: f(0.5)}},
			},
			service: "eth",
			want:    0.5,
		},
		{
			name: "per-service disable (1.0) is honored",
			cfg: &UnifiedServicesConfig{
				Services: []ServiceConfig{{ID: "eth", MaxOperatorTrafficShare: f(1.0)}},
			},
			service: "eth",
			want:    1.0,
		},
		{
			name: "per-service disable (0) is honored",
			cfg: &UnifiedServicesConfig{
				Services: []ServiceConfig{{ID: "eth", MaxOperatorTrafficShare: f(0)}},
			},
			service: "eth",
			want:    0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.cfg.GetMaxOperatorShareForService(tc.service))
		})
	}
}

// TestDefaultMaxOperatorShare_ShippedOn documents that the cap is ON by default and set to
// a sane in-range value (a change here is a deliberate rollout decision).
func TestDefaultMaxOperatorShare_ShippedOn(t *testing.T) {
	require.Greater(t, DefaultMaxOperatorShare, 0.0, "default must be enabled (> 0)")
	require.Less(t, DefaultMaxOperatorShare, 1.0, "default must be an actual cap (< 1)")
}
