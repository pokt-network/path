package metrics

import (
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	protocolobs "github.com/pokt-network/path/observation/protocol"
)

// Test_RequestStatus_TransportFailureIsNotCountedAsSuccess is the regression test for a
// metric that reported the wrong operator as the healthy one.
//
// A relay that times out, is refused, or fails signature validation never receives an HTTP
// status from the backend, so the observation carries status 0. That was defaulted to 200,
// which counted every such failure as a success against the endpoint that produced it.
//
// Measured on solana 2026-08-18: an operator generating 242 relay errors/s — 5s timeouts,
// P95 relay latency 7.6s — reported 0/s non-200 on path_requests_total and read as ~95%
// successful, while an operator returning honest HTTP error codes in 50ms read as ~43%. The
// supplier-quality panel ranked them backwards.
func Test_RequestStatus_TransportFailureIsNotCountedAsSuccess(t *testing.T) {
	const serviceID = "test-transport-failure"

	timeout := protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_TIMEOUT
	reporter := &PrometheusMetricsReporter{Logger: polyzero.NewLogger()}

	// Drive the production recorder, not a hand-rolled label set: the defect was in how this
	// function derives the label, so a test that passes its own status proves nothing.
	reporter.processEndpointObservation(serviceID, &protocolobs.ShannonEndpointObservation{
		Supplier:    "pokt1timeout",
		EndpointUrl: "https://a001.op-alpha.example",
		// No EndpointBackendServiceHttpResponseStatusCode: the backend never answered.
		ErrorType: &timeout,
	}, 0, nil)

	require.Equal(t, float64(0), requestCount(t, serviceID, StatusCategorySuccess),
		"a relay that never received an HTTP status must not be counted as a success")
	require.Equal(t, float64(1), requestCount(t, serviceID, StatusCategoryError),
		"a transport failure must land on its own status category")
}

// Test_RequestStatus_MissingStatusWithoutErrorStaysSuccess keeps the other half of the
// status-0 case intact. Not every missing status is a failure — when no error is set the
// relay succeeded and the status simply was not recorded, and turning those into errors
// would swap one wrong number for another.
func Test_RequestStatus_MissingStatusWithoutErrorStaysSuccess(t *testing.T) {
	const serviceID = "test-missing-status-ok"

	reporter := &PrometheusMetricsReporter{Logger: polyzero.NewLogger()}
	reporter.processEndpointObservation(serviceID, &protocolobs.ShannonEndpointObservation{
		Supplier:    "pokt1quiet",
		EndpointUrl: "https://b001.op-beta.example",
		// Neither a status code nor an error.
	}, 0, nil)

	require.Equal(t, float64(1), requestCount(t, serviceID, StatusCategorySuccess))
	require.Equal(t, float64(0), requestCount(t, serviceID, StatusCategoryError))
}

// Test_RequestStatus_RealBackendStatusIsPreserved guards the path that was already correct:
// a backend that answers keeps its own status category, error set or not. An endpoint
// answering 500 is reachable; one that never answers is not, and collapsing the two would
// destroy the distinction this fix exists to expose.
func Test_RequestStatus_RealBackendStatusIsPreserved(t *testing.T) {
	const serviceID = "test-real-status"

	validationErr := protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RESPONSE_VALIDATION_ERR
	reporter := &PrometheusMetricsReporter{Logger: polyzero.NewLogger()}

	for _, tc := range []struct {
		name     string
		status   int32
		errType  *protocolobs.ShannonEndpointErrorType
		expected string
	}{
		{name: "429 from the backend", status: 429, expected: "4xx"},
		{name: "500 from the backend", status: 500, expected: "5xx"},
		{name: "200 answered but validation failed", status: 200, errType: &validationErr, expected: StatusCategorySuccess},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := serviceID + "-" + tc.name
			status := tc.status
			reporter.processEndpointObservation(svc, &protocolobs.ShannonEndpointObservation{
				Supplier:    "pokt1answers",
				EndpointUrl: "https://c001.op-gamma.example",
				EndpointBackendServiceHttpResponseStatusCode: &status,
				ErrorType: tc.errType,
			}, 0, nil)

			require.Equal(t, float64(1), requestCount(t, svc, tc.expected))
			require.Equal(t, float64(0), requestCount(t, svc, StatusCategoryError),
				"an endpoint that returned an HTTP status must not be filed under 'no status at all'")
		})
	}
}

// requestCount reads one path_requests_total child back out of the registry.
//
// Gather() only reports children that exist, so an absent series reads as 0 — which is what
// the assertions above want, and why every test uses its own service_id rather than sharing
// a counter across cases.
func requestCount(t *testing.T, serviceID, statusCode string) float64 {
	t.Helper()

	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)

	for _, family := range families {
		if family.GetName() != MetricPrefix+"requests_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			if labelValue(metric, LabelServiceID) == serviceID && labelValue(metric, LabelStatusCode) == statusCode {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func labelValue(metric *dto.Metric, name string) string {
	for _, label := range metric.GetLabel() {
		if label.GetName() == name {
			return label.GetValue()
		}
	}
	return ""
}
