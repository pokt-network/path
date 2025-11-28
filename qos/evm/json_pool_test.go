package evm

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMarshalJSONPooled(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		wantErr bool
	}{
		{
			name: "should marshal simple struct",
			input: struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
			}{ID: 1, Name: "test"},
			wantErr: false,
		},
		{
			name: "should marshal map",
			input: map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"result":  "0x123",
			},
			wantErr: false,
		},
		{
			name:    "should marshal nil",
			input:   nil,
			wantErr: false,
		},
		{
			name:    "should marshal string",
			input:   "test string",
			wantErr: false,
		},
		{
			name:    "should marshal slice",
			input:   []string{"a", "b", "c"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := require.New(t)

			// Marshal using pooled function
			pooledResult, err := marshalJSONPooled(tt.input)
			if tt.wantErr {
				c.Error(err)
				return
			}
			c.NoError(err)

			// Compare with standard json.Marshal
			standardResult, err := json.Marshal(tt.input)
			c.NoError(err)

			// Results should be equivalent
			c.JSONEq(string(standardResult), string(pooledResult))
		})
	}
}

func TestMarshalJSONPooled_BufferReuse(t *testing.T) {
	c := require.New(t)

	// Run multiple marshaling operations to test buffer reuse
	for i := 0; i < 100; i++ {
		input := map[string]any{
			"jsonrpc": "2.0",
			"id":      i,
			"result":  "0x" + string(rune('a'+i%26)),
		}

		result, err := marshalJSONPooled(input)
		c.NoError(err)
		c.NotEmpty(result)

		// Verify it's valid JSON
		var unmarshaled map[string]any
		err = json.Unmarshal(result, &unmarshaled)
		c.NoError(err)
		c.Equal(float64(i), unmarshaled["id"])
	}
}

func TestMarshalJSONPooled_LargePayload(t *testing.T) {
	c := require.New(t)

	// Create a large payload
	largeData := make([]string, 1000)
	for i := range largeData {
		largeData[i] = "test data string with some content"
	}

	input := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"result":  largeData,
	}

	result, err := marshalJSONPooled(input)
	c.NoError(err)
	c.NotEmpty(result)

	// Verify it's valid JSON
	var unmarshaled map[string]any
	err = json.Unmarshal(result, &unmarshaled)
	c.NoError(err)
}

func BenchmarkMarshalJSONPooled(b *testing.B) {
	input := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"result":  "0x1234567890abcdef",
	}

	b.Run("pooled", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = marshalJSONPooled(input)
		}
	})

	b.Run("standard", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = json.Marshal(input)
		}
	})
}
