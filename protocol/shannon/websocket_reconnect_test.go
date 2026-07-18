package shannon

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// rcEndpoint wraps cfgEndpoint to make IsFallback configurable for tier-2 selection tests.
type rcEndpoint struct {
	*cfgEndpoint
	fallback bool
}

func (e *rcEndpoint) IsFallback() bool { return e.fallback }

func newRCEndpoint(addr string, fallback bool) *rcEndpoint {
	return &rcEndpoint{
		cfgEndpoint: &cfgEndpoint{addr: protocol.EndpointAddr(addr), supplier: addr, sessionID: "sess"},
		fallback:    fallback,
	}
}

// Test_selectBestReconnectEndpoint_PrefersNonFallback verifies that a protocol
// (non-fallback) endpoint is chosen over a fallback one for a tier-2 rebind.
func Test_selectBestReconnectEndpoint_PrefersNonFallback(t *testing.T) {
	c := require.New(t)

	fallback := newRCEndpoint("aaa-fallback", true)
	protocolEP := newRCEndpoint("zzz-protocol", false)

	endpoints := map[protocol.EndpointAddr]endpoint{
		fallback.Addr():   fallback,
		protocolEP.Addr(): protocolEP,
	}

	best := selectBestReconnectEndpoint(endpoints)
	c.Equal(protocolEP.Addr(), best.Addr(), "non-fallback endpoint must win even with a larger address")
}

// Test_selectBestReconnectEndpoint_DeterministicTiebreak verifies that among equally-ranked
// (non-fallback) endpoints, selection is deterministic: the lexicographically smallest
// address wins, regardless of map iteration order.
func Test_selectBestReconnectEndpoint_DeterministicTiebreak(t *testing.T) {
	c := require.New(t)

	a := newRCEndpoint("aaa", false)
	b := newRCEndpoint("bbb", false)
	d := newRCEndpoint("ddd", false)

	endpoints := map[protocol.EndpointAddr]endpoint{
		d.Addr(): d,
		b.Addr(): b,
		a.Addr(): a,
	}

	// Run several times to guard against map-iteration-order flakiness.
	for i := 0; i < 20; i++ {
		best := selectBestReconnectEndpoint(endpoints)
		c.Equal(a.Addr(), best.Addr(), "smallest address must win the tiebreak deterministically")
	}
}

// Test_selectBestReconnectEndpoint_SingleAndEmpty covers the boundary cases.
func Test_selectBestReconnectEndpoint_SingleAndEmpty(t *testing.T) {
	c := require.New(t)

	only := newRCEndpoint("solo", false)
	c.Equal(only.Addr(), selectBestReconnectEndpoint(map[protocol.EndpointAddr]endpoint{only.Addr(): only}).Addr())

	c.Nil(selectBestReconnectEndpoint(map[protocol.EndpointAddr]endpoint{}), "empty set returns nil")
}

// Test_betterReconnectCandidate verifies the ordering used by selectBestReconnectEndpoint.
func Test_betterReconnectCandidate(t *testing.T) {
	c := require.New(t)

	protoSmall := newRCEndpoint("aaa", false)
	protoLarge := newRCEndpoint("bbb", false)
	fallbackSmall := newRCEndpoint("aaa", true)

	// Non-fallback beats fallback regardless of address.
	c.True(betterReconnectCandidate(protoLarge, fallbackSmall), "non-fallback beats fallback")
	c.False(betterReconnectCandidate(fallbackSmall, protoLarge), "fallback never beats non-fallback")

	// Among non-fallback, smaller address wins.
	c.True(betterReconnectCandidate(protoSmall, protoLarge))
	c.False(betterReconnectCandidate(protoLarge, protoSmall))
}
