package shannon

import (
	"context"

	apptypes "github.com/pokt-network/poktroll/x/application/types"
	servicetypes "github.com/pokt-network/poktroll/x/service/types"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	sdk "github.com/pokt-network/shannon-sdk"

	"github.com/buildwithgrove/path/protocol"
)

// FullNode defines the set of capabilities the Shannon protocol integration needs
// from a fullnode for sending relays.
//
// A properly initialized fullNode struct can:
// 1. Return the onchain apps matching a service ID.
// 2. Fetch a session for a (service,app) combination.
// 3. Validate a relay response.
// 4. Etc...
type FullNode interface {
	// GetApp returns the onchain application matching the application address
	GetApp(ctx context.Context, appAddr string) (*apptypes.Application, error)

	// GetSession returns the latest session matching the supplied service+app combination.
	// Sessions are solely used for sending relays, and therefore only the latest session for any service+app combination is needed.
	// Note: Shannon returns the latest session for a service+app combination if no blockHeight is provided.
	GetSession(ctx context.Context, serviceID protocol.ServiceID, appAddr string) (hydratedSession, error)

	// GetSessionWithExtendedValidity implements session retrieval with support for
	// Pocket Network's native "session grace period" business logic.
	//
	// At the protocol level, it is used to account for the case when:
	// - RelayMiner.FullNode.Height > Gateway.FullNode.Height
	// AND
	// - RelayMiner.FullNode.Session > Gateway.FullNodeSession
	//
	// PATH leverages it by accounting for the case when:
	// - RelayMiner.FullNode.Height < Gateway.FullNode.Height
	// AND
	// - Gateway.FullNode.Session > RelayMiner.FullNodeSession
	//
	// This enables signing and sending relays to Suppliers who are behind the Gateway.
	//
	// The recommendation usage is to use both GetSession and GetSessionWithExtendedValidity
	// in order to account for both cases when selecting the pool of available Suppliers.
	//
	// Protocol References:
	// - https://github.com/pokt-network/poktroll/blob/main/proto/pocket/shared/params.proto
	// - https://dev.poktroll.com/protocol/governance/gov_params
	// - https://dev.poktroll.com/protocol/primitives/claim_and_proof_lifecycle
	// If within grace period of a session rollover, it may return the previous session.
	GetSessionWithExtendedValidity(ctx context.Context, serviceID protocol.ServiceID, appAddr string) (hydratedSession, error)

	// GetSharedParams returns the shared module parameters from the blockchain.
	GetSharedParams(ctx context.Context) (*sharedtypes.Params, error)

	// GetCurrentBlockHeight returns the current block height from the blockchain.
	GetCurrentBlockHeight(ctx context.Context) (int64, error)

	// ValidateRelayResponse validates the raw bytes returned from an endpoint (in response to a relay request) and returns the parsed response.
	ValidateRelayResponse(supplierAddr sdk.SupplierAddress, responseBz []byte) (*servicetypes.RelayResponse, error)

	// IsHealthy returns true if the FullNode instance is healthy.
	// A LazyFullNode will always return true.
	// A CachingFullNode will return true if it has data in app and session caches.
	IsHealthy() bool

	// GetAccountClient returns the account client from the fullnode, to be used in building relay request signers.
	GetAccountClient() *sdk.AccountClient

	// IsInSessionRollover returns true if the system is currently in a session rollover period.
	//
	// A session rollover period is a critical time window that occurs around session transitions
	// and can cause reliability issues for relay operations. The rollover period is defined as:
	//   - 1 block before the session end height
	//   - Plus a configurable grace period after the session end
	//
	// This method enables the gateway to implement adaptive retry strategies during rollover periods
	//
	// The monitoring is performed automatically in the background and this method
	// provides a thread-safe way to check the current rollover status.
	IsInSessionRollover() bool
}
