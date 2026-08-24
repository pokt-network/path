package evm

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// The gateway's batch collector calls UpdateWithResponse for every item, including one that
// failed every attempt — with nil bytes. That item was then dropped on the way into the
// batch response, the length check rejected the whole batch, and the client got a single
// id:null -32603 in place of the N-1 answers that had been relayed successfully.
//
// Drives the same calls the gateway makes and asserts on what the client receives.
func TestGetHTTPResponse_BatchItemWithoutResponseGetsItsOwnErrorObject(t *testing.T) {
	rc := &requestContext{
		logger:  polyzero.NewLogger(),
		isBatch: true,
		servicePayloads: map[jsonrpc.ID]protocol.Payload{
			jsonrpc.IDFromInt(1): {Data: `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`},
			jsonrpc.IDFromInt(2): {Data: `{"jsonrpc":"2.0","id":2,"method":"eth_blockNumber","params":[]}`},
			jsonrpc.IDFromInt(3): {Data: `{"jsonrpc":"2.0","id":3,"method":"eth_blockNumber","params":[]}`},
		},
	}

	rc.UpdateWithResponse("pokt1a-https://a.example.com", []byte(`{"jsonrpc":"2.0","id":1,"result":"0x10"}`), http.StatusOK, "1")
	rc.UpdateWithResponse("pokt1b-https://b.example.com", nil, 0, "2") // failed every attempt: no body
	rc.UpdateWithResponse("pokt1a-https://a.example.com", []byte(`{"jsonrpc":"2.0","id":3,"result":"0x10"}`), http.StatusOK, "3")

	resp := rc.GetHTTPResponse()
	require.Equal(t, http.StatusOK, resp.GetHTTPStatusCode())

	var got []jsonrpc.Response
	require.NoError(t, json.Unmarshal(resp.GetPayload(), &got), "payload: %s", resp.GetPayload())
	require.Len(t, got, 3, "one response object per request object; payload: %s", resp.GetPayload())

	byID := map[string]jsonrpc.Response{}
	for _, r := range got {
		byID[r.ID.String()] = r
	}
	require.Nil(t, byID["1"].Error)
	require.Nil(t, byID["3"].Error)
	require.NotNil(t, byID["2"].Error, "the item nothing answered gets an error object with ITS id")
	require.Equal(t, jsonrpc.ResponseCodeDefaultInternalErr, byID["2"].Error.Code)
	require.NotContains(t, string(resp.GetPayload()), "length mismatch")
}
