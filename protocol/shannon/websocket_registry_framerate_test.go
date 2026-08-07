package shannon

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	pathmetrics "github.com/pokt-network/path/metrics"
)

// observedCount returns how many per-connection rate observations have been recorded for a
// domain/service pair.
func observedCount(t *testing.T, domain, serviceID string) uint64 {
	t.Helper()
	obs, err := pathmetrics.WebsocketConnectionFrameRate.GetMetricWithLabelValues(domain, serviceID)
	require.NoError(t, err)
	m, ok := obs.(prometheus.Metric)
	require.True(t, ok, "histogram child must expose the Metric interface")
	var pb dto.Metric
	require.NoError(t, m.Write(&pb))
	return pb.GetHistogram().GetSampleCount()
}

// A per-domain SUM cannot tell one firehose apart from many ordinary subscribers, which is
// the entire reason this histogram exists. That only works if EVERY live connection is
// observed on every pass — including the silent ones. Dropping the zeros would leave the
// quantiles describing only the connections that carry traffic, i.e. exactly the population
// the metric is supposed to be measured against.
func Test_sampleRates_ObservesEveryConnectionIncludingIdleOnes(t *testing.T) {
	c := require.New(t)
	pathmetrics.WebsocketConnectionFrameRate.Reset()

	r := newWebsocketConnRegistry()

	// One firehose and two silent connections on the same operator — the shape measured on
	// live gnosis, where 2 of ~70 connections carried ~97% of the frames.
	var busyFrames uint64
	r.register("gnosis", &websocketRequestContext{}, &fakeController{},
		"loud.example", "supplier-loud", func() uint64 { return busyFrames })
	r.register("gnosis", &websocketRequestContext{}, &fakeController{},
		"loud.example", "supplier-loud", func() uint64 { return 0 })
	r.register("gnosis", &websocketRequestContext{}, &fakeController{},
		"quiet.example", "supplier-quiet", func() uint64 { return 0 })

	busyFrames = 7500 // 500 frames/s across one 15s interval

	base := time.Now()
	r.sampleRates(base.Add(websocketRateSampleInterval))

	c.Equal(uint64(2), observedCount(t, "loud.example", "gnosis"),
		"both connections on the operator must be observed, not just the busy one")
	c.Equal(uint64(1), observedCount(t, "quiet.example", "gnosis"),
		"a silent connection still contributes an observation at zero")

	// A second pass must add one observation per live connection, so the histogram tracks
	// the population over time rather than only the moment a connection appeared.
	r.sampleRates(base.Add(2 * websocketRateSampleInterval))
	c.Equal(uint64(4), observedCount(t, "loud.example", "gnosis"))
	c.Equal(uint64(2), observedCount(t, "quiet.example", "gnosis"))
}

// The histogram must agree with the ranking a capped tumble is spent on: both read the same
// entry.rate from the same pass. If they could diverge, a dashboard would justify a tumble
// that the tumble itself would then decline to make.
func Test_sampleRates_HistogramAgreesWithTumbleRanking(t *testing.T) {
	c := require.New(t)
	pathmetrics.WebsocketConnectionFrameRate.Reset()

	r := newWebsocketConnRegistry()
	var frames uint64
	r.register("gnosis", &websocketRequestContext{}, &fakeController{},
		"loud.example", "supplier-loud", func() uint64 { return frames })

	frames = 7500
	now := time.Now().Add(websocketRateSampleInterval)
	r.sampleRates(now)

	r.mu.Lock()
	var sampled float64
	for _, entry := range r.conns["gnosis"] {
		sampled = entry.rate
	}
	r.mu.Unlock()

	c.Greater(sampled, 0.0, "the sampled rate feeds both the metric and the tumble ranking")
	c.Equal(uint64(1), observedCount(t, "loud.example", "gnosis"))
}

// Deregistered connections must stop contributing: a histogram that kept observing closed
// sockets at zero would drag the distribution down over time and make a busy operator look
// progressively idler the longer the pod ran.
func Test_sampleRates_StopsObservingAfterDeregister(t *testing.T) {
	c := require.New(t)
	pathmetrics.WebsocketConnectionFrameRate.Reset()

	r := newWebsocketConnRegistry()
	wrc := &websocketRequestContext{}
	r.register("gnosis", wrc, &fakeController{}, "loud.example", "supplier-loud", func() uint64 { return 0 })

	base := time.Now()
	r.sampleRates(base.Add(websocketRateSampleInterval))
	c.Equal(uint64(1), observedCount(t, "loud.example", "gnosis"))

	r.deregister("gnosis", wrc)
	r.sampleRates(base.Add(2 * websocketRateSampleInterval))
	c.Equal(uint64(1), observedCount(t, "loud.example", "gnosis"),
		"a closed connection must not keep contributing observations")
}
