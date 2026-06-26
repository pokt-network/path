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
