package shannon

import (
	"testing"

	sdktypes "github.com/pokt-network/shannon-sdk/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// serializePOKTHTTPResponse builds the wire form the relay miner puts in
// RelayResponse.Payload for a control/error response — the same proto.Marshal of
// a POKTHTTPResponse that sdk.SerializeHTTPResponse produces.
func serializePOKTHTTPResponse(t *testing.T, status uint32, body string) []byte {
	t.Helper()
	bz, err := proto.Marshal(&sdktypes.POKTHTTPResponse{
		StatusCode: status,
		BodyBz:     []byte(body),
	})
	require.NoError(t, err)
	return bz
}

// Test_extractWebsocketMessageBody guards the fix for the session-expired
// websocket bug: the relay miner delivers control/error responses (e.g. HTTP 410
// "session expired") as a serialized POKTHTTPResponse envelope in the same payload
// field used for raw streaming frames. The old code forwarded that envelope
// verbatim, so the client received an undecoded protobuf blob AND the error was
// recorded as a healthy (2xx) message. extractWebsocketMessageBody must:
//   - forward raw JSON frames unchanged, reporting status 200,
//   - unwrap POKTHTTPResponse envelopes to their body + real status,
//   - never misparse a normal frame as an envelope.
func Test_extractWebsocketMessageBody(t *testing.T) {
	const (
		sessionExpired = `{"error":"session expired"}`
		jsonRPCResult  = `{"jsonrpc":"2.0","id":1,"result":"0x1"}`
	)

	tests := []struct {
		name           string
		payload        []byte
		wantBody       string
		wantStatusCode int
		wantUnwrapped  bool
	}{
		{
			name:           "raw JSON-RPC response forwarded verbatim as 200",
			payload:        []byte(jsonRPCResult),
			wantBody:       jsonRPCResult,
			wantStatusCode: 200,
			wantUnwrapped:  false,
		},
		{
			name:           "raw JSON-RPC batch response forwarded verbatim",
			payload:        []byte(`[{"jsonrpc":"2.0","id":1,"result":"0x1"}]`),
			wantBody:       `[{"jsonrpc":"2.0","id":1,"result":"0x1"}]`,
			wantStatusCode: 200,
			wantUnwrapped:  false,
		},
		{
			name:           "subscription push with leading whitespace forwarded verbatim",
			payload:        []byte("  \n" + `{"jsonrpc":"2.0","method":"eth_subscription","params":{}}`),
			wantBody:       "  \n" + `{"jsonrpc":"2.0","method":"eth_subscription","params":{}}`,
			wantStatusCode: 200,
			wantUnwrapped:  false,
		},
		{
			name:           "410 session-expired envelope unwrapped to its body",
			payload:        serializePOKTHTTPResponse(t, 410, sessionExpired),
			wantBody:       sessionExpired,
			wantStatusCode: 410,
			wantUnwrapped:  true,
		},
		{
			name:           "200 envelope unwrapped to its JSON body",
			payload:        serializePOKTHTTPResponse(t, 200, jsonRPCResult),
			wantBody:       jsonRPCResult,
			wantStatusCode: 200,
			wantUnwrapped:  true,
		},
		{
			name:           "empty payload does not panic, treated as raw frame",
			payload:        []byte{},
			wantBody:       "",
			wantStatusCode: 200,
			wantUnwrapped:  false,
		},
		{
			name:           "non-JSON non-protobuf bytes fall back to raw frame",
			payload:        []byte("not-json-not-proto"),
			wantBody:       "not-json-not-proto",
			wantStatusCode: 200,
			wantUnwrapped:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, statusCode, unwrapped := extractWebsocketMessageBody(tt.payload)
			require.Equal(t, tt.wantUnwrapped, unwrapped, "unwrapped mismatch")
			require.Equal(t, tt.wantStatusCode, statusCode, "status mismatch")
			require.Equal(t, tt.wantBody, string(body), "body mismatch")
		})
	}
}
