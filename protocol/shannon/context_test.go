package shannon

import (
	"testing"

	"github.com/stretchr/testify/require"

	pathhttp "github.com/buildwithgrove/path/network/http"
)

// TestBackendServiceHTTPValidation tests the backend service HTTP status validation logic
// This tests the EnsureHTTPSuccess function integration in the Shannon protocol
func TestBackendServiceHTTPValidation(t *testing.T) {

	tests := []struct {
		name           string
		statusCode     int
		expectError    bool
		expectedErrMsg string
	}{
		{"successful_200_response", 200, false, ""},
		{"successful_201_response", 201, false, ""},
		{"successful_204_no_content", 204, false, ""},
		{"bad_request_400", 400, true, "400"},
		{"unauthorized_401", 401, true, "401"},
		{"not_found_404", 404, true, "404"},
		{"internal_server_error_500", 500, true, "500"},
		{"bad_gateway_502", 502, true, "502"},
		{"service_unavailable_503", 503, true, "503"},
		{"gateway_timeout_504", 504, true, "504"},
		{"edge_case_1xx_continue", 100, true, "100"},
		{"edge_case_3xx_redirect", 301, true, "301"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Test the HTTP validation logic directly used by Shannon protocol
			err := pathhttp.EnsureHTTPSuccess(tc.statusCode)

			if tc.expectError {
				require.Error(t, err)
				require.ErrorIs(t, err, pathhttp.ErrRelayEndpointHTTPError)
				require.Contains(t, err.Error(), tc.expectedErrMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestHTTPValidationBoundaryValues tests edge cases for HTTP status validation
func TestHTTPValidationBoundaryValues(t *testing.T) {
	boundaryTests := []struct {
		name        string
		statusCode  int
		expectError bool
	}{
		{"lowest_2xx_boundary", 200, false},
		{"highest_2xx_boundary", 299, false},
		{"just_below_2xx", 199, true},
		{"just_above_2xx", 300, true},
		{"zero_status", 0, true},
		{"negative_status", -1, true},
		{"very_high_status", 999, true},
	}
	
	for _, tc := range boundaryTests {
		t.Run(tc.name, func(t *testing.T) {
			err := pathhttp.EnsureHTTPSuccess(tc.statusCode)
			
			if tc.expectError {
				require.Error(t, err)
				require.ErrorIs(t, err, pathhttp.ErrRelayEndpointHTTPError)
			} else {
				require.NoError(t, err)
			}
		})
	}
}