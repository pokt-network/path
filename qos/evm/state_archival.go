package evm

// isArchivalCapable returns an error if the endpoint is not archival-capable.
// The archival capability is determined by external health checks.
//
// This function is called during endpoint filtering when a request requires archival data.
// When requiresArchival is true in endpoint selection, only endpoints that pass this
// check will be considered for the request.
func isArchivalCapable(check endpointCheckArchival) error {
	if !check.isValid() {
		return errEndpointNotArchival
	}
	return nil
}
