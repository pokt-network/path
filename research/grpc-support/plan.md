# gRPC Support for Cosmos Blockchains: Implementation Plan

## Context

The Pocket Network stack consists of three main components for relay processing:

1. **poktroll** - The blockchain. Suppliers stake endpoints with RPC types.
2. **Relayminer** - Offchain actor representing suppliers. Receives relays, forwards to backend endpoints.
3. **PATH** - Gateway. Receives client requests, selects suppliers by endpoint type, sends to Relayminer.

For a client to make a gRPC request to a Cosmos blockchain through Pocket Network:
- Supplier must stake a gRPC endpoint (poktroll)
- Relayminer must properly handle gRPC requests and responses (including trailers)
- PATH must recognize gRPC requests and filter suppliers accordingly

## Current Blockers

gRPC is blocked at multiple points:

1. **poktroll** - Supplier stake config reader rejects "GRPC" in YAML
2. **Shannon SDK** - `POKTHTTPResponse` proto lacks `Trailer` field (critical for gRPC status)
3. **Relayminer** - No HTTP trailer capture, no HTTP/2 server support, no gRPC-Web handling
4. **PATH** - No gRPC detection, validation, or response handling in Cosmos QoS

## Scope

This plan covers:
- Both gRPC-Web (HTTP/1.1) and native gRPC (HTTP/2)
- Unary calls and server streaming
- Updates to Shannon SDK, poktroll, Relayminer, and PATH

## Why gRPC for Cosmos?

Cosmos SDK exposes three query interfaces:
- REST API (port 1317) - HTTP/JSON, legacy
- CometBFT RPC (port 26657) - JSON-RPC style
- gRPC (port 9090) - Protocol buffers, typed, efficient

gRPC advantages:
- Strongly typed via proto definitions
- More efficient serialization (protobuf vs JSON)
- Native streaming support
- Better tooling (grpcurl, generated clients)
- Increasingly preferred by Cosmos developers

Many Cosmos applications now use gRPC exclusively. Without gRPC support, Pocket Network cannot serve these clients.

## Implementation Order

### Phase 0: Shannon SDK (Prerequisite)

Add HTTP trailer support to `POKTHTTPResponse`:

```protobuf
message POKTHTTPResponse {
    uint32 status_code = 1;
    map<string, Header> header = 2;
    bytes body_bz = 3;
    map<string, Header> trailer = 4;  // NEW: HTTP trailers for gRPC
}
```

This is required before Relayminer or PATH can properly handle gRPC responses.

See: `relayminer.md` for details on why trailers are critical.

### Phase 1: poktroll - Stake Config Reader

File: `x/supplier/config/supplier_configs_reader.go`

Add GRPC (and COMET_BFT) to `parseEndpointRPCType()`.

This unblocks suppliers from staking gRPC endpoints.

See: `poktroll.md`

### Phase 2: Relayminer - Core gRPC Support

Files:
- `pkg/relayer/proxy/http_utils.go` - Capture trailers in `SerializeHTTPResponse`
- `pkg/relayer/proxy/http_server.go` - Add h2c (HTTP/2 cleartext) support

This enables:
- Native gRPC over HTTP/2
- Proper gRPC error propagation via trailers

See: `relayminer.md`

### Phase 3: PATH - Core gRPC Support

Implement gRPC detection and handling in Cosmos QoS:

1. Create `rpctype_grpc.go` - gRPC request detection
2. Create `request_validator_grpc.go` - Request validation
3. Create `response_grpc.go` - Response handling with trailer extraction
4. Update `request_validator.go` - Route gRPC requests
5. Update protocol layer - gRPC endpoint handling
6. Update service configuration - Add GRPC to Cosmos services

See: `path.md`

### Phase 4: gRPC-Web Support

For browser clients, add gRPC-Web handling:

**Relayminer**: Either transparent proxy (if backends support gRPC-Web) or translation layer.

**PATH**: Create `gateway/grpc_web.go` for format handling.

gRPC-Web differences:
- Works over HTTP/1.1 (no HTTP/2 requirement)
- Trailers encoded in response body, not HTTP trailers
- May use base64 encoding (`application/grpc-web-text`)

### Phase 5: Server Streaming

Add streaming support for subscription-style methods:

**PATH**: Create `grpc_stream.go` to handle streaming responses.

## Dependency Chain

```
Shannon SDK (POKTHTTPResponse.Trailer)
    ↓
Relayminer (SerializeHTTPResponse + h2c server)
    ↓
PATH (gRPC detection + trailer handling)
```

Shannon SDK must be updated first. Relayminer and PATH can proceed in parallel after that.

## Architecture Reasoning

### Why trailers are critical

gRPC uses HTTP trailers for status information:
- `grpc-status`: 0 = OK, non-zero = error
- `grpc-message`: Human-readable error message

Without trailer support:
- Clients cannot determine if requests succeeded
- Error information is lost
- gRPC contract is broken

### Why HTTP/2 server support

Native gRPC requires HTTP/2. Options:
1. **h2c**: HTTP/2 over cleartext (for non-TLS deployments)
2. **TLS**: HTTP/2 over TLS (production recommended)

If PATH-to-Relayminer uses plaintext, h2c is required.

### Why pass-through instead of parsing protobuf

PATH and Relayminer are proxies, not application servers. Benefits:
- No proto compilation dependency
- No schema management
- Lower latency
- Simpler implementation

Only parse:
- Content-Type header for detection
- grpc-status trailer for metrics

## Verification

### After Phase 1 (poktroll)

```bash
# Supplier can stake with gRPC endpoint
pocketd tx supplier stake ... --config supplier_config.yaml
# Where supplier_config.yaml includes:
# services:
#   - service_id: cosmos
#     endpoints:
#       - url: http://node:9090
#         rpc_type: GRPC
```

### After Phase 3 (PATH)

```bash
# gRPC request through full stack
grpcurl -plaintext -d '{"address":"cosmos1..."}' \
  localhost:3000 cosmos.bank.v1beta1.Query/Balance

# Verify grpc-status is returned in response
```

### After Phase 4 (gRPC-Web)

```bash
# gRPC-Web request
curl -X POST http://localhost:3000/cosmos.bank.v1beta1.Query/Balance \
  -H "Content-Type: application/grpc-web+proto" \
  -d '<base64-encoded-request>'
```

## Risks and Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| Shannon SDK proto change breaks compatibility | Medium | High | Version proto properly, coordinate release |
| HTTP/2 not working through proxies | Medium | High | Test with Envoy, document requirements |
| Binary logging issues | Low | Low | Skip or base64-encode gRPC in debug logs |
| Streaming complexity | Medium | Medium | Implement unary first, streaming as separate phase |

## Open Questions

1. Should we add gRPC health checks (`grpc.health.v1.Health/Check`)?

2. Which Cosmos services should have gRPC enabled initially?

3. Is TLS always used between PATH and Relayminer, making h2c unnecessary?

4. Should Relayminer translate between gRPC-Web and native gRPC, or require backends to support both?

## Related Documents

- `poktroll.md` - poktroll stake config changes
- `relayminer.md` - Relayminer gRPC support requirements
- `path.md` - PATH gRPC implementation
- `implementation.md` - Step-by-step implementation guide