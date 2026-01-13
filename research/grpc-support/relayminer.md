# Relayminer: gRPC Support Requirements

## Current State Analysis

The Relayminer has partial infrastructure for gRPC but is missing critical parts for proper support.

### What Works

1. **Config parsing**: `GetRPCTypeFromConfig()` in `x/shared/types/service.go` supports GRPC
2. **RPC-type routing**: `getServiceConfig()` in `proxy/sync.go` routes based on `Rpc-Type` header
3. **HTTP/2 client**: Outbound HTTP client has `ForceAttemptHTTP2: true`
4. **RPC-type-specific backends**: `RPCTypeServiceConfigs` map allows different backend URLs per RPC type

### What's Missing

## Gap 1: HTTP Trailer Support (Critical)

File: `pkg/relayer/proxy/http_utils.go` (lines 169-220)

The `SerializeHTTPResponse` function only captures:
- `response.StatusCode`
- `response.Header`
- `response.Body`

**Missing**: `response.Trailer`

gRPC uses HTTP trailers for critical information:
- `grpc-status`: The gRPC status code (0 = OK, non-zero = error)
- `grpc-message`: Human-readable error message
- `grpc-status-details-bin`: Detailed error information

Without trailer support, gRPC clients cannot determine if a request succeeded or failed.

### Required Change

The `POKTHTTPResponse` proto in Shannon SDK needs a `Trailer` field:

```protobuf
message POKTHTTPResponse {
    uint32 status_code = 1;
    map<string, Header> header = 2;
    bytes body_bz = 3;
    map<string, Header> trailer = 4;  // ADD: HTTP trailers for gRPC
}
```

Then `SerializeHTTPResponse` must capture trailers:

```go
// In pkg/relayer/proxy/http_utils.go
trailers := make(map[string]*sdktypes.Header, len(response.Trailer))
for trailerKey := range response.Trailer {
    trailerValues := response.Trailer.Values(trailerKey)
    trailers[trailerKey] = &sdktypes.Header{
        Key:    trailerKey,
        Values: trailerValues,
    }
}

poktHTTPResponse = &sdktypes.POKTHTTPResponse{
    StatusCode: uint32(response.StatusCode),
    Header:     headers,
    BodyBz:     responseBodyBz,
    Trailer:    trailers,  // ADD
}
```

## Gap 2: HTTP/2 Server Support (For Native gRPC)

File: `pkg/relayer/proxy/http_server.go` (lines 117-134)

The HTTP server is a standard `http.Server`:

```go
httpServer := &http.Server{
    IdleTimeout:  60 * time.Second,
    ReadTimeout:  config.DefaultRequestTimeoutDuration,
    WriteTimeout: config.DefaultRequestTimeoutDuration,
    // ... no HTTP/2 configuration
}
```

Go's `http.Server` only supports HTTP/2 over TLS by default. For plaintext HTTP/2 (h2c), explicit configuration is required.

Native gRPC requires HTTP/2. If the PATH-to-Relayminer connection uses plaintext HTTP, native gRPC will fail.

### Typical Operator Deployment

Most operators run Relayminer behind a reverse proxy (nginx, HAProxy, Envoy):

```
Client (gRPC/HTTP2) → Reverse Proxy (TLS termination) → Relayminer (plaintext)
```

The proxy terminates TLS but must forward HTTP/2 to the backend. This means Relayminer needs h2c support even though TLS is handled upstream.

| Deployment                                | Result                                  |
|-------------------------------------------|-----------------------------------------|
| Proxy forwards HTTP/2 (h2c) to Relayminer | Native gRPC works (requires h2c)        |
| Proxy downgrades to HTTP/1.1              | Native gRPC breaks, only gRPC-Web works |

For typical operator setups, **h2c is required**.

### Required Change

Add h2c (HTTP/2 cleartext) support:

```go
import "golang.org/x/net/http2"
import "golang.org/x/net/http2/h2c"

// Wrap the handler with h2c support
h2cHandler := h2c.NewHandler(server, &http2.Server{})
httpServer.Handler = h2cHandler
```

Or require TLS for HTTP/2 (production recommended anyway).

## Gap 3: gRPC-Web Format Handling (Optional but Important)

gRPC-Web is different from native gRPC:

| Aspect       | Native gRPC        | gRPC-Web                                       |
|--------------|--------------------|------------------------------------------------|
| Protocol     | HTTP/2 required    | HTTP/1.1 works                                 |
| Trailers     | HTTP trailers      | Encoded in response body                       |
| Content-Type | `application/grpc` | `application/grpc-web`                         |
| Encoding     | Binary             | Binary or base64 (`application/grpc-web-text`) |

Currently, the Relayminer has no gRPC-Web specific handling. If a gRPC-Web request comes in:
1. The body format is different (trailers appended to body)
2. The response format must match (trailers in body, not HTTP trailers)

### Required Change

Either:
1. **Transparent proxy**: Detect gRPC-Web and pass through without modification (if backend supports gRPC-Web)
2. **Translation**: Convert gRPC-Web to native gRPC for backend, convert response back

For initial implementation, transparent proxy is simpler if backends support gRPC-Web directly.

## Gap 4: Content-Type Preservation

File: `pkg/relayer/http_request.go`

The `BuildServiceBackendRequest` function copies headers from the relay request:

```go
poktHTTPRequest.CopyToHTTPHeader(header)
```

This should preserve `Content-Type: application/grpc` or `application/grpc-web`, but should be verified.

## Implementation Priority

1. **HTTP Trailer Support** (Critical)
   - Without this, gRPC error handling is broken
   - Requires Shannon SDK proto change
   - Affects: Shannon SDK, Relayminer, PATH

2. **HTTP/2 Server** (Required for native gRPC)
   - Without this, native gRPC won't work over plaintext
   - Required for typical deployments where proxy terminates TLS upstream

3. **gRPC-Web Handling** (Required for browser clients)
   - Without this, browser-based gRPC clients won't work
   - Can be transparent proxy if backends support gRPC-Web

## Dependency Chain

```
Shannon SDK (POKTHTTPResponse.Trailer)
    → Relayminer (SerializeHTTPResponse)
    → PATH (response reconstruction)
```

The Shannon SDK change must come first since both Relayminer and PATH depend on it.

## Files to Modify

### Shannon SDK
1. `types/http.proto` - Add `Trailer` field to `POKTHTTPResponse`
2. Regenerate Go code

### Relayminer (poktroll)
1. `pkg/relayer/proxy/http_utils.go` - Capture trailers in `SerializeHTTPResponse`
2. `pkg/relayer/proxy/http_server.go` - Add h2c support for HTTP/2 over plaintext
3. (Optional) `pkg/relayer/proxy/grpc_web.go` - gRPC-Web format handling

### PATH
1. Response reconstruction must include trailers
2. Forward trailers to client in response

## Testing

1. Send native gRPC request through PATH → Relayminer → backend
2. Verify `grpc-status` trailer is preserved in response
3. Verify gRPC error cases propagate correctly
4. Test gRPC-Web if implemented

## Open Questions

1. Should Relayminer translate between gRPC-Web and native gRPC, or require backends to support both?
2. Should streaming gRPC be supported? (More complex, different handling than unary)
