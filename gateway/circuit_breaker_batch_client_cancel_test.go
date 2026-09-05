package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
)

// A batch item's relay fails with "context canceled" when the client hangs up while the
// batch is in flight: net/http cancels the request context, every in-flight item aborts on
// whichever supplier it was sitting on, and the batch path stamped each abort on that
// supplier's domain as a transport failure. Measured 2026-08-24 on one batch-heavy service:
// 43 of 43 circuit breaks over two hours, both environments, were this shape — spread across
// every operator serving the service, which is the tell that the cause was the client and
// not any of them.
//
// The single-request retry loop already returns on a done context before it reaches
// MarkBroken; the batch item loop did not. A canceled request is not a fault of the domain
// that happened to be holding the relay, so it must not feed the circuit breaker's
// failure-rate gate at all — neither as a break nor as a failure in the window.
//
// The control is the same transport error with the request context still live: that one
// IS the domain's fault and must keep counting.
func TestProcessSinglePayloadWithRetry_ClientCancelDoesNotFeedCircuitBreaker(t *testing.T) {
	const (
		serviceID = "test-service"
		endpoint  = protocol.EndpointAddr("pokt1a-https://a.example.com")
		domain    = "a.example.com"
	)

	// The control deliberately does NOT use context.Canceled. An endpoint cannot cancel our
	// context — only we can — so a cancel is never evidence about the endpoint, whether or
	// not the client's own context is still live. The original control asserted the
	// opposite and encoded the hedge-cancel bug as expected behaviour: the hedge racer
	// cancels the primary branch's detached context on every exit path, and a batch item
	// falling through from the race reused it, so "cancel + live parent" was reached in
	// production constantly and benched healthy operators. connection refused is what a
	// genuinely broken endpoint produces, and that must still break.
	connRefused := errors.New("dial tcp 10.0.0.1:443: connect: connection refused")
	for _, tc := range []struct {
		name         string
		cancelParent bool
		relayErr     error
		wantFailures int
	}{
		{name: "client hung up mid-relay", cancelParent: true, wantFailures: 0},
		{name: "cancel with the client context still live (hedge race)", cancelParent: false, wantFailures: 0},
		{name: "genuine transport error (control)", cancelParent: false, relayErr: connRefused, wantFailures: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
			protocolCtx := &transportErrorProtocolCtx{
				endpoint: endpoint,
				relayErr: tc.relayErr,
				onRelay: func() {
					if tc.cancelParent {
						cancel()
					}
				},
			}
			rc := &requestContext{
				logger:         polyzero.NewLogger(),
				context:        ctx,
				serviceID:      serviceID,
				qosCtx:         &recordingQoSContext{},
				circuitBreaker: cb,
				protocol: &pluggableCtxProtocol{
					mockProtocolForRetry: mockProtocolForRetry{
						retryConfig: &ServiceRetryConfig{
							Enabled:    boolPtr(true),
							MaxRetries: intPtr(0),
						},
					},
					endpoints:   protocol.EndpointAddrList{endpoint},
					protocolCtx: protocolCtx,
				},
				originalHTTPRequest: httptestRequest(),
			}

			payload := protocol.Payload{
				Data:          `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`,
				RPCType:       sharedtypes.RPCType_JSON_RPC,
				JSONRPCMethod: "eth_blockNumber",
			}
			_, err := rc.processSinglePayloadWithRetry(payload, 0, 2, sharedtypes.RPCType_JSON_RPC, rc.logger)
			require.Error(t, err)
			require.Equal(t, 1, protocolCtx.calls, "the relay must actually have been sent")

			cb.statsMu.Lock()
			w := cb.windowLocked(serviceID, domain, time.Now())
			failures := w.failures
			cb.statsMu.Unlock()

			require.Equal(t, tc.wantFailures, failures,
				"failures fed to the circuit breaker's rate gate from this batch item")
		})
	}
}

// transportErrorProtocolCtx fails every relay at the transport layer — no HTTP response,
// no body — after running onRelay, which lets a test cancel the parent context the way a
// client disconnect does while the relay is in flight.
type transportErrorProtocolCtx struct {
	endpoint protocol.EndpointAddr
	onRelay  func()
	calls    int
	// relayErr is the wrapped cause the relay fails with. Nil means context.Canceled,
	// the shape a cancelled in-flight request produces.
	relayErr error
}

func (m *transportErrorProtocolCtx) HandleServiceRequest([]protocol.Payload) ([]protocol.Response, error) {
	m.calls++
	m.onRelay()
	cause := m.relayErr
	if cause == nil {
		cause = context.Canceled
	}
	return nil, fmt.Errorf("relay: error sending relay: %w: connection error: Post \"https://a.example.com\": %w",
		errors.New("HTTP relay request failed"), cause)
}

func (m *transportErrorProtocolCtx) SetParentContext(context.Context) {}
func (m *transportErrorProtocolCtx) MarkAsHedge()                     {}
func (m *transportErrorProtocolCtx) MarkAsRetry()                     {}
func (m *transportErrorProtocolCtx) MarkAsHealthCheck()               {}
func (m *transportErrorProtocolCtx) GetObservations() protocolobservations.Observations {
	return protocolobservations.Observations{}
}

// pluggableCtxProtocol hands the retry/batch loops a fixed endpoint list and whatever
// protocol context the test supplies.
type pluggableCtxProtocol struct {
	mockProtocolForRetry
	endpoints   protocol.EndpointAddrList
	protocolCtx ProtocolRequestContext
	// onBuild, when set, fires on every protocol-context build. Lets a test see the
	// ORDER of builds and relays, which is what distinguishes a fallthrough that reused
	// a context from one that rebuilt it.
	onBuild func()
}

func (m *pluggableCtxProtocol) AvailableHTTPEndpoints(
	_ context.Context, _ protocol.ServiceID, _ sharedtypes.RPCType, _ *http.Request,
) (protocol.EndpointAddrList, protocolobservations.Observations, error) {
	return m.endpoints, protocolobservations.Observations{}, nil
}

func (m *pluggableCtxProtocol) BuildHTTPRequestContextForEndpoint(
	_ context.Context, _ protocol.ServiceID, _ protocol.EndpointAddr, _ sharedtypes.RPCType, _ *http.Request, _ bool,
) (ProtocolRequestContext, protocolobservations.Observations, error) {
	if m.onBuild != nil {
		m.onBuild()
	}
	return m.protocolCtx, protocolobservations.Observations{}, nil
}
