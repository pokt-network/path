package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"testing"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRPCTypeDetector_CheckRPCTypeHeader(t *testing.T) {
	detector := NewRPCTypeDetector()

	tests := []struct {
		name            string
		headerValue     string
		serviceRPCTypes []string
		expectedRPCType sharedtypes.RPCType
		expectedOK      bool
		expectError     bool
		errorContains   string
	}{
		{
			name:            "valid json_rpc header",
			headerValue:     "json_rpc",
			serviceRPCTypes: []string{"json_rpc", "websocket"},
			expectedRPCType: sharedtypes.RPCType_JSON_RPC,
			expectedOK:      true,
			expectError:     false,
		},
		{
			name:            "valid rest header",
			headerValue:     "rest",
			serviceRPCTypes: []string{"rest", "comet_bft"},
			expectedRPCType: sharedtypes.RPCType_REST,
			expectedOK:      true,
			expectError:     false,
		},
		{
			name:            "valid websocket header",
			headerValue:     "websocket",
			serviceRPCTypes: []string{"json_rpc", "websocket"},
			expectedRPCType: sharedtypes.RPCType_WEBSOCKET,
			expectedOK:      true,
			expectError:     false,
		},
		{
			name:            "no header - continue to next step",
			headerValue:     "",
			serviceRPCTypes: []string{"json_rpc"},
			expectedRPCType: sharedtypes.RPCType_UNKNOWN_RPC,
			expectedOK:      false,
			expectError:     false,
		},
		{
			name:            "invalid header value",
			headerValue:     "invalid_type",
			serviceRPCTypes: []string{"json_rpc"},
			expectedRPCType: sharedtypes.RPCType_UNKNOWN_RPC,
			expectedOK:      false,
			expectError:     true,
			errorContains:   "invalid RPC-Type header value",
		},
		{
			name:            "header value not supported by service",
			headerValue:     "rest",
			serviceRPCTypes: []string{"json_rpc", "websocket"},
			expectedRPCType: sharedtypes.RPCType_UNKNOWN_RPC,
			expectedOK:      false,
			expectError:     true,
			errorContains:   "not supported by service",
		},
		{
			name:            "case insensitive header",
			headerValue:     "JSON_RPC",
			serviceRPCTypes: []string{"json_rpc"},
			expectedRPCType: sharedtypes.RPCType_JSON_RPC,
			expectedOK:      true,
			expectError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{
				Header: http.Header{},
			}
			if tt.headerValue != "" {
				req.Header.Set(RPCTypeHeader, tt.headerValue)
			}

			rpcType, ok, err := detector.checkRPCTypeHeader(req, "test-service", tt.serviceRPCTypes)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tt.expectedOK, ok)
			if tt.expectedOK {
				assert.Equal(t, tt.expectedRPCType, rpcType)
			}
		})
	}
}

func TestRPCTypeDetector_EasyDetection(t *testing.T) {
	detector := NewRPCTypeDetector()

	tests := []struct {
		name            string
		headers         map[string]string
		expectedRPCType sharedtypes.RPCType
		expectedOK      bool
	}{
		{
			name: "websocket upgrade",
			headers: map[string]string{
				"Upgrade": "websocket",
			},
			expectedRPCType: sharedtypes.RPCType_WEBSOCKET,
			expectedOK:      true,
		},
		{
			name: "websocket upgrade case insensitive",
			headers: map[string]string{
				"Upgrade": "WebSocket",
			},
			expectedRPCType: sharedtypes.RPCType_WEBSOCKET,
			expectedOK:      true,
		},
		{
			name: "grpc content type",
			headers: map[string]string{
				"Content-Type": "application/grpc",
			},
			expectedRPCType: sharedtypes.RPCType_GRPC,
			expectedOK:      true,
		},
		{
			name: "grpc content type with encoding",
			headers: map[string]string{
				"Content-Type": "application/grpc+proto",
			},
			expectedRPCType: sharedtypes.RPCType_GRPC,
			expectedOK:      true,
		},
		{
			name:            "regular http request",
			headers:         map[string]string{},
			expectedRPCType: sharedtypes.RPCType_UNKNOWN_RPC,
			expectedOK:      false,
		},
		{
			name: "json content type not grpc",
			headers: map[string]string{
				"Content-Type": "application/json",
			},
			expectedRPCType: sharedtypes.RPCType_UNKNOWN_RPC,
			expectedOK:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{
				Header: http.Header{},
			}
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}

			rpcType, ok := detector.easyDetection(req)

			assert.Equal(t, tt.expectedOK, ok)
			if tt.expectedOK {
				assert.Equal(t, tt.expectedRPCType, rpcType)
			}
		})
	}
}

func TestRPCTypeDetector_ProcessOfElimination(t *testing.T) {
	detector := NewRPCTypeDetector()

	tests := []struct {
		name            string
		serviceRPCTypes []string
		expectedRPCType sharedtypes.RPCType
		expectedOK      bool
	}{
		{
			name:            "common case - json_rpc + websocket",
			serviceRPCTypes: []string{"json_rpc", "websocket"},
			expectedRPCType: sharedtypes.RPCType_JSON_RPC,
			expectedOK:      true,
		},
		{
			name:            "single http type - rest",
			serviceRPCTypes: []string{"rest"},
			expectedRPCType: sharedtypes.RPCType_REST,
			expectedOK:      true,
		},
		{
			name:            "single http type - comet_bft",
			serviceRPCTypes: []string{"comet_bft"},
			expectedRPCType: sharedtypes.RPCType_COMET_BFT,
			expectedOK:      true,
		},
		{
			name:            "rest + websocket",
			serviceRPCTypes: []string{"rest", "websocket"},
			expectedRPCType: sharedtypes.RPCType_REST,
			expectedOK:      true,
		},
		{
			name:            "multiple http types - need payload inspection",
			serviceRPCTypes: []string{"json_rpc", "rest", "comet_bft"},
			expectedRPCType: sharedtypes.RPCType_UNKNOWN_RPC,
			expectedOK:      false,
		},
		{
			name:            "only websocket - no http types",
			serviceRPCTypes: []string{"websocket"},
			expectedRPCType: sharedtypes.RPCType_UNKNOWN_RPC,
			expectedOK:      false,
		},
		{
			name:            "only grpc - no http types",
			serviceRPCTypes: []string{"grpc"},
			expectedRPCType: sharedtypes.RPCType_UNKNOWN_RPC,
			expectedOK:      false,
		},
		{
			name:            "websocket + grpc - no http types",
			serviceRPCTypes: []string{"websocket", "grpc"},
			expectedRPCType: sharedtypes.RPCType_UNKNOWN_RPC,
			expectedOK:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rpcType, ok := detector.processOfElimination(tt.serviceRPCTypes)

			assert.Equal(t, tt.expectedOK, ok)
			if tt.expectedOK {
				assert.Equal(t, tt.expectedRPCType, rpcType)
			}
		})
	}
}

func TestRPCTypeDetector_DetectRPCType_WithHeader(t *testing.T) {
	detector := NewRPCTypeDetector()

	tests := []struct {
		name            string
		headerValue     string
		serviceRPCTypes []string
		expectedRPCType sharedtypes.RPCType
		expectError     bool
	}{
		{
			name:            "header specifies json_rpc",
			headerValue:     "json_rpc",
			serviceRPCTypes: []string{"json_rpc", "rest"},
			expectedRPCType: sharedtypes.RPCType_JSON_RPC,
			expectError:     false,
		},
		{
			name:            "header specifies rest",
			headerValue:     "rest",
			serviceRPCTypes: []string{"json_rpc", "rest"},
			expectedRPCType: sharedtypes.RPCType_REST,
			expectError:     false,
		},
		{
			name:            "header not in service types",
			headerValue:     "rest",
			serviceRPCTypes: []string{"json_rpc"},
			expectError:     true,
		},
		{
			name:            "invalid header value",
			headerValue:     "invalid",
			serviceRPCTypes: []string{"json_rpc"},
			expectError:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{
				Header: http.Header{},
			}
			req.Header.Set(RPCTypeHeader, tt.headerValue)

			rpcType, err := detector.DetectRPCType(req, "test-service", tt.serviceRPCTypes)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedRPCType, rpcType)
			}
		})
	}
}

func TestRPCTypeDetector_DetectRPCType_CommonCases(t *testing.T) {
	detector := NewRPCTypeDetector()

	tests := []struct {
		name            string
		serviceRPCTypes []string
		headers         map[string]string
		body            string
		expectedRPCType sharedtypes.RPCType
		expectError     bool
	}{
		{
			name:            "websocket upgrade detected",
			serviceRPCTypes: []string{"json_rpc", "websocket"},
			headers: map[string]string{
				"Upgrade": "websocket",
			},
			expectedRPCType: sharedtypes.RPCType_WEBSOCKET,
			expectError:     false,
		},
		{
			name:            "json_rpc by elimination (no payload inspection)",
			serviceRPCTypes: []string{"json_rpc", "websocket"},
			headers:         map[string]string{},
			expectedRPCType: sharedtypes.RPCType_JSON_RPC,
			expectError:     false,
		},
		{
			name:            "rest by elimination",
			serviceRPCTypes: []string{"rest", "websocket"},
			headers:         map[string]string{},
			expectedRPCType: sharedtypes.RPCType_REST,
			expectError:     false,
		},
		{
			name:            "single type service",
			serviceRPCTypes: []string{"json_rpc"},
			headers:         map[string]string{},
			expectedRPCType: sharedtypes.RPCType_JSON_RPC,
			expectError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{
				Header: http.Header{},
				Body:   io.NopCloser(bytes.NewReader([]byte(tt.body))),
			}
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}

			rpcType, err := detector.DetectRPCType(req, "test-service", tt.serviceRPCTypes)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedRPCType, rpcType)
			}
		})
	}
}

func TestRPCTypeDetector_InspectPayload(t *testing.T) {
	detector := NewRPCTypeDetector()

	tests := []struct {
		name            string
		serviceRPCTypes []string
		body            string
		path            string
		expectedRPCType sharedtypes.RPCType
		expectError     bool
	}{
		{
			name:            "json-rpc payload",
			serviceRPCTypes: []string{"json_rpc", "rest"},
			body:            `{"jsonrpc":"2.0","method":"eth_blockNumber","id":1}`,
			expectedRPCType: sharedtypes.RPCType_JSON_RPC,
			expectError:     false,
		},
		{
			name:            "comet_bft method in payload",
			serviceRPCTypes: []string{"json_rpc", "comet_bft"},
			body:            `{"method":"block","id":1}`,
			expectedRPCType: sharedtypes.RPCType_COMET_BFT,
			expectError:     false,
		},
		{
			name:            "comet_bft abci method",
			serviceRPCTypes: []string{"json_rpc", "comet_bft"},
			body:            `{"method":"abci_info","id":1}`,
			expectedRPCType: sharedtypes.RPCType_COMET_BFT,
			expectError:     false,
		},
		{
			name:            "rest by default",
			serviceRPCTypes: []string{"rest"},
			body:            ``,
			expectedRPCType: sharedtypes.RPCType_REST,
			expectError:     false,
		},
		{
			name:            "comet_bft by path",
			serviceRPCTypes: []string{"rest", "comet_bft"},
			body:            ``,
			path:            "/status",
			expectedRPCType: sharedtypes.RPCType_COMET_BFT,
			expectError:     false,
		},
		{
			name:            "comet_bft by block path",
			serviceRPCTypes: []string{"rest", "comet_bft"},
			body:            ``,
			path:            "/block",
			expectedRPCType: sharedtypes.RPCType_COMET_BFT,
			expectError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{
				Header: http.Header{},
				Body:   io.NopCloser(bytes.NewReader([]byte(tt.body))),
				URL:    &url.URL{Path: tt.path},
			}

			rpcType, err := detector.inspectPayload(req, "test-service", tt.serviceRPCTypes)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedRPCType, rpcType)
			}
		})
	}
}

func TestRPCTypeDetector_CometBFTMethodDetection(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		expected bool
	}{
		{name: "abci_info", method: "abci_info", expected: true},
		{name: "abci_query", method: "abci_query", expected: true},
		{name: "block", method: "block", expected: true},
		{name: "block_results", method: "block_results", expected: true},
		{name: "blockchain", method: "blockchain", expected: true},
		{name: "broadcast_tx_sync", method: "broadcast_tx_sync", expected: true},
		{name: "commit", method: "commit", expected: true},
		{name: "consensus_state", method: "consensus_state", expected: true},
		{name: "genesis", method: "genesis", expected: true},
		{name: "health", method: "health", expected: true},
		{name: "net_info", method: "net_info", expected: true},
		{name: "status", method: "status", expected: true},
		{name: "tx", method: "tx", expected: true},
		{name: "tx_search", method: "tx_search", expected: true},
		{name: "validators", method: "validators", expected: true},
		{name: "unconfirmed_txs", method: "unconfirmed_txs", expected: true},
		{name: "eth_blockNumber not comet", method: "eth_blockNumber", expected: false},
		{name: "eth_getBalance not comet", method: "eth_getBalance", expected: false},
		{name: "getinfo not comet", method: "getinfo", expected: false},
		{name: "empty string", method: "", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isCometBFTMethod(tt.method)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRPCTypeDetector_CometBFTPathDetection(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{name: "/status", path: "/status", expected: true},
		{name: "/health", path: "/health", expected: true},
		{name: "/block", path: "/block", expected: true},
		{name: "/block_results", path: "/block_results", expected: true},
		{name: "/tx", path: "/tx", expected: true},
		{name: "/validators", path: "/validators", expected: true},
		{name: "/genesis", path: "/genesis", expected: true},
		{name: "/abci_info", path: "/abci_info", expected: true},
		{name: "/net_info", path: "/net_info", expected: true},
		{name: "/broadcast_tx_sync", path: "/broadcast_tx_sync", expected: true},
		{name: "/cosmos/base not comet", path: "/cosmos/base", expected: false},
		{name: "/v1/accounts not comet", path: "/v1/accounts", expected: false},
		{name: "/ root", path: "/", expected: false},
		{name: "empty string", path: "", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isCometBFTPath(tt.path)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRPCTypeDetector_HelperFunctions(t *testing.T) {
	t.Run("hasJSONRPCStructure", func(t *testing.T) {
		tests := []struct {
			name     string
			body     string
			expected bool
		}{
			{name: "valid jsonrpc", body: `{"jsonrpc":"2.0","method":"test"}`, expected: true},
			{name: "has method", body: `{"method":"test"}`, expected: true},
			{name: "has id", body: `{"id":1}`, expected: true},
			{name: "plain json", body: `{"foo":"bar"}`, expected: false},
			{name: "empty body", body: ``, expected: false},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := hasJSONRPCStructure([]byte(tt.body))
				assert.Equal(t, tt.expected, result)
			})
		}
	})

	t.Run("extractJSONRPCMethod", func(t *testing.T) {
		tests := []struct {
			name     string
			body     string
			expected string
		}{
			{name: "eth_blockNumber", body: `{"method":"eth_blockNumber"}`, expected: "eth_blockNumber"},
			{name: "status", body: `{"method":"status"}`, expected: "status"},
			{name: "no method", body: `{"foo":"bar"}`, expected: ""},
			{name: "invalid json", body: `{invalid}`, expected: ""},
			{name: "empty body", body: ``, expected: ""},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				result := extractJSONRPCMethod([]byte(tt.body))
				assert.Equal(t, tt.expected, result)
			})
		}
	})

	t.Run("readBodySafely", func(t *testing.T) {
		req := &http.Request{
			Body: io.NopCloser(bytes.NewReader([]byte("test body content"))),
		}

		body, err := readBodySafely(req, 1024)
		require.NoError(t, err)
		assert.Equal(t, "test body content", string(body))
	})

	t.Run("readBodySafely with limit", func(t *testing.T) {
		largeBody := bytes.Repeat([]byte("a"), 1000)
		req := &http.Request{
			Body: io.NopCloser(bytes.NewReader(largeBody)),
		}

		body, err := readBodySafely(req, 100)
		require.NoError(t, err)
		assert.Len(t, body, 100)
	})
}

func TestRPCTypeDetector_OptimizationBehavior(t *testing.T) {
	detector := NewRPCTypeDetector()

	t.Run("most common case - no payload inspection", func(t *testing.T) {
		// Service with ["json_rpc", "websocket"] should NEVER inspect payload
		// for non-websocket requests
		req := &http.Request{
			Header: http.Header{},
			Body:   io.NopCloser(bytes.NewReader([]byte(`{"method":"test"}`))),
		}

		rpcType, err := detector.DetectRPCType(req, "eth", []string{"json_rpc", "websocket"})

		require.NoError(t, err)
		assert.Equal(t, sharedtypes.RPCType_JSON_RPC, rpcType)

		// Body should not have been consumed (optimization worked)
		bodyBytes, _ := io.ReadAll(req.Body)
		assert.NotEmpty(t, bodyBytes, "Body should still be readable - detector didn't consume it")
	})

	t.Run("single http type - no payload inspection", func(t *testing.T) {
		req := &http.Request{
			Header: http.Header{},
			Body:   io.NopCloser(bytes.NewReader([]byte(`some payload`))),
		}

		rpcType, err := detector.DetectRPCType(req, "service", []string{"rest"})

		require.NoError(t, err)
		assert.Equal(t, sharedtypes.RPCType_REST, rpcType)

		// Body should not have been consumed
		bodyBytes, _ := io.ReadAll(req.Body)
		assert.NotEmpty(t, bodyBytes)
	})

	t.Run("header bypasses all detection", func(t *testing.T) {
		req := &http.Request{
			Header: http.Header{},
			Body:   io.NopCloser(bytes.NewReader([]byte(`some payload`))),
		}
		req.Header.Set(RPCTypeHeader, "json_rpc")

		rpcType, err := detector.DetectRPCType(req, "service", []string{"json_rpc", "rest", "comet_bft"})

		require.NoError(t, err)
		assert.Equal(t, sharedtypes.RPCType_JSON_RPC, rpcType)

		// Body should not have been consumed
		bodyBytes, _ := io.ReadAll(req.Body)
		assert.NotEmpty(t, bodyBytes)
	})
}
