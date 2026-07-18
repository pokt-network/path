package websockets

import (
	"bytes"
	"encoding/json"
	"sync"
)

// SubscriptionRegistry tracks JSON-RPC subscription state for a single websocket
// bridge so that active subscriptions can be transparently replayed onto a fresh
// endpoint connection after a Shannon session rollover, while the client keeps the
// subscription IDs it already holds.
//
// It is pure (no I/O, no protocol/QoS dependencies): the caller feeds it every
// client-to-endpoint and endpoint-to-client JSON-RPC frame and acts on the return
// values. The subscription-aware message path (gateway/QoS) drives it — the
// protocol-agnostic frame pump does not.
//
// Scope: the near-universal single (non-batch) eth_subscribe / eth_unsubscribe
// shape used by EVM chains — the only WS services PATH serves. Batch-embedded
// subscribes are left untracked (they degrade to the pre-rebind behavior: lost on
// rollover) rather than mis-tracked.
//
// The three concerns it solves:
//   - replay: remember each eth_subscribe request verbatim so it can be re-sent
//     (re-signed by the caller with the current session) on a new connection.
//   - stable IDs: the FIRST subscription id the endpoint returns is frozen as the
//     client-facing id; the client holds it for the connection's life. Post-reconnect
//     the supplier assigns a NEW id — that is internal only and never reaches the client.
//   - remap: rewrite outbound eth_subscription notifications (current supplier id →
//     client-facing id) and inbound eth_unsubscribe (client-facing id → current
//     supplier id).
//
// Concurrency: the bridge processes client and endpoint frames on its single
// message-processing goroutine, so registry calls are already serialized. The mutex
// is defensive — it keeps the registry correct if a reconnect ever replays frames
// from a different goroutine than the pump.
type SubscriptionRegistry struct {
	mu sync.Mutex

	// byReqID indexes subscriptions by the JSON-RPC request id of their
	// eth_subscribe call, used to match the subscribe response back to the request.
	byReqID map[string]*subscription
	// byClientSubID indexes by the frozen client-facing subscription id, used to
	// rewrite inbound eth_unsubscribe.
	byClientSubID map[string]*subscription
	// byCurrentSubID indexes by the current supplier subscription id, used to
	// rewrite outbound eth_subscription notifications.
	byCurrentSubID map[string]*subscription

	// order preserves subscription creation order so replay is deterministic.
	order []*subscription
}

// subscription is the per-eth_subscribe state the registry reconstructs after a
// reconnect.
type subscription struct {
	// reqID is the JSON-RPC request id (raw, trimmed) of the eth_subscribe call.
	reqID string
	// subscribeFrame is the raw client eth_subscribe JSON, replayed verbatim (and
	// re-signed by the caller) onto a new endpoint connection after a rollover.
	subscribeFrame []byte
	// clientSubID is the subscription id the client received on the FIRST subscribe
	// response and now holds forever. Empty until that first response arrives.
	clientSubID string
	// currentSubID is the subscription id from the CURRENT endpoint connection.
	// After a reconnect it differs from clientSubID; notifications carrying it are
	// rewritten to clientSubID before forwarding.
	currentSubID string
}

// NewSubscriptionRegistry returns an empty registry for one bridge.
func NewSubscriptionRegistry() *SubscriptionRegistry {
	return &SubscriptionRegistry{
		byReqID:        make(map[string]*subscription),
		byClientSubID:  make(map[string]*subscription),
		byCurrentSubID: make(map[string]*subscription),
	}
}

// wsRPCRequest is the minimal shape parsed from a client-to-endpoint frame.
type wsRPCRequest struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// wsRPCResponse is the minimal shape parsed from an endpoint-to-client frame,
// covering both a subscribe response (id+result) and a notification (method+params).
type wsRPCResponse struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

// subscriptionNotificationParams is the params of an eth_subscription notification:
// {"subscription":"0x..","result":{...}}. result is kept as raw bytes so the payload
// is preserved byte-for-byte across a rewrite.
type subscriptionNotificationParams struct {
	Subscription string          `json:"subscription"`
	Result       json.RawMessage `json:"result,omitempty"`
}

const (
	methodSubscribe    = "eth_subscribe"
	methodUnsubscribe  = "eth_unsubscribe"
	methodNotification = "eth_subscription"
)

// TrackClientMessage inspects a client-to-endpoint JSON-RPC frame (raw, before it is
// wrapped/signed for the endpoint) and returns the frame to forward to the endpoint:
//   - eth_subscribe: records the subscription for later replay; frame unchanged.
//   - eth_unsubscribe: rewrites the client-facing subscription id to the current
//     supplier id (a no-op until a reconnect has remapped it) and removes the
//     subscription.
//   - anything else (including batches and malformed frames): returned unchanged.
func (r *SubscriptionRegistry) TrackClientMessage(frame []byte) []byte {
	if isBatchOrEmpty(frame) {
		return frame
	}

	var req wsRPCRequest
	if err := json.Unmarshal(frame, &req); err != nil {
		return frame
	}

	switch req.Method {
	case methodSubscribe:
		reqID := rawKey(req.ID)
		if reqID == "" {
			return frame
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		// A fresh subscribe on a reused request id replaces the old tracking.
		if existing, ok := r.byReqID[reqID]; ok {
			r.removeLocked(existing)
		}
		sub := &subscription{
			reqID:          reqID,
			subscribeFrame: append([]byte(nil), frame...),
		}
		r.byReqID[reqID] = sub
		r.order = append(r.order, sub)
		return frame

	case methodUnsubscribe:
		clientSubID, ok := firstStringParam(req.Params)
		if !ok {
			return frame
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		sub, ok := r.byClientSubID[clientSubID]
		if !ok {
			return frame
		}
		rewritten := frame
		if sub.currentSubID != "" && sub.currentSubID != clientSubID {
			if out, ok := rewriteUnsubscribeParam(frame, sub.currentSubID); ok {
				rewritten = out
			}
		}
		r.removeLocked(sub)
		return rewritten

	default:
		return frame
	}
}

// TranslateEndpointMessage inspects an endpoint-to-client JSON-RPC frame (the decoded
// response/notification body) and returns the frame to forward plus whether to
// forward it at all:
//   - subscribe response, initial: freezes the client-facing subscription id; forwards.
//   - subscribe response, post-reconnect replay: updates the current supplier id and
//     returns forward=false so the duplicate response is NOT delivered to the client.
//   - subscribe error response: drops the tracked subscription; forwards unchanged.
//   - eth_subscription notification: rewrites params.subscription to the client-facing
//     id; forwards.
//   - anything else: returned unchanged, forward=true.
func (r *SubscriptionRegistry) TranslateEndpointMessage(frame []byte) ([]byte, bool) {
	if isBatchOrEmpty(frame) {
		return frame, true
	}

	var resp wsRPCResponse
	if err := json.Unmarshal(frame, &resp); err != nil {
		return frame, true
	}

	// Notification path: method set, no id.
	if resp.Method == methodNotification {
		return r.translateNotification(frame, resp.Params)
	}

	// Response path: id present, no method.
	reqID := rawKey(resp.ID)
	if reqID == "" {
		return frame, true
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	sub, ok := r.byReqID[reqID]
	if !ok {
		return frame, true
	}

	subID, ok := rawString(resp.Result)
	if !ok {
		// A subscribe that did not return a string subscription id (error or
		// unexpected shape): stop tracking it and let the client see the response.
		r.removeLocked(sub)
		return frame, true
	}

	if sub.clientSubID == "" {
		// Initial subscribe response: freeze the client-facing id and forward.
		sub.clientSubID = subID
		sub.currentSubID = subID
		r.byClientSubID[subID] = sub
		r.byCurrentSubID[subID] = sub
		return frame, true
	}

	// Post-reconnect replay response: the client already holds clientSubID. Rebind the
	// current supplier id to the new one and swallow the duplicate response.
	if sub.currentSubID != "" {
		delete(r.byCurrentSubID, sub.currentSubID)
	}
	sub.currentSubID = subID
	r.byCurrentSubID[subID] = sub
	return frame, false
}

// translateNotification rewrites an eth_subscription notification's subscription id
// from the current supplier id to the frozen client-facing id.
func (r *SubscriptionRegistry) translateNotification(frame []byte, rawParams json.RawMessage) ([]byte, bool) {
	var params subscriptionNotificationParams
	if err := json.Unmarshal(rawParams, &params); err != nil || params.Subscription == "" {
		return frame, true
	}

	r.mu.Lock()
	sub, ok := r.byCurrentSubID[params.Subscription]
	if !ok || sub.clientSubID == "" || sub.clientSubID == params.Subscription {
		r.mu.Unlock()
		return frame, true
	}
	clientSubID := sub.clientSubID
	r.mu.Unlock()

	out, ok := rewriteNotificationSubscription(frame, clientSubID)
	if !ok {
		return frame, true
	}
	return out, true
}

// ActiveSubscribeFrames returns the raw client eth_subscribe frames for every
// established subscription, in creation order, so the caller can replay them (re-sign
// and re-send) onto a fresh endpoint connection after a session rollover. Only
// subscriptions that already returned a client-facing id are included; a subscribe
// still awaiting its first response is left to the client's own retry.
func (r *SubscriptionRegistry) ActiveSubscribeFrames() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()

	frames := make([][]byte, 0, len(r.order))
	for _, sub := range r.order {
		if sub.clientSubID == "" {
			continue
		}
		frames = append(frames, append([]byte(nil), sub.subscribeFrame...))
	}
	return frames
}

// Len reports the number of tracked subscriptions (test/observability aid).
func (r *SubscriptionRegistry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.byReqID)
}

// removeLocked deletes a subscription from every index and the order slice. Caller
// must hold r.mu.
func (r *SubscriptionRegistry) removeLocked(sub *subscription) {
	delete(r.byReqID, sub.reqID)
	if sub.clientSubID != "" {
		delete(r.byClientSubID, sub.clientSubID)
	}
	if sub.currentSubID != "" {
		delete(r.byCurrentSubID, sub.currentSubID)
	}
	for i, s := range r.order {
		if s == sub {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}

// ---------- JSON helpers (pure) ----------

// isBatchOrEmpty reports whether a frame is a JSON batch (leading '[') or has no
// meaningful content — both are passed through untouched.
func isBatchOrEmpty(frame []byte) bool {
	trimmed := bytes.TrimSpace(frame)
	return len(trimmed) == 0 || trimmed[0] == '['
}

// rawKey normalizes a raw JSON-RPC id to a stable map key. Both peers echo the id
// with the same encoding, so the trimmed raw bytes match without further parsing.
// A JSON null id is treated as absent.
func rawKey(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	return string(trimmed)
}

// rawString unmarshals a raw JSON value as a string, reporting whether it was one.
func rawString(raw json.RawMessage) (string, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// firstStringParam extracts the first element of a JSON array params as a string,
// e.g. eth_unsubscribe(["0x.."]).
func firstStringParam(raw json.RawMessage) (string, bool) {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
		return "", false
	}
	return rawString(arr[0])
}

// rewriteUnsubscribeParam returns the frame with the first param replaced by newID,
// preserving any additional params.
func rewriteUnsubscribeParam(frame []byte, newID string) ([]byte, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(frame, &m); err != nil {
		return nil, false
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(m["params"], &arr); err != nil || len(arr) == 0 {
		return nil, false
	}
	newParam, err := json.Marshal(newID)
	if err != nil {
		return nil, false
	}
	arr[0] = newParam
	newParams, err := json.Marshal(arr)
	if err != nil {
		return nil, false
	}
	m["params"] = newParams
	out, err := json.Marshal(m)
	if err != nil {
		return nil, false
	}
	return out, true
}

// rewriteNotificationSubscription returns the notification frame with
// params.subscription replaced by clientSubID, preserving params.result byte-for-byte.
func rewriteNotificationSubscription(frame []byte, clientSubID string) ([]byte, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(frame, &m); err != nil {
		return nil, false
	}
	var params subscriptionNotificationParams
	if err := json.Unmarshal(m["params"], &params); err != nil {
		return nil, false
	}
	params.Subscription = clientSubID
	newParams, err := json.Marshal(params)
	if err != nil {
		return nil, false
	}
	m["params"] = newParams
	out, err := json.Marshal(m)
	if err != nil {
		return nil, false
	}
	return out, true
}
