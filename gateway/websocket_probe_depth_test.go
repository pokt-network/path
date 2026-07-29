package gateway

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// The probe depth is derived from the rule's own payload, so getting this mapping wrong is
// silent: the check still runs and still reports ok, it just stops testing the thing that
// breaks. This is exactly how the previous handshake-only check passed an endpoint that
// acknowledged a subscription and then delivered nothing for 25s.
func Test_websocketPayloadSubscribes_DecidesProbeDepth(t *testing.T) {
	tests := []struct {
		name       string
		payload    string
		subscribes bool
	}{
		{
			name:       "eth_subscribe must wait for a notification",
			payload:    `{"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["newHeads"]}`,
			subscribes: true,
		},
		{
			name:       "non-EVM chains follow the same *_subscribe shape",
			payload:    `{"jsonrpc":"2.0","id":1,"method":"cometbft_subscribe","params":["tm.event='NewBlock'"]}`,
			subscribes: true,
		},
		{
			name:       "a plain request is satisfied by its response",
			payload:    `{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`,
			subscribes: false,
		},
		{
			name:       "eth_unsubscribe must NOT demand a notification",
			payload:    `{"jsonrpc":"2.0","id":1,"method":"eth_unsubscribe","params":["0xabc"]}`,
			subscribes: false,
		},
		{
			name:       "no payload at all stays handshake-only",
			payload:    "",
			subscribes: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.subscribes, websocketPayloadSubscribes(tt.payload))
		})
	}
}

// The leading underscore is load-bearing: it is what keeps eth_unsubscribe out. Matching on
// bare "subscribe" would make an unsubscribe payload wait for a notification that never
// comes and fail every endpoint indiscriminately.
func Test_websocketPayloadSubscribes_UnderscoreAnchorExcludesUnsubscribe(t *testing.T) {
	c := require.New(t)
	c.False(websocketPayloadSubscribes(`{"method":"eth_unsubscribe"}`))
	c.True(websocketPayloadSubscribes(`{"method":"eth_subscribe"}`))
}

// A probe built from an empty payload must be inert, so a service whose rules carry no
// websocket body keeps the previous handshake-only behaviour rather than starting to fail.
func Test_WebsocketProbe_EmptyPayloadIsHandshakeOnly(t *testing.T) {
	c := require.New(t)

	c.True(protocol.WebsocketProbe{}.IsHandshakeOnly())
	c.False(protocol.WebsocketProbe{Payload: `{"method":"eth_subscribe"}`}.IsHandshakeOnly())
}
