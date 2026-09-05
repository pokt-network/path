package gateway

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// When a hedge race produces no usable response, the batch item falls through to the normal
// request path. That fallthrough must build a FRESH protocol context.
//
// race() hands the primary's protocol context a DETACHED parent (hedge.go: detachedHedgeCtx
// + SetParentContext) and cancels it on every one of its exit paths (six call sites of
// cancelBranches). Reusing that context on the fallthrough meant the relay failed instantly
// with "context canceled" — it never reached the endpoint at all — and the batch path then
// stamped that on whichever endpoint happened to be primary as a transport fault. Measured
// 2026-09-05 on one batch-heavy service, every circuit break carried exactly that signature.
//
// The assertion is on ORDER, not on a count: the last relay must be preceded by a build.
// A count alone passes on the buggy code, because the race builds a context for the hedge
// endpoint too.
func TestProcessSinglePayloadWithRetry_HedgeFallthroughRebuildsProtocolContext(t *testing.T) {
	c := require.New(t)

	const serviceID = "test-service"
	primary := protocol.EndpointAddr("pokt1a-https://a.example.com")
	hedge := protocol.EndpointAddr("pokt1b-https://b.example.net")

	var mu sync.Mutex
	var trace []string
	record := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		trace = append(trace, event)
	}

	protocolCtx := &transportErrorProtocolCtx{
		endpoint: primary,
		onRelay:  func() { record("relay") },
	}
	proto := &pluggableCtxProtocol{
		mockProtocolForRetry: mockProtocolForRetry{
			retryConfig: &ServiceRetryConfig{
				Enabled:    boolPtr(true),
				MaxRetries: intPtr(0),
				HedgeDelay: durationPtr(time.Millisecond),
			},
		},
		endpoints:   protocol.EndpointAddrList{primary, hedge},
		protocolCtx: protocolCtx,
		onBuild:     func() { record("build") },
	}

	rc := &requestContext{
		logger:              polyzero.NewLogger(),
		context:             context.Background(),
		serviceID:           serviceID,
		qosCtx:              &recordingQoSContext{},
		circuitBreaker:      NewDomainCircuitBreaker(nil, testCircuitBreakerLogger()),
		protocol:            proto,
		originalHTTPRequest: httptestRequest(),
	}

	payload := protocol.Payload{
		Data:          `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`,
		RPCType:       sharedtypes.RPCType_JSON_RPC,
		JSONRPCMethod: "eth_blockNumber",
	}
	_, err := rc.processSinglePayloadWithRetry(payload, 0, 2, sharedtypes.RPCType_JSON_RPC, rc.logger)
	c.Error(err, "every relay in this test fails at the transport layer")

	mu.Lock()
	defer mu.Unlock()
	c.GreaterOrEqual(len(trace), 2, "expected at least one build and one relay, got %v", trace)
	c.Equal("relay", trace[len(trace)-1], "the fallthrough must end in a relay: %v", trace)
	c.Equal("build", trace[len(trace)-2],
		"the fallthrough relay must use a freshly built protocol context, not the one the "+
			"hedge race cancelled: %v", trace)
}
