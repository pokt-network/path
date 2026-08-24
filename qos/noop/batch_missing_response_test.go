package noop

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/qos/jsonrpc"
)

// A batch item that fails every attempt reaches UpdateWithResponse with nil bytes. The
// pass-through QoS silently dropped it: the client got N-1 response objects and HTTP 200,
// with nothing at all for the id it was waiting on. Every request object in a batch gets a
// response object; the one nothing answered gets an error object with its own id.
func TestNoOpQoS_BatchItemWithoutResponseGetsItsOwnErrorObject(t *testing.T) {
	batchBody := `[{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":60},{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":"sixty-one"},{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":62}]`

	qos := newTestQoS()
	httpReq := &http.Request{
		Method: http.MethodPost,
		URL:    &url.URL{Path: "/"},
		Body:   io.NopCloser(bytes.NewBufferString(batchBody)),
	}
	reqCtx, ok := qos.ParseHTTPRequest(context.Background(), httpReq, sharedtypes.RPCType_JSON_RPC)
	require.True(t, ok)
	require.Len(t, reqCtx.GetServicePayloads(), 3)

	// The same calls the gateway's batch collector makes, in item order.
	reqCtx.UpdateWithResponse("pokt1a-https://a.example.com", []byte(`{"jsonrpc":"2.0","id":60,"result":"0x10"}`), http.StatusOK, "60")
	reqCtx.UpdateWithResponse("pokt1b-https://b.example.com", nil, 0, "sixty-one") // failed every attempt: no body
	reqCtx.UpdateWithResponse("pokt1a-https://a.example.com", []byte(`{"jsonrpc":"2.0","id":62,"result":"0x10"}`), http.StatusOK, "62")

	resp := reqCtx.GetHTTPResponse()
	require.Equal(t, http.StatusOK, resp.GetHTTPStatusCode())

	var got []jsonrpc.Response
	require.NoError(t, json.Unmarshal(resp.GetPayload(), &got), "payload: %s", resp.GetPayload())
	require.Len(t, got, 3, "one response object per request object; payload: %s", resp.GetPayload())

	byID := map[string]jsonrpc.Response{}
	for _, r := range got {
		byID[r.ID.String()] = r
	}
	require.Nil(t, byID["60"].Error)
	require.Nil(t, byID["62"].Error)
	r, ok := byID["sixty-one"]
	require.True(t, ok, "the item nothing answered gets an error object with ITS id")
	require.NotNil(t, r.Error)
	require.Equal(t, jsonrpc.ResponseCodeDefaultInternalErr, r.Error.Code)
	// The id keeps its JSON type.
	require.Contains(t, string(resp.GetPayload()), `"id":"sixty-one"`)
}
