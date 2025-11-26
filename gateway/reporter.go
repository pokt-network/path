package gateway

import (
	"github.com/pokt-network/path/observation"
)

// RequestResponseReporter defines the interface for reporting observations with respect to
// a request, its corresponding response, and the set of events to any interested entity.
// Examples of reporters include:
//   - MetricsReporter: exports metrics based on the observations
//   - DataReporter: exports observations to external components (e.g.Messaging system or Database)
type RequestResponseReporter interface {
	// Publish exports observations made on a service request and response(s), to the external component used by the corresponding implementation.
	Publish(*observation.RequestResponseObservations)
}
