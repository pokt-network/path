package shannon

import (
	"context"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	apptypes "github.com/pokt-network/poktroll/x/application/types"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/protocol"
)

// The health-check endpoint lookup used to run on context.Background(), so a chain node
// that stopped answering made it block forever. That single unbounded call held the whole
// health-check cycle: measured in production on 2026-07-30 as 0 of 64 services keeping
// half their check rate for 21 minutes, in both environments at once.

// stallingSessionFullNode answers GetSession only after `delay`, or never (delay < 0),
// standing in for a chain node that has stopped responding.
//
// ignoreCtx makes it answer SUCCESSFULLY after the deadline has already passed, which is
// the case the post-lookup deadline check exists for.
type stallingSessionFullNode struct {
	FullNode // embedded: the other methods are never reached here

	delay     time.Duration
	ignoreCtx bool
	session   sessiontypes.Session
}

func (f *stallingSessionFullNode) IsInSessionRollover() bool { return false }

func (f *stallingSessionFullNode) GetSession(
	ctx context.Context,
	_ protocol.ServiceID,
	_ string,
) (sessiontypes.Session, error) {
	if f.delay < 0 {
		<-ctx.Done()
		return sessiontypes.Session{}, ctx.Err()
	}
	if f.ignoreCtx {
		time.Sleep(f.delay)
		return f.session, nil
	}
	select {
	case <-time.After(f.delay):
		return f.session, nil
	case <-ctx.Done():
		return sessiontypes.Session{}, ctx.Err()
	}
}

func (f *stallingSessionFullNode) GetSessionWithExtendedValidity(
	ctx context.Context,
	serviceID protocol.ServiceID,
	appAddr string,
) (sessiontypes.Session, error) {
	return f.GetSession(ctx, serviceID, appAddr)
}

func newHealthCheckLookupProtocol(fullNode FullNode) *Protocol {
	return &Protocol{
		logger:      polyzero.NewLogger(),
		FullNode:    fullNode,
		gatewayMode: protocol.GatewayModeCentralized,
		gatewayAddr: "pokt1gateway",
		ownedApps:   map[protocol.ServiceID][]string{"eth": {"pokt1abc123"}},
	}
}

// shrinkLookupTimeout keeps these tests fast; production uses the 3s default.
func shrinkLookupTimeout(t *testing.T, timeout time.Duration) {
	t.Helper()
	original := healthCheckEndpointLookupTimeout
	healthCheckEndpointLookupTimeout = timeout
	t.Cleanup(func() { healthCheckEndpointLookupTimeout = original })
}

// A chain node that never answers must produce an error, not a hung goroutine.
func Test_GetEndpointsForHealthCheck_HungChainQueryTimesOut(t *testing.T) {
	c := require.New(t)
	shrinkLookupTimeout(t, 100*time.Millisecond)

	p := newHealthCheckLookupProtocol(&stallingSessionFullNode{delay: -1})

	done := make(chan struct{})
	var infos []gateway.EndpointInfo
	var err error
	go func() {
		defer close(done)
		infos, err = p.GetEndpointsForHealthCheck()("eth")
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("GetEndpointsForHealthCheck never returned - the lookup is still unbounded")
	}

	c.Error(err, "a hung chain query must surface as an error so the service is skipped this cycle")
	c.Empty(infos)
}

// The deadline can also fire AFTER the session list arrives. Proceeding from there is
// worse than skipping: the block-height and shared-params failures below are lenient by
// design (no height disables the session-expiry filter entirely), so the cycle would be
// handed endpoints from already-rolled-over sessions.
func Test_GetEndpointsForHealthCheck_DeadlineAfterSessionLookupSkipsService(t *testing.T) {
	c := require.New(t)
	shrinkLookupTimeout(t, 50*time.Millisecond)

	p := newHealthCheckLookupProtocol(&stallingSessionFullNode{
		// Answers successfully, but only after the deadline has passed.
		delay:     150 * time.Millisecond,
		ignoreCtx: true,
		session: sessiontypes.Session{
			SessionId: "session-100",
			Header: &sessiontypes.SessionHeader{
				SessionStartBlockHeight: 1000,
				SessionEndBlockHeight:   1020,
			},
			Application: &apptypes.Application{
				Address:                   "pokt1abc123",
				DelegateeGatewayAddresses: []string{"pokt1gateway"},
			},
		},
	})

	infos, err := p.GetEndpointsForHealthCheck()("eth")

	c.Error(err)
	c.ErrorIs(err, context.DeadlineExceeded)
	c.Empty(infos)
}
