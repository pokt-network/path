package gateway

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddRelayMetadataHeaders_AlwaysPresent(t *testing.T) {
	tests := []struct {
		name                    string
		suppliersTried          []string
		archivalRequestDetected bool
		retryCount              int
		expectedHeaders         map[string]string
	}{
		{
			name:                    "early error - no suppliers, not archival",
			suppliersTried:          nil,
			archivalRequestDetected: false,
			retryCount:              0,
			expectedHeaders: map[string]string{
				"X-Archival-Request": "false",
				"X-Suppliers-Tried":  "",
				"X-Retry-Count":      "0",
			},
		},
		{
			name:                    "archival error - detected but no suppliers",
			suppliersTried:          nil,
			archivalRequestDetected: true,
			retryCount:              0,
			expectedHeaders: map[string]string{
				"X-Archival-Request": "true",
				"X-Suppliers-Tried":  "",
				"X-Retry-Count":      "0",
			},
		},
		{
			name:                    "retry with suppliers",
			suppliersTried:          []string{"supplier1", "supplier2"},
			archivalRequestDetected: true,
			retryCount:              1,
			expectedHeaders: map[string]string{
				"X-Archival-Request": "true",
				"X-Suppliers-Tried":  "supplier1,supplier2",
				"X-Retry-Count":      "1",
			},
		},
		{
			name:                    "multiple retries with many suppliers",
			suppliersTried:          []string{"pokt1abc", "pokt1def", "pokt1ghi"},
			archivalRequestDetected: false,
			retryCount:              2,
			expectedHeaders: map[string]string{
				"X-Archival-Request": "false",
				"X-Suppliers-Tried":  "pokt1abc,pokt1def,pokt1ghi",
				"X-Retry-Count":      "2",
			},
		},
		{
			name:                    "archival request with single supplier",
			suppliersTried:          []string{"pokt1supplier"},
			archivalRequestDetected: true,
			retryCount:              0,
			expectedHeaders: map[string]string{
				"X-Archival-Request": "true",
				"X-Suppliers-Tried":  "pokt1supplier",
				"X-Retry-Count":      "0",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create minimal requestContext with test logger
			logger := polylog.Ctx(context.Background())
			rc := &requestContext{
				suppliersTried:          tt.suppliersTried,
				archivalRequestDetected: tt.archivalRequestDetected,
				retryCount:              tt.retryCount,
				logger:                  logger,
			}

			// Create response recorder
			w := httptest.NewRecorder()

			// Call addRelayMetadataHeaders
			rc.addRelayMetadataHeaders(w)

			// Verify all expected headers present
			for header, expected := range tt.expectedHeaders {
				actual := w.Header().Get(header)
				require.Equal(t, expected, actual, "header %s mismatch", header)
			}
		})
	}
}

func TestIsDeceptiveResponsePattern(t *testing.T) {
	tests := []struct {
		reason   string
		expected bool
	}{
		{"jsonrpc_invalid_empty_array", true},
		{"jsonrpc_empty_object_result", true},
		{"rest_protocol_mismatch", true},
		{"cometbft_invalid_empty_array", true},
		{"http_5xx", false},
		{"jsonrpc_success", false},
		{"rest_error_field", false},
		{"empty_response", false},
	}

	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			assert.Equal(t, tt.expected, isDeceptiveResponsePattern(tt.reason))
		})
	}
}
