package shannon

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/metrics"
)

// wsEndpoint builds an endpoint whose PublicURL resolves to the given host, so a test can
// assert which OPERATOR a connection's traffic is attributed to.
func wsEndpoint(supplier, host string, fallback bool) *rcEndpoint {
	return newRCEndpoint(supplier+"-https://"+host, fallback)
}

// A connection's metrics must follow it across rebinds. Before this, every per-domain
// websocket metric used the setup-time endpoint, so an operator a connection had long since
// left kept being credited with its frames — and the operator actually serving looked idle.
func Test_currentDomain_FollowsTheLiveBinding(t *testing.T) {
	c := require.New(t)

	wrc := &websocketRequestContext{
		logger:           testLogger(),
		serviceID:        "bsc",
		selectedEndpoint: wsEndpoint("pokt1a", "n1.first.tech", false),
	}
	c.Equal("first.tech", wrc.currentDomain(), "before any rebind, the setup endpoint is the live one")

	wrc.reconnectEndpoint = wsEndpoint("pokt1b", "n2.second.xyz", false)
	c.Equal("second.xyz", wrc.currentDomain(), "after a rebind, metrics must follow the new operator")

	wrc.reconnectEndpoint = wsEndpoint("pokt1c", "n3.third.net", false)
	c.Equal("third.net", wrc.currentDomain(), "and keep following across repeated rebinds")
}

// The fallback branch decides whether frames get signed. Testing the setup-time endpoint means
// a connection that rebinds across the fallback boundary either sends unsigned frames to a
// protocol endpoint or signs frames for a fallback that expects raw ones.
func Test_signingEndpoint_FallbackStatusFollowsTheLiveBinding(t *testing.T) {
	c := require.New(t)

	// Started on a fallback, rebound onto a protocol endpoint: frames must now be signed.
	wrc := &websocketRequestContext{
		selectedEndpoint: wsEndpoint("pokt1fb", "fb.fallback.tech", true),
	}
	c.True(wrc.signingEndpoint().IsFallback())
	wrc.reconnectEndpoint = wsEndpoint("pokt1p", "n.protocol.tech", false)
	c.False(wrc.signingEndpoint().IsFallback(),
		"rebinding onto a protocol endpoint must switch the connection back to signing")

	// And the reverse.
	wrc2 := &websocketRequestContext{
		selectedEndpoint: wsEndpoint("pokt1p", "n.protocol.tech", false),
	}
	c.False(wrc2.signingEndpoint().IsFallback())
	wrc2.reconnectEndpoint = wsEndpoint("pokt1fb", "fb.fallback.tech", true)
	c.True(wrc2.signingEndpoint().IsFallback(),
		"rebinding onto a fallback must stop signing")
}

// The gauge must never leak. An Inc at establish and a Dec at close, with any number of moves
// in between, has to net to zero on EVERY operator — otherwise a phantom connection is left
// credited to whoever the client happened to open on.
func Test_MoveWebsocketConnection_GaugeNetsToZeroAcrossRebinds(t *testing.T) {
	c := require.New(t)

	const svc = "gauge-conservation-test"
	gauge := func(domain string) float64 {
		return testutil.ToFloat64(metrics.WebsocketConnectionsActive.WithLabelValues(domain, svc))
	}

	// establish on A
	metrics.RecordWebsocketConnectionEstablished("a.tech", svc)
	c.Equal(1.0, gauge("a.tech"))

	// rollover A -> B, stall B -> C, tumble C -> A
	metrics.MoveWebsocketConnection("a.tech", "b.tech", svc)
	c.Equal(0.0, gauge("a.tech"), "the operator it left must not keep the credit")
	c.Equal(1.0, gauge("b.tech"))

	metrics.MoveWebsocketConnection("b.tech", "c.tech", svc)
	c.Equal(0.0, gauge("b.tech"))
	c.Equal(1.0, gauge("c.tech"))

	metrics.MoveWebsocketConnection("c.tech", "a.tech", svc)
	c.Equal(1.0, gauge("a.tech"), "coming back to the original operator is just another move")
	c.Equal(0.0, gauge("c.tech"))

	// close decrements the CURRENT domain, which is where the credit now sits
	metrics.RecordWebsocketConnectionClosed("a.tech", svc, 1.0)
	for _, d := range []string{"a.tech", "b.tech", "c.tech"} {
		c.Equal(0.0, gauge(d), "domain %s must be left at zero after close", d)
	}
}

// A rebind that stays on the same operator (a sibling registration behind the same eTLD+1)
// must not churn the gauge — a stray Dec/Inc pair on one label is harmless, but treating it as
// a move would misreport if the two ever diverged.
func Test_MoveWebsocketConnection_SameDomainIsANoOp(t *testing.T) {
	c := require.New(t)

	const svc = "gauge-noop-test"
	metrics.RecordWebsocketConnectionEstablished("same.tech", svc)
	metrics.MoveWebsocketConnection("same.tech", "same.tech", svc)
	c.Equal(1.0, testutil.ToFloat64(metrics.WebsocketConnectionsActive.WithLabelValues("same.tech", svc)))

	metrics.RecordWebsocketConnectionClosed("same.tech", svc, 1.0)
	c.Equal(0.0, testutil.ToFloat64(metrics.WebsocketConnectionsActive.WithLabelValues("same.tech", svc)))
}
