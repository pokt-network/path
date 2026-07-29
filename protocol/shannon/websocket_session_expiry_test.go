package shannon

import (
	"context"
	"errors"
	"testing"

	"github.com/pokt-network/path/protocol"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
	"github.com/stretchr/testify/require"
)

// sessionEndpoint is an endpoint carrying a session header with a settable end height, which
// cfgEndpoint does not provide (its Session() has a nil Header).
type sessionEndpoint struct {
	*cfgEndpoint
	endHeight  int64
	nilSession bool
	nilHeader  bool
}

func (e *sessionEndpoint) Session() *sessiontypes.Session {
	if e.nilSession {
		return nil
	}
	s := &sessiontypes.Session{SessionId: "sess"}
	if !e.nilHeader {
		s.Header = &sessiontypes.SessionHeader{SessionEndBlockHeight: e.endHeight}
	}
	return s
}

func newSessionEndpoint(addr string, endHeight int64) *sessionEndpoint {
	return &sessionEndpoint{
		cfgEndpoint: &cfgEndpoint{addr: protocol.EndpointAddr("pokt1x-" + addr), supplier: "pokt1x", sessionID: "sess"},
		endHeight:   endHeight,
	}
}

// heightFullNode is a FullNode stub that only answers GetCurrentBlockHeight.
type heightFullNode struct {
	FullNode // embedded: the other methods are never called here
	height   int64
	err      error
}

func (f *heightFullNode) GetCurrentBlockHeight(context.Context) (int64, error) {
	return f.height, f.err
}

func wrcWithSession(ep endpoint, fn FullNode) *websocketRequestContext {
	return &websocketRequestContext{
		logger:           testLogger(),
		context:          context.Background(),
		serviceID:        "bsc",
		selectedEndpoint: ep,
		fullNode:         fn,
	}
}

// The boundary that matters: a session is only "expired" once it is past its end height AND
// past the rollover grace the session logic itself applies. Firing inside the grace window
// would rebind connections the ordinary supplier-initiated rollover is still about to handle.
func Test_BoundSessionExpired_HonoursTheRolloverGrace(t *testing.T) {
	const endHeight = 1000
	tests := []struct {
		name    string
		height  int64
		expired bool
	}{
		{"well inside the session", 900, false},
		{"exactly at session end", endHeight, false},
		{"inside the rollover grace", endHeight + boundSessionGraceBlocks - 1, false},
		{"at the edge of the grace", endHeight + boundSessionGraceBlocks, false},
		{"one block past the grace", endHeight + boundSessionGraceBlocks + 1, true},
		{"long past the grace", endHeight + 500, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrc := wrcWithSession(
				newSessionEndpoint("https://n.op.tech", endHeight),
				&heightFullNode{height: tt.height},
			)
			require.Equal(t, tt.expired, wrc.BoundSessionExpired())
		})
	}
}

// A rebind costs the client a gap in its stream, so anything we cannot determine must read as
// "not expired". Every one of these would otherwise disrupt working connections.
func Test_BoundSessionExpired_UnknownStateNeverTriggersARebind(t *testing.T) {
	c := require.New(t)

	// Block height unavailable — the case that would fire fleet-wide during a full-node blip.
	c.False(wrcWithSession(
		newSessionEndpoint("https://n.op.tech", 1000),
		&heightFullNode{err: errors.New("full node unreachable")},
	).BoundSessionExpired(), "an unavailable block height must never read as expired")

	// Nonsense height.
	c.False(wrcWithSession(
		newSessionEndpoint("https://n.op.tech", 1000),
		&heightFullNode{height: 0},
	).BoundSessionExpired())

	// No session on the endpoint.
	c.False(wrcWithSession(
		&sessionEndpoint{cfgEndpoint: &cfgEndpoint{addr: protocol.EndpointAddr("pokt1x-https://n.op.tech")}, nilSession: true},
		&heightFullNode{height: 99999},
	).BoundSessionExpired())

	// Session with no header.
	c.False(wrcWithSession(
		&sessionEndpoint{cfgEndpoint: &cfgEndpoint{addr: protocol.EndpointAddr("pokt1x-https://n.op.tech")}, nilHeader: true},
		&heightFullNode{height: 99999},
	).BoundSessionExpired())

	// Header with no end height.
	c.False(wrcWithSession(
		newSessionEndpoint("https://n.op.tech", 0),
		&heightFullNode{height: 99999},
	).BoundSessionExpired())

	// No full node wired at all.
	c.False(wrcWithSession(
		newSessionEndpoint("https://n.op.tech", 1000), nil,
	).BoundSessionExpired())

	// No endpoint at all.
	wrc := &websocketRequestContext{logger: testLogger(), context: context.Background()}
	c.False(wrc.BoundSessionExpired())
}

// The check must follow the CURRENT binding. After a rebind the connection lives on a fresh
// session, and testing the setup-time endpoint would keep reporting expired forever — the
// bridge would rebind on every tick until it hit the give-up cap.
func Test_BoundSessionExpired_FollowsTheLiveBinding(t *testing.T) {
	c := require.New(t)

	stale := newSessionEndpoint("https://old.op.tech", 1000)
	fresh := newSessionEndpoint("https://new.op.tech", 5000)
	wrc := wrcWithSession(stale, &heightFullNode{height: 1500})

	c.True(wrc.BoundSessionExpired(), "the setup-time session has ended")

	wrc.reconnectEndpoint = fresh
	c.False(wrc.BoundSessionExpired(),
		"after a rebind the live session is current — must not keep reporting expired")
}

// The rebind must be labelled session_expired, not rollover: a supplier-initiated rollover is
// healthy, while this counts connections that would otherwise have been stranded.
func Test_ReconnectEndpoint_LabelsSessionExpiryDistinctly(t *testing.T) {
	c := require.New(t)

	var gotScope rebindAvoidScope
	sentinel := errors.New("stop after capture")
	wrc := &websocketRequestContext{
		logger:           testLogger(),
		selectedEndpoint: newSessionEndpoint("https://n.op.tech", 1000),
		reconnectProvider: func(_ context.Context, scope rebindAvoidScope) (endpoint, bool, string, error) {
			gotScope = scope
			return nil, false, "", sentinel
		},
	}
	wrc.OnSessionExpiryRebindRequested()
	c.True(wrc.sessionExpiryRebindRequested)

	_, err := wrc.ReconnectEndpoint(context.Background(), false)
	c.ErrorIs(err, sentinel)
	c.Equal("session_expired", wrc.lastReconnectTrigger)
	c.Equal(avoidNothing, gotScope,
		"session expiry needs a live session, not a different backend or operator")
	c.False(wrc.sessionExpiryRebindRequested, "the flag must be consumed by the rebind it labelled")
}

// An admin tumble and a session expiry can both be pending; the tumble is the operator's
// explicit instruction and must win, keeping its operator-wide avoid scope.
func Test_ReconnectEndpoint_AdminTumbleOutranksSessionExpiry(t *testing.T) {
	c := require.New(t)

	var gotScope rebindAvoidScope
	sentinel := errors.New("stop after capture")
	wrc := &websocketRequestContext{
		logger:           testLogger(),
		selectedEndpoint: newSessionEndpoint("https://n.op.tech", 1000),
		reconnectProvider: func(_ context.Context, scope rebindAvoidScope) (endpoint, bool, string, error) {
			gotScope = scope
			return nil, false, "", sentinel
		},
	}
	wrc.OnTumbleRequested()
	wrc.OnSessionExpiryRebindRequested()

	_, err := wrc.ReconnectEndpoint(context.Background(), true)
	c.ErrorIs(err, sentinel)
	c.Equal("admin", wrc.lastReconnectTrigger)
	c.Equal(avoidBoundOperator, gotScope)
}
