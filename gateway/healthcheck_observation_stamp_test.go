package gateway

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/observation"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
)

// observationVerdictProtocol records the isHealthCheck verdict each Apply*Observations
// caller supplies. The verdict decides whether a probe result reaches the
// volume-independent rate detectors, and it is set by the CALLER — so it has to be asserted
// at the caller, not inside the protocol implementation the caller passes it to.
type observationVerdictProtocol struct {
	*mockProtocolForRetry

	httpCalls atomic.Int32
	httpFlag  atomic.Bool
}

func (m *observationVerdictProtocol) ApplyHTTPObservations(_ *protocolobservations.Observations, isHealthCheck bool) error {
	m.httpCalls.Add(1)
	m.httpFlag.Store(isHealthCheck)
	return nil
}

// Test_HealthCheckExecutor_MarksHTTPObservationsAsProbe guards the HTTP half of the
// probe-contamination fix at its call site.
//
// One health check produces TWO reputation signals: the relay itself (stamped in
// protocol/shannon/context.go) and this observation publish. Only this call site knows the
// observations are synthetic — the Shannon layer receiving them cannot tell — so a caller
// that passes the wrong verdict silently restores the whole bug while every
// protocol-package test stays green.
func Test_HealthCheckExecutor_MarksHTTPObservationsAsProbe(t *testing.T) {
	c := require.New(t)

	proto := &observationVerdictProtocol{mockProtocolForRetry: &mockProtocolForRetry{}}
	executor := NewHealthCheckExecutor(HealthCheckExecutorConfig{
		Config:     &ActiveHealthChecksConfig{Enabled: true},
		Logger:     polyzero.NewLogger(),
		Protocol:   proto,
		MaxWorkers: 1,
	})
	t.Cleanup(executor.Stop)

	executor.publishHealthCheckObservations(
		protocol.ServiceID("solana"),
		protocol.EndpointAddr("pokt1supplier-https://probe.example.com/rpc"),
		time.Now(),
		nil,
		&protocolobservations.Observations{},
	)

	c.Equal(int32(1), proto.httpCalls.Load(),
		"the health-check executor must apply its protocol observations exactly once")
	c.True(proto.httpFlag.Load(),
		"health-check observations must be applied as a probe; applied as user traffic they feed "+
			"the rate detectors and the endpoint benches itself off its own probes")
}

// Test_RequestContext_MarksHTTPObservationsAsUserTraffic is the control at the OTHER call
// site. Without it, a change that hard-codes true everywhere would pass the test above
// while making the rate detectors permanently inert for real user traffic.
func Test_RequestContext_MarksHTTPObservationsAsUserTraffic(t *testing.T) {
	c := require.New(t)

	proto := &observationVerdictProtocol{mockProtocolForRetry: &mockProtocolForRetry{}}
	rc := &requestContext{
		logger:               polyzero.NewLogger(),
		context:              context.Background(),
		protocol:             proto,
		protocolObservations: &protocolobservations.Observations{},
		gatewayObservations:  &observation.GatewayObservations{},
	}

	rc.broadcastObservationsInternal()

	c.Equal(int32(1), proto.httpCalls.Load())
	c.False(proto.httpFlag.Load(),
		"a user request's observations must reach the rate detectors")
}
