package solana

import (
	"github.com/pokt-network/path/qos/jsonrpc"
)

const (
	// methodGetEpochInfo is the JSON-RPC method for getting the epoch information.
	// Reference: https://docs.solana.com/developing/clients/jsonrpc-api#getepochinfo
	methodGetEpochInfo = jsonrpc.Method("getEpochInfo")

	// methodGetHealth is the JSON-RPC method for checking the health of the node.
	// Reference: https://docs.solana.com/developing/clients/jsonrpc-api#gethealth
	methodGetHealth = jsonrpc.Method("getHealth")
)
