package reputation

import (
	"testing"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

func TestSupplierFromKey(t *testing.T) {
	cases := []struct {
		name string
		addr protocol.EndpointAddr
		want string
	}{
		{
			name: "per-endpoint key (supplier-url)",
			addr: protocol.EndpointAddr("pokt1abc123def-https://node.example.com"),
			want: "pokt1abc123def",
		},
		{
			name: "per-supplier key (bech32 only)",
			addr: protocol.EndpointAddr("pokt1abc123def"),
			want: "pokt1abc123def",
		},
		{
			name: "per-domain key (no dash, no pokt1 prefix)",
			addr: protocol.EndpointAddr("nodefleet.net"),
			want: "",
		},
		{
			name: "empty",
			addr: protocol.EndpointAddr(""),
			want: "",
		},
		{
			name: "leading dash (malformed) → empty",
			addr: protocol.EndpointAddr("-https://x"),
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key := NewEndpointKey("eth", c.addr, sharedtypes.RPCType_JSON_RPC)
			require.Equal(t, c.want, supplierFromKey(key))
		})
	}
}
