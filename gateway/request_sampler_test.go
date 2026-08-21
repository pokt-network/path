package gateway

import (
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/metrics"
	"github.com/pokt-network/path/protocol"
)

func newTestSampler(rate uint64, window time.Duration, maxFPs int) (*RequestSampler, *time.Time) {
	s := NewRequestSampler(polyzero.NewLogger(), rate, window, maxFPs)
	now := time.Date(2026, 8, 21, 20, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return s, &now
}

// The fingerprint is the logical request: method + params. The JSON-RPC id and formatting
// must not split one request into many, or repeated traffic with rotating ids reads as
// diverse — which is exactly the traffic shape the sampler exists to expose.
func TestRequestSampler_FingerprintIgnoresIDAndFormatting(t *testing.T) {
	s, _ := newTestSampler(1, time.Hour, 100)
	svc := protocol.ServiceID("solana")
	bodies := []string{
		`{"jsonrpc":"2.0","id":1,"method":"getAccountInfo","params":["Vote111111111111111111111111111111111111111"]}`,
		`{"jsonrpc":"2.0","id":2,"method":"getAccountInfo","params":["Vote111111111111111111111111111111111111111"]}`,
		`{"jsonrpc": "2.0", "id": "abc", "method": "getAccountInfo", "params": [ "Vote111111111111111111111111111111111111111" ]}`,
		`{"id":9,"method":"getAccountInfo","params":["Vote111111111111111111111111111111111111111"],"jsonrpc":"2.0"}`,
	}
	for _, b := range bodies {
		s.Observe(svc, "POST", "/v1", []byte(b))
	}
	// One genuinely different request.
	s.Observe(svc, "POST", "/v1", []byte(`{"jsonrpc":"2.0","id":1,"method":"getAccountInfo","params":["11111111111111111111111111111111"]}`))

	r, ok := s.Report("solana", false, 10)
	require.True(t, ok)
	require.Equal(t, uint64(5), r.RequestsSeen)
	require.Equal(t, uint64(5), r.Sampled)
	require.Equal(t, uint64(2), r.Distinct, "four id/formatting variants of one request plus one other request = 2 fingerprints")
	require.InDelta(t, 0.4, r.Uniqueness, 1e-9)
	require.InDelta(t, 0.8, r.Top1Share, 1e-9)
	require.Equal(t, "getAccountInfo", r.Top[0].Method)
	require.Equal(t, uint64(4), r.Top[0].Count)
	require.Len(t, r.Methods, 1)
	require.Equal(t, uint64(2), r.Methods[0].Distinct)
}

// A batch contributes one fingerprint per item, so a client that packs the same call into
// batches is measured on its calls, not on its envelopes.
func TestRequestSampler_BatchItemsAreSeparateFingerprints(t *testing.T) {
	s, _ := newTestSampler(1, time.Hour, 100)
	svc := protocol.ServiceID("eth")
	s.Observe(svc, "POST", "/v1", []byte(`[{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]},{"jsonrpc":"2.0","id":2,"method":"eth_blockNumber","params":[]},{"jsonrpc":"2.0","id":3,"method":"eth_getBalance","params":["0xabc","latest"]}]`))

	r, ok := s.Report("eth", false, 10)
	require.True(t, ok)
	require.Equal(t, uint64(1), r.RequestsSeen)
	require.Equal(t, uint64(3), r.Sampled)
	require.Equal(t, uint64(2), r.Distinct)
	require.Equal(t, "eth_blockNumber", r.Top[0].Method)
	require.Equal(t, uint64(2), r.Top[0].Count)
}

// Only one request in `rate` is fingerprinted, but every request is counted as seen — the
// report must make the sampling visible rather than present a 1-in-100 sample as the total.
func TestRequestSampler_SamplesOneInN(t *testing.T) {
	s, _ := newTestSampler(10, time.Hour, 100)
	svc := protocol.ServiceID("poly")
	for i := 0; i < 100; i++ {
		s.Observe(svc, "POST", "/v1", []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}`))
	}
	r, ok := s.Report("poly", false, 10)
	require.True(t, ok)
	require.Equal(t, uint64(100), r.RequestsSeen)
	require.Equal(t, uint64(10), r.Sampled)
	require.Equal(t, "1-in-10", r.SampleRate)
}

// The table is bounded. Past the cap, new fingerprints are counted (so uniqueness stays
// honest) but not stored, and the report says how many were dropped.
func TestRequestSampler_TableIsBoundedAndOverflowIsCountedNotStored(t *testing.T) {
	s, _ := newTestSampler(1, time.Hour, 3)
	svc := protocol.ServiceID("solana")
	for _, acct := range []string{"a", "b", "c", "d", "e"} {
		s.Observe(svc, "POST", "/v1", []byte(`{"jsonrpc":"2.0","id":1,"method":"getAccountInfo","params":["`+acct+`"]}`))
	}
	r, ok := s.Report("solana", false, 10)
	require.True(t, ok)
	require.Equal(t, uint64(5), r.Sampled)
	require.Equal(t, uint64(5), r.Distinct, "overflowed fingerprints still count as distinct")
	require.Equal(t, uint64(2), r.TableOverflow)
	require.Len(t, r.Top, 3, "only the stored fingerprints are listed")
	require.InDelta(t, 1.0, r.Uniqueness, 1e-9)
}

// Windows rotate on the clock; the completed window is kept as "previous" and is what the
// gauges describe, so a reader always has a full window and never a ratio over three samples.
func TestRequestSampler_WindowRotationKeepsPreviousAndPublishes(t *testing.T) {
	s, now := newTestSampler(1, 10*time.Minute, 100)
	svc := protocol.ServiceID("solana")
	same := []byte(`{"jsonrpc":"2.0","id":1,"method":"getSlot","params":[]}`)
	for i := 0; i < 4; i++ {
		s.Observe(svc, "POST", "/v1", same)
	}
	_, ok := s.Report("solana", true, 10)
	require.False(t, ok, "no completed window yet")

	*now = now.Add(11 * time.Minute)
	s.Observe(svc, "POST", "/v1", []byte(`{"jsonrpc":"2.0","id":1,"method":"getSlot","params":[{"commitment":"finalized"}]}`))

	prev, ok := s.Report("solana", true, 10)
	require.True(t, ok)
	require.Equal(t, "previous", prev.Window)
	require.Equal(t, uint64(4), prev.Sampled)
	require.Equal(t, uint64(1), prev.Distinct)
	require.False(t, prev.WindowEnd.IsZero())

	cur, ok := s.Report("solana", false, 10)
	require.True(t, ok)
	require.Equal(t, "current", cur.Window)
	require.Equal(t, uint64(1), cur.Sampled)

	// The gauges describe the completed window: 1 distinct / 4 sampled.
	require.InDelta(t, 0.25, gaugeValue(t, "path_request_sample_uniqueness", "solana"), 1e-9)
	require.InDelta(t, 1.0, gaugeValue(t, "path_request_sample_top1_share", "solana"), 1e-9)

	sum := s.Summary()
	require.Len(t, sum, 1)
	require.Equal(t, "previous", sum[0].Window)
	require.Equal(t, "getSlot", sum[0].TopMethod)
}

// A nil sampler (sampling disabled) is a no-op everywhere the gateway and router touch it.
func TestRequestSampler_NilIsNoOp(t *testing.T) {
	var s *RequestSampler
	s.Observe("solana", "POST", "/v1", []byte(`{}`))
	_, ok := s.Report("solana", false, 10)
	require.False(t, ok)
	require.Nil(t, s.Summary())
}

// Non-JSON-RPC traffic (REST, CometBFT GET paths) is fingerprinted on method + path + body.
func TestRequestSampler_RESTFingerprint(t *testing.T) {
	s, _ := newTestSampler(1, time.Hour, 100)
	svc := protocol.ServiceID("xrplevm")
	s.Observe(svc, "GET", "/v1/cosmos/base/tendermint/v1beta1/blocks/latest", nil)
	s.Observe(svc, "GET", "/v1/cosmos/base/tendermint/v1beta1/blocks/latest", nil)
	s.Observe(svc, "GET", "/v1/status", nil)
	r, ok := s.Report("xrplevm", false, 10)
	require.True(t, ok)
	require.Equal(t, uint64(3), r.Sampled)
	require.Equal(t, uint64(2), r.Distinct)
	require.Equal(t, "GET /v1/cosmos/base/tendermint/v1beta1/blocks/latest", r.Top[0].Method)
}

// gaugeValue reads a per-service gauge from the production metric vec.
func gaugeValue(t *testing.T, name, serviceID string) float64 {
	t.Helper()
	var g prometheus.Gauge
	switch name {
	case "path_request_sample_uniqueness":
		g = metrics.RequestSampleUniqueness.WithLabelValues(serviceID)
	case "path_request_sample_top1_share":
		g = metrics.RequestSampleTop1Share.WithLabelValues(serviceID)
	default:
		t.Fatalf("unknown gauge %s", name)
	}
	var m dto.Metric
	require.NoError(t, g.Write(&m))
	return m.GetGauge().GetValue()
}
