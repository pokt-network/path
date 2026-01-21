package http

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/buildwithgrove/path/network/concurrency"
)

func TestEnsureHTTPSuccess(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		expectError bool
	}{
		// 2xx Success codes - should pass
		{
			name:        "200 OK",
			statusCode:  http.StatusOK,
			expectError: false,
		},
		{
			name:        "201 Created",
			statusCode:  http.StatusCreated,
			expectError: false,
		},
		{
			name:        "202 Accepted",
			statusCode:  http.StatusAccepted,
			expectError: false,
		},
		{
			name:        "204 No Content",
			statusCode:  http.StatusNoContent,
			expectError: false,
		},
		{
			name:        "299 Last 2xx",
			statusCode:  299,
			expectError: false,
		},
		// Non-2xx codes - should fail
		{
			name:        "100 Continue",
			statusCode:  http.StatusContinue,
			expectError: true,
		},
		{
			name:        "300 Multiple Choices",
			statusCode:  http.StatusMultipleChoices,
			expectError: true,
		},
		{
			name:        "301 Moved Permanently",
			statusCode:  http.StatusMovedPermanently,
			expectError: true,
		},
		{
			name:        "400 Bad Request",
			statusCode:  http.StatusBadRequest,
			expectError: true,
		},
		{
			name:        "401 Unauthorized",
			statusCode:  http.StatusUnauthorized,
			expectError: true,
		},
		{
			name:        "403 Forbidden",
			statusCode:  http.StatusForbidden,
			expectError: true,
		},
		{
			name:        "404 Not Found",
			statusCode:  http.StatusNotFound,
			expectError: true,
		},
		{
			name:        "429 Too Many Requests",
			statusCode:  http.StatusTooManyRequests,
			expectError: true,
		},
		{
			name:        "500 Internal Server Error",
			statusCode:  http.StatusInternalServerError,
			expectError: true,
		},
		{
			name:        "502 Bad Gateway",
			statusCode:  http.StatusBadGateway,
			expectError: true,
		},
		{
			name:        "503 Service Unavailable",
			statusCode:  http.StatusServiceUnavailable,
			expectError: true,
		},
		{
			name:        "504 Gateway Timeout",
			statusCode:  http.StatusGatewayTimeout,
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := EnsureHTTPSuccess(tc.statusCode)
			if tc.expectError {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrRelayEndpointHTTPError)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestReadAndValidateResponse_Integration tests the complete HTTP response validation flow
func TestReadAndValidateResponse_Integration(t *testing.T) {
	// Create a minimal HTTP client for testing
	client := &HTTPClientWithDebugMetrics{
		bufferPool: concurrency.NewBufferPool(1024), // 1KB max buffer size
	}

	tests := []struct {
		name           string
		statusCode     int
		body           string
		expectError    bool
		expectedErrMsg string
	}{
		{
			name:        "successful response with JSON",
			statusCode:  http.StatusOK,
			body:        `{"result":"success","data":{"value":42}}`,
			expectError: false,
		},
		{
			name:        "successful response with empty body",
			statusCode:  http.StatusNoContent,
			body:        "",
			expectError: false,
		},
		{
			name:           "bad request error",
			statusCode:     http.StatusBadRequest,
			body:           `{"error":"invalid request"}`,
			expectError:    true,
			expectedErrMsg: "400",
		},
		{
			name:           "internal server error",
			statusCode:     http.StatusInternalServerError,
			body:           `{"error":"server error"}`,
			expectError:    true,
			expectedErrMsg: "500",
		},
		{
			name:           "bad gateway error",
			statusCode:     http.StatusBadGateway,
			body:           "<html><body>502 Bad Gateway</body></html>",
			expectError:    true,
			expectedErrMsg: "502",
		},
		{
			name:           "service unavailable",
			statusCode:     http.StatusServiceUnavailable,
			body:           "Service temporarily unavailable",
			expectError:    true,
			expectedErrMsg: "503",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create mock HTTP response
			resp := &http.Response{
				StatusCode: tc.statusCode,
				Body:       io.NopCloser(bytes.NewBufferString(tc.body)),
			}

			// Test the validation function
			responseBody, err := client.readAndValidateResponse(resp)

			if tc.expectError {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrRelayEndpointHTTPError)
				require.Contains(t, err.Error(), tc.expectedErrMsg)
				require.Nil(t, responseBody)
			} else {
				require.NoError(t, err)
				require.NotNil(t, responseBody)
				require.Equal(t, tc.body, string(responseBody))
			}
		})
	}
}

// TestReadAndValidateResponse_ErrorCases tests edge cases and error conditions
func TestReadAndValidateResponse_ErrorCases(t *testing.T) {
	client := &HTTPClientWithDebugMetrics{
		bufferPool: concurrency.NewBufferPool(10), // Very small buffer for testing limits
	}

	tests := []struct {
		name           string
		setupResponse  func() *http.Response
		expectError    bool
		expectedErrMsg string
	}{
		{
			name: "large response body exceeding buffer",
			setupResponse: func() *http.Response {
				largeBody := bytes.Repeat([]byte("x"), 2048) // 2KB body, 10B buffer
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBuffer(largeBody)),
				}
			},
			expectError:    true,
			expectedErrMsg: "failed to read response body",
		},
		{
			name: "body read error",
			setupResponse: func() *http.Response {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       &errorReader{},
				}
			},
			expectError:    true,
			expectedErrMsg: "failed to read response body",
		},
		{
			name: "successful large response within buffer limits",
			setupResponse: func() *http.Response {
				smallBody := []byte("small response")
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBuffer(smallBody)),
				}
			},
			expectError: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.setupResponse()
			responseBody, err := client.readAndValidateResponse(resp)

			if tc.expectError {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedErrMsg)
				require.Nil(t, responseBody)
			} else {
				require.NoError(t, err)
				require.NotNil(t, responseBody)
			}
		})
	}
}

// TestEnsureHTTPSuccess_ErrorWrapping tests error wrapping behavior
func TestEnsureHTTPSuccess_ErrorWrapping(t *testing.T) {
	testCodes := []int{400, 500, 502, 503}

	for _, code := range testCodes {
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			err := EnsureHTTPSuccess(code)

			require.Error(t, err)
			require.ErrorIs(t, err, ErrRelayEndpointHTTPError)

			// Verify the error message contains the status code
			require.Contains(t, err.Error(), fmt.Sprintf("%d", code))
		})
	}
}

// errorReader is a mock io.ReadCloser that always returns an error
type errorReader struct{}

func (e *errorReader) Read([]byte) (int, error) {
	return 0, fmt.Errorf("mock read error")
}

func (e *errorReader) Close() error {
	return nil
}
