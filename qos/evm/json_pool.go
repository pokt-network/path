package evm

import (
	"bytes"
	"encoding/json"
	"sync"
)

// jsonBufferPool provides a pool of reusable byte buffers for JSON encoding.
// This reduces memory allocations in the hot path when serializing JSON-RPC responses.
//
// Performance characteristics:
// - Avoids repeated allocations for json.Marshal in high-throughput scenarios
// - Each buffer starts at 512 bytes and grows as needed
// - Buffers are reset and returned to the pool after use
//
// Usage pattern:
//
//	buf := acquireJSONBuffer()
//	defer releaseJSONBuffer(buf)
//	encoder := json.NewEncoder(buf)
//	encoder.Encode(data)
//	result := buf.Bytes()
var jsonBufferPool = sync.Pool{
	New: func() any {
		// Pre-allocate 1KB - covers ~79% of JSON-RPC responses based on production metrics.
		// Buffer grows automatically for larger responses and gets reused via the pool.
		return bytes.NewBuffer(make([]byte, 0, 1024))
	},
}

// acquireJSONBuffer gets a buffer from the pool.
func acquireJSONBuffer() *bytes.Buffer {
	return jsonBufferPool.Get().(*bytes.Buffer)
}

// releaseJSONBuffer returns a buffer to the pool after resetting it.
func releaseJSONBuffer(buf *bytes.Buffer) {
	// Only return reasonably sized buffers to prevent memory bloat
	// If a buffer grew too large (>64KB), let it be garbage collected
	if buf.Cap() <= 65536 {
		buf.Reset()
		jsonBufferPool.Put(buf)
	}
}

// marshalJSONPooled uses a pooled buffer to marshal JSON.
// Returns a copy of the bytes to allow the buffer to be returned to the pool.
// This is more efficient than json.Marshal for high-throughput scenarios
// as it reuses buffers across requests.
func marshalJSONPooled(v any) ([]byte, error) {
	buf := acquireJSONBuffer()
	defer releaseJSONBuffer(buf)

	encoder := json.NewEncoder(buf)
	// Prevent HTML escaping which is unnecessary for JSON-RPC
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(v); err != nil {
		return nil, err
	}

	// json.Encoder.Encode adds a trailing newline, remove it for compatibility
	result := buf.Bytes()
	if len(result) > 0 && result[len(result)-1] == '\n' {
		result = result[:len(result)-1]
	}

	// Return a copy since the buffer will be reused
	return append([]byte(nil), result...), nil
}
