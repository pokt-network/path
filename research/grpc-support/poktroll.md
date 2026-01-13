# poktroll: gRPC Endpoint Type Support

## Problem Statement

The poktroll blockchain defines `RPCType_GRPC = 1` in the protobuf enum at `proto/pocket/shared/service.proto`, but the **on-chain supplier staking** configuration reader does not parse this type. Suppliers cannot stake gRPC endpoints because the stake config reader rejects "GRPC" as an unknown RPC type.

## Analysis: Two Different Config Readers

There are two distinct config readers in poktroll that handle RPC types differently:

### 1. Supplier Stake Config (ON-CHAIN - The Problem)

File: `x/supplier/config/supplier_configs_reader.go`

This is used when suppliers submit stake transactions to register their endpoints on-chain.

```go
// Lines 148-160
func parseEndpointRPCType(endpoint YAMLServiceEndpoint) (sharedtypes.RPCType, error) {
    switch strings.ToLower(endpoint.RPCType) {
    case "json_rpc":
        return sharedtypes.RPCType_JSON_RPC, nil
    case "rest":
        return sharedtypes.RPCType_REST, nil
    case "websocket":
        return sharedtypes.RPCType_WEBSOCKET, nil
    default:
        return sharedtypes.RPCType_UNKNOWN_RPC, ErrSupplierConfigInvalidRPCType.Wrapf("%s", endpoint.RPCType)
    }
}
```

**Missing**: `grpc` and `comet_bft`

### 2. Relayminer Config (OFF-CHAIN - Already Works)

File: `pkg/relayer/config/supplier_hydrator.go`

This is used by the Relayminer to configure backend routing.

```go
// Line 64
rpcType, err := sharedtypes.GetRPCTypeFromConfig(rpcType)
```

This calls `sharedtypes.GetRPCTypeFromConfig()` defined in `x/shared/types/service.go`:

```go
// Lines 172-181
func GetRPCTypeFromConfig(rpcType string) (RPCType, error) {
    rpcTypeInt, ok := RPCType_value[strings.ToUpper(rpcType)]  // Uses proto-generated map
    if !ok {
        return 0, fmt.Errorf("invalid rpc type %s", rpcType)
    }
    if !RPCTypeIsValid(RPCType(rpcTypeInt)) {
        return 0, fmt.Errorf("rpc type %s is in the list of valid RPC types", rpcType)
    }
    return RPCType(rpcTypeInt), nil
}
```

And `RPCTypeIsValid()` at lines 185-192 explicitly includes `RPCType_GRPC`:

```go
func RPCTypeIsValid(rpcType RPCType) bool {
    switch rpcType {
    case RPCType_GRPC,          // GRPC is valid
        RPCType_WEBSOCKET,
        RPCType_JSON_RPC,
        RPCType_REST,
        RPCType_COMET_BFT:
        return true
    }
    return false
}
```

## Relayminer gRPC Support Status

The Relayminer has **partial** infrastructure for gRPC, but critical pieces are missing.

### What EXISTS (config and routing):

1. **Config parsing**: `GetRPCTypeFromConfig()` supports GRPC
2. **Backend routing**: `getServiceConfig()` in `proxy/sync.go` routes by `Rpc-Type` header
3. **HTTP/2 client** (outbound): `ForceAttemptHTTP2: true` for requests TO backends
4. **RPC-type-specific backends**: `RPCTypeServiceConfigs` map allows different backend URLs per RPC type

### What is MISSING (critical for gRPC):

1. **HTTP Trailer capture**: `SerializeHTTPResponse()` in `http_utils.go` does NOT capture `response.Trailer`. gRPC uses trailers for `grpc-status` and `grpc-message`. Without this, clients cannot determine if requests succeeded.

2. **Shannon SDK Trailer field**: `POKTHTTPResponse` proto lacks a `Trailer` field. This is a prerequisite for trailer support.

3. **HTTP/2 server** (inbound): The HTTP server only supports HTTP/2 over TLS. Native gRPC requires HTTP/2. For plaintext deployments, h2c (HTTP/2 cleartext) support is needed.

4. **gRPC-Web handling**: No detection or format handling for gRPC-Web requests.

See `relayminer.md` for detailed Relayminer requirements.

## Required Change

File: `x/supplier/config/supplier_configs_reader.go`

Modify `parseEndpointRPCType()` (lines 148-160):

```go
func parseEndpointRPCType(endpoint YAMLServiceEndpoint) (sharedtypes.RPCType, error) {
    switch strings.ToLower(endpoint.RPCType) {
    case "json_rpc":
        return sharedtypes.RPCType_JSON_RPC, nil
    case "rest":
        return sharedtypes.RPCType_REST, nil
    case "websocket":
        return sharedtypes.RPCType_WEBSOCKET, nil
    case "grpc":                                    // ADD
        return sharedtypes.RPCType_GRPC, nil        // ADD
    case "comet_bft":                               // ADD
        return sharedtypes.RPCType_COMET_BFT, nil   // ADD
    default:
        return sharedtypes.RPCType_UNKNOWN_RPC, ErrSupplierConfigInvalidRPCType.Wrapf("%s", endpoint.RPCType)
    }
}
```

## Alternative: Use Existing Function

Instead of duplicating logic, the stake config reader could use the existing `GetRPCTypeFromConfig()`:

```go
func parseEndpointRPCType(endpoint YAMLServiceEndpoint) (sharedtypes.RPCType, error) {
    rpcType, err := sharedtypes.GetRPCTypeFromConfig(endpoint.RPCType)
    if err != nil {
        return sharedtypes.RPCType_UNKNOWN_RPC, ErrSupplierConfigInvalidRPCType.Wrapf("%s", endpoint.RPCType)
    }
    return rpcType, nil
}
```

This would automatically support all valid RPC types without requiring future updates.

## Reasoning

1. The proto already defines `RPCType_GRPC = 1`. The stake config reader is inconsistent with the proto definition.
2. The shared `GetRPCTypeFromConfig()` function already validates GRPC. The stake config reader duplicates logic and misses types.
3. Without this change, suppliers cannot register gRPC endpoints on-chain.
4. This is a necessary (but not sufficient) step for gRPC support. Relayminer also needs updates (see `relayminer.md`).

## Impact

- Backwards compatible: existing configs continue to work
- No proto changes required
- No state machine changes
- No consensus-breaking changes
- Suppliers can immediately begin staking gRPC endpoints after this change

## Potential Concern: HTTP/2 for Incoming Requests

The Relayminer's HTTP server (`http.Server` in `proxy/http_server.go`) uses standard Go HTTP server. By default:
- HTTP/2 is enabled when using HTTPS (TLS)
- HTTP/2 is NOT enabled for plaintext HTTP

For native gRPC (not gRPC-Web), HTTP/2 is required. If the Gateway-to-Relayminer connection uses plaintext HTTP, native gRPC requests may fail. gRPC-Web would work since it uses HTTP/1.1.

This may require investigation depending on deployment configuration.

## Files to Modify

1. `x/supplier/config/supplier_configs_reader.go` - Update `parseEndpointRPCType()` function

## Testing

Add test cases to verify:
- "grpc" parses to `RPCType_GRPC`
- "GRPC" parses to `RPCType_GRPC` (case insensitivity via `strings.ToLower`)
- "comet_bft" parses to `RPCType_COMET_BFT`

## Dependencies

This stake config change is standalone and can be done first.

For end-to-end gRPC support, the full dependency chain is:

1. **Shannon SDK**: Add `Trailer` field to `POKTHTTPResponse` proto
2. **poktroll stake config**: This document - add GRPC parsing
3. **Relayminer**: Add trailer capture + HTTP/2 server (see `relayminer.md`)
4. **PATH**: Add gRPC detection and handling (see `path.md`)