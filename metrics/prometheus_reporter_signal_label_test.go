package metrics

import (
	"testing"

	"github.com/stretchr/testify/require"

	protocolobs "github.com/pokt-network/path/observation/protocol"
)

// The observation pipeline's reputation_signal label is re-derived from the observation's
// error type, not read from the signal reputation actually recorded. Every protocol path
// that records an error with type UNSPECIFIED pairs it with a SUCCESS signal — capability
// limitation, over-servicing, session mismatch — so labelling that pair "major_error"
// reports a penalty that was never applied. Seen in production the moment a new capability
// phrase was catalogued: the service's observation pipeline lit up with major_error while
// path_relays_total (labelled from the real signal) and the mean score said no penalty.
func TestGetReputationSignalFromEndpoint_UnspecifiedErrorIsNoFault(t *testing.T) {
	pmr := &PrometheusMetricsReporter{}

	require.Equal(t, SignalOK,
		pmr.getReputationSignalFromEndpoint(true, protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNSPECIFIED, 10),
		"an error recorded with an UNSPECIFIED type is a no-fault error and must not read as a penalty")

	// Controls: a typed error keeps its severity, and no error at all is still ok.
	require.Equal(t, SignalMajorError,
		pmr.getReputationSignalFromEndpoint(true, protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_TIMEOUT, 10))
	require.Equal(t, SignalOK,
		pmr.getReputationSignalFromEndpoint(false, protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNSPECIFIED, 10))
}
