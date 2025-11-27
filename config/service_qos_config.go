package config

import (
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/cosmos"
	"github.com/pokt-network/path/qos/evm"
	"github.com/pokt-network/path/qos/solana"
)

// How to add archival checks: https://path.grove.city/learn/qos/adding_new_archival

// IMPORTANT: PATH requires service IDs to be registered here for Quality of Service (QoS) endpoint checks.
// Unregistered services use NoOp QoS type with random endpoint selection and no monitoring.

var _ ServiceQoSConfig = (evm.EVMServiceQoSConfig)(nil)
var _ ServiceQoSConfig = (cosmos.CosmosSDKServiceQoSConfig)(nil)
var _ ServiceQoSConfig = (solana.SolanaServiceQoSConfig)(nil)

type ServiceQoSConfig interface {
	GetServiceID() protocol.ServiceID
	GetServiceQoSType() string
}

// qosServiceConfigs captures the list of blockchains that PATH supports QoS for.
type qosServiceConfigs struct {
	shannonServices []ServiceQoSConfig
}

// GetServiceConfigs returns the service configs for the provided protocol supported by the Gateway.
func (c qosServiceConfigs) GetServiceConfigs(config GatewayConfig) []ServiceQoSConfig {
	return shannonServices
}

// The QoSServiceConfigs map associates each supported service ID with a specific
// implementation of the gateway.QoSService interface.
var QoSServiceConfigs = qosServiceConfigs{
	shannonServices: shannonServices,
}

const (
	defaultEVMChainID       = "0x1" // ETH Mainnet (1)
	defaultCosmosSDKChainID = "cosmoshub-4"
)

// shannonServices is the list of QoS service configs for the Shannon protocol.
var shannonServices = []ServiceQoSConfig{
	// *** EVM Services (Archival) ***

	// Arbitrum One
	evm.NewEVMServiceQoSConfig(
		"arb-one", "0xa4b1",
		evm.NewEVMArchivalCheckConfig(
			// https://arbiscan.io/address/0xb38e8c17e38363af6ebdcb3dae12e0243582891d
			"0xb38e8c17e38363af6ebdcb3dae12e0243582891d",
			// Contract start block
			3_057_700,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Arbitrum Sepolia Testnet
	evm.NewEVMServiceQoSConfig(
		"arb-sepolia-testnet",
		"0x66EEE",
		evm.NewEVMArchivalCheckConfig(
			// https://sepolia.arbiscan.io/address/0x22b65d0b9b59af4d3ed59f18b9ad53f5f4908b54
			"0x22b65d0b9b59af4d3ed59f18b9ad53f5f4908b54",
			// Contract start block
			132_000_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Avalanche
	evm.NewEVMServiceQoSConfig(
		"avax",
		"0xa86a",
		evm.NewEVMArchivalCheckConfig(
			// https://avascan.info/blockchain/c/address/0x9f8c163cBA728e99993ABe7495F06c0A3c8Ac8b9
			"0x9f8c163cBA728e99993ABe7495F06c0A3c8Ac8b9",
			// Contract start block
			5_000_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Avalanche-DFK
	evm.NewEVMServiceQoSConfig(
		"avax-dfk",
		"0xd2af",
		evm.NewEVMArchivalCheckConfig(
			// https://avascan.info/blockchain/dfk/address/0xCCb93dABD71c8Dad03Fc4CE5559dC3D89F67a260
			"0xCCb93dABD71c8Dad03Fc4CE5559dC3D89F67a260",
			// Contract start block
			45_000_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Base
	evm.NewEVMServiceQoSConfig(
		"base",
		"0x2105",
		evm.NewEVMArchivalCheckConfig(
			// https://basescan.org/address/0x3304e22ddaa22bcdc5fca2269b418046ae7b566a
			"0x3304E22DDaa22bCdC5fCa2269b418046aE7b566A",
			// Contract start block
			4_504_400,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC:  {},
			sharedtypes.RPCType_WEBSOCKET: {},
		},
	),

	// Base Sepolia Testnet
	evm.NewEVMServiceQoSConfig(
		"base-sepolia-testnet",
		"0x14a34",
		evm.NewEVMArchivalCheckConfig(
			// https://sepolia.basescan.org/address/0xbab76e4365a2dff89ddb2d3fc9994103b48886c0
			"0xbab76e4365a2dff89ddb2d3fc9994103b48886c0",
			// Contract start block
			13_000_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Berachain
	evm.NewEVMServiceQoSConfig(
		"bera",
		"0x138de",
		evm.NewEVMArchivalCheckConfig(
			// https://berascan.com/address/0x6969696969696969696969696969696969696969
			"0x6969696969696969696969696969696969696969",
			// Contract start block
			2_000_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Blast
	evm.NewEVMServiceQoSConfig(
		"blast",
		"0x13e31",
		evm.NewEVMArchivalCheckConfig(
			// https://blastscan.io/address/0x4300000000000000000000000000000000000004
			"0x4300000000000000000000000000000000000004",
			// Contract start block
			1_000_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// BNB Smart Chain
	evm.NewEVMServiceQoSConfig(
		"bsc",
		"0x38",
		evm.NewEVMArchivalCheckConfig(
			// https://bsctrace.com/address/0xfb50526f49894b78541b776f5aaefe43e3bd8590
			"0xfb50526f49894b78541b776f5aaefe43e3bd8590",
			// Contract start block
			33_049_200,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC:  {},
			sharedtypes.RPCType_WEBSOCKET: {},
		},
	),

	// Boba
	evm.NewEVMServiceQoSConfig(
		"boba",
		"0x120",
		evm.NewEVMArchivalCheckConfig(
			// https://bobascan.com/address/0x3A92cA39476fF84Dc579C868D4D7dE125513B034
			"0x3A92cA39476fF84Dc579C868D4D7dE125513B034",
			// Contract start block
			3_060_300,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Celo
	evm.NewEVMServiceQoSConfig(
		"celo",
		"0xa4ec",
		evm.NewEVMArchivalCheckConfig(
			// https://celo.blockscout.com/address/0xf89d7b9c864f589bbF53a82105107622B35EaA40
			"0xf89d7b9c864f589bbF53a82105107622B35EaA40",
			// Contract start block
			20_000_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Ethereum
	evm.NewEVMServiceQoSConfig(
		"eth",
		defaultEVMChainID,
		evm.NewEVMArchivalCheckConfig(
			// https://etherscan.io/address/0x28C6c06298d514Db089934071355E5743bf21d60
			"0x28C6c06298d514Db089934071355E5743bf21d60",
			// Contract start block
			12_300_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC:  {},
			sharedtypes.RPCType_WEBSOCKET: {},
		},
	),

	// Ethereum Holesky Testnet
	evm.NewEVMServiceQoSConfig(
		"eth-holesky-testnet",
		"0x4268",
		evm.NewEVMArchivalCheckConfig(
			// https://holesky.etherscan.io/address/0xc6392ad8a14794ea57d237d12017e7295bea2363
			"0xc6392ad8a14794ea57d237d12017e7295bea2363",
			// Contract start block
			1_900_384,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Ethereum Sepolia Testnet
	evm.NewEVMServiceQoSConfig(
		"eth-sepolia-testnet",
		"0xaa36a7",
		evm.NewEVMArchivalCheckConfig(
			// https://sepolia.etherscan.io/address/0xc0f3833b7e7216eecd9f6bc2c7927a7aa36ab58b
			"0xc0f3833b7e7216eecd9f6bc2c7927a7aa36ab58b",
			// Contract start block
			6_412_177,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Fantom
	evm.NewEVMServiceQoSConfig(
		"fantom",
		"0xfa",
		evm.NewEVMArchivalCheckConfig(
			// https://explorer.fantom.network/address/0xaabf86ab3646a7064aa2f61e5959e39129ca46b6
			"0xaabf86ab3646a7064aa2f61e5959e39129ca46b6",
			// Contract start block
			110_633_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Fuse
	evm.NewEVMServiceQoSConfig(
		"fuse",
		"0x7a",
		evm.NewEVMArchivalCheckConfig(
			// https://explorer.fuse.io/address/0x3014ca10b91cb3D0AD85fEf7A3Cb95BCAc9c0f79
			"0x3014ca10b91cb3D0AD85fEf7A3Cb95BCAc9c0f79",
			// Contract start block
			15_000_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Giwa
	// TODO_NEXT(@commoddity): Update to use correct EVM chain ID once `giwa` mainnet is live.
	// TODO_NEXT(@commoddity): Add archival check config for Giwa once `giwa` mainnet is live.
	evm.NewEVMServiceQoSConfig("giwa", "0x1", nil, map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_JSON_RPC: {},
	}),

	// Giwa Sepolia Testnet
	evm.NewEVMServiceQoSConfig(
		"giwa-sepolia-testnet",
		"0x164ce",
		evm.NewEVMArchivalCheckConfig(
			// https://sepolia-explorer.giwa.io/address/0xA2a51Cca837B8ebc00dA2810e72F386Ee0dD08a0
			"0xA2a51Cca837B8ebc00dA2810e72F386Ee0dD08a0",
			// Contract start block
			3_456_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Gnosis
	evm.NewEVMServiceQoSConfig(
		"gnosis",
		"0x64",
		evm.NewEVMArchivalCheckConfig(
			// https://gnosisscan.io/address/0xe91d153e0b41518a2ce8dd3d7944fa863463a97d
			"0xe91d153e0b41518a2ce8dd3d7944fa863463a97d",
			// Contract start block
			20_000_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Harmony-0
	evm.NewEVMServiceQoSConfig(
		"harmony",
		"0x63564c40",
		evm.NewEVMArchivalCheckConfig(
			// https://explorer.harmony.one/address/one19senwle0ezp3he6ed9xkc7zeg5rs94r0ecpp0a?shard=0
			"one19senwle0ezp3he6ed9xkc7zeg5rs94r0ecpp0a",
			// Contract start block
			60_000_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Ink
	evm.NewEVMServiceQoSConfig(
		"ink",
		"0xdef1",
		evm.NewEVMArchivalCheckConfig(
			// https://explorer.inkonchain.com/address/0x4200000000000000000000000000000000000006
			"0x4200000000000000000000000000000000000006",
			// Contract start block
			4_500_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// IoTeX
	evm.NewEVMServiceQoSConfig(
		"iotex",
		"0x1251",
		evm.NewEVMArchivalCheckConfig(
			// https://iotexscan.io/address/0x0a7f9ea31ca689f346e1661cf73a47c69d4bd883#transactions
			"0x0a7f9ea31ca689f346e1661cf73a47c69d4bd883",
			// Contract start block
			6_440_916,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Kaia
	evm.NewEVMServiceQoSConfig(
		"kaia",
		"0x2019",
		evm.NewEVMArchivalCheckConfig(
			// https://www.kaiascan.io/address/0x0051ef9259c7ec0644a80e866ab748a2f30841b3
			"0x0051ef9259c7ec0644a80e866ab748a2f30841b3",
			// Contract start block
			170_000_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Linea
	evm.NewEVMServiceQoSConfig(
		"linea",
		"0xe708",
		evm.NewEVMArchivalCheckConfig(
			// https://lineascan.build/address/0xf89d7b9c864f589bbf53a82105107622b35eaa40
			"0xf89d7b9c864f589bbf53a82105107622b35eaa40",
			// Contract start block
			10_000_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Mantle
	evm.NewEVMServiceQoSConfig(
		"mantle",
		"0x1388",
		evm.NewEVMArchivalCheckConfig(
			// https://explorer.mantle.xyz/address/0x588846213A30fd36244e0ae0eBB2374516dA836C
			"0x588846213A30fd36244e0ae0eBB2374516dA836C",
			// Contract start block
			60_000_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Metis
	evm.NewEVMServiceQoSConfig(
		"metis",
		"0x440",
		evm.NewEVMArchivalCheckConfig(
			// https://explorer.metis.io/address/0xfad31cd4d45Ac7C4B5aC6A0044AA05Ca7C017e62
			"0xfad31cd4d45Ac7C4B5aC6A0044AA05Ca7C017e62",
			// Contract start block
			15_000_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Moonbeam
	evm.NewEVMServiceQoSConfig(
		"moonbeam",
		"0x504",
		evm.NewEVMArchivalCheckConfig(
			// https://moonscan.io/address/0xf89d7b9c864f589bbf53a82105107622b35eaa40
			"0xf89d7b9c864f589bbf53a82105107622b35eaa40",
			// Contract start block
			677_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Oasys
	evm.NewEVMServiceQoSConfig(
		"oasys",
		"0xf8",
		evm.NewEVMArchivalCheckConfig(
			// https://explorer.oasys.games/address/0xf89d7b9c864f589bbF53a82105107622B35EaA40
			"0xf89d7b9c864f589bbF53a82105107622B35EaA40",
			// Contract start block
			424_300,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Optimism
	evm.NewEVMServiceQoSConfig(
		"op",
		"0xa",
		evm.NewEVMArchivalCheckConfig(
			// https://optimistic.etherscan.io/address/0xacd03d601e5bb1b275bb94076ff46ed9d753435a
			"0xacD03D601e5bB1B275Bb94076fF46ED9D753435A",
			// Contract start block
			8_121_800,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Optimism Sepolia Testnet
	evm.NewEVMServiceQoSConfig(
		"op-sepolia-testnet",
		"0xAA37DC",
		evm.NewEVMArchivalCheckConfig(
			// https://sepolia-optimism.etherscan.io/address/0x734d539a7efee15714a2755caa4280e12ef3d7e4
			"0x734d539a7efee15714a2755caa4280e12ef3d7e4",
			// Contract start block
			18_241_388,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Polygon
	evm.NewEVMServiceQoSConfig(
		"poly",
		"0x89",
		evm.NewEVMArchivalCheckConfig(
			// https://polygonscan.com/address/0x0d500B1d8E8eF31E21C99d1Db9A6444d3ADf1270
			"0x0d500B1d8E8eF31E21C99d1Db9A6444d3ADf1270",
			// Contract start block
			5_000_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Polygon Amoy Testnet
	evm.NewEVMServiceQoSConfig(
		"poly-amoy-testnet",
		"0x13882", evm.NewEVMArchivalCheckConfig(
			// https://amoy.polygonscan.com/address/0x54d03ec0c462e9a01f77579c090cde0fc2617817
			"0x54d03ec0c462e9a01f77579c090cde0fc2617817",
			// Contract start block
			10_453_569,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Polygon zkEVM
	evm.NewEVMServiceQoSConfig(
		"poly-zkevm",
		"0x44d",
		evm.NewEVMArchivalCheckConfig(
			// https://zkevm.polygonscan.com/address/0xee1727f5074e747716637e1776b7f7c7133f16b1
			"0xee1727f5074E747716637e1776B7F7C7133f16b1",
			// Contract start block
			111,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Scroll
	evm.NewEVMServiceQoSConfig(
		"scroll",
		"0x82750",
		evm.NewEVMArchivalCheckConfig(
			// https://scrollscan.com/address/0x5300000000000000000000000000000000000004
			"0x5300000000000000000000000000000000000004",
			// Contract start block
			5_000_000,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Sonic
	evm.NewEVMServiceQoSConfig(
		"sonic",
		"0x92",
		evm.NewEVMArchivalCheckConfig(
			// https://sonicscan.org/address/0xfc00face00000000000000000000000000000000
			"0xfc00face00000000000000000000000000000000",
			// Contract start block
			10_769_279,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Taiko
	evm.NewEVMServiceQoSConfig(
		"taiko",
		"0x28c58",
		evm.NewEVMArchivalCheckConfig(
			// https://taikoscan.io/address/0x1670000000000000000000000000000000000001
			"0x1670000000000000000000000000000000000001",
			// Contract start block
			170_163,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// Taiko Hekla Testnet
	evm.NewEVMServiceQoSConfig(
		"taiko-hekla-testnet",
		"0x28c61",
		evm.NewEVMArchivalCheckConfig(
			// https://hekla.taikoscan.io/address/0x1670090000000000000000000000000000010001
			"0x1670090000000000000000000000000000010001",
			// Contract start block
			420_139,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),
	// zkLink
	evm.NewEVMServiceQoSConfig(
		"zklink-nova",
		"0xc5cc4", evm.NewEVMArchivalCheckConfig(
			// https://explorer.zklink.io/address/0xa3cb8648d12bD36e713af27D92968B370D7A9546
			"0xa3cb8648d12bD36e713af27D92968B370D7A9546",
			// Contract start block
			5_004_627,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// zkSync
	evm.NewEVMServiceQoSConfig(
		"zksync-era",
		"0x144",
		evm.NewEVMArchivalCheckConfig(
			// https://explorer.zksync.io/address/0x03AC0b1b952C643d66A4Dc1fBc75118109cC074C
			"0x03AC0b1b952C643d66A4Dc1fBc75118109cC074C",
			// Contract start block
			55_405_668,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// *** EVM Services (testing) ***

	// Anvil - Ethereum development/testing
	evm.NewEVMServiceQoSConfig("anvil", "0x7a69", nil, map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_JSON_RPC: {},
	}),

	// Anvil WebSockets - Ethereum WebSockets development/testing
	evm.NewEVMServiceQoSConfig("anvilws", "0x7a69", nil, map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_JSON_RPC: {},
	}),

	// *** EVM Services (Non-Archival) ***

	// Fraxtal
	evm.NewEVMServiceQoSConfig("fraxtal", "0xfc", nil, map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_JSON_RPC: {},
	}),

	// Kava
	evm.NewEVMServiceQoSConfig("kava", "0x8ae", nil, map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_JSON_RPC: {},
	}),

	// Moonriver
	evm.NewEVMServiceQoSConfig("moonriver", "0x505", nil, map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_JSON_RPC: {},
	}),

	// opBNB
	evm.NewEVMServiceQoSConfig("opbnb", "0xcc", nil, map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_JSON_RPC: {},
	}),

	// Sui
	evm.NewEVMServiceQoSConfig("sui", "0x101", nil, map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_JSON_RPC: {},
	}),

	// TRON
	evm.NewEVMServiceQoSConfig("tron", "0x2b6653dc", nil, map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_JSON_RPC: {},
	}),

	// Sei
	evm.NewEVMServiceQoSConfig("sei", "0x531", nil, map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_JSON_RPC: {},
	}),

	// Hey
	// TODO_TECHDEBT(@olshansk): Either remove or format this correctly
	evm.NewEVMServiceQoSConfig("hey", defaultEVMChainID, nil, map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_JSON_RPC: {},
	}),

	// Hyperliquid
	evm.NewEVMServiceQoSConfig("hyperliquid", "0x3e7",
		evm.NewEVMArchivalCheckConfig(
			// https://app.hyperliquid.xyz/explorer/address/0x162cc7c861ebd0c06b3d72319201150482518185
			// https://hypurrscan.io/address/0x162cc7c861ebd0c06b3d72319201150482518185
			"0x162cc7c861ebd0c06b3d72319201150482518185",
			// First block (649106292) taken from this post: https://x.com/Crypto_Noddy/status/1943780258370428938:
			// Adding 1_000 blocks as a buffer for the archival check baseline.
			649_107_292,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		}),

	// Unichain
	evm.NewEVMServiceQoSConfig("unichain", "0x82",
		evm.NewEVMArchivalCheckConfig(
			// https://unichain.blockscout.com/address/0x1F98400000000000000000000000000000000004
			"0x1F98400000000000000000000000000000000004",
			// Below is not the first block, but one that is sufficiently long ago for archival check baseline.
			// https://unichain.blockscout.com/address/0x1F98400000000000000000000000000000000004?tab=txs&page=100000
			21_495_984,
		),
		map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	),

	// TODO_TECHDEBT: Add support for Radix QoS
	// Radix
	// radix.NewRadixServiceQoSConfig("radix", "", nil),

	// TODO_TECHDEBT: Add support for Near QoS
	// Near
	// near.NewNearServiceQoSConfig("near", "", nil),

	// *** Cosmos SDK Services ***

	// Akash - https://github.com/cosmos/chain-registry/blob/master/akash/chain.json#L9
	cosmos.NewCosmosSDKServiceQoSConfig("akash", "akashnet-2", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Arkeo - https://github.com/cosmos/chain-registry/blob/master/arkeo/chain.json#L9
	cosmos.NewCosmosSDKServiceQoSConfig("arkeo", "arkeo-main-v1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// AtomOne - https://github.com/cosmos/chain-registry/blob/master/atomone/chain.json#L5
	cosmos.NewCosmosSDKServiceQoSConfig("atomone", "atomone-1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Babylon - https://github.com/cosmos/chain-registry/blob/master/babylon/chain.json#L9
	cosmos.NewCosmosSDKServiceQoSConfig("babylon", "bbn-1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Celestia - https://github.com/cosmos/chain-registry/blob/master/celestia/chain.json#L5
	cosmos.NewCosmosSDKServiceQoSConfig("celestia", "celestia", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Cheqd - https://github.com/cosmos/chain-registry/blob/master/cheqd/chain.json#L9
	cosmos.NewCosmosSDKServiceQoSConfig("cheqd", "cheqd-mainnet-1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Chihuahua - https://github.com/cosmos/chain-registry/blob/master/chihuahua/chain.json#L9
	cosmos.NewCosmosSDKServiceQoSConfig("chihuahua", "chihuahua-1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Cosmos Hub - https://github.com/cosmos/chain-registry/blob/master/cosmoshub/chain.json#L5
	cosmos.NewCosmosSDKServiceQoSConfig("cosmoshub", "cosmoshub-4", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Dungeon Chain - https://github.com/cosmos/chain-registry/blob/master/dungeon1/chain.json#L9
	cosmos.NewCosmosSDKServiceQoSConfig("dungeon-chain", "dungeon-1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Elys Network - https://github.com/cosmos/chain-registry/blob/master/elys/chain.json#L8
	cosmos.NewCosmosSDKServiceQoSConfig("elys-network", "elys-1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Fetch - https://github.com/cosmos/chain-registry/blob/master/fetchhub/chain.json#L8
	cosmos.NewCosmosSDKServiceQoSConfig("fetch", "fetchhub-4", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Jackal - https://github.com/cosmos/chain-registry/blob/master/jackal/chain.json#L5
	cosmos.NewCosmosSDKServiceQoSConfig("jackal", "jackal-1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Juno - https://github.com/cosmos/chain-registry/blob/master/juno/chain.json#L9
	cosmos.NewCosmosSDKServiceQoSConfig("juno", "juno-1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// KYVE - https://github.com/cosmos/chain-registry/blob/master/kyve/chain.json#L5
	cosmos.NewCosmosSDKServiceQoSConfig("kyve", "kyve-1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Namada TODO_TECHDEBT(@commoddity): Namada is not a conventional Cosmos SDK chain and likely requires a custom implementation.
	// Reference: https://github.com/pokt-network/path/issues/376#issuecomment-3127611273
	// cosmos.NewCosmosSDKServiceQoSConfig("namada", "","", map[sharedtypes.RPCType]struct{}{
	// 	sharedtypes.RPCType_REST:      {}, // CosmosSDK
	// 	sharedtypes.RPCType_COMET_BFT: {},
	// }),

	// Neutron - https://github.com/cosmos/chain-registry/blob/master/neutron/chain.json#L8
	cosmos.NewCosmosSDKServiceQoSConfig("neutron", "neutron-1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Nillion - https://github.com/cosmos/chain-registry/blob/master/nillion/chain.json#L8
	cosmos.NewCosmosSDKServiceQoSConfig("nillion", "nillion-1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Osmosis - https://github.com/cosmos/chain-registry/blob/master/osmosis/chain.json#L9
	cosmos.NewCosmosSDKServiceQoSConfig("osmosis", "osmosis-1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Passage - https://github.com/cosmos/chain-registry/blob/master/passage/chain.json#L5
	cosmos.NewCosmosSDKServiceQoSConfig("passage", "passage-2", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Persistence - https://github.com/cosmos/chain-registry/blob/master/persistence/chain.json#L5
	cosmos.NewCosmosSDKServiceQoSConfig("persistence", "core-1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Provenance - https://github.com/cosmos/chain-registry/blob/master/provenance/chain.json#L9
	cosmos.NewCosmosSDKServiceQoSConfig("provenance", "pio-mainnet-1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Pocket Mainnet - https://github.com/cosmos/chain-registry/blob/master/pocket/chain.json#L9
	cosmos.NewCosmosSDKServiceQoSConfig("pocket", "pocket", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Pocket Alpha Testnet - (Not in the chain registry - present here for onchain load testing)
	cosmos.NewCosmosSDKServiceQoSConfig("pocket-alpha", "pocket-alpha", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Pocket Beta Testnet - https://github.com/cosmos/chain-registry/blob/master/testnets/pockettestnet/chain.json#L9
	cosmos.NewCosmosSDKServiceQoSConfig("pocket-beta", "pocket-beta", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Pocket Beta Testnet 1 - (Not in the chain registry - present here for onchain load testing)
	cosmos.NewCosmosSDKServiceQoSConfig("pocket-beta1", "pocket-beta", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Pocket Beta Testnet 2 - (Not in the chain registry - present here for onchain load testing)
	cosmos.NewCosmosSDKServiceQoSConfig("pocket-beta2", "pocket-beta", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Pocket Beta Testnet 3 - (Not in the chain registry - present here for onchain load testing)
	cosmos.NewCosmosSDKServiceQoSConfig("pocket-beta3", "pocket-beta", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Pocket Beta Testnet 4 - (Not in the chain registry - present here for onchain load testing)
	cosmos.NewCosmosSDKServiceQoSConfig("pocket-beta4", "pocket-beta", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Quicksilver - https://github.com/cosmos/chain-registry/blob/master/quicksilver/chain.json#L9
	cosmos.NewCosmosSDKServiceQoSConfig("quicksilver", "quicksilver-2", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Router - https://github.com/cosmos/chain-registry/blob/master/routerchain/chain.json#L5
	cosmos.NewCosmosSDKServiceQoSConfig("router", "router_9600-1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
		sharedtypes.RPCType_JSON_RPC:  {},
	}),

	// Seda - https://github.com/cosmos/chain-registry/blob/master/seda/chain.json#L9
	cosmos.NewCosmosSDKServiceQoSConfig("seda", "seda-1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Shentu - https://github.com/cosmos/chain-registry/blob/master/shentu/chain.json#L9
	cosmos.NewCosmosSDKServiceQoSConfig("shentu", "shentu-2.2", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Bitway - https://github.com/cosmos/chain-registry/blob/master/bitway/chain.json
	// FORMERLY KNOWN AS: Side Protocol - https://github.com/cosmos/chain-registry/blob/master/sidechain/chain.json#L9
	cosmos.NewCosmosSDKServiceQoSConfig("bitway", "bitway-1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Stargaze - https://github.com/cosmos/chain-registry/blob/master/stargaze/chain.json#L9
	cosmos.NewCosmosSDKServiceQoSConfig("stargaze", "stargaze-1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// Stride - https://github.com/cosmos/chain-registry/blob/master/stride/chain.json#L9
	cosmos.NewCosmosSDKServiceQoSConfig("stride", "stride-1", "", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
	}),

	// XRPLEVM - https://github.com/cosmos/chain-registry/blob/master/xrplevm/chain.json#L9
	cosmos.NewCosmosSDKServiceQoSConfig("xrplevm", "xrplevm_1440000-1", "0x15f900", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_JSON_RPC:  {}, // XRPLEVM supports the EVM API over JSON-RPC.
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
		sharedtypes.RPCType_WEBSOCKET: {}, // XRPLEVM supports the EVM API over JSON-RPC WebSockets.
	}),

	// XRPLEVM Testnet - https://github.com/cosmos/chain-registry/blob/master/testnets/xrplevmtestnet/chain.json#L9
	cosmos.NewCosmosSDKServiceQoSConfig("xrplevm-testnet", "xrplevm_1449000-1", "0x161c28", map[sharedtypes.RPCType]struct{}{
		sharedtypes.RPCType_JSON_RPC:  {}, // XRPLEVM supports the EVM API over JSON-RPC.
		sharedtypes.RPCType_REST:      {}, // CosmosSDK
		sharedtypes.RPCType_COMET_BFT: {},
		sharedtypes.RPCType_WEBSOCKET: {}, // XRPLEVM supports the EVM API over JSON-RPC WebSockets.
	}),

	// *** Solana Services ***

	// Solana
	solana.NewSolanaServiceQoSConfig("solana", "solana"),

	// *** LLM Services ***

}

// 3. deepseek

// TODO(@olshansk): Make sure all of these are supported
// sonieum
// atomone
// akash
// fetch
// persistence
// router
// seda
// shentu
// arkeo
// babylon
// celestia
// cheqd
// chihuahua
// cosmoshub
// elys-network
// jackal
// juno
// kyve
// namada
// neutron
// nillion
// passage
// provenance
// quicksilver
// side-protocol
// stargaze
// stride
// grok
// qwen
// gpt-oss
// bittensor
// bittensor-testnet
// planq
// stellar-soroban
// stellar-soroban-testnet
// stellar-horizon
// stellar-horizon-testnet
