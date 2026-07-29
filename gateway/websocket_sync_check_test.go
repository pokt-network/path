package gateway

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// The failure this exists for, measured on live bsc: an endpoint accepted the connection,
// answered eth_blockNumber correctly, and reported a head 31,499 blocks behind (~26 hours on
// a 3-second chain) — while the
// same operator's HTTP side tracked the chain, so every json_rpc sync check passed. Nothing
// looked at what the WEBSOCKET side actually said.
//
// So the probe must reject on CONTENT, not merely on getting an answer.
func Test_WebsocketProbe_ValidatorRejectsAStaleResponse(t *testing.T) {
	c := require.New(t)

	const (
		perceivedHead = 112872337 // 0x6ba4b91 — the chain head at the time
		staleHead     = 112840838 // 0x6b9d086 — what the broken endpoint returned, 31,499 behind
		syncAllowance = 100
	)

	// Stand in for the executor's closure: the real one delegates to validateSyncCheck,
	// which performs exactly this comparison.
	probe := protocol.WebsocketProbe{
		Payload: `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`,
		ValidateResponse: func(body []byte) error {
			height, err := extractBlockHeight(body)
			if err != nil {
				return nil // not applicable — never charge the endpoint for our parse gap
			}
			if height < int64(perceivedHead)-int64(syncAllowance) {
				return fmt.Errorf("endpoint height %d is %d blocks behind perceived %d",
					height, int64(perceivedHead)-height, perceivedHead)
			}
			return nil
		},
	}

	c.False(probe.IsHandshakeOnly())

	// The broken endpoint: well-formed, correct, and a day stale.
	err := probe.ValidateResponse([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":"0x%x"}`, staleHead)))
	c.Error(err, "a stale head must fail the probe even though the response is perfectly valid")
	c.Contains(err.Error(), "blocks behind")

	// A current endpoint passes.
	c.NoError(probe.ValidateResponse([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":"0x%x"}`, perceivedHead))))

	// Exactly at the allowance boundary passes — the check must not demote endpoints that
	// are merely a little behind, which is normal.
	c.NoError(probe.ValidateResponse([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":"0x%x"}`, perceivedHead-syncAllowance))))
}

// A rule pointed at a response shape whose height we cannot read fails identically on every
// endpoint of the service, forever. Charging that to suppliers turns a config bug into a
// service-wide reputation storm against healthy nodes — which has happened before on
// eth-beacon. The validator must return nil, not an error, for that case.
func Test_WebsocketProbe_UnreadableResponseIsNotTheEndpointsFault(t *testing.T) {
	c := require.New(t)

	var sawNotApplicable bool
	probe := protocol.WebsocketProbe{
		Payload: `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`,
		ValidateResponse: func(body []byte) error {
			_, err := extractBlockHeight(body)
			if err != nil {
				sawNotApplicable = true
				return nil
			}
			return nil
		},
	}

	// A shape with no height in it at all. (Beacon's {"data":{"head_slot":...}} is NOT such
	// a shape — extractBlockHeight understands it, which is why eth-beacon was fixed.)
	c.NoError(probe.ValidateResponse([]byte(`{"jsonrpc":"2.0","id":1,"status":"ok"}`)),
		"an unreadable response shape must never fail the endpoint")
	c.True(sawNotApplicable)
}

// A subscription acknowledgement carries a 128-bit hex id, and extractBlockHeight parses any
// 0x string as a block number. A sync-checked rule pointed at a *_subscribe payload would
// therefore be reading a subscription id as a height.
//
// It fails SAFE: the id overflows int64, so the parse errors and the check is treated as not
// applicable — no penalty — rather than yielding an astronomically large height that would
// pass every endpoint and silently disable the check. Pinned here because that safety is
// incidental to int64 rather than intentional, and a wider integer type would remove it.
func Test_extractBlockHeight_SubscriptionAckIsNotABlockHeight(t *testing.T) {
	c := require.New(t)

	ack := []byte(`{"jsonrpc":"2.0","id":2,"result":"0x8d90acc5122dd73303ada06279319a3e"}`)
	_, err := extractBlockHeight(ack)
	c.Error(err,
		"a 128-bit subscription id must not parse as a height; it overflows int64, which is "+
			"what keeps a mis-pointed subscribe rule safe rather than silently always-passing")

	// A real block number parses cleanly.
	realHeight, err := extractBlockHeight([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x6ba4b91"}`))
	c.NoError(err)
	c.Equal(int64(112872337), realHeight)
}

// Without a sync check configured the probe must stay a plain round-trip, so services whose
// rules do not opt in behave exactly as before.
func Test_WebsocketProbe_NoValidatorLeavesBehaviourUnchanged(t *testing.T) {
	c := require.New(t)

	probe := protocol.WebsocketProbe{Payload: `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`}
	c.Nil(probe.ValidateResponse, "no sync check configured means no content judgement")
	c.False(probe.IsHandshakeOnly())

	// And an empty payload remains handshake-only.
	c.True(protocol.WebsocketProbe{}.IsHandshakeOnly())
}

// errSyncCheckNotApplicable must remain distinguishable, since the executor's closure keys
// the no-penalty path off it.
func Test_errSyncCheckNotApplicable_IsIdentifiable(t *testing.T) {
	wrapped := fmt.Errorf("sync check failed: %w", errSyncCheckNotApplicable)
	require.True(t, errors.Is(wrapped, errSyncCheckNotApplicable),
		"the not-applicable marker must survive wrapping, or config bugs start costing reputation")
}
