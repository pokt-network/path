package protocol

import "errors"

// ErrEndpointUnavailable indicates that a selected endpoint is no longer available.
// This can happen due to:
// - Session rollover between endpoint selection and context building
// - Endpoint filtered out (low reputation) due to an observation while selection logic was running
// - Race condition between endpoint listing and selection
//
// Callers should handle this error by re-selecting from available endpoints.
var ErrEndpointUnavailable = errors.New("selected endpoint is not available")
