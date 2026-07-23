package gateway

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func staticTestConfig() *UnifiedServicesConfig {
	return &UnifiedServicesConfig{
		Defaults: ServiceDefaults{
			StaticRoutes: []StaticRoute{
				{Path: "/example", Body: "global-body"},
			},
		},
		Services: []ServiceConfig{
			{
				ID: "solana",
				StaticRoutes: []StaticRoute{
					{Path: "/example", Body: "solana-body"},
				},
			},
			{
				ID: "eth",
				StaticRoutes: []StaticRoute{
					{
						Path:        "/info",
						Methods:     []string{"POST"},
						StatusCode:  201,
						ContentType: "application/json",
						Body:        `{"ok":true}`,
						Headers:     map[string]string{"X-Custom": "1"},
					},
				},
			},
		},
	}
}

// Test_ResolveStaticResponse_PerServiceOverridesDefault verifies a per-service route wins
// over a global default on the same path, while a service without its own route falls back
// to the default.
func Test_ResolveStaticResponse_PerServiceOverridesDefault(t *testing.T) {
	c := require.New(t)
	cfg := staticTestConfig()

	body, status, headers, ok := cfg.ResolveStaticResponse("solana", "/example", "GET")
	c.True(ok)
	c.Equal("solana-body", string(body), "per-service route overrides the global default")
	c.Equal(200, status, "status defaults to 200")
	c.Equal("text/plain; charset=utf-8", headers["Content-Type"], "content-type defaults to text/plain")

	body, _, _, ok = cfg.ResolveStaticResponse("eth", "/example", "GET")
	c.True(ok)
	c.Equal("global-body", string(body), "service without its own /example falls back to the default")

	body, _, _, ok = cfg.ResolveStaticResponse("unknown-service", "/example", "GET")
	c.True(ok)
	c.Equal("global-body", string(body), "unknown service still gets the global default")
}

// Test_ResolveStaticResponse_NoMatch verifies a path with no configured route resolves to
// ok=false so the caller relays normally.
func Test_ResolveStaticResponse_NoMatch(t *testing.T) {
	c := require.New(t)
	cfg := staticTestConfig()

	_, _, _, ok := cfg.ResolveStaticResponse("solana", "/not-configured", "GET")
	c.False(ok)
}

// Test_ResolveStaticResponse_MethodFilter verifies a route with a Methods set matches only
// the listed methods, and that all response fields are honored.
func Test_ResolveStaticResponse_MethodFilter(t *testing.T) {
	c := require.New(t)
	cfg := staticTestConfig()

	_, _, _, ok := cfg.ResolveStaticResponse("eth", "/info", "GET")
	c.False(ok, "GET must not match a POST-only route")

	body, status, headers, ok := cfg.ResolveStaticResponse("eth", "/info", "post")
	c.True(ok, "method match is case-insensitive")
	c.Equal(`{"ok":true}`, string(body))
	c.Equal(201, status)
	c.Equal("application/json", headers["Content-Type"])
	c.Equal("1", headers["X-Custom"])
}

// Test_ResolveStaticResponse_HeadersCopied verifies the returned headers map is a fresh copy
// so a caller writing onto it cannot mutate the shared config.
func Test_ResolveStaticResponse_HeadersCopied(t *testing.T) {
	c := require.New(t)
	cfg := staticTestConfig()

	_, _, headers, ok := cfg.ResolveStaticResponse("eth", "/info", "POST")
	c.True(ok)
	headers["X-Custom"] = "mutated"

	_, _, headers2, _ := cfg.ResolveStaticResponse("eth", "/info", "POST")
	c.Equal("1", headers2["X-Custom"], "config must be unaffected by a caller mutating returned headers")
}

func Test_validateStaticRoutes(t *testing.T) {
	tests := []struct {
		name    string
		routes  []StaticRoute
		wantErr bool
	}{
		{
			name:   "valid single route",
			routes: []StaticRoute{{Path: "/example", Body: "x"}},
		},
		{
			name:   "valid same path, disjoint methods",
			routes: []StaticRoute{{Path: "/p", Methods: []string{"GET"}}, {Path: "/p", Methods: []string{"POST"}}},
		},
		{
			name:    "empty path",
			routes:  []StaticRoute{{Path: "", Body: "x"}},
			wantErr: true,
		},
		{
			name:    "path without leading slash",
			routes:  []StaticRoute{{Path: "example", Body: "x"}},
			wantErr: true,
		},
		{
			name:    "relay-root path is rejected",
			routes:  []StaticRoute{{Path: "/", Body: "x"}},
			wantErr: true,
		},
		{
			name:    "status code out of range",
			routes:  []StaticRoute{{Path: "/p", StatusCode: 99}},
			wantErr: true,
		},
		{
			name:    "duplicate all-methods path",
			routes:  []StaticRoute{{Path: "/p"}, {Path: "/p"}},
			wantErr: true,
		},
		{
			name:    "specific method conflicts with all-methods on same path",
			routes:  []StaticRoute{{Path: "/p"}, {Path: "/p", Methods: []string{"GET"}}},
			wantErr: true,
		},
		{
			name:    "duplicate path+method",
			routes:  []StaticRoute{{Path: "/p", Methods: []string{"GET"}}, {Path: "/p", Methods: []string{"get"}}},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateStaticRoutes(tc.routes)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
