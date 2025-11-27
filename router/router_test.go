package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"

	"github.com/pokt-network/path/config"
	"github.com/pokt-network/path/health"
)

func newTestRouter(t *testing.T) (*router, *MockgatewayHandler, *httptest.Server) {
	ctrl := gomock.NewController(t)
	mockGateway := NewMockgatewayHandler(ctrl)
	mockDisqualifiedEndpointsReporter := NewMockdisqualifiedEndpointsReporter(ctrl)

	r := NewRouter(
		polyzero.NewLogger(),
		mockGateway,
		mockDisqualifiedEndpointsReporter,
		&health.Checker{},
		config.RouterConfig{},
	)
	ts := httptest.NewServer(r.mux)
	t.Cleanup(ts.Close)

	return r, mockGateway, ts
}

func Test_handleHealthz(t *testing.T) {
	tests := []struct {
		name           string
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "should return 200 with status ok",
			expectedStatus: http.StatusOK,
			expectedBody:   `{"status":"ready","imageTag":"development"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := require.New(t)

			_, _, ts := newTestRouter(t)

			// Create request
			req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/healthz", ts.URL), nil)
			c.NoError(err)

			// Perform request
			client := &http.Client{}
			resp, err := client.Do(req)
			c.NoError(err)
			defer resp.Body.Close()

			// Test assertions
			c.Equal(test.expectedStatus, resp.StatusCode)

			body, err := io.ReadAll(resp.Body)
			c.NoError(err)
			c.JSONEq(test.expectedBody, string(body))
		})
	}
}

func Test_handleHTTPServiceRequest(t *testing.T) {
	tests := []struct {
		name           string
		payload        string
		expectedBytes  []byte
		expectedStatus int
		expectedError  error
		path           string
		expectedPath   string
	}{
		{
			name:           "should perform a service request successfully",
			payload:        `{"jsonrpc": "2.0", "id": 1, "method": "eth_blockNumber"}`,
			expectedBytes:  []byte(`{"jsonrpc": "2.0", "id": 1, "result": "0x10d4f"}`),
			expectedStatus: http.StatusOK,
			path:           "/v1",
			expectedPath:   "",
		},
		{
			name:           "should fail if service request handler returns an error",
			payload:        `{"jsonrpc": "2.0", "id": 1, "method": "eth_blockNumber"}`,
			expectedBytes:  []byte("failed to send service request: some error\n"),
			expectedStatus: http.StatusInternalServerError,
			expectedError:  errors.New("some error"),
			path:           "/v1/",
			expectedPath:   "/",
		},
		{
			name:           "should handle /v1/whatever",
			payload:        `{"jsonrpc": "2.0", "id": 1, "method": "eth_blockNumber"}`,
			expectedBytes:  []byte(`{"jsonrpc": "2.0", "id": 1, "result": "0x10d4f"}`),
			expectedStatus: http.StatusOK,
			path:           "/v1/whatever",
			expectedPath:   "/whatever",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := require.New(t)

			_, mockGateway, ts := newTestRouter(t)

			mockGateway.EXPECT().HandleServiceRequest(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
				func(ctx context.Context, req *http.Request, w http.ResponseWriter) error {
					if req.URL.Path != test.expectedPath {
						t.Errorf("expected path %s, got %s", test.expectedPath, req.URL.Path)
					}
					if test.expectedStatus == http.StatusOK {
						w.WriteHeader(http.StatusOK)
						numBytesWritten, err := w.Write(test.expectedBytes)
						require.NoError(t, err)
						require.Equal(t, len(test.expectedBytes), numBytesWritten)
						return nil
					} else {
						http.Error(w, "failed to send service request: some error", http.StatusInternalServerError)
					}
					return test.expectedError
				},
			)

			req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s%s", ts.URL, test.path), strings.NewReader(test.payload))
			c.NoError(err)

			client := &http.Client{}
			resp, err := client.Do(req)
			c.NoError(err)
			defer resp.Body.Close()

			c.Equal(test.expectedStatus, resp.StatusCode)

			body, err := io.ReadAll(resp.Body)
			c.NoError(err)
			c.Equal(test.expectedBytes, body)
		})
	}
}

func Test_removePrefixMiddleware(t *testing.T) {
	tests := []struct {
		name                         string
		inputPath                    string
		inputHeaders                 map[string]string
		expectedPath                 string
		expectedPortalAppIDHeader    string
		shouldPortalAppIDHeaderExist bool
	}{
		{
			name:                         "should remove API version prefix only",
			inputPath:                    "/v1/path/segment",
			inputHeaders:                 map[string]string{},
			expectedPath:                 "/path/segment",
			expectedPortalAppIDHeader:    "",
			shouldPortalAppIDHeaderExist: false,
		},
		{
			name:                         "should remove API version prefix from root path",
			inputPath:                    "/v1",
			inputHeaders:                 map[string]string{},
			expectedPath:                 "",
			expectedPortalAppIDHeader:    "",
			shouldPortalAppIDHeaderExist: false,
		},
		{
			name:                         "should remove API version prefix with trailing slash",
			inputPath:                    "/v1/",
			inputHeaders:                 map[string]string{},
			expectedPath:                 "/",
			expectedPortalAppIDHeader:    "",
			shouldPortalAppIDHeaderExist: false,
		},
		{
			name:      "should remove both API version prefix and endpoint ID when header is present",
			inputPath: "/v1/1a2b3c4d/path/segment",
			inputHeaders: map[string]string{
				"Portal-Application-ID": "1a2b3c4d",
			},
			expectedPath:                 "/path/segment",
			expectedPortalAppIDHeader:    "1a2b3c4d",
			shouldPortalAppIDHeaderExist: true,
		},
		{
			name:      "should remove both API version prefix and endpoint ID with trailing slash",
			inputPath: "/v1/1a2b3c4d/",
			inputHeaders: map[string]string{
				"Portal-Application-ID": "1a2b3c4d",
			},
			expectedPath:                 "/",
			expectedPortalAppIDHeader:    "1a2b3c4d",
			shouldPortalAppIDHeaderExist: true,
		},
		{
			name:                         "should not remove endpoint ID when header is missing",
			inputPath:                    "/v1/1a2b3c4d/path/segment",
			inputHeaders:                 map[string]string{},
			expectedPath:                 "/1a2b3c4d/path/segment",
			expectedPortalAppIDHeader:    "",
			shouldPortalAppIDHeaderExist: false,
		},
		{
			name:      "should not remove endpoint ID when it doesn't match header value",
			inputPath: "/v1/different123/path/segment",
			inputHeaders: map[string]string{
				"Portal-Application-ID": "1a2b3c4d",
			},
			expectedPath:                 "/different123/path/segment",
			expectedPortalAppIDHeader:    "1a2b3c4d",
			shouldPortalAppIDHeaderExist: true,
		},
		{
			name:      "should not remove endpoint ID when it's not in the path",
			inputPath: "/v1/path/segment",
			inputHeaders: map[string]string{
				"Portal-Application-ID": "1a2b3c4d",
			},
			expectedPath:                 "/path/segment",
			expectedPortalAppIDHeader:    "1a2b3c4d",
			shouldPortalAppIDHeaderExist: true,
		},
		{
			name:      "should handle empty endpoint ID header",
			inputPath: "/v1/path/segment",
			inputHeaders: map[string]string{
				"Portal-Application-ID": "",
			},
			expectedPath:                 "/path/segment",
			expectedPortalAppIDHeader:    "",
			shouldPortalAppIDHeaderExist: true,
		},
		{
			name:      "should remove endpoint ID when it appears exactly in path",
			inputPath: "/v1/abc123/endpoint/test",
			inputHeaders: map[string]string{
				"Portal-Application-ID": "abc123",
			},
			expectedPath:                 "/endpoint/test",
			expectedPortalAppIDHeader:    "abc123",
			shouldPortalAppIDHeaderExist: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := require.New(t)

			r, _, _ := newTestRouter(t)

			// Create a mock next handler that captures the request
			var capturedRequest *http.Request
			nextHandler := func(w http.ResponseWriter, req *http.Request) {
				capturedRequest = req
				w.WriteHeader(http.StatusOK)
			}

			// Create the middleware
			middleware := r.removeGrovePortalPrefixMiddleware(nextHandler)

			// Create request with test path and headers
			req := httptest.NewRequest(http.MethodPost, test.inputPath, nil)
			for key, value := range test.inputHeaders {
				req.Header.Set(key, value)
			}

			// Create response recorder
			w := httptest.NewRecorder()

			// Execute the middleware
			middleware(w, req)

			// Verify the request was processed
			c.NotNil(capturedRequest, "next handler should have been called")

			// Check the modified path
			c.Equal(test.expectedPath, capturedRequest.URL.Path, "URL path should be modified correctly")

			// Check the Portal-Application-ID header
			actualHeaderValue := capturedRequest.Header.Get("Portal-Application-ID")
			if test.shouldPortalAppIDHeaderExist {
				c.Equal(test.expectedPortalAppIDHeader, actualHeaderValue, "Portal-Application-ID header should match expected value")
			} else {
				c.Equal("", actualHeaderValue, "Portal-Application-ID header should be removed or empty")
			}
		})
	}
}
