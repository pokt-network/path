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

// A request without an id is NOT a notification once it has been through PATH: every batch
// item is relayed on its own with "id":null written out, and the node answers it with a
// null-id response. Excluding such requests from the expected count rejected every batch
// that carried one with "expected N, got N+1" — seen live the moment it shipped, on two
// services within the hour. One request object, one response object, id or not.
func TestValidateAndBuildBatchResponse_IDLessRequestExpectsItsNullIDResponse(t *testing.T) {
	logger := polyzero.NewLogger()
	payloads := map[ID]protocol.Payload{
		IDFromInt(1): {Data: `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber"}`},
		{}:           {Data: `{"jsonrpc":"2.0","id":null,"method":"eth_blockNumber"}`},
	}

	// The node answered both — the production shape.
	out, err := ValidateAndBuildBatchResponse(logger, []json.RawMessage{
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":"0x10"}`),
		json.RawMessage(`{"jsonrpc":"2.0","id":null,"result":"0x10"}`),
	}, payloads)
	require.NoError(t, err)
	var got []Response
	require.NoError(t, json.Unmarshal(out, &got))
	require.Len(t, got, 2)
	for _, r := range got {
		require.Nil(t, r.Error, "both answers must be returned untouched: %s", out)
	}

	// The id-less item got no answer: it is filled like any other, with a null id.
	out, err = ValidateAndBuildBatchResponse(logger, []json.RawMessage{
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":"0x10"}`),
	}, payloads)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(out, &got))
	require.Len(t, got, 2)
	filled := 0
	for _, r := range got {
		if r.ID.IsEmpty() {
			filled++
			require.NotNil(t, r.Error)
		}
	}
	require.Equal(t, 1, filled)
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

// A response that can be attributed to no request — an id the batch never sent, or bytes that
// are not a JSON-RPC response at all — is still the supplier's answer to SOME item. Filling
// the item it most likely belongs to on top of it made the batch one response long
// ("expected N, got N+1"), and the length check threw the whole batch away. Like a null-id
// response, an unattributable one stands in for a missing request; only requests beyond
// those are filled.
func TestValidateAndBuildBatchResponse_UnattributableResponseCoversAMissingRequest(t *testing.T) {
	logger := polyzero.NewLogger()
	payloads := map[ID]protocol.Payload{
		IDFromInt(1): {Data: `{"id":1}`},
		IDFromInt(2): {Data: `{"id":2}`},
	}

	t.Run("unknown id", func(t *testing.T) {
		out, err := ValidateAndBuildBatchResponse(logger, []json.RawMessage{
			json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`),
			json.RawMessage(`{"jsonrpc":"2.0","id":99,"error":{"code":-32000,"message":"relayer error"}}`),
		}, payloads)
		require.NoError(t, err)
		var got []Response
		require.NoError(t, json.Unmarshal(out, &got))
		require.Len(t, got, 2, "%s", out)
	})

	t.Run("not a json-rpc response", func(t *testing.T) {
		out, err := ValidateAndBuildBatchResponse(logger, []json.RawMessage{
			json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`),
			json.RawMessage(`{"jsonrpc":"2.0","id":2.5,"result":"0x2"}`), // id type the parser rejects
		}, payloads)
		require.NoError(t, err)
		var got []json.RawMessage
		require.NoError(t, json.Unmarshal(out, &got))
		require.Len(t, got, 2, "%s", out)
	})

	t.Run("unattributable plus a genuinely missing request is filled once", func(t *testing.T) {
		three := map[ID]protocol.Payload{
			IDFromInt(1): {Data: `{"id":1}`},
			IDFromInt(2): {Data: `{"id":2}`},
			IDFromInt(3): {Data: `{"id":3}`},
		}
		out, err := ValidateAndBuildBatchResponse(logger, []json.RawMessage{
			json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`),
			json.RawMessage(`{"jsonrpc":"2.0","id":99,"result":"0x2"}`),
		}, three)
		require.NoError(t, err)
		var got []Response
		require.NoError(t, json.Unmarshal(out, &got))
		require.Len(t, got, 3, "%s", out)
	})
}

// The likely production shape: a relay-miner or proxy error with "error" as a bare string.
// It does not unmarshal as a Response, but its id is right there — attribute it, do not fill.
func TestValidateAndBuildBatchResponse_StringErrorResponseIsAttributedByID(t *testing.T) {
	logger := polyzero.NewLogger()
	payloads := map[ID]protocol.Payload{
		IDFromInt(1): {Data: `{"id":1}`},
		IDFromInt(2): {Data: `{"id":2}`},
	}
	out, err := ValidateAndBuildBatchResponse(logger, []json.RawMessage{
		json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`),
		json.RawMessage(`{"jsonrpc":"2.0","id":2,"error":"upstream request timeout"}`),
	}, payloads)
	require.NoError(t, err)
	var got []json.RawMessage
	require.NoError(t, json.Unmarshal(out, &got))
	require.Len(t, got, 2, "%s", out)
	require.Contains(t, string(out), `"upstream request timeout"`, "the supplier's own answer is returned, not a synthesized one")
}
