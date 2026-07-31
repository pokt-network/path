package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	pathhttp "github.com/pokt-network/path/network/http"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/heuristic"
)

// prunedStateBody is what a supplier running a pruned (non-archival) node returns for a
// historical request such as eth_getBalance at an old block. It is a well-formed JSON-RPC
// error response — the answer the client should receive, not a gateway-synthesized 500.
const prunedStateBody = `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"missing trie node 0x4a2b (path ) state 0x4a2b is not available"}}`

// protocolCapabilityError reproduces the error the Shannon protocol layer returns for a
// capability limitation: the structured heuristic AnalysisResult is FLATTENED into the error
// string (dropping MatchedPattern) and the response body is embedded in it.
func protocolCapabilityError(body string) error {
	return fmt.Errorf("relay: error sending relay for service eth endpoint pokt1abc-https://example.com: raw_payload: %s: heuristic detected error_indicator_blockchain_error (method=eth_getBalance): backend returned error response", body)
}

// isCapabilityLimitationFailure must recognize a capability limitation from EITHER source,
// because only one of them is populated depending on which layer detected the error first.
func TestIsCapabilityLimitationFailure(t *testing.T) {
	tests := []struct {
		name string
		hr   *heuristic.AnalysisResult
		err  error
		want bool
	}{
		{
			name: "structured result: archival pattern",
			hr:   &heuristic.AnalysisResult{MatchedPattern: "missing trie node"},
			want: true,
		},
		{
			name: "structured result: lite fullnode capability",
			hr:   &heuristic.AnalysisResult{MatchedPattern: "capability_limitation"},
			want: true,
		},
		{
			name: "structured result: non-capability fault",
			hr:   &heuristic.AnalysisResult{MatchedPattern: "bad gateway"},
			want: false,
		},
		{
			// The protocol layer detects first and flattens the result, so MatchedPattern
			// is gone by the time the gateway sees this. The body inside the error string
			// is the only thing left to key off.
			name: "flattened protocol error: pruned state",
			err:  protocolCapabilityError(prunedStateBody),
			want: true,
		},
		{
			name: "flattened protocol error: cometbft pruned height",
			err:  protocolCapabilityError(`{"error":{"code":-32603,"message":"height 100 is not available, lowest height is 21500000"}}`),
			want: true,
		},
		{
			// A structural-tier detection carries an EMPTY MatchedPattern, so the
			// structured branch alone would miss it; the error-string scan must catch it.
			name: "empty MatchedPattern but capability body in error",
			hr:   &heuristic.AnalysisResult{Reason: "error_without_jsonrpc_version"},
			err:  protocolCapabilityError(`{"error":{"message":"block has been pruned"}}`),
			want: true,
		},
		{
			name: "transport failure is not a capability limitation",
			err:  errors.New("relay: dial tcp 10.0.0.1:443: connect: connection refused"),
			want: false,
		},
		{
			name: "no heuristic result and no error",
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isCapabilityLimitationFailure(tc.hr, tc.err))
		})
	}
}

// The capability-limitation budget must permit the discovering attempt plus EXACTLY ONE
// retry (supplier A may be pruned while supplier B is archival), and nothing beyond it.
// Non-capability failures must not consume the budget.
func TestNoteCapabilityFailure_AllowsExactlyOneRetry(t *testing.T) {
	capErr := protocolCapabilityError(prunedStateBody)
	count := 0

	// Attempt 1 discovers the capability limitation: retry allowed.
	isCapability, budgetSpent := noteCapabilityFailure(&count, nil, capErr)
	require.True(t, isCapability)
	require.False(t, budgetSpent, "the first capability failure must still permit one retry")

	// Attempt 2 hits it again on a different supplier: budget spent, stop.
	isCapability, budgetSpent = noteCapabilityFailure(&count, nil, capErr)
	require.True(t, isCapability)
	require.True(t, budgetSpent, "a capability limitation must be retried at most once")

	// A non-capability failure neither reports as one nor consumes budget.
	countBefore := count
	isCapability, budgetSpent = noteCapabilityFailure(&count, nil, errors.New("connection refused"))
	require.False(t, isCapability)
	require.False(t, budgetSpent)
	require.Equal(t, countBefore, count, "non-capability failures must not consume the capability budget")
}

// End-to-end through the real retry loop: a capability limitation must be retried EXACTLY
// once even when the service is configured for 3 retries, and the backend's own error body
// must be handed to the QoS context verbatim (same bytes, same HTTP status) so the client
// receives the backend's error rather than a synthesized 500.
func TestHandleSingleRelayRequest_CapabilityError_RetriesOnceAndPassesBodyVerbatim(t *testing.T) {
	protocolCtx := &capabilityProtocolCtx{body: prunedStateBody, statusCode: http.StatusOK}
	qosCtx := &recordingQoSContext{}

	rc := &requestContext{
		logger:    polyzero.NewLogger(),
		context:   context.Background(),
		serviceID: "test-service",
		qosCtx:    qosCtx,
		protocol: &capabilityProtocol{
			mockProtocolForRetry: mockProtocolForRetry{
				retryConfig: &ServiceRetryConfig{
					Enabled:           boolPtr(true),
					MaxRetries:        intPtr(3), // 4 attempts if the budget were not enforced
					RetryOn5xx:        boolPtr(true),
					RetryOnTimeout:    boolPtr(true),
					RetryOnConnection: boolPtr(true),
				},
			},
			endpoints: protocol.EndpointAddrList{
				"pokt1a-https://a.example.com",
				"pokt1b-https://b.example.net",
				"pokt1c-https://c.example.org",
			},
			protocolCtx: protocolCtx,
		},
		protocolContexts:    []ProtocolRequestContext{protocolCtx},
		originalHTTPRequest: httptestRequest(),
	}

	err := rc.handleSingleRelayRequest()
	require.Error(t, err, "the relay ultimately failed, so the loop must report the failure")

	require.Equal(t, 2, protocolCtx.calls(),
		"a capability limitation must cost the initial attempt plus exactly one retry, not the full retry budget")

	// The backend's error must have reached the QoS layer unmodified — that is what makes
	// the client's response the backend's error instead of a gateway-built envelope.
	updates := qosCtx.updates()
	require.NotEmpty(t, updates, "the failing response must be handed to the QoS context")
	require.Equal(t, prunedStateBody, string(updates[len(updates)-1].body))
	require.Equal(t, http.StatusOK, updates[len(updates)-1].statusCode)
}

// A capability limitation surfacing through the HEDGE path must behave the same way. The
// hedge branch dropped the response bytes entirely, which is how a "state is pruned" answer
// reached clients as a synthesized 500: with no raw response recorded, the QoS layer has
// nothing to return and falls back to building an internal-error envelope out of the
// flattened protocol error string.
//
// MaxRetries is 0 here on purpose: the hedged attempt is the ONLY attempt, so the sole
// chance to record the backend's response is the hedge path itself. With a retry available,
// the following normal-path attempt would record it and mask the defect.
func TestHandleSingleRelayRequest_CapabilityError_HedgePathPassesBodyVerbatim(t *testing.T) {
	protocolCtx := &capabilityProtocolCtx{body: prunedStateBody, statusCode: http.StatusOK}
	qosCtx := &recordingQoSContext{}

	hedgeDelay := hedgeDelayPtr()
	rc := &requestContext{
		logger:    polyzero.NewLogger(),
		context:   context.Background(),
		serviceID: "test-service",
		qosCtx:    qosCtx,
		protocol: &capabilityProtocol{
			mockProtocolForRetry: mockProtocolForRetry{
				retryConfig: &ServiceRetryConfig{
					Enabled:           boolPtr(true),
					MaxRetries:        intPtr(0),
					RetryOn5xx:        boolPtr(true),
					RetryOnTimeout:    boolPtr(true),
					RetryOnConnection: boolPtr(true),
					HedgeDelay:        hedgeDelay,
				},
			},
			endpoints: protocol.EndpointAddrList{
				"pokt1a-https://a.example.com",
				"pokt1b-https://b.example.net",
				"pokt1c-https://c.example.org",
			},
			protocolCtx: protocolCtx,
		},
		protocolContexts:    []ProtocolRequestContext{protocolCtx},
		originalHTTPRequest: httptestRequest(),
	}

	err := rc.handleSingleRelayRequest()
	require.Error(t, err)

	updates := qosCtx.updates()
	require.NotEmpty(t, updates,
		"the hedge path must hand the backend's capability error to the QoS context instead of dropping the bytes")
	require.Equal(t, prunedStateBody, string(updates[len(updates)-1].body))
	require.Equal(t, http.StatusOK, updates[len(updates)-1].statusCode)
}

// ---------------------------------------------------------------------------
// test doubles
// ---------------------------------------------------------------------------

func httptestRequest() *http.Request {
	req, _ := http.NewRequest(http.MethodPost, "http://localhost:3069/v1", nil)
	return req
}

func hedgeDelayPtr() *time.Duration {
	d := time.Millisecond // the hedge branch starts almost immediately
	return &d
}

// capabilityProtocolCtx returns a capability-limitation failure on every relay, exactly the
// way the Shannon protocol layer does: the response bytes are preserved on the response AND
// the structured heuristic result is flattened into the error string.
type capabilityProtocolCtx struct {
	body       string
	statusCode int

	mu        sync.Mutex
	callCount int
}

func (m *capabilityProtocolCtx) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

func (m *capabilityProtocolCtx) HandleServiceRequest([]protocol.Payload) ([]protocol.Response, error) {
	m.mu.Lock()
	m.callCount++
	m.mu.Unlock()

	return []protocol.Response{{
		Bytes:          []byte(m.body),
		HTTPStatusCode: m.statusCode,
		EndpointAddr:   "pokt1a-https://a.example.com",
	}}, protocolCapabilityError(m.body)
}

func (m *capabilityProtocolCtx) SetParentContext(context.Context) {}
func (m *capabilityProtocolCtx) MarkAsHedge()                     {}
func (m *capabilityProtocolCtx) MarkAsRetry()                     {}
func (m *capabilityProtocolCtx) MarkAsHealthCheck()               {}
func (m *capabilityProtocolCtx) GetObservations() protocolobservations.Observations {
	return protocolobservations.Observations{}
}

// capabilityProtocol supplies endpoints and protocol contexts for the retry loop.
type capabilityProtocol struct {
	mockProtocolForRetry
	endpoints   protocol.EndpointAddrList
	protocolCtx *capabilityProtocolCtx
}

func (m *capabilityProtocol) GetUnifiedServicesConfig() *UnifiedServicesConfig {
	return &UnifiedServicesConfig{
		Services: []ServiceConfig{{ID: "test-service", RetryConfig: m.retryConfig}},
	}
}

func (m *capabilityProtocol) AvailableHTTPEndpoints(
	_ context.Context,
	_ protocol.ServiceID,
	_ sharedtypes.RPCType,
	_ *http.Request,
) (protocol.EndpointAddrList, protocolobservations.Observations, error) {
	return m.endpoints, protocolobservations.Observations{}, nil
}

func (m *capabilityProtocol) BuildHTTPRequestContextForEndpoint(
	_ context.Context,
	_ protocol.ServiceID,
	_ protocol.EndpointAddr,
	_ sharedtypes.RPCType,
	_ *http.Request,
	_ bool,
) (ProtocolRequestContext, protocolobservations.Observations, error) {
	return m.protocolCtx, protocolobservations.Observations{}, nil
}

// recordingQoSContext records everything the gateway hands to the QoS layer so tests can
// assert the client-facing response is built from the backend's own bytes.
type recordingQoSContext struct {
	mu            sync.Mutex
	recorded      []qosUpdate
	protocolError error
}

type qosUpdate struct {
	endpointAddr protocol.EndpointAddr
	body         []byte
	statusCode   int
}

func (m *recordingQoSContext) updates() []qosUpdate {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]qosUpdate, len(m.recorded))
	copy(out, m.recorded)
	return out
}

func (m *recordingQoSContext) GetServicePayloads() []protocol.Payload {
	return []protocol.Payload{{
		Data:          `{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x0","0x1"]}`,
		RPCType:       sharedtypes.RPCType_JSON_RPC,
		JSONRPCMethod: "eth_getBalance",
	}}
}

func (m *recordingQoSContext) UpdateWithResponse(
	endpointAddr protocol.EndpointAddr,
	body []byte,
	httpStatusCode int,
	_ string,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recorded = append(m.recorded, qosUpdate{endpointAddr: endpointAddr, body: body, statusCode: httpStatusCode})
}

func (m *recordingQoSContext) SetProtocolError(err error) { m.protocolError = err }

func (m *recordingQoSContext) GetHTTPResponse() pathhttp.HTTPResponse { return nil }

func (m *recordingQoSContext) GetObservations() qos.Observations { return qos.Observations{} }

func (m *recordingQoSContext) GetEndpointSelector() protocol.EndpointSelector {
	return passthroughSelector{}
}

// passthroughSelector performs no QoS filtering — endpoint quality is not what these tests
// are about.
type passthroughSelector struct{}

func (passthroughSelector) Select(endpoints protocol.EndpointAddrList) (protocol.EndpointAddr, error) {
	if len(endpoints) == 0 {
		return "", errors.New("no endpoints")
	}
	return endpoints[0], nil
}

func (passthroughSelector) SelectMultiple(endpoints protocol.EndpointAddrList, _ uint) (protocol.EndpointAddrList, error) {
	return endpoints, nil
}

func (passthroughSelector) SelectMultipleWithArchival(endpoints protocol.EndpointAddrList, _ uint, _ bool) (protocol.EndpointAddrList, error) {
	return endpoints, nil
}
