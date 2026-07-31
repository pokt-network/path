package gateway

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestExtractBlockHeight_GetEpochInfo verifies the external block fetcher can parse a
// Solana getEpochInfo response by its explicit blockHeight field, and that it NEVER
// substitutes absoluteSlot (the slot, ~5% higher, which poisons the max-based perceived
// height). getEpochInfo is the preferred Solana external-source method precisely because
// getBlockHeight returns a bare number some providers mislabel with the slot.
func TestExtractBlockHeight_GetEpochInfo(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		want     int64
		wantErr  bool
	}{
		{
			name: "getEpochInfo uses blockHeight, not absoluteSlot",
			body: `{"jsonrpc":"2.0","id":1,"result":{"absoluteSlot":434344678,"blockHeight":412405238,"epoch":1005}}`,
			want: 412405238,
		},
		{
			name:    "getEpochInfo with only absoluteSlot (no blockHeight) must error, not return the slot",
			body:    `{"jsonrpc":"2.0","id":1,"result":{"absoluteSlot":434344678,"epoch":1005}}`,
			wantErr: true,
		},
		{
			name: "getBlockHeight bare number still works",
			body: `{"jsonrpc":"2.0","id":1,"result":412405238}`,
			want: 412405238,
		},
		{
			name: "EVM hex still works",
			body: `{"jsonrpc":"2.0","id":1,"result":"0x1940c6f5"}`,
			want: 0x1940c6f5,
		},
		{
			name: "Cosmos sync_info still works",
			body: `{"jsonrpc":"2.0","id":1,"result":{"sync_info":{"latest_block_height":"12345"}}}`,
			want: 12345,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractBlockHeight([]byte(tc.body))
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestExtractBlockHeight_BeaconREST covers the Ethereum Beacon REST API, which wraps its
// payload in {"data": {...}} and has no top-level "result" at all. Before this was handled,
// every eth-beacon node_syncing check failed extraction and was charged to the supplier as
// critical_error - on healthy endpoints reporting "sync_distance":"0".
func TestExtractBlockHeight_BeaconREST(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    int64
		wantErr bool
	}{
		{
			name: "beacon node/syncing head_slot as string",
			body: `{"data":{"el_offline":false,"head_slot":"14874850","is_optimistic":true,"is_syncing":false,"sync_distance":"0"}}`,
			want: 14874850,
		},
		{
			name: "beacon head_slot as number",
			body: `{"data":{"head_slot":14874850,"sync_distance":0,"is_syncing":false}}`,
			want: 14874850,
		},
		{
			name:    "data object without head_slot still errors",
			body:    `{"data":{"version":"Lighthouse/v8.2.1"}}`,
			wantErr: true,
		},
		{
			name:    "non-numeric head_slot errors",
			body:    `{"data":{"head_slot":"not-a-number"}}`,
			wantErr: true,
		},
		{
			name: "top-level result still wins over data",
			body: `{"result":"0x1940c6f5","data":{"head_slot":"1"}}`,
			want: 0x1940c6f5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractBlockHeight([]byte(tc.body))
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestValidateSyncCheck_UnreadableShapeIsNotEndpointFault asserts the failure mode that
// bricked eth-beacon: when the response carries no block height we know how to read, the
// sync check must report errSyncCheckNotApplicable so the caller skips the reputation
// penalty. A rule aimed at the wrong shape fails on every endpoint of the service forever,
// so charging it to suppliers turns a config bug into a service-wide critical_error storm.
func TestValidateSyncCheck_UnreadableShapeIsNotEndpointFault(t *testing.T) {
	e := &HealthCheckExecutor{logger: testCircuitBreakerLogger()}

	// Beacon /eth/v1/node/version - a 200 with no height anywhere.
	err := e.validateSyncCheck("eth-beacon", []byte(`{"data":{"version":"Lighthouse/v8.2.1"}}`), 25)
	require.Error(t, err)
	require.True(t, errors.Is(err, errSyncCheckNotApplicable),
		"unreadable response shape must not be charged to the endpoint, got: %v", err)

	// Malformed JSON is equally unreadable.
	err = e.validateSyncCheck("eth-beacon", []byte(`not json`), 25)
	require.Error(t, err)
	require.True(t, errors.Is(err, errSyncCheckNotApplicable))

	// A height of 0 IS the endpoint's fault - it must stay penalizable.
	err = e.validateSyncCheck("eth", []byte(`{"jsonrpc":"2.0","id":1,"result":"0x0"}`), 25)
	require.Error(t, err)
	require.False(t, errors.Is(err, errSyncCheckNotApplicable),
		"block height 0 is an endpoint fault and must remain penalizable")
}
