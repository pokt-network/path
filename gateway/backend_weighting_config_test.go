package gateway

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/selector"
)

// The selector applies a concentration cap for services whose resolved configuration has not
// been published to it (tests, and any binary that skips the QoS bootstrap). Its fallback
// mirrors the config-layer default by hand, so a change to one that is not mirrored in the
// other would silently give those paths a different cap than the fleet runs under.
func TestDefaultMaxOperatorShare_MatchesSelectorFallback(t *testing.T) {
	require.Equal(t, DefaultMaxOperatorShare, selector.DefaultMaxOperatorShareFallback,
		"gateway.DefaultMaxOperatorShare and selector.DefaultMaxOperatorShareFallback must agree")
}

func TestGetBackendRegistrationWeightCapForService(t *testing.T) {
	i := func(v int) *int { return &v }

	tests := []struct {
		name    string
		cfg     *UnifiedServicesConfig
		service protocol.ServiceID
		want    int
	}{
		{
			// 0 means "unset", which is what lets the process-wide value (and therefore the
			// env-var lever) apply. Resolving a default here would disable that lever.
			name:    "unset anywhere → 0, meaning use the process-wide K",
			cfg:     &UnifiedServicesConfig{Services: []ServiceConfig{{ID: "svc-a"}}},
			service: "svc-a",
			want:    0,
		},
		{
			name:    "unknown service → 0",
			cfg:     &UnifiedServicesConfig{},
			service: "missing",
			want:    0,
		},
		{
			name: "defaults set, no per-service → defaults",
			cfg: &UnifiedServicesConfig{
				Defaults: ServiceDefaults{BackendRegistrationWeightCap: i(3)},
				Services: []ServiceConfig{{ID: "svc-a"}},
			},
			service: "svc-a",
			want:    3,
		},
		{
			name: "per-service overrides defaults",
			cfg: &UnifiedServicesConfig{
				Defaults: ServiceDefaults{BackendRegistrationWeightCap: i(3)},
				Services: []ServiceConfig{{ID: "svc-a", BackendRegistrationWeightCap: i(1)}},
			},
			service: "svc-a",
			want:    1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.cfg.GetBackendRegistrationWeightCapForService(tc.service))
		})
	}
}

func TestGetDefaultMaxOperatorShare(t *testing.T) {
	f := func(v float64) *float64 { return &v }

	require.Equal(t, DefaultMaxOperatorShare, (&UnifiedServicesConfig{}).GetDefaultMaxOperatorShare())
	require.Equal(t, 0.7, (&UnifiedServicesConfig{
		Defaults: ServiceDefaults{MaxOperatorShare: f(0.7)},
	}).GetDefaultMaxOperatorShare())
}
