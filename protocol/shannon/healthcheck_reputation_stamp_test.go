package shannon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
)

// signalCapture records every signal the code under test hands to reputation, so a test can
// assert on what the PRODUCTION CALLER actually delivers rather than on a local variable.
//
// The embedded nil interface makes any method the code unexpectedly depends on panic rather
// than silently return a zero value.
type signalCapture struct {
	reputation.ReputationService

	mu      sync.Mutex
	signals []reputation.Signal
}

func (c *signalCapture) RecordSignal(_ context.Context, _ reputation.EndpointKey, signal reputation.Signal) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.signals = append(c.signals, signal)
	return nil
}

func (c *signalCapture) KeyBuilderForService(_ protocol.ServiceID) reputation.KeyBuilder {
	return reputation.NewKeyBuilder(reputation.KeyGranularityEndpoint)
}

func (c *signalCapture) GetLatencyConfigForService(_ protocol.ServiceID) reputation.LatencyConfig {
	return reputation.LatencyConfig{}
}

func (c *signalCapture) recorded() []reputation.Signal {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]reputation.Signal, len(c.signals))
	copy(out, c.signals)
	return out
}

// newStampTestContext builds a requestContext wired to a signal capture, with a fallback
// endpoint standing in as the selected endpoint (it satisfies the full `endpoint` interface
// with no session or chain state required).
func newStampTestContext(t *testing.T, cap *signalCapture) *requestContext {
	t.Helper()
	rc := &requestContext{
		logger:            testLogger(),
		context:           context.Background(),
		serviceID:         protocol.ServiceID("solana"),
		reputationService: cap,
		selectedEndpoint: fallbackEndpoint{
			defaultURL: "https://probe.example.com/rpc",
		},
	}
	rc.currentRPCType.Store(int32(sharedtypes.RPCType_JSON_RPC))
	return rc
}

// TestHealthCheckRelay_StampsReputationSignal_ErrorPath is the fix-1 test for the relay
// path, and it asserts on the signal reputation RECEIVES — not on rc.isHealthCheck, which
// was already true and told us nothing.
//
// A health-check relay reaches reputation through this handler because the executor calls
// MarkAsHealthCheck() and then HandleServiceRequest(). The `isHealthCheck` flag was
// consulted only to skip the relay METRIC; the field doc said outright that it "does not
// affect reputation signals", so every probe failure fed the volume-independent rate
// detectors as though a user had experienced it.
func TestHealthCheckRelay_StampsReputationSignal_ErrorPath(t *testing.T) {
	cap := &signalCapture{}
	rc := newStampTestContext(t, cap)
	rc.MarkAsHealthCheck()

	_, _ = rc.handleEndpointError(time.Now(), protocol.Response{}, errors.New("connection refused"))

	sigs := cap.recorded()
	require.Len(t, sigs, 1, "the error handler must record exactly one reputation signal")
	require.True(t, sigs[0].IsHealthCheck,
		"a health-check relay's failure must arrive at reputation stamped, or the rate detectors will act on a probe result")
}

// TestUserRelay_DoesNotStampReputationSignal_ErrorPath is the control. Without it the test
// above passes on a change that stamps every signal unconditionally, which would make the
// rate detectors permanently inert — a far worse bug than the one being fixed.
func TestUserRelay_DoesNotStampReputationSignal_ErrorPath(t *testing.T) {
	cap := &signalCapture{}
	rc := newStampTestContext(t, cap)

	_, _ = rc.handleEndpointError(time.Now(), protocol.Response{}, errors.New("connection refused"))

	sigs := cap.recorded()
	require.Len(t, sigs, 1)
	require.False(t, sigs[0].IsHealthCheck,
		"control: user traffic must reach the rate detectors")
}

// TestHealthCheckObservations_StampReputationSignal is the fix-1 test for the SECOND path a
// probe reaches reputation by.
//
// One health check produces two reputation signals: the relay itself (above) and
// ApplyHTTPObservations, which the executor calls on the same relay's observations. Fixing
// only the relay path would have left half the probe volume feeding the EWMAs while looking
// fixed — the trap this repo has hit repeatedly, where one of several call sites is missed
// and the metric still reports success.
func TestHealthCheckObservations_StampReputationSignal(t *testing.T) {
	for _, tc := range []struct {
		name          string
		isHealthCheck bool
	}{
		{"health check observations are stamped", true},
		{"user traffic observations are not", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cap := &signalCapture{}
			p := &Protocol{logger: testLogger(), reputationService: cap}

			p.recordSignalFromObservation(
				protocol.ServiceID("solana"),
				&protocolobservations.ShannonEndpointObservation{
					Supplier:    "pokt1supplier",
					EndpointUrl: "https://probe.example.com/rpc",
				},
				tc.isHealthCheck,
			)

			sigs := cap.recorded()
			require.Len(t, sigs, 1)
			require.Equal(t, tc.isHealthCheck, sigs[0].IsHealthCheck,
				"the observation path must carry the caller's health-check verdict through to reputation")
		})
	}
}
