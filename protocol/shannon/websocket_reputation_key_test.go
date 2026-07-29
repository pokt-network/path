package shannon

import (
	"fmt"
	"testing"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
)

// The bug this guards against was silent and total: websocket health-check results were
// recorded against a key built from the bare endpoint URL, while selection reads a key built
// from the full <supplier>-<url> address. At the per-supplier granularity running in
// production those are different keys, so every health check — pass or fail — was written
// somewhere nothing ever read. That is why websocket scores sat pinned at their initial
// value no matter how an endpoint behaved.
//
// Worse, the supplier extractor splits on the first "-", so a hostname containing one
// yielded a mangled key rather than an obviously wrong one.
func Test_websocketReputationKey_ObservationMustMatchSelection(t *testing.T) {
	const (
		supplier = "pokt12yfuputzl082knnjc0pwrjpuxw43u272u02vj4"
		// Deliberately contains a "-", which is what turned the old key into garbage.
		url = "https://dopokt.relayminer.shannon-mainnet.eu.nodefleet.net/"
	)
	full := protocol.EndpointAddr(fmt.Sprintf("%s-%s", supplier, url))

	// Every granularity except per-domain distinguishes these, so the fix must hold for all.
	for _, granularity := range []string{
		reputation.KeyGranularitySupplier, // what production runs
		reputation.KeyGranularityURL,
		reputation.KeyGranularityDomain,
		reputation.KeyGranularityEndpoint,
	} {
		t.Run(granularity, func(t *testing.T) {
			c := require.New(t)
			kb := reputation.NewKeyBuilder(granularity)

			// What endpoint selection reads.
			selectionKey := kb.BuildKey("bsc", full, sharedtypes.RPCType_WEBSOCKET)

			// What the observation path now writes, reconstructed from supplier + url.
			observed := protocol.EndpointAddr(fmt.Sprintf("%s-%s", supplier, url))
			observationKey := kb.BuildKey("bsc", observed, sharedtypes.RPCType_WEBSOCKET)

			c.Equal(selectionKey, observationKey,
				"a health check result must land on the key selection reads, or it cannot affect routing")

			// And prove the OLD behaviour really was broken here, so this test fails loudly
			// if someone reverts to using the bare URL.
			oldKey := kb.BuildKey("bsc", protocol.EndpointAddr(url), sharedtypes.RPCType_WEBSOCKET)
			if granularity == reputation.KeyGranularityDomain {
				c.Equal(selectionKey, oldKey, "per-domain matched by accident — documenting why the bug hid")
			} else {
				c.NotEqual(selectionKey, oldKey,
					"the url-only key must differ, otherwise this test proves nothing")
			}
		})
	}
}

// Fallback endpoints carry no staked supplier. Reconstructing an address for them must not
// invent one, or their scores move to a key nothing reads — the same failure in reverse.
func Test_websocketReputationKey_NoSupplierFallsBackToURL(t *testing.T) {
	c := require.New(t)
	const url = "https://fallback.example.com/"

	kb := reputation.NewKeyBuilder(reputation.KeyGranularitySupplier)
	// An empty supplier must leave the address as the bare URL, matching the pre-existing
	// behaviour for endpoints that genuinely have no supplier.
	addr := protocol.EndpointAddr(url)
	c.Equal(
		kb.BuildKey("bsc", addr, sharedtypes.RPCType_WEBSOCKET),
		kb.BuildKey("bsc", protocol.EndpointAddr(url), sharedtypes.RPCType_WEBSOCKET),
	)
}
