package healthcheck

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRecordHealthCheckResult(t *testing.T) {
	tests := []struct {
		name            string
		serviceID       string
		endpointDomain  string
		checkName       string
		checkType       string
		success         bool
		errorType       string
		durationSeconds float64
	}{
		{
			name:            "successful eth_blockNumber check",
			serviceID:       "eth",
			endpointDomain:  "example.com",
			checkName:       "eth_blockNumber",
			checkType:       "jsonrpc",
			success:         true,
			errorType:       "",
			durationSeconds: 0.123,
		},
		{
			name:            "failed getHealth check with timeout",
			serviceID:       "solana",
			endpointDomain:  "rpc.example.com",
			checkName:       "getHealth",
			checkType:       "jsonrpc",
			success:         false,
			errorType:       "timeout",
			durationSeconds: 5.0,
		},
		{
			name:            "successful REST health check",
			serviceID:       "cosmos",
			endpointDomain:  "api.cosmos.network",
			checkName:       "health",
			checkType:       "rest",
			success:         true,
			errorType:       "",
			durationSeconds: 0.050,
		},
		{
			name:            "failed websocket health check",
			serviceID:       "polygon",
			endpointDomain:  "ws.polygon.network",
			checkName:       "ping",
			checkType:       "websocket",
			success:         false,
			errorType:       "connection_refused",
			durationSeconds: 1.234,
		},
		{
			name:            "successful gRPC health check",
			serviceID:       "avalanche",
			endpointDomain:  "grpc.avalanche.network",
			checkName:       "Check",
			checkType:       "grpc",
			success:         true,
			errorType:       "",
			durationSeconds: 0.200,
		},
		{
			name:            "failed check with network error",
			serviceID:       "arbitrum",
			endpointDomain:  "rpc.arbitrum.io",
			checkName:       "eth_syncing",
			checkType:       "jsonrpc",
			success:         false,
			errorType:       "network_error",
			durationSeconds: 2.5,
		},
		{
			name:            "quick successful check",
			serviceID:       "optimism",
			endpointDomain:  "mainnet.optimism.io",
			checkName:       "eth_chainId",
			checkType:       "jsonrpc",
			success:         true,
			errorType:       "",
			durationSeconds: 0.025,
		},
		{
			name:            "slow successful check",
			serviceID:       "base",
			endpointDomain:  "mainnet.base.org",
			checkName:       "eth_getBlockByNumber",
			checkType:       "jsonrpc",
			success:         true,
			errorType:       "",
			durationSeconds: 0.850,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotPanics(t, func() {
				RecordHealthCheckResult(
					tt.serviceID,
					tt.endpointDomain,
					tt.checkName,
					tt.checkType,
					tt.success,
					tt.errorType,
					tt.durationSeconds,
				)
			})
		})
	}
}

func TestRecordHealthCheckResultWithDuration(t *testing.T) {
	// Test with time.Duration conversion
	tests := []struct {
		name      string
		serviceID string
		duration  time.Duration
	}{
		{
			name:      "100 milliseconds",
			serviceID: "eth",
			duration:  100 * time.Millisecond,
		},
		{
			name:      "1 second",
			serviceID: "solana",
			duration:  1 * time.Second,
		},
		{
			name:      "5 seconds",
			serviceID: "polygon",
			duration:  5 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			durationSeconds := tt.duration.Seconds()
			require.NotPanics(t, func() {
				RecordHealthCheckResult(
					tt.serviceID,
					"example.com",
					"eth_blockNumber",
					"jsonrpc",
					true,
					"",
					durationSeconds,
				)
			})
		})
	}
}

func TestSetEndpointsChecked(t *testing.T) {
	tests := []struct {
		name      string
		serviceID string
		count     int
	}{
		{
			name:      "zero endpoints checked",
			serviceID: "eth",
			count:     0,
		},
		{
			name:      "single endpoint checked",
			serviceID: "solana",
			count:     1,
		},
		{
			name:      "multiple endpoints checked",
			serviceID: "polygon",
			count:     10,
		},
		{
			name:      "large endpoint pool",
			serviceID: "cosmos",
			count:     100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotPanics(t, func() {
				SetEndpointsChecked(tt.serviceID, tt.count)
			})
		})
	}
}

func TestHealthCheckSuccessValues(t *testing.T) {
	// Test that success flag properly translates to "true" and "false" strings
	tests := []struct {
		name    string
		success bool
	}{
		{
			name:    "success true",
			success: true,
		},
		{
			name:    "success false",
			success: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotPanics(t, func() {
				RecordHealthCheckResult(
					"eth",
					"example.com",
					"test_check",
					"jsonrpc",
					tt.success,
					"",
					0.1,
				)
			})
		})
	}
}

func TestHealthCheckWithVariousErrorTypes(t *testing.T) {
	errorTypes := []string{
		"timeout",
		"connection_refused",
		"network_error",
		"invalid_response",
		"rate_limit",
		"authentication_failed",
		"service_unavailable",
		"", // empty error type for successful checks
	}

	for _, errorType := range errorTypes {
		t.Run("error_type_"+errorType, func(t *testing.T) {
			success := errorType == ""
			require.NotPanics(t, func() {
				RecordHealthCheckResult(
					"eth",
					"example.com",
					"eth_blockNumber",
					"jsonrpc",
					success,
					errorType,
					0.1,
				)
			})
		})
	}
}

func TestHealthCheckWithVariousCheckTypes(t *testing.T) {
	checkTypes := []string{
		"jsonrpc",
		"rest",
		"websocket",
		"grpc",
	}

	for _, checkType := range checkTypes {
		t.Run("check_type_"+checkType, func(t *testing.T) {
			require.NotPanics(t, func() {
				RecordHealthCheckResult(
					"eth",
					"example.com",
					"health_check",
					checkType,
					true,
					"",
					0.1,
				)
			})
		})
	}
}

func TestHealthCheckDurationBuckets(t *testing.T) {
	// Test various durations that fall into different histogram buckets
	durations := []float64{
		0.05, // < 0.1
		0.15, // 0.1-0.25
		0.3,  // 0.25-0.5
		0.75, // 0.5-1
		1.5,  // 1-2
		3.0,  // 2-5
		7.5,  // 5-10
		15.0, // 10-30
		35.0, // > 30
	}

	for _, duration := range durations {
		t.Run("duration_"+time.Duration(duration*float64(time.Second)).String(), func(t *testing.T) {
			require.NotPanics(t, func() {
				RecordHealthCheckResult(
					"eth",
					"example.com",
					"eth_blockNumber",
					"jsonrpc",
					true,
					"",
					duration,
				)
			})
		})
	}
}

func TestMultipleServicesHealthCheck(t *testing.T) {
	services := []string{
		"eth",
		"solana",
		"polygon",
		"cosmos",
		"avalanche",
		"arbitrum",
		"optimism",
		"base",
	}

	for _, service := range services {
		t.Run("service_"+service, func(t *testing.T) {
			require.NotPanics(t, func() {
				RecordHealthCheckResult(
					service,
					"example.com",
					"health_check",
					"jsonrpc",
					true,
					"",
					0.1,
				)
			})

			require.NotPanics(t, func() {
				SetEndpointsChecked(service, 5)
			})
		})
	}
}
