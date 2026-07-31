package shannon

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/metrics"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/selector"
)

// stackedSession mirrors the shape that motivated the widened avoid-set: one operator fronting
// a machine with SEVERAL supplier registrations, so excluding a single endpoint address leaves
// siblings that resolve to the same box.
//
//	bigop.tech  → https://a.bigop.tech  (3 registrations)  ← the stacked backend
//	            → https://b.bigop.tech  (1 registration)
//	other.tech  → https://c.other.tech  (1 registration)
func stackedSession() (map[protocol.EndpointAddr]endpoint, protocol.EndpointAddr) {
	eps := map[protocol.EndpointAddr]endpoint{}
	add := func(addr string) endpoint {
		e := newRCEndpoint(addr, false)
		eps[e.Addr()] = e
		return e
	}
	bound := add("pokt1big0-https://a.bigop.tech").Addr()
	add("pokt1big1-https://a.bigop.tech")
	add("pokt1big2-https://a.bigop.tech")
	add("pokt1big3-https://b.bigop.tech")
	add("pokt1oth0-https://c.other.tech")
	return eps, bound
}

// A stall means the BACKEND went silent. Excluding only the bound address leaves the sibling
// registrations that front the same machine, so the rebind can land straight back on the box
// it was told to escape — the connection stalls again and burns a stall-rebind budget.
func Test_chooseRebindEndpoint_StallEscapesTheWholeBackend(t *testing.T) {
	c := require.New(t)

	// Repeat: selection is randomized, so a single pass could miss the sibling by luck.
	for i := 0; i < 200; i++ {
		endpoints, bound := stackedSession()
		ep, different, reason, err := chooseRebindEndpoint(
			testLogger(), "svc", endpoints, bound, avoidBoundBackend, 0.5, true, noScore,
		)
		c.NoError(err)
		c.Empty(reason)
		c.True(different)
		c.NotEqual("https://a.bigop.tech", selector.BackendKey(ep.Addr()),
			"a stall escape must leave the stalling BACKEND, not just the bound registration")
	}
}

// An admin tumble exists to redistribute away from an operator. Hopping to another machine at
// the same operator moves the connection without moving the concentration.
func Test_chooseRebindEndpoint_TumbleEscapesTheWholeOperator(t *testing.T) {
	c := require.New(t)

	for i := 0; i < 200; i++ {
		endpoints, bound := stackedSession()
		ep, different, reason, err := chooseRebindEndpoint(
			testLogger(), "svc", endpoints, bound, avoidBoundOperator, 0.5, true, noScore,
		)
		c.NoError(err)
		c.Empty(reason)
		c.True(different)
		c.Equal("other.tech", selector.OperatorKey(ep.Addr()),
			"a tumble must leave the operator, not hop registrations inside it")
	}
}

// Narrowing, not failing: a session with nothing outside the bound operator must still let the
// connection escape the bound BACKEND. Refusing would close a client that survives today.
func Test_chooseRebindEndpoint_TumbleNarrowsToBackendOnSingleOperatorSession(t *testing.T) {
	c := require.New(t)

	endpoints := map[protocol.EndpointAddr]endpoint{}
	for _, a := range []string{
		"pokt1big0-https://a.bigop.tech",
		"pokt1big1-https://a.bigop.tech",
		"pokt1big2-https://b.bigop.tech",
	} {
		e := newRCEndpoint(a, false)
		endpoints[e.Addr()] = e
	}
	bound := protocol.EndpointAddr("pokt1big0-https://a.bigop.tech")

	ep, different, reason, err := chooseRebindEndpoint(
		testLogger(), "svc", endpoints, bound, avoidBoundOperator, 0.5, true, noScore,
	)
	c.NoError(err)
	c.Empty(reason)
	c.True(different)
	c.Equal("https://b.bigop.tech", selector.BackendKey(ep.Addr()),
		"single-operator session must still escape the bound backend rather than fail")
}

// The last rung: when the whole session is one backend, a stall escape can only drop the bound
// registration. It still moves (better than pinning a silent socket), but it may land on the
// same machine — which is exactly what the narrowed metric exists to surface.
func Test_chooseRebindEndpoint_StallNarrowsToEndpointOnSingleBackendSession(t *testing.T) {
	c := require.New(t)

	endpoints := map[protocol.EndpointAddr]endpoint{}
	for _, a := range []string{
		"pokt1big0-https://a.bigop.tech",
		"pokt1big1-https://a.bigop.tech",
	} {
		e := newRCEndpoint(a, false)
		endpoints[e.Addr()] = e
	}
	bound := protocol.EndpointAddr("pokt1big0-https://a.bigop.tech")

	ep, different, reason, err := chooseRebindEndpoint(
		testLogger(), "svc", endpoints, bound, avoidBoundBackend, 0.5, true, noScore,
	)
	c.NoError(err)
	c.Empty(reason)
	c.True(different)
	c.Equal(protocol.EndpointAddr("pokt1big1-https://a.bigop.tech"), ep.Addr())
}

// Failure semantics must be unchanged from the single-address avoid-set: only when the bound
// endpoint is the session's sole member is there nowhere to go.
func Test_chooseRebindEndpoint_OnlyBoundEndpoint_StillErrors(t *testing.T) {
	c := require.New(t)

	solo := newRCEndpoint("pokt1solo-https://n.solo.tech", false)
	for _, scope := range []rebindAvoidScope{avoidBoundEndpoint, avoidBoundBackend, avoidBoundOperator} {
		endpoints := map[protocol.EndpointAddr]endpoint{solo.Addr(): solo}
		ep, different, reason, err := chooseRebindEndpoint(
			testLogger(), "svc", endpoints, solo.Addr(), scope, 0.5, true, noScore,
		)
		c.Error(err, "scope %s", scope)
		c.Nil(ep)
		c.False(different)
		c.Equal(metrics.WSRebindFailedNoEndpoints, reason)
		c.ErrorIs(err, protocol.ErrEndpointUnavailable)
	}
}

func Test_applyAvoidScope_ReportsTheScopeItCouldHonor(t *testing.T) {
	c := require.New(t)

	tests := []struct {
		name  string
		addrs []string
		want  rebindAvoidScope
	}{
		{
			name:  "another operator present → operator scope honored",
			addrs: []string{"pokt1a-https://a.bigop.tech", "pokt1b-https://c.other.tech"},
			want:  avoidBoundOperator,
		},
		{
			name:  "same operator, another backend → narrows to backend",
			addrs: []string{"pokt1a-https://a.bigop.tech", "pokt1b-https://b.bigop.tech"},
			want:  avoidBoundBackend,
		},
		{
			name:  "same backend, another registration → narrows to endpoint",
			addrs: []string{"pokt1a-https://a.bigop.tech", "pokt1b-https://a.bigop.tech"},
			want:  avoidBoundEndpoint,
		},
		{
			name:  "nothing else at all → nothing can be honored",
			addrs: []string{"pokt1a-https://a.bigop.tech"},
			want:  avoidNothing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoints := map[protocol.EndpointAddr]endpoint{}
			for _, a := range tt.addrs {
				e := newRCEndpoint(a, false)
				endpoints[e.Addr()] = e
			}
			bound := protocol.EndpointAddr(tt.addrs[0])

			got := applyAvoidScope(endpoints, bound, avoidBoundOperator)
			c.Equal(tt.want, got)

			// Whatever scope was applied, the bound endpoint is always gone unless nothing
			// could be honored at all (in which case the pool is left intact for the caller
			// to reject).
			if got != avoidNothing {
				_, stillThere := endpoints[bound]
				c.False(stillThere, "the bound endpoint must never survive a honored avoid")
				c.NotEmpty(endpoints, "a honored avoid must leave something selectable")
			}
		})
	}
}

// Scopes must stay strictly nested (operator ⊇ backend ⊇ endpoint) — applyAvoidScope's
// narrowing ladder is only correct if a wider scope excludes everything a narrower one does.
func Test_excludedByScope_ScopesAreNested(t *testing.T) {
	c := require.New(t)

	bound := protocol.EndpointAddr("pokt1a-https://a.bigop.tech")
	candidates := []protocol.EndpointAddr{
		"pokt1a-https://a.bigop.tech", // the bound endpoint itself
		"pokt1b-https://a.bigop.tech", // same backend, different registration
		"pokt1c-https://b.bigop.tech", // same operator, different backend
		"pokt1d-https://c.other.tech", // different operator
	}

	for _, addr := range candidates {
		byEndpoint := excludedByScope(addr, bound, avoidBoundEndpoint)
		byBackend := excludedByScope(addr, bound, avoidBoundBackend)
		byOperator := excludedByScope(addr, bound, avoidBoundOperator)

		if byEndpoint {
			c.True(byBackend, "%s: endpoint-excluded must also be backend-excluded", addr)
		}
		if byBackend {
			c.True(byOperator, "%s: backend-excluded must also be operator-excluded", addr)
		}
		c.False(excludedByScope(addr, bound, avoidNothing), "%s: avoidNothing excludes nothing", addr)
	}
}

// ReconnectEndpoint owns the trigger→scope mapping. Getting it wrong is silent: the rebind
// still succeeds, it just fails to escape what it was supposed to escape.
func Test_ReconnectEndpoint_MapsTriggerToAvoidScope(t *testing.T) {
	c := require.New(t)

	tests := []struct {
		name                 string
		adminTumbleRequested bool
		avoidCurrentSupplier bool
		wantScope            rebindAvoidScope
		wantTrigger          string
	}{
		{"admin tumble condemns the operator", true, true, avoidBoundOperator, metrics.WSRebindTriggerAdmin},
		{"stall condemns the backend", false, true, avoidBoundBackend, metrics.WSRebindTriggerStall},
		{"rollover condemns nothing", false, false, avoidNothing, metrics.WSRebindTriggerRollover},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotScope rebindAvoidScope
			sentinel := errors.New("stop after scope capture")

			wrc := &websocketRequestContext{
				logger:               testLogger(),
				selectedEndpoint:     newRCEndpoint("pokt1a-https://a.bigop.tech", false),
				adminTumbleRequested: tt.adminTumbleRequested,
				reconnectProvider: func(_ context.Context, scope rebindAvoidScope) (endpoint, bool, string, error) {
					gotScope = scope
					return nil, false, metrics.WSRebindFailedSelect, sentinel
				},
			}

			conn, err := wrc.ReconnectEndpoint(context.Background(), tt.avoidCurrentSupplier)
			c.ErrorIs(err, sentinel)
			c.Nil(conn)
			c.Equal(tt.wantScope, gotScope)
			c.Equal(tt.wantTrigger, wrc.lastReconnectTrigger)
			c.False(wrc.adminTumbleRequested, "the admin flag must be consumed by the rebind it labelled")
		})
	}
}
