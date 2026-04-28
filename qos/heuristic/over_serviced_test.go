package heuristic

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsOverServicedError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{
			name: "poktroll main exact phrase",
			in:   "offchain rate limit hit by relayer proxy",
			want: true,
		},
		{
			name: "poktroll main wrapped error",
			in:   "raw_payload: offchain rate limit hit by relayer proxy: foo",
			want: true,
		},
		{
			name: "HA exact phrase",
			in:   `{"error":"session relay limit reached: claimable portion fully consumed"}`,
			want: true,
		},
		{
			name: "HA partial — session limit only",
			in:   "session relay limit reached",
			want: true,
		},
		{
			name: "HA partial — claimable only",
			in:   "claimable portion fully consumed",
			want: true,
		},
		{
			name: "case-insensitive",
			in:   "OFFCHAIN RATE LIMIT HIT BY RELAYER PROXY",
			want: true,
		},
		{
			name: "generic rate limit (NOT over-serviced)",
			in:   "rate limit exceeded",
			want: false,
		},
		{
			name: "backend 429",
			in:   "too many requests",
			want: false,
		},
		{
			name: "unrelated error",
			in:   "connection refused",
			want: false,
		},
		{
			name: "empty",
			in:   "",
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, IsOverServicedError(c.in))
		})
	}
}

func TestIsOverServicedRelayMinerError(t *testing.T) {
	cases := []struct {
		name      string
		codespace string
		code      uint32
		want      bool
	}{
		{"relayer_proxy code 7 (rate-limited)", "relayer_proxy", 7, true},
		{"relayer_proxy other code", "relayer_proxy", 1, false},
		{"different codespace, code 7", "service", 7, false},
		{"empty codespace", "", 7, false},
		{"empty + 0", "", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, IsOverServicedRelayMinerError(c.codespace, c.code))
		})
	}
}

func TestIndicatorAnalysis_OverServicedPatternsWinOverGenericRateLimit(t *testing.T) {
	// "session relay limit reached" must beat the generic rate-limit patterns
	// (none of which actually contain that substring) so MatchedPattern lets
	// downstream detect over-servicing specifically.
	body := []byte(`{"error":"session relay limit reached: claimable portion fully consumed"}`)
	res := IndicatorAnalysis(body, false)
	require.True(t, res.Found)
	// Either of the over-serviced patterns is acceptable as the winning match.
	require.True(t,
		res.Pattern == "session relay limit reached" || res.Pattern == "claimable portion fully consumed",
		"expected an over-serviced pattern to win, got %q", res.Pattern,
	)
	require.True(t, IsOverServicedError(res.Pattern))
}

func TestIndicatorAnalysis_PoktrollMainOverServicedWinsOverRateLimit(t *testing.T) {
	body := []byte("raw_payload: offchain rate limit hit by relayer proxy: foo")
	res := IndicatorAnalysis(body, false)
	require.True(t, res.Found)
	require.Equal(t, "offchain rate limit hit by relayer proxy", res.Pattern)
	require.True(t, IsOverServicedError(res.Pattern))
}
