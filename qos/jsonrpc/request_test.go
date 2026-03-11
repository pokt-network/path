package jsonrpc

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// Unit tests to verify the Request struct serialization maintains the JSONRPC 2.0 spec.
func TestMarshalJSON(t *testing.T) {
	testCases := []struct {
		name       string
		rawPayload string
	}{
		{
			name:       "empty id field is automatically set to null for JSONRPC compliance",
			rawPayload: `{"jsonrpc":"2.0","method":"eth_chainId","id":null}`,
		},
		{
			name: "param field as empty array is present in the serialized format",
			// DEV_NOTE: the order of fields should be the same as that of the Request struct, to get the same string post deserialization and serialization.
			rawPayload: `{"jsonrpc":"2.0","method":"eth_chainId","params":[],"id":1}`,
		},
		{
			name: "null id field is preserved and param field with single object as value is present in the serialized format",
			// payload example from: https://polkadot.js.org/docs/substrate/rpc/#querystorageatkeys-vec-storagehash-at-hash-option-vec-storagedata
			// DEV_NOTE: the order of fields should be the same as that of the Request struct, to get the same string post deserialization and serialization.
			rawPayload: `{"jsonrpc":"2.0","method":"state_queryStorageAt","params":{"keys":["0x5f3e4907f716ac89b6347d15ececedca1c0000000000000000"],"at":"0x6857c3c171f65f77f52cd566c574c1f59b0a3738b8d487967e9c54789ee621dd"},"id":null}`,
		},
		{
			name: "id and params fields are both present in the serialized format when specified",
			// rawPayload is from: https://solana.com/docs/rpc/http/getblockcommitment
			// DEV_NOTE: the order of fields should be the same as that of the Request struct, to get the same string post deserialization and serialization.
			rawPayload: `{"jsonrpc":"2.0","method":"getBlockCommitment","params":[5],"id":1}`,
		},
		{
			name:       "string id is properly serialized",
			rawPayload: `{"jsonrpc":"2.0","method":"eth_chainId","id":"test-id-123"}`,
		},
		{
			name:       "explicit null id in input is preserved",
			rawPayload: `{"jsonrpc":"2.0","method":"eth_chainId","id":null}`,
		},
		{
			name:       "empty string id is converted to null",
			rawPayload: `{"jsonrpc":"2.0","method":"eth_chainId","id":null}`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var req Request
			err := json.Unmarshal([]byte(testCase.rawPayload), &req)
			require.NoError(t, err)

			marshaledRequest, err := json.Marshal(req)
			require.NoError(t, err)

			require.Equal(t, testCase.rawPayload, string(marshaledRequest))
		})
	}
}

// Test edge cases for ID handling
func TestIDHandling(t *testing.T) {
	testCases := []struct {
		name           string
		inputPayload   string
		expectedOutput string
	}{
		{
			name:           "missing id field becomes null",
			inputPayload:   `{"jsonrpc":"2.0","method":"test"}`,
			expectedOutput: `{"jsonrpc":"2.0","method":"test","id":null}`,
		},
		{
			name:           "empty string id becomes null",
			inputPayload:   `{"id":"","jsonrpc":"2.0","method":"test"}`,
			expectedOutput: `{"jsonrpc":"2.0","method":"test","id":null}`,
		},
		{
			name:           "null id stays null",
			inputPayload:   `{"id":null,"jsonrpc":"2.0","method":"test"}`,
			expectedOutput: `{"jsonrpc":"2.0","method":"test","id":null}`,
		},
		{
			name:           "zero integer id is preserved",
			inputPayload:   `{"id":0,"jsonrpc":"2.0","method":"test"}`,
			expectedOutput: `{"jsonrpc":"2.0","method":"test","id":0}`,
		},
		{
			name:           "negative integer id is preserved",
			inputPayload:   `{"id":-1,"jsonrpc":"2.0","method":"test"}`,
			expectedOutput: `{"jsonrpc":"2.0","method":"test","id":-1}`,
		},
		{
			name:           "string id is preserved",
			inputPayload:   `{"id":"abc123","jsonrpc":"2.0","method":"test"}`,
			expectedOutput: `{"jsonrpc":"2.0","method":"test","id":"abc123"}`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var req Request
			err := json.Unmarshal([]byte(testCase.inputPayload), &req)
			require.NoError(t, err)

			marshaledRequest, err := json.Marshal(req)
			require.NoError(t, err)

			require.Equal(t, testCase.expectedOutput, string(marshaledRequest))
		})
	}
}

// TestIDEqual tests the Equal method for ID comparison,
// including cross-type comparisons (int vs string) which are important
// for JSON-RPC compatibility with endpoints that may return string IDs
// even when given integer IDs.
func TestIDEqual(t *testing.T) {
	testCases := []struct {
		name     string
		id1      ID
		id2      ID
		expected bool
	}{
		{
			name:     "both empty",
			id1:      ID{},
			id2:      ID{},
			expected: true,
		},
		{
			name:     "same integer",
			id1:      IDFromInt(1),
			id2:      IDFromInt(1),
			expected: true,
		},
		{
			name:     "different integers",
			id1:      IDFromInt(1),
			id2:      IDFromInt(2),
			expected: false,
		},
		{
			name:     "same string",
			id1:      IDFromStr("abc"),
			id2:      IDFromStr("abc"),
			expected: true,
		},
		{
			name:     "different strings",
			id1:      IDFromStr("abc"),
			id2:      IDFromStr("def"),
			expected: false,
		},
		{
			name:     "int vs string with same value",
			id1:      IDFromInt(1),
			id2:      IDFromStr("1"),
			expected: true,
		},
		{
			name:     "string vs int with same value",
			id1:      IDFromStr("1"),
			id2:      IDFromInt(1),
			expected: true,
		},
		{
			name:     "int vs string with different value",
			id1:      IDFromInt(1),
			id2:      IDFromStr("2"),
			expected: false,
		},
		{
			name:     "empty vs int",
			id1:      ID{},
			id2:      IDFromInt(1),
			expected: false,
		},
		{
			name:     "empty vs string",
			id1:      ID{},
			id2:      IDFromStr("abc"),
			expected: false,
		},
		{
			name:     "zero int vs string zero",
			id1:      IDFromInt(0),
			id2:      IDFromStr("0"),
			expected: true,
		},
		{
			name:     "negative int vs string negative",
			id1:      IDFromInt(-1),
			id2:      IDFromStr("-1"),
			expected: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.id1.Equal(tc.id2)
			require.Equal(t, tc.expected, result, "ID1: %v, ID2: %v", tc.id1, tc.id2)

			// Test symmetry: a.Equal(b) should equal b.Equal(a)
			reverseResult := tc.id2.Equal(tc.id1)
			require.Equal(t, tc.expected, reverseResult, "Symmetry check failed for ID1: %v, ID2: %v", tc.id1, tc.id2)
		})
	}
}

// TODO_MVP(@adshmh): add a test case for batch JSONRPC requests
func TestUnmarshalParams(t *testing.T) {
	testCases := []struct {
		name       string
		rawPayload []byte
		expectErr  bool
	}{
		{
			name:       "malformed params field fails to parse",
			rawPayload: []byte(`{"jsonrpc":"2.0","id":1,"method":"test","params":"incomplete...}`),
			expectErr:  true,
		},
		{
			name:       "params field not specified",
			rawPayload: []byte(`{"jsonrpc":"2.0","id":12345678,"method":"eth_chainId"}`),
		},
		{
			name: "params field as a single object",
			// payload example from: https://polkadot.js.org/docs/substrate/rpc/#querystorageatkeys-vec-storagehash-at-hash-option-vec-storagedata
			rawPayload: []byte(`{"jsonrpc":"2.0","id":1,"method":"state_queryStorageAt","params":{"keys": ["0x5f3e4907f716ac89b6347d15ececedca1c0000000000000000"], "at": "0x6857c3c171f65f77f52cd566c574c1f59b0a3738b8d487967e9c54789ee621dd"}}`),
		},
		{
			name: "params field as an array of a single value of a basic type",
			// rawPayload is a copy-paste from: https://solana.com/docs/rpc/http/getblockcommitment
			rawPayload: []byte(`{"jsonrpc":"2.0","id":1,"method":"getBlockCommitment","params":[5]}`),
		},
		{
			name: "params field as an empty list",
			// rawPayload is a copy-paste from: https://ethereum.org/en/developers/docs/apis/json-rpc/#net_version
			rawPayload: []byte(`{"jsonrpc":"2.0","method":"net_version","params":[],"id":67}`),
		},
		{
			name:       "params as array of single object",
			rawPayload: []byte(`{"jsonrpc":"2.0","id":8522963871549545,"method":"eth_getLogs","params":[{"address":["0x1234","0xabcd","0xffff"],"fromBlock":"0x1234567","toBlock":"0xfffffff","topics":[]}]}`),
		},
		{
			name:       "params as array of multiple objects",
			rawPayload: []byte(`{"jsonrpc":"2.0","id":1949014,"method":"eth_call","params":[{"data":"0x12345678","from":"0x0000000000000000000000000000000000000000","to":"0x12345678abcdef12345678abcdef12345678abcd"},"latest"]}`),
		},
		{
			name: "params as mix of string and boolean",
			// rawPayload is a copy-paste from: https://ethereum.org/en/developers/docs/apis/json-rpc/#eth_getblockbynumber
			rawPayload: []byte(`{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["0x1b4", true],"id":1}`),
		},
		{
			name:       "params as an empty object",
			rawPayload: []byte(`{"jsonrpc":"2.0","method":"eth_chainId","id":1,"params":{}}`),
		},
		{
			name:       "missing id field with params",
			rawPayload: []byte(`{"jsonrpc":"2.0","method":"test","params":[1,2,3]}`),
		},
		{
			name:       "null id with params",
			rawPayload: []byte(`{"jsonrpc":"2.0","id":null,"method":"test","params":[1,2,3]}`),
		},
		{
			name:       "empty string id with params",
			rawPayload: []byte(`{"jsonrpc":"2.0","id":"","method":"test","params":[1,2,3]}`),
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := json.Unmarshal(testCase.rawPayload, &Request{})
			if testCase.expectErr {
				require.NotNil(t, err)
				return
			}

			require.NoError(t, err)
		})
	}
}
