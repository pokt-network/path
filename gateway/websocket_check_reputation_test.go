package gateway

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/metrics"
	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
)

const (
	wsCheckService  protocol.ServiceID = "poly"
	wsCheckName                        = "ws-block-number"
	wsCheckURL                         = "https://relay.example.com/v1"
	wsCheckEndpoint                    = protocol.EndpointAddr("pokt1supplier-" + wsCheckURL)
)

// websocketCheckProtocol stands in for the Shannon protocol on the websocket check path.
//
// ApplyWebSocketObservations deliberately MIRRORS the real implementation
// (recordSignalFromWebsocketConnectionObservation): one reputation signal per connection
// observation, severity derived from the observation's error type. Both it and the executor
// write into the SAME recorder, which is what makes a double write detectable — counting
// recorded signals counts writers.
type websocketCheckProtocol struct {
	*mockProtocolForRetry

	// obs is what CheckWebsocketConnection reports. nil means "the check passed": the real
	// protocol returns no observation on success, on both the handshake-only and the probe path.
	obs *protocolobservations.Observations

	rep *recordedSignalReputationSvc

	checksDispatched atomic.Int32
}

func (m *websocketCheckProtocol) CheckWebsocketConnection(
	_ context.Context,
	_ protocol.ServiceID,
	_ protocol.EndpointAddr,
	_ protocol.WebsocketProbe,
) ([]byte, *protocolobservations.Observations) {
	m.checksDispatched.Add(1)
	return nil, m.obs
}

func (m *websocketCheckProtocol) ApplyWebSocketObservations(obs *protocolobservations.Observations, isHealthCheck bool) error {
	if obs == nil || obs.GetShannon() == nil {
		return nil
	}
	for _, reqObs := range obs.GetShannon().GetObservations() {
		connObs := reqObs.GetWebsocketConnectionObservation()
		if connObs == nil {
			continue
		}
		signal := reputation.NewMajorErrorSignal("connection_error", 0)
		if connObs.GetErrorType() == protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNSPECIFIED {
			signal = reputation.NewSuccessSignal(0)
		}
		// Mirrors the real implementation, which stamps the caller's verdict onto the
		// signal so the volume-independent rate detectors can exclude probe results.
		signal.IsHealthCheck = isHealthCheck
		if err := m.rep.RecordSignal(context.Background(), reputation.EndpointKey{}, signal); err != nil {
			return err
		}
	}
	return nil
}

// failedWebsocketObservation is the shape the protocol really returns for a failed check:
// a request-level error plus a connection observation carrying the classified endpoint error.
func failedWebsocketObservation(details string) *protocolobservations.Observations {
	errType := protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_WEBSOCKET_CONNECTION_FAILED
	return &protocolobservations.Observations{
		Shannon: &protocolobservations.ShannonObservationsList{
			Observations: []*protocolobservations.ShannonRequestObservations{{
				ServiceId: string(wsCheckService),
				RequestError: &protocolobservations.ShannonRequestError{
					ErrorType:    protocolobservations.ShannonRequestErrorType_SHANNON_REQUEST_ERROR_INTERNAL,
					ErrorDetails: details,
				},
				ObservationData: &protocolobservations.ShannonRequestObservations_WebsocketConnectionObservation{
					WebsocketConnectionObservation: &protocolobservations.ShannonWebsocketConnectionObservation{
						Supplier:     "pokt1supplier",
						EndpointUrl:  wsCheckURL,
						ErrorType:    &errType,
						ErrorDetails: &details,
					},
				},
			}},
		},
	}
}

func websocketCheckConfig() HealthCheckConfig {
	return HealthCheckConfig{
		Name:    wsCheckName,
		Type:    HealthCheckTypeWebSocket,
		Body:    `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`,
		Timeout: time.Second,
	}
}

// newWebsocketCheckExecutor wires an executor whose only check is a websocket check, with the
// executor's reputation service and the protocol's observation writer sharing one recorder.
func newWebsocketCheckExecutor(t *testing.T, obs *protocolobservations.Observations) (
	*HealthCheckExecutor,
	*websocketCheckProtocol,
	*recordedSignalReputationSvc,
	*ServiceHealthCheckConfig,
) {
	t.Helper()

	rep := &recordedSignalReputationSvc{}
	proto := &websocketCheckProtocol{
		mockProtocolForRetry: &mockProtocolForRetry{},
		obs:                  obs,
		rep:                  rep,
	}

	svcConfig := &ServiceHealthCheckConfig{
		ServiceID: wsCheckService,
		Checks:    []HealthCheckConfig{websocketCheckConfig()},
	}

	executor := NewHealthCheckExecutor(HealthCheckExecutorConfig{
		Config:        &ActiveHealthChecksConfig{Enabled: true, Local: []ServiceHealthCheckConfig{*svcConfig}},
		ReputationSvc: rep,
		Logger:        polyzero.NewLogger(),
		Protocol:      proto,
		MaxWorkers:    4,
	})
	t.Cleanup(executor.Stop)

	return executor, proto, rep, svcConfig
}

// healthCheckMetric reads the current value of path_health_check_status_total for a websocket
// check outcome, using the same label derivation the executor uses so the test cannot drift
// from it.
func healthCheckMetric(signal string) float64 {
	domain, err := shannonmetrics.ExtractDomainOrHost(string(wsCheckEndpoint))
	if err != nil {
		domain = shannonmetrics.ErrDomain
	}
	rpcType := metrics.NormalizeRPCType(sharedtypes.RPCType_WEBSOCKET.String())
	return testutil.ToFloat64(
		metrics.HealthCheckStatus.WithLabelValues(domain, rpcType, string(wsCheckService), wsCheckName, signal),
	)
}

// A websocket check must not be dispatched to an endpoint that advertises no websocket URL.
//
// Dispatching anyway charged the endpoint a MAJOR error (-10) on its websocket reputation key
// for lacking a capability it never claimed: the protocol fails with "selected endpoint does
// not support websocket RPC type", which classifies as WEBSOCKET_CONNECTION_FAILED. Roughly 12
// of 50 endpoints on one websocket-enabled service are json_rpc-only, and one pod logged 39 of
// these in six minutes.
//
// The skip must be silent in BOTH directions: no reputation signal, and no metric — neither a
// pass nor a failure. Recording it as a pass would be worse than the bug, since the endpoint
// would read healthy on a capability it does not have.
func Test_WebsocketCheck_EndpointWithoutWebsocketURLIsNeitherPassedNorFailed(t *testing.T) {
	c := require.New(t)

	executor, proto, rep, svcConfig := newWebsocketCheckExecutor(t, failedWebsocketObservation("unused"))

	okBefore := healthCheckMetric(metrics.SignalOK)
	failBefore := healthCheckMetric(metrics.SignalMajorError)

	// hasWebsocketURL=false — the endpoint advertises no websocket URL.
	executor.runEndpointChecks(context.Background(), wsCheckService, wsCheckEndpoint, svcConfig, false, true, false)
	executor.wsPool.StopAndWait() // drain: websocket checks run off the cycle

	c.Zero(proto.checksDispatched.Load(),
		"a websocket check was dispatched to an endpoint with no websocket URL")
	c.Empty(rep.RecordedSignals(),
		"a skipped websocket check must record no reputation signal at all")
	c.Equal(okBefore, healthCheckMetric(metrics.SignalOK),
		"a skip must not be recorded as a pass")
	c.Equal(failBefore, healthCheckMetric(metrics.SignalMajorError),
		"a skip must not be recorded as a failure")
}

// A failing websocket check must return an error and be recorded as a failure.
//
// ExecuteWebSocketCheckViaProtocol used to end in `return latency, nil` unconditionally, so
// recordCheckResult always took its success branch: EVERY websocket check recorded a recovery
// success and a reputation_signal="ok" metric regardless of outcome. That is why
// path_health_check_status_total{rpc_type="websocket"} read 100% ok in production while these
// checks were failing, and why the metric was useless as evidence.
func Test_WebsocketCheck_FailureIsReturnedAndRecordedAsAFailure(t *testing.T) {
	c := require.New(t)

	const details = "error creating Websocket connection: failed to connect to websocket endpoint: dial tcp: i/o timeout"
	executor, _, rep, _ := newWebsocketCheckExecutor(t, failedWebsocketObservation(details))
	check := websocketCheckConfig()

	latency, err := executor.ExecuteWebSocketCheckViaProtocol(
		context.Background(), wsCheckService, wsCheckEndpoint, check, 0,
	)
	c.Error(err, "a failed websocket check must return a non-nil error")
	c.Contains(err.Error(), "WEBSOCKET_CONNECTION_FAILED",
		"the classified error type must survive into the returned error")
	c.Contains(err.Error(), details,
		"the observation's error details must survive so the log and metric are diagnosable")

	// The observation path wrote the one reputation signal, with the severity it derived from
	// the real failure.
	signals := rep.RecordedSignals()
	c.Len(signals, 1)
	c.Equal(reputation.SignalTypeMajorError, signals[0].Type)

	okBefore := healthCheckMetric(metrics.SignalOK)
	failBefore := healthCheckMetric(metrics.SignalMajorError)

	executor.recordCheckResult(context.Background(), wsCheckService, wsCheckEndpoint, check, err, latency)

	c.Equal(failBefore+1, healthCheckMetric(metrics.SignalMajorError),
		"a failed websocket check must increment the failure metric")
	c.Equal(okBefore, healthCheckMetric(metrics.SignalOK),
		"a failed websocket check must not increment the ok metric")
	c.Len(rep.RecordedSignals(), 1,
		"recordCheckResult must record the METRIC ONLY for websocket checks - reputation belongs to the observation path")
}

// THE regression test. A websocket check must record EXACTLY ONE reputation signal.
//
// Two writers exist on this path: the observation path (ApplyWebSocketObservations ->
// recordSignalFromWebsocketConnectionObservation, severity from the real error classification)
// and recordCheckResult (severity from the rule's configured reputation_signal, which SILENTLY
// DEFAULTS to minor_error when unset — and the websocket rules are hot-loaded from outside this
// repo, so that default cannot be audited here). Only one may write.
//
// This test fails if either writer is reintroduced: both land in the same recorder.
func Test_WebsocketCheck_RecordsExactlyOneReputationSignal(t *testing.T) {
	t.Run("failing check - the observation path owns the severity", func(t *testing.T) {
		c := require.New(t)

		executor, _, rep, svcConfig := newWebsocketCheckExecutor(t, failedWebsocketObservation("i/o timeout"))

		executor.runEndpointChecks(context.Background(), wsCheckService, wsCheckEndpoint, svcConfig, false, true, true)
		executor.wsPool.StopAndWait()

		signals := rep.RecordedSignals()
		c.Len(signals, 1,
			"a failing websocket check must record exactly one reputation signal; two means both "+
				"the check path and the observation path wrote it")
		c.Equal(reputation.SignalTypeMajorError, signals[0].Type,
			"the single signal must come from the observation path's real error classification, "+
				"not from mapSignalType's silent minor_error default")
	})

	t.Run("passing check - recorded once, by the check path", func(t *testing.T) {
		c := require.New(t)

		// nil observations: what the protocol really returns on success.
		executor, proto, rep, svcConfig := newWebsocketCheckExecutor(t, nil)

		executor.runEndpointChecks(context.Background(), wsCheckService, wsCheckEndpoint, svcConfig, false, true, true)
		executor.wsPool.StopAndWait()

		c.Equal(int32(1), proto.checksDispatched.Load())

		signals := rep.RecordedSignals()
		c.Len(signals, 1,
			"a passing websocket check must record exactly one reputation signal")
		c.Equal(reputation.SignalTypeRecoverySuccess, signals[0].Type,
			"the protocol emits NO observation on success, so the pass is recorded here; "+
				"without it a penalized websocket endpoint would have no health-check route back")
	})
}

// Test_WebsocketCheck_StampsSignalsAsHealthCheck covers the websocket half of the
// probe-contamination fix.
//
// Both reputation writers on this path must stamp the signal, and they are reached by
// different routes: a FAILING check goes out through ApplyWebSocketObservations, a PASSING
// one is recorded directly by the executor because the protocol emits no observation on
// success. Fixing only one leaves half of every websocket service's probe volume feeding
// the volume-independent rate detectors, which is what benches an endpoint that no user
// ever complained about.
func Test_WebsocketCheck_StampsSignalsAsHealthCheck(t *testing.T) {
	t.Run("failing check - stamped on the observation path", func(t *testing.T) {
		c := require.New(t)

		executor, _, rep, svcConfig := newWebsocketCheckExecutor(t, failedWebsocketObservation("i/o timeout"))

		executor.runEndpointChecks(context.Background(), wsCheckService, wsCheckEndpoint, svcConfig, false, true, true)
		executor.wsPool.StopAndWait()

		signals := rep.RecordedSignals()
		c.Len(signals, 1)
		c.True(signals[0].IsHealthCheck,
			"a websocket probe failure must not be able to trip the rate detectors on its own")
	})

	t.Run("passing check - stamped by the executor", func(t *testing.T) {
		c := require.New(t)

		executor, _, rep, svcConfig := newWebsocketCheckExecutor(t, nil)

		executor.runEndpointChecks(context.Background(), wsCheckService, wsCheckEndpoint, svcConfig, false, true, true)
		executor.wsPool.StopAndWait()

		signals := rep.RecordedSignals()
		c.Len(signals, 1)
		c.True(signals[0].IsHealthCheck,
			"a probe success must be excluded from the rate detectors too: the EWMAs are ratios, "+
				"so leaving successes in the denominator while excluding failures biases them")
	})
}

// websocketObservationError is the whole basis of the failure verdict, so pin its contract:
// only a real failure produces an error, and the error stays diagnosable.
func Test_websocketObservationError(t *testing.T) {
	c := require.New(t)

	c.NoError(websocketObservationError(nil), "no observation means the check passed")
	c.NoError(websocketObservationError(&protocolobservations.Observations{}),
		"an observation set with no Shannon observations is not a failure")

	// A connection observation with no error type is a success, even though the field exists.
	c.NoError(websocketObservationError(&protocolobservations.Observations{
		Shannon: &protocolobservations.ShannonObservationsList{
			Observations: []*protocolobservations.ShannonRequestObservations{{
				ServiceId: string(wsCheckService),
				ObservationData: &protocolobservations.ShannonRequestObservations_WebsocketConnectionObservation{
					WebsocketConnectionObservation: &protocolobservations.ShannonWebsocketConnectionObservation{
						Supplier:    "pokt1supplier",
						EndpointUrl: wsCheckURL,
					},
				},
			}},
		},
	}))

	err := websocketObservationError(failedWebsocketObservation("dial tcp: i/o timeout"))
	c.Error(err)
	c.Contains(err.Error(), "WEBSOCKET_CONNECTION_FAILED")
	c.Contains(err.Error(), "dial tcp: i/o timeout")

	// A request-level error alone (a PATH-side/setup failure with no endpoint error type) still
	// means the check did not pass.
	requestErrorOnly := &protocolobservations.Observations{
		Shannon: &protocolobservations.ShannonObservationsList{
			Observations: []*protocolobservations.ShannonRequestObservations{{
				ServiceId: string(wsCheckService),
				RequestError: &protocolobservations.ShannonRequestError{
					ErrorType:    protocolobservations.ShannonRequestErrorType_SHANNON_REQUEST_ERROR_INTERNAL,
					ErrorDetails: "failed to get pre-selected endpoint",
				},
			}},
		},
	}
	err = websocketObservationError(requestErrorOnly)
	c.Error(err)
	c.Contains(err.Error(), "failed to get pre-selected endpoint")
}

// The metric label for a FAILED websocket check must never read "ok".
//
// mapReputationSignalToMetricSignal returns SignalOK for an unset reputation_signal, and unset
// is the common case because the websocket rules are hot-loaded from outside this repo. Using
// it directly for failures would rebuild the exact blind spot this work closed.
func Test_websocketFailureMetricSignal_NeverReportsAFailureAsOK(t *testing.T) {
	c := require.New(t)

	c.Equal(metrics.SignalOK, mapReputationSignalToMetricSignal(""),
		"pinning the trap: the generic mapper calls an unset signal 'ok'")

	c.Equal(metrics.SignalMajorError, websocketFailureMetricSignal(""),
		"an unset rule signal must fall back to the severity the observation path charges")
	c.Equal(metrics.SignalCriticalError, websocketFailureMetricSignal("critical_error"),
		"an explicitly configured severity must still win")
}
