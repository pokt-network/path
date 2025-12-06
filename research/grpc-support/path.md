# PATH: gRPC Support for Cosmos Blockchains

## Problem Statement

PATH serves as a gateway that filters suppliers by endpoint type and forwards requests to Relayminer. Currently, PATH has no support for `RPCType_GRPC`. Requests with gRPC content-type are either rejected or misrouted.

The goal is to support gRPC for Cosmos SDK blockchains, which expose query endpoints via gRPC on port 9090 (bank, staking, gov, auth, etc.).

## Current Architecture

PATH uses a layered approach:
1. Gateway receives HTTP requests
2. QoS service parses and validates requests, determines RPC type
3. Protocol layer selects endpoints filtered by RPC type
4. Request is forwarded to Relayminer with `X-RPC-Type` header

The Cosmos QoS service (`qos/cosmos/`) currently handles:
- `RPCType_JSON_RPC` - EVM JSON-RPC (for Cosmos chains with EVM)
- `RPCType_REST` - Cosmos SDK REST API
- `RPCType_COMET_BFT` - CometBFT RPC
- `RPCType_WEBSOCKET` - WebSocket connections

The pattern for each RPC type follows:
- `rpctype_<name>.go` - Detection logic
- `request_validator_<name>.go` - Request validation and context building
- `response_<name>.go` - Response handling

## Required Changes

### 1. gRPC Request Detection

Create `qos/cosmos/rpctype_grpc.go` to detect gRPC requests.

Detection criteria:
- Content-Type header starts with `application/grpc`
- For gRPC-Web: `application/grpc-web` or `application/grpc-web+proto`

This is deterministic: if the Content-Type matches, it's gRPC. No ambiguity with JSON-RPC or REST.

### 2. Request Validation

Create `qos/cosmos/request_validator_grpc.go` following the existing pattern.

The validator must:
- Check if `RPCType_GRPC` is in the service's supported APIs
- Build a `protocol.Payload` with `RPCType: sharedtypes.RPCType_GRPC`
- Preserve gRPC-specific headers (Content-Type, grpc-encoding, te)
- Pass through the binary payload without modification

Unlike JSON-RPC validation, gRPC payloads are binary protobuf. We do not need to parse them. The gateway acts as a pass-through proxy.

### 3. Response Handling

Create `qos/cosmos/response_grpc.go` for response handling.

gRPC responses differ from HTTP/JSON:
- Status is in HTTP trailers (`grpc-status`, `grpc-message`)
- Body is binary protobuf
- Success is `grpc-status: 0`

The response handler should extract `grpc-status` from trailers for observation metrics, then pass through the response body unchanged.

### 4. Request Validator Entry Point

Modify `qos/cosmos/request_validator.go` to route gRPC requests.

Add gRPC detection before the JSON-RPC/REST check in `validateHTTPRequest()`. The order matters: check gRPC first because it's unambiguous (Content-Type based), while JSON-RPC vs REST requires body inspection.

### 5. Protocol Layer: Endpoint URL Handling

Modify `protocol/shannon/endpoint.go` to handle gRPC URLs.

The `protocolEndpoint` struct needs a `grpcUrl` field. The `GetURL(rpcType)` method should return the gRPC URL when `rpcType == RPCType_GRPC`.

Suppliers may expose gRPC on a different port (9090 vs 1317 for REST). The endpoint must track this separately.

### 6. Protocol Layer: Sanctioned Endpoints

Modify `protocol/shannon/protocol.go` to add a sanctioned endpoints store for gRPC.

Currently there are stores for `RPCType_JSON_RPC` and `RPCType_WEBSOCKET`. Add one for `RPCType_GRPC` to track endpoint quality independently.

### 7. Observation Protos

Modify `proto/path/qos/cosmos_request.proto`:
- Add `BACKEND_SERVICE_TYPE_GRPC = 4` to the enum
- Add `GRPCRequest` message for observation data

This enables metrics and observability for gRPC traffic.

### 8. Service Configuration

Modify `config/service_qos_config.go` to add `RPCType_GRPC` to Cosmos services.

Each service declares its supported APIs via a map. Add gRPC to services that should support it (pocket, celestia, osmosis, cosmoshub, etc.).

### 9. Server Streaming Support

Create `qos/cosmos/grpc_stream.go` for server streaming.

Some Cosmos gRPC methods use server streaming (event subscriptions). The handler must:
- Detect streaming responses
- Forward gRPC frames as they arrive (5-byte header + message)
- Handle `grpc-status` in final trailers

This is more complex than unary calls. The response body is a stream of length-prefixed messages.

### 10. gRPC-Web Support

Create `gateway/grpc_web.go` for browser compatibility.

gRPC-Web differs from native gRPC:
- Can work over HTTP/1.1
- May use base64 encoding (`application/grpc-web-text`)
- Trailers are encoded in the response body, not HTTP trailers

The handler must detect gRPC-Web and handle the format differences.

## Design Decisions

### Why extend Cosmos QoS instead of creating qos/grpc/?

gRPC in this context is Cosmos-specific. The service path patterns (`cosmos.bank.v1beta1.Query`), the proto definitions, and the endpoint ports are all Cosmos SDK conventions. A generic gRPC QoS service would be over-abstracted.

The existing Cosmos QoS already handles multiple RPC types. Adding gRPC follows the established pattern.

### Why pass through binary payloads?

PATH doesn't need to understand the protobuf content. It's a proxy. Deserializing and re-serializing protobuf adds latency and complexity for no benefit.

The only parsing needed is:
- Content-Type header for detection
- `grpc-status` trailer for metrics

### Why separate sanctioned endpoints store for gRPC?

Endpoint quality may differ by protocol. A supplier's REST endpoint may be healthy while their gRPC endpoint is down. Independent tracking allows accurate endpoint selection.

## Files Summary

New files:
- `qos/cosmos/rpctype_grpc.go`
- `qos/cosmos/request_validator_grpc.go`
- `qos/cosmos/response_grpc.go`
- `qos/cosmos/grpc_stream.go`
- `gateway/grpc_web.go`

Modified files:
- `qos/cosmos/request_validator.go`
- `qos/cosmos/request_validator_jsonrpc.go` (update `convertToProtoBackendServiceType`)
- `protocol/shannon/endpoint.go`
- `protocol/shannon/protocol.go`
- `proto/path/qos/cosmos_request.proto`
- `config/service_qos_config.go`
- `network/http/http_client.go` (allow Content-Type override)

## Dependencies

- **Shannon SDK**: Must add `Trailer` field to `POKTHTTPResponse` proto (prerequisite for trailer support)
- **poktroll stake config**: Must support GRPC type parsing (see `poktroll.md`)
- **Relayminer**: Must add trailer capture and HTTP/2 server support (see `relayminer.md`)

PATH depends on Relayminer properly forwarding gRPC responses with trailers intact.

## Risks

1. HTTP/2 requirement for native gRPC: Verify PATH's HTTP client supports HTTP/2 (`ForceAttemptHTTP2: true` should be set)
2. Binary payload logging: Existing logging may not handle binary data. May need to skip or base64-encode gRPC payloads in logs.
3. Streaming complexity: Server streaming adds state management. Start with unary calls, add streaming incrementally.