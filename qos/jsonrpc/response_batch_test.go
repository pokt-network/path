package jsonrpc

import (
	"encoding/json"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// A batch item that fails every attempt produces no response body, and the batch was then
// one response short. The length check turned that into a single id:null -32603 for the
// WHOLE batch — every other item's answer, already paid for, thrown away. Measured on one
// batch-heavy service: ~7-8% of requests, every one of them 'expected N, got N-1' or close.
//
// Per JSON-RPC 2.0 each request object gets its own response object; a request PATH could
// not serve gets an error object carrying its id, next to the successes.
func TestValidateAndBuildBatchResponse_FillsMissingResponsesWithPerIDErrors(t *testing.T) {
	logger := polyzero.NewLogger()

	payloads := map[ID]protocol.Payload{
		IDFromInt(1):     {Data: `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber"}`},
		IDFromInt(2):     {Data: `{"jsonrpc":"2.0","id":2,"method":"eth_blockNumber"}`},
		IDFromStr("abc"): {Data: `{"jsonrpc":"2.0","id":"abc","method":"eth_blockNumber"}`},
	}
	responses := []json.RawMessage{
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":"0x10"}`),
	}

	out, err := ValidateAndBuildBatchResponse(logger, responses, payloads)
	require.NoError(t, err)

	var got []Response
	require.NoError(t, json.Unmarshal(out, &got))
	require.Len(t, got, 3, "one response object per request object")

	byID := map[string]Response{}
	for _, r := range got {
		byID[r.ID.String()] = r
	}
	require.Nil(t, byID["1"].Error, "the answer we had must be returned untouched")

	for _, id := range []string{"2", "abc"} {
		r, ok := byID[id]
		require.True(t, ok, "request %s must get a response object", id)
		require.NotNil(t, r.Error)
		require.Equal(t, ResponseCodeDefaultInternalErr, r.Error.Code)
		data, ok := r.Error.Data.(map[string]any)
		require.True(t, ok, "error data must be an object")
		require.Equal(t, "true", data["retryable"])
	}

	// The id must keep its JSON type: the client correlates on it.
	require.Contains(t, string(out), `"id":2`)
	require.Contains(t, string(out), `"id":"abc"`)
}

// Same-value ids are legal in a batch (node-equivalent behaviour): one response for two
// id:1 requests means one of them is still missing.
func TestValidateAndBuildBatchResponse_FillsMissingDuplicateValueIDs(t *testing.T) {
	logger := polyzero.NewLogger()
	payloads := map[ID]protocol.Payload{
		IDFromInt(1): {Data: `{"id":1}`},
		IDFromInt(1): {Data: `{"id":1}`}, // distinct key: ID holds a pointer
	}
	require.Len(t, payloads, 2, "test precondition: same-value ids are distinct keys")

	out, err := ValidateAndBuildBatchResponse(logger, []json.RawMessage{
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":"0x10"}`),
	}, payloads)
	require.NoError(t, err)
	var got []Response
	require.NoError(t, json.Unmarshal(out, &got))
	require.Len(t, got, 2)
}

// A null-id response is the endpoint saying it could not read the request's id; it stands
// in for one missing request and must not be doubled up with a synthesized error.
func TestValidateAndBuildBatchResponse_NullIDResponseStillCoversAMissingRequest(t *testing.T) {
	logger := polyzero.NewLogger()
	payloads := map[ID]protocol.Payload{
		IDFromInt(1): {Data: `{"id":1}`},
		IDFromInt(2): {Data: `{"id":2}`},
	}
	out, err := ValidateAndBuildBatchResponse(logger, []json.RawMessage{
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":"0x10"}`),
		json.RawMessage(`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`),
	}, payloads)
	require.NoError(t, err)
	var got []Response
	require.NoError(t, json.Unmarshal(out, &got))
	require.Len(t, got, 2)
}

// Notifications (no id) never get a response object; they must not be "filled".
func TestValidateAndBuildBatchResponse_NotificationsAreNotFilled(t *testing.T) {
	logger := polyzero.NewLogger()
	payloads := map[ID]protocol.Payload{
		IDFromInt(1): {Data: `{"id":1}`},
		{}:           {Data: `{"method":"notify"}`},
	}
	out, err := ValidateAndBuildBatchResponse(logger, []json.RawMessage{
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":"0x10"}`),
	}, payloads)
	require.NoError(t, err)
	var got []Response
	require.NoError(t, json.Unmarshal(out, &got))
	require.Len(t, got, 1)
}

// More responses than requests for one id: keep the LAST one per request slot (a retry's
// success supersedes the attempt that failed), never reject the batch.
func TestValidateAndBuildBatchResponse_SurplusResponsesForAnIDKeepTheLatest(t *testing.T) {
	logger := polyzero.NewLogger()
	payloads := map[ID]protocol.Payload{
		IDFromInt(1): {Data: `{"id":1}`},
		IDFromInt(2): {Data: `{"id":2}`},
	}
	out, err := ValidateAndBuildBatchResponse(logger, []json.RawMessage{
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"boom"}}`),
		json.RawMessage(`{"jsonrpc":"2.0","id":2,"result":"0x2"}`),
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`),
	}, payloads)
	require.NoError(t, err)
	var got []Response
	require.NoError(t, json.Unmarshal(out, &got))
	require.Len(t, got, 2)
	byID := map[string]Response{}
	for _, r := range got {
		byID[r.ID.String()] = r
	}
	require.Nil(t, byID["1"].Error, "the later (successful) response for id 1 wins")
	require.JSONEq(t, `"0x1"`, string(*byID["1"].Result))
	require.JSONEq(t, `"0x2"`, string(*byID["2"].Result))
}

// Same-value ids: two id:1 requests keep two id:1 responses — the LAST two.
func TestValidateAndBuildBatchResponse_SurplusWithDuplicateValueIDsKeepsOnePerRequest(t *testing.T) {
	logger := polyzero.NewLogger()
	payloads := map[ID]protocol.Payload{
		IDFromInt(1): {Data: `{"id":1}`},
		IDFromInt(1): {Data: `{"id":1}`},
	}
	require.Len(t, payloads, 2)
	out, err := ValidateAndBuildBatchResponse(logger, []json.RawMessage{
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":"first"}`),
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":"second"}`),
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":"third"}`),
	}, payloads)
	require.NoError(t, err)
	var got []Response
	require.NoError(t, json.Unmarshal(out, &got))
	require.Len(t, got, 2)
	require.ElementsMatch(t, []string{`"second"`, `"third"`}, []string{string(*got[0].Result), string(*got[1].Result)})
}
