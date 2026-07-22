package gateway

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Test_GetWebsocketRebindOperatorUniformForService verifies the resolution order:
// per-service > global default > DefaultWebsocketRebindOperatorUniform (ON).
func Test_GetWebsocketRebindOperatorUniformForService(t *testing.T) {
	c := require.New(t)
	on, off := true, false

	cfg := &UnifiedServicesConfig{
		Services: []ServiceConfig{
			{ID: "explicit-off", WebsocketRebindOperatorUniform: &off},
			{ID: "explicit-on", WebsocketRebindOperatorUniform: &on},
			{ID: "inherits"},
		},
	}

	c.True(cfg.GetWebsocketRebindOperatorUniformForService("inherits"), "default is ON")
	c.True(cfg.GetWebsocketRebindOperatorUniformForService("unknown"), "unknown service → default ON")
	c.False(cfg.GetWebsocketRebindOperatorUniformForService("explicit-off"))
	c.True(cfg.GetWebsocketRebindOperatorUniformForService("explicit-on"))

	// A global default flips the inheriting services, but a per-service value still wins.
	cfg.Defaults.WebsocketRebindOperatorUniform = &off
	c.False(cfg.GetWebsocketRebindOperatorUniformForService("inherits"), "global default overrides to OFF")
	c.True(cfg.GetWebsocketRebindOperatorUniformForService("explicit-on"), "per-service still wins over the default")
}
