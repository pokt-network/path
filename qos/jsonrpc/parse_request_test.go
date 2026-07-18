package jsonrpc

import (
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
)

func TestParseJSONRPCFromRequestBody(t *testing.T) {
	logger := polyzero.NewLogger(polyzero.WithLevel(polyzero.ParseLevel("warn")))

	tests := []struct {
		name      string
		body      string
		wantErr   bool
		wantBatch bool
		wantCount int
	}{
		{name: "single object", body: `{"jsonrpc":"2.0","method":"eth_blockNumber","id":1}`, wantBatch: false, wantCount: 1},
		{name: "single with leading whitespace", body: "  \n\t" + `{"jsonrpc":"2.0","method":"m","id":1}`, wantBatch: false, wantCount: 1},
		{name: "batch array", body: `[{"jsonrpc":"2.0","method":"a","id":1},{"jsonrpc":"2.0","method":"b","id":2}]`, wantBatch: true, wantCount: 2},
		{name: "batch with leading whitespace", body: "  " + `[{"jsonrpc":"2.0","method":"a","id":1}]`, wantBatch: true, wantCount: 1},
		{name: "empty body", body: ``, wantErr: true},
		{name: "whitespace only", body: "   \n  ", wantErr: true},
		{name: "malformed single", body: `{not json`, wantErr: true},
		{name: "malformed batch", body: `[not json`, wantErr: true},
		{name: "empty batch rejected", body: `[]`, wantErr: true},
		// Duplicate IDs are accepted (node-equivalent): public RPC nodes serve
		// these, so PATH does too. Each request keeps its own ID pointer, so both
		// payloads survive as distinct map entries (N requests → N responses).
		{name: "duplicate int ids accepted", body: `[{"jsonrpc":"2.0","method":"a","id":1},{"jsonrpc":"2.0","method":"b","id":1}]`, wantBatch: true, wantCount: 2},
		{name: "duplicate string ids accepted", body: `[{"jsonrpc":"2.0","method":"a","id":"x"},{"jsonrpc":"2.0","method":"b","id":"x"}]`, wantBatch: true, wantCount: 2},
		{name: "cross-type duplicate ids accepted", body: `[{"jsonrpc":"2.0","method":"a","id":1},{"jsonrpc":"2.0","method":"b","id":"1"}]`, wantBatch: true, wantCount: 2},
		{name: "distinct ids ok", body: `[{"jsonrpc":"2.0","method":"a","id":1},{"jsonrpc":"2.0","method":"b","id":2}]`, wantBatch: true, wantCount: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqs, isBatch, err := ParseJSONRPCFromRequestBody(logger, []byte(tt.body))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (reqs=%v)", reqs)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if isBatch != tt.wantBatch {
				t.Fatalf("isBatch = %v, want %v", isBatch, tt.wantBatch)
			}
			if len(reqs) != tt.wantCount {
				t.Fatalf("len(reqs) = %d, want %d", len(reqs), tt.wantCount)
			}
		})
	}
}

// TestParseJSONRPCFromRequestBody_NotificationsAllowed verifies that multiple
// notifications (requests without an ID) are not treated as duplicate IDs.
func TestParseJSONRPCFromRequestBody_NotificationsAllowed(t *testing.T) {
	logger := polyzero.NewLogger(polyzero.WithLevel(polyzero.ParseLevel("warn")))
	body := []byte(`[{"jsonrpc":"2.0","method":"a"},{"jsonrpc":"2.0","method":"b"}]`)

	if _, _, err := ParseJSONRPCFromRequestBody(logger, body); err != nil {
		t.Fatalf("notifications without IDs must not be rejected as duplicates: %v", err)
	}
}
