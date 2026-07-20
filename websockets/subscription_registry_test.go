package websockets

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// helper: run a client-to-endpoint frame through the registry.
func track(r *SubscriptionRegistry, frame string) string {
	return string(r.TrackClientMessage([]byte(frame)))
}

// helper: run an endpoint-to-client frame through the registry.
func translate(r *SubscriptionRegistry, frame string) (string, bool) {
	out, fwd := r.TranslateEndpointMessage([]byte(frame))
	return string(out), fwd
}

func Test_SubscriptionRegistry_PassthroughTraffic(t *testing.T) {
	r := NewSubscriptionRegistry()

	cases := []string{
		`{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`,           // normal request
		`{"jsonrpc":"2.0","id":1,"result":"0x1"}`,                                   // normal response
		`[{"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["newHeads"]}]`, // batch (untracked)
		`not-json`, // malformed
		``,         // empty
		`  `,       // whitespace only
	}
	for _, c := range cases {
		require.Equal(t, c, track(r, c), "client frame must pass through unchanged")
		out, fwd := translate(r, c)
		require.True(t, fwd, "endpoint frame must be forwarded")
		require.Equal(t, c, out, "endpoint frame must pass through unchanged")
	}
	require.Equal(t, 0, r.Len(), "no subscriptions should be tracked")
	require.Empty(t, r.ActiveSubscribeFrames())
}

func Test_SubscriptionRegistry_InitialSubscribeFreezesClientID(t *testing.T) {
	r := NewSubscriptionRegistry()

	subReq := `{"jsonrpc":"2.0","id":7,"method":"eth_subscribe","params":["newHeads"]}`
	require.Equal(t, subReq, track(r, subReq), "subscribe forwarded verbatim")
	require.Equal(t, 1, r.Len())

	// Initial response freezes the client-facing subscription id and is forwarded.
	resp := `{"jsonrpc":"2.0","id":7,"result":"0xAAA"}`
	out, fwd := translate(r, resp)
	require.True(t, fwd)
	require.Equal(t, resp, out)

	// Now it is an active subscription eligible for replay.
	frames := r.ActiveSubscribeFrames()
	require.Len(t, frames, 1)
	require.JSONEq(t, subReq, string(frames[0]))
}

func Test_SubscriptionRegistry_NotificationUnchangedBeforeReconnect(t *testing.T) {
	r := NewSubscriptionRegistry()
	track(r, `{"jsonrpc":"2.0","id":7,"method":"eth_subscribe","params":["newHeads"]}`)
	translate(r, `{"jsonrpc":"2.0","id":7,"result":"0xAAA"}`)

	// clientSubID == currentSubID (no reconnect yet) → notification passes through as-is.
	notif := `{"jsonrpc":"2.0","method":"eth_subscription","params":{"subscription":"0xAAA","result":{"number":"0x1"}}}`
	out, fwd := translate(r, notif)
	require.True(t, fwd)
	require.Equal(t, notif, out, "no rewrite needed before a reconnect")
}

func Test_SubscriptionRegistry_ReconnectRemapsIDs(t *testing.T) {
	r := NewSubscriptionRegistry()
	subReq := `{"jsonrpc":"2.0","id":7,"method":"eth_subscribe","params":["newHeads"]}`
	track(r, subReq)
	translate(r, `{"jsonrpc":"2.0","id":7,"result":"0xAAA"}`) // client now holds 0xAAA

	// --- session rollover: caller replays subscribe frames onto the new connection ---
	frames := r.ActiveSubscribeFrames()
	require.Len(t, frames, 1)
	require.JSONEq(t, subReq, string(frames[0]))

	// The new endpoint returns a DIFFERENT subscription id. The replay response must be
	// swallowed (client already has 0xAAA) and the current id rebound to 0xBBB.
	out, fwd := translate(r, `{"jsonrpc":"2.0","id":7,"result":"0xBBB"}`)
	require.False(t, fwd, "replay response must not reach the client")
	require.Equal(t, `{"jsonrpc":"2.0","id":7,"result":"0xBBB"}`, out)

	// A notification on the NEW id must be rewritten back to the frozen client id.
	notif := `{"jsonrpc":"2.0","method":"eth_subscription","params":{"subscription":"0xBBB","result":{"number":"0x2"}}}`
	out, fwd = translate(r, notif)
	require.True(t, fwd)
	require.JSONEq(t,
		`{"jsonrpc":"2.0","method":"eth_subscription","params":{"subscription":"0xAAA","result":{"number":"0x2"}}}`,
		out,
		"notification subscription id rewritten to client-facing id; result preserved",
	)

	// A stray notification on the OLD id is no longer mapped → passthrough.
	stale := `{"jsonrpc":"2.0","method":"eth_subscription","params":{"subscription":"0xAAA","result":{}}}`
	_, fwd = translate(r, stale)
	require.True(t, fwd)
}

func Test_SubscriptionRegistry_UnsubscribeAfterReconnectRewritten(t *testing.T) {
	r := NewSubscriptionRegistry()
	track(r, `{"jsonrpc":"2.0","id":7,"method":"eth_subscribe","params":["newHeads"]}`)
	translate(r, `{"jsonrpc":"2.0","id":7,"result":"0xAAA"}`)
	translate(r, `{"jsonrpc":"2.0","id":7,"result":"0xBBB"}`) // reconnect: current is now 0xBBB

	// Client unsubscribes with the id it holds (0xAAA); must be rewritten to 0xBBB for
	// the current supplier.
	out := track(r, `{"jsonrpc":"2.0","id":9,"method":"eth_unsubscribe","params":["0xAAA"]}`)
	require.JSONEq(t, `{"jsonrpc":"2.0","id":9,"method":"eth_unsubscribe","params":["0xBBB"]}`, out)

	// Subscription is gone: not replayed, not tracked.
	require.Equal(t, 0, r.Len())
	require.Empty(t, r.ActiveSubscribeFrames())
}

func Test_SubscriptionRegistry_UnsubscribeBeforeReconnectUnchanged(t *testing.T) {
	r := NewSubscriptionRegistry()
	track(r, `{"jsonrpc":"2.0","id":7,"method":"eth_subscribe","params":["newHeads"]}`)
	translate(r, `{"jsonrpc":"2.0","id":7,"result":"0xAAA"}`)

	unsub := `{"jsonrpc":"2.0","id":9,"method":"eth_unsubscribe","params":["0xAAA"]}`
	require.Equal(t, unsub, track(r, unsub), "no reconnect → no rewrite")
	require.Equal(t, 0, r.Len())
}

func Test_SubscriptionRegistry_UnsubscribeUnknownIDPassthrough(t *testing.T) {
	r := NewSubscriptionRegistry()
	unsub := `{"jsonrpc":"2.0","id":9,"method":"eth_unsubscribe","params":["0xUNKNOWN"]}`
	require.Equal(t, unsub, track(r, unsub))
}

func Test_SubscriptionRegistry_FailedSubscribeDropsTracking(t *testing.T) {
	r := NewSubscriptionRegistry()
	track(r, `{"jsonrpc":"2.0","id":7,"method":"eth_subscribe","params":["newHeads"]}`)
	require.Equal(t, 1, r.Len())

	// Error response (no string result) → stop tracking, forward to client.
	errResp := `{"jsonrpc":"2.0","id":7,"error":{"code":-32000,"message":"subscribe failed"}}`
	out, fwd := translate(r, errResp)
	require.True(t, fwd)
	require.Equal(t, errResp, out)
	require.Equal(t, 0, r.Len())
	require.Empty(t, r.ActiveSubscribeFrames())
}

func Test_SubscriptionRegistry_PendingSubscribeNotReplayed(t *testing.T) {
	r := NewSubscriptionRegistry()
	// Subscribe sent but no response yet: tracked, but not eligible for replay because
	// the client never received a subscription id to keep stable.
	track(r, `{"jsonrpc":"2.0","id":7,"method":"eth_subscribe","params":["newHeads"]}`)
	require.Equal(t, 1, r.Len())
	require.Empty(t, r.ActiveSubscribeFrames())
}

func Test_SubscriptionRegistry_MultipleSubscriptionsOrderPreserved(t *testing.T) {
	r := NewSubscriptionRegistry()
	sub1 := `{"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["newHeads"]}`
	sub2 := `{"jsonrpc":"2.0","id":2,"method":"eth_subscribe","params":["logs",{"address":"0xabc"}]}`
	sub3 := `{"jsonrpc":"2.0","id":3,"method":"eth_subscribe","params":["newPendingTransactions"]}`
	track(r, sub1)
	track(r, sub2)
	track(r, sub3)
	translate(r, `{"jsonrpc":"2.0","id":1,"result":"0x1"}`)
	translate(r, `{"jsonrpc":"2.0","id":2,"result":"0x2"}`)
	translate(r, `{"jsonrpc":"2.0","id":3,"result":"0x3"}`)

	frames := r.ActiveSubscribeFrames()
	require.Len(t, frames, 3)
	require.JSONEq(t, sub1, string(frames[0]))
	require.JSONEq(t, sub2, string(frames[1]))
	require.JSONEq(t, sub3, string(frames[2]))

	// Unsubscribing the middle one preserves the order of the rest.
	track(r, `{"jsonrpc":"2.0","id":9,"method":"eth_unsubscribe","params":["0x2"]}`)
	frames = r.ActiveSubscribeFrames()
	require.Len(t, frames, 2)
	require.JSONEq(t, sub1, string(frames[0]))
	require.JSONEq(t, sub3, string(frames[1]))
}

func Test_SubscriptionRegistry_StringRequestIDSupported(t *testing.T) {
	r := NewSubscriptionRegistry()
	// Some clients use string JSON-RPC ids.
	track(r, `{"jsonrpc":"2.0","id":"sub-a","method":"eth_subscribe","params":["newHeads"]}`)
	out, fwd := translate(r, `{"jsonrpc":"2.0","id":"sub-a","result":"0xAAA"}`)
	require.True(t, fwd)
	require.Contains(t, out, "0xAAA")
	require.Len(t, r.ActiveSubscribeFrames(), 1)
}

func Test_SubscriptionRegistry_ResubscribeReusedRequestID(t *testing.T) {
	r := NewSubscriptionRegistry()
	// Subscribe, unsubscribe, then reuse the same request id for a new subscribe.
	track(r, `{"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["newHeads"]}`)
	translate(r, `{"jsonrpc":"2.0","id":1,"result":"0xAAA"}`)
	track(r, `{"jsonrpc":"2.0","id":9,"method":"eth_unsubscribe","params":["0xAAA"]}`)
	require.Equal(t, 0, r.Len())

	// Reuse id 1 for a brand-new subscription; the new response freezes a new id.
	track(r, `{"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["logs"]}`)
	out, fwd := translate(r, `{"jsonrpc":"2.0","id":1,"result":"0xCCC"}`)
	require.True(t, fwd, "this is a fresh initial subscribe, not a replay")
	require.Contains(t, out, "0xCCC")
	require.Equal(t, 1, r.Len())
}
