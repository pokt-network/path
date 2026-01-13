# gRPC Support for Cosmos Blockchains

## Why Cosmos Only

gRPC support targets Cosmos SDK chains exclusively because Cosmos is the only blockchain ecosystem with native gRPC as a first-class query interface.

| Blockchain                                       | Query Interface          |
|--------------------------------------------------|--------------------------|
| **Cosmos SDK** (Osmosis, Celestia, Pocket, etc.) | gRPC, REST, CometBFT RPC |
| **Ethereum/EVM**                                 | JSON-RPC only            |
| **Solana**                                       | JSON-RPC only            |
| **Bitcoin**                                      | JSON-RPC only            |
| **Polkadot/Substrate**                           | JSON-RPC, WebSocket      |

Cosmos SDK uses Protocol Buffers throughout its architecture—state encoding, transactions, and queries. Every Cosmos module (bank, staking, gov, auth) auto-generates gRPC query endpoints from proto definitions. gRPC is not an add-on; it's fundamental to how Cosmos works.

Other ecosystems standardized on JSON-RPC before gRPC existed or chose HTTP/JSON for simplicity. There's no native gRPC to support.

This is why gRPC handling lives in `qos/cosmos/` rather than a generic gRPC service.

## Documents

- `plan.md` - Overall plan, reasoning, dependencies, and implementation order
- `poktroll.md` - Changes required in poktroll (stake config reader)
- `relayminer.md` - Changes required in Relayminer (trailer support, HTTP/2 server)
- `path.md` - Changes required in PATH (QoS, protocol, config)
- `implementation.md` - Step-by-step implementation guide for PATH

## Summary

Add gRPC support for Cosmos blockchains across the stack:

1. **Shannon SDK**: Add `Trailer` field to `POKTHTTPResponse` proto (prerequisite)
2. **poktroll**: Update stake config reader to parse GRPC endpoint type
3. **Relayminer**: Add trailer capture, HTTP/2 server support (h2c)
4. **PATH**: Add gRPC detection, validation, response handling, and endpoint routing

Scope: gRPC-Web + native gRPC, unary + server streaming.

## Dependency Chain

```
Shannon SDK (POKTHTTPResponse.Trailer)
    ↓
Relayminer (trailer capture + h2c server)
    ↓
PATH (gRPC detection + trailer handling)
```

## Quick Reference

### Shannon SDK
- `types/http.proto` - Add `Trailer` field to `POKTHTTPResponse`

### poktroll
- `x/supplier/config/supplier_configs_reader.go` - Add GRPC to `parseEndpointRPCType()`

### Relayminer (poktroll)
- `pkg/relayer/proxy/http_utils.go` - Capture trailers in `SerializeHTTPResponse`
- `pkg/relayer/proxy/http_server.go` - Add h2c (HTTP/2 cleartext) support

### PATH - New Files
- `qos/cosmos/rpctype_grpc.go`
- `qos/cosmos/request_validator_grpc.go`
- `qos/cosmos/response_grpc.go`
- `qos/cosmos/grpc_stream.go`
- `gateway/grpc_web.go`

### PATH - Modified Files
- `qos/cosmos/request_validator.go`
- `qos/cosmos/request_validator_jsonrpc.go`
- `protocol/shannon/endpoint.go`
- `protocol/shannon/protocol.go`
- `proto/path/qos/cosmos_request.proto`
- `config/service_qos_config.go`
- `network/http/http_client.go`
