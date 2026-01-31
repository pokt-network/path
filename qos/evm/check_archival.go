package evm

import (
	"errors"
	"time"
)

// checkArchivalTTL is how long an archival status from health checks remains valid.
// Health checks run periodically, so this should be longer than the health check interval.
const checkArchivalTTL = 30 * time.Minute

var (
	// errEndpointNotArchival is returned when an endpoint has not been marked as archival-capable.
	errEndpointNotArchival = errors.New("endpoint has not been marked as archival-capable by health checks")
)

// endpointCheckArchival tracks the archival capability status of an endpoint.
// Archival status is determined by external health checks that verify the endpoint
// can serve historical blockchain data.
type endpointCheckArchival struct {
	// isArchival indicates whether the endpoint has passed archival health checks.
	// Set to true when health check confirms endpoint can serve historical data.
	isArchival bool

	// expiresAt indicates when the archival status should be re-validated.
	// After expiry, the endpoint will need to pass health checks again to be
	// considered archival-capable.
	expiresAt time.Time
}

// isValid returns true if the endpoint has been marked as archival-capable
// and the status has not expired.
func (e *endpointCheckArchival) isValid() bool {
	if !e.isArchival {
		return false
	}
	// If expiresAt is zero, the status was never set properly
	if e.expiresAt.IsZero() {
		return false
	}
	// Check if the status has expired
	return time.Now().Before(e.expiresAt)
}
