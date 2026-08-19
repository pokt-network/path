package evm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// IsArchival decides whether an endpoint enters the archival pool. Two independent
// defects let pruned nodes in, and both are reproduced here.
func Test_IsArchival_LatestTagIsNotProofOfArchival(t *testing.T) {
	e := NewEVMDataExtractor()

	// eth_getBalance at "latest" is ordinary current-state traffic and a pruned node
	// answers it perfectly. Treating the success as proof of archival capability
	// promoted pruned nodes into the archival pool, which then handed them the
	// historical queries they cannot serve.
	request := []byte(`{"jsonrpc":"2.0","method":"eth_getBalance","params":["0x56Eddb7aa87536c09CCc2793473599fD21A8b17F","latest"],"id":1}`)
	response := []byte(`{"jsonrpc":"2.0","id":1,"result":"0x13570a9"}`)

	isArchival, err := e.IsArchival(request, response)
	require.Error(t, err, "a success at \"latest\" must be inconclusive, not archival")
	require.False(t, isArchival)
}

func Test_IsArchival_CurrentStateTagsAndOmittedParam(t *testing.T) {
	e := NewEVMDataExtractor()
	response := []byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`)

	for _, req := range []string{
		`{"jsonrpc":"2.0","method":"eth_getBalance","params":["0xabc","pending"],"id":1}`,
		`{"jsonrpc":"2.0","method":"eth_getBalance","params":["0xabc","safe"],"id":1}`,
		`{"jsonrpc":"2.0","method":"eth_getBalance","params":["0xabc","finalized"],"id":1}`,
		// Omitted block parameter defaults to "latest" in every EVM client.
		`{"jsonrpc":"2.0","method":"eth_getBalance","params":["0xabc"],"id":1}`,
		// A block hash carries no depth.
		`{"jsonrpc":"2.0","method":"eth_getBalance","params":["0xabc","0x4e3a3754410177e6937ef1f84bba68ea139e8d1a2258c5f85db9f1cd715a1bdd"],"id":1}`,
	} {
		isArchival, err := e.IsArchival([]byte(req), response)
		require.Error(t, err, "request must be inconclusive: %s", req)
		require.False(t, isArchival)
	}
}

func Test_IsArchival_HistoricalBlockStillProvesArchival(t *testing.T) {
	e := NewEVMDataExtractor()
	response := []byte(`{"jsonrpc":"2.0","id":1,"result":"0x0"}`)

	for _, req := range []string{
		`{"jsonrpc":"2.0","method":"eth_getBalance","params":["0xabc","0x13570a9"],"id":1}`,
		`{"jsonrpc":"2.0","method":"eth_getBalance","params":["0xabc","earliest"],"id":1}`,
		`{"jsonrpc":"2.0","method":"eth_getStorageAt","params":["0xabc","0x0","0x1"],"id":1}`,
	} {
		isArchival, err := e.IsArchival([]byte(req), response)
		require.NoError(t, err, "request must prove archival capability: %s", req)
		require.True(t, isArchival)
	}
}

// Geth PBSS reports unavailable historical state as "metadata is not found, <block>".
// Unrecognised, it fell through to the "some other error" branch, which returns an
// error -- so the endpoint that had just failed an archival query was never demoted
// out of the archival pool and kept receiving them.
func Test_IsArchival_PBSSPrunedStateDemotes(t *testing.T) {
	e := NewEVMDataExtractor()

	request := []byte(`{"jsonrpc":"2.0","method":"eth_getBalance","params":["0x56Eddb7aa87536c09CCc2793473599fD21A8b17F","0x13570a9"],"id":338498041}`)
	response := []byte(`{"jsonrpc":"2.0","id":338498041,"error":{"code":-32000,"message":"metadata is not found, 12114132"}}`)

	isArchival, err := e.IsArchival(request, response)
	require.NoError(t, err, "PBSS pruned-state error must be a definitive not-archival result")
	require.False(t, isArchival)
}
