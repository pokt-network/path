package gateway

import (
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
