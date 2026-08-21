package gateway

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
)

// The circuit breaker's failure-rate gate divides failures by (failures + successes). Every
// attempt that fails reaches MarkBroken through the retry loop, but a success only counts if
// the path it returned on calls RecordSuccess — and the hedge-race path did not. On a service
// with a hedge delay configured EVERY first attempt goes through the racer, including the
// overwhelming majority where the hedge never fires ("primary_only"), so the gate's
// denominator was fed almost exclusively by retries and batch items.
//
// Measured 2026-08-21, one environment, one hour: a high-volume operator produced 680,893
// successful first-attempt relays and the gate counted 36,287 of them; fleet-wide the gate
// saw 26% of successes. A low-volume operator whose traffic is mostly hedges is judged on a
// fraction dominated by its failures, breaks at a rate it does not have, and the hysteresis
// margin added to stop marginal hosts flapping never gets a chance to hold — the rate it
// sees is inflated past the margin by construction.
//
// The normal path is exercised alongside as the control: the same request, the same
// response, one success either way.
func TestHandleSingleRelayRequest_HedgeRaceSuccessFeedsCircuitBreakerDenominator(t *testing.T) {
	const (
		serviceID = "test-service"
		endpoint  = protocol.EndpointAddr("pokt1a-https://a.example.com")
		domain    = "a.example.com"
	)

	for _, tc := range []struct {
		name       string
		hedgeDelay *time.Duration
	}{
		{name: "hedge race path", hedgeDelay: hedgeDelayPtr()},
		{name: "normal path (control)", hedgeDelay: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
			protocolCtx := &okProtocolCtx{endpoint: endpoint}
			rc := &requestContext{
				logger:         polyzero.NewLogger(),
				context:        context.Background(),
				serviceID:      serviceID,
				qosCtx:         &recordingQoSContext{},
				circuitBreaker: cb,
				protocol: &okProtocol{
					mockProtocolForRetry: mockProtocolForRetry{
						retryConfig: &ServiceRetryConfig{
							Enabled:    boolPtr(true),
							MaxRetries: intPtr(0),
							HedgeDelay: tc.hedgeDelay,
						},
					},
					endpoints: protocol.EndpointAddrList{
						endpoint,
						"pokt1b-https://b.example.net",
					},
					protocolCtx: protocolCtx,
				},
				protocolContexts:    []ProtocolRequestContext{protocolCtx},
				originalHTTPRequest: httptestRequest(),
			}

			require.NoError(t, rc.handleSingleRelayRequest())
			require.GreaterOrEqual(t, protocolCtx.calls(), 1, "the relay must actually have been sent")

			cb.statsMu.Lock()
			w := cb.windowLocked(serviceID, domain, time.Now())
			successes, failures := w.successes, w.failures
			cb.statsMu.Unlock()

			require.Equal(t, 0, failures, "a successful relay must not register as a failure")
			require.Equal(t, 1, successes,
				"a successful relay must be counted exactly once in the gate's denominator, on whichever path returned it")
		})
	}
}

// okProtocolCtx answers every relay with a valid JSON-RPC result from a fixed endpoint.
type okProtocolCtx struct {
	endpoint protocol.EndpointAddr

	mu        sync.Mutex
	callCount int
}

func (m *okProtocolCtx) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

func (m *okProtocolCtx) HandleServiceRequest([]protocol.Payload) ([]protocol.Response, error) {
	m.mu.Lock()
	m.callCount++
	m.mu.Unlock()
	return []protocol.Response{{
		Bytes:          []byte(`{"jsonrpc":"2.0","id":1,"result":"0x10"}`),
		HTTPStatusCode: http.StatusOK,
		EndpointAddr:   m.endpoint,
	}}, nil
}

func (m *okProtocolCtx) SetParentContext(context.Context) {}
func (m *okProtocolCtx) MarkAsHedge()                     {}
func (m *okProtocolCtx) MarkAsRetry()                     {}
func (m *okProtocolCtx) MarkAsHealthCheck()               {}
func (m *okProtocolCtx) GetObservations() protocolobservations.Observations {
	return protocolobservations.Observations{}
}

// okProtocol supplies endpoints and the always-succeeding protocol context to the retry loop.
type okProtocol struct {
	mockProtocolForRetry
	endpoints   protocol.EndpointAddrList
	protocolCtx *okProtocolCtx
}

func (m *okProtocol) GetUnifiedServicesConfig() *UnifiedServicesConfig {
	return &UnifiedServicesConfig{
		Services: []ServiceConfig{{ID: "test-service", RetryConfig: m.retryConfig}},
	}
}

func (m *okProtocol) AvailableHTTPEndpoints(
	_ context.Context, _ protocol.ServiceID, _ sharedtypes.RPCType, _ *http.Request,
) (protocol.EndpointAddrList, protocolobservations.Observations, error) {
	return m.endpoints, protocolobservations.Observations{}, nil
}

func (m *okProtocol) BuildHTTPRequestContextForEndpoint(
	_ context.Context, _ protocol.ServiceID, _ protocol.EndpointAddr, _ sharedtypes.RPCType, _ *http.Request, _ bool,
) (ProtocolRequestContext, protocolobservations.Observations, error) {
	return m.protocolCtx, protocolobservations.Observations{}, nil
}
