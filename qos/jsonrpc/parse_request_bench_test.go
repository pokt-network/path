package jsonrpc

import (
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
)

func BenchmarkParseJSONRPCFromRequestBody_Single(b *testing.B) {
	logger := polyzero.NewLogger(polyzero.WithLevel(polyzero.ParseLevel("warn")))
	body := []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := ParseJSONRPCFromRequestBody(logger, body); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseJSONRPCFromRequestBody_Batch(b *testing.B) {
	logger := polyzero.NewLogger(polyzero.WithLevel(polyzero.ParseLevel("warn")))
	body := []byte(`[{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1},` +
		`{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":2}]`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := ParseJSONRPCFromRequestBody(logger, body); err != nil {
			b.Fatal(err)
		}
	}
}
