# Implementation Guide

This document provides step-by-step implementation instructions for adding gRPC support. Each step includes the exact file path, what to do, and patterns to follow.

## Prerequisites

Before implementing, read these files to understand existing patterns:

```
# poktroll
../poktroll/x/supplier/config/supplier_configs_reader.go
../poktroll/proto/pocket/shared/service.proto

# PATH - Cosmos QoS patterns
qos/cosmos/rpctype_cometbft.go
qos/cosmos/request_validator.go
qos/cosmos/request_validator_rest.go
qos/cosmos/response_rest.go

# PATH - Protocol layer
protocol/shannon/endpoint.go
protocol/shannon/protocol.go
protocol/payload.go

# PATH - Configuration
config/service_qos_config.go
```

## Part 1: poktroll Config Reader

### Step 1.1: Update RPC Type Parsing

File: `../poktroll/x/supplier/config/supplier_configs_reader.go`

Find the function that parses RPC type strings (likely named `parseRPCType` or similar, or a switch statement in `hydrateSupplierServiceConfig`).

Add cases:
```go
case "GRPC", "grpc":
    return sharedtypes.RPCType_GRPC, nil
case "COMET_BFT", "comet_bft", "COMETBFT", "cometbft":
    return sharedtypes.RPCType_COMET_BFT, nil
```

Run tests: `go test ./x/supplier/config/...`

## Part 2: PATH Core Implementation

### Step 2.1: Create gRPC Detection

Create file: `qos/cosmos/rpctype_grpc.go`

```go
package cosmos

import (
    "strings"

    sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
)

// isGRPCRequest returns true if the Content-Type indicates a gRPC request.
func isGRPCRequest(contentType string) bool {
    return strings.HasPrefix(contentType, "application/grpc")
}

// isGRPCWebRequest returns true if this is specifically a gRPC-Web request.
func isGRPCWebRequest(contentType string) bool {
    return strings.HasPrefix(contentType, "application/grpc-web")
}

// getGRPCRPCType returns RPCType_GRPC for gRPC requests.
func getGRPCRPCType() sharedtypes.RPCType {
    return sharedtypes.RPCType_GRPC
}
```

### Step 2.2: Create gRPC Request Validator

Create file: `qos/cosmos/request_validator_grpc.go`

Follow the structure of `request_validator_rest.go`. Key functions:

1. `validateGRPCRequest(httpRequestURL *url.URL, httpRequestMethod string, httpRequestBody []byte, httpHeaders http.Header, contentType string) (gateway.RequestQoSContext, bool)`

2. `buildGRPCRequestContext(rpcType sharedtypes.RPCType, httpRequestURL *url.URL, httpRequestMethod string, httpRequestBody []byte, httpHeaders http.Header, requestOrigin qosobservations.RequestOrigin) (gateway.RequestQoSContext, bool)`

3. `buildGRPCServicePayload(rpcType sharedtypes.RPCType, httpRequestURL *url.URL, httpRequestMethod string, httpRequestBody []byte, httpHeaders http.Header) protocol.Payload`

4. `buildGRPCRequestObservations(...) *qosobservations.CosmosRequestObservations`

5. `getGRPCEndpointResponseValidator() func(polylog.Logger, []byte) response`

Key implementation details:

For `buildGRPCServicePayload`:
```go
func buildGRPCServicePayload(
    rpcType sharedtypes.RPCType,
    httpRequestURL *url.URL,
    httpRequestMethod string,
    httpRequestBody []byte,
    httpHeaders http.Header,
) protocol.Payload {
    // Preserve gRPC-specific headers
    headers := make(map[string]string)
    grpcHeaders := []string{"Content-Type", "grpc-encoding", "grpc-accept-encoding", "te", "grpc-timeout"}
    for _, key := range grpcHeaders {
        if val := httpHeaders.Get(key); val != "" {
            headers[key] = val
        }
    }

    return protocol.Payload{
        Data:    string(httpRequestBody),
        Method:  httpRequestMethod,
        Path:    httpRequestURL.Path,
        Headers: headers,
        RPCType: rpcType,
    }
}
```

For request ID, use a constant like `grpcRequestID = "grpc_request"` (similar to REST).

### Step 2.3: Create gRPC Response Handler

Create file: `qos/cosmos/response_grpc.go`

```go
package cosmos

import (
    "net/http"

    "github.com/pokt-network/poktroll/pkg/polylog"

    pathhttp "github.com/buildwithgrove/path/network/http"
    qosobservations "github.com/buildwithgrove/path/observation/qos"
)

// responseGRPC handles gRPC response pass-through.
type responseGRPC struct {
    logger         polylog.Logger
    responseBz     []byte
    httpStatusCode int
}

// unmarshalGRPCResponse creates a response from raw gRPC response bytes.
// gRPC responses are passed through without parsing.
func unmarshalGRPCResponse(
    logger polylog.Logger,
    endpointResponseBz []byte,
) response {
    return &responseGRPC{
        logger:         logger,
        responseBz:     endpointResponseBz,
        httpStatusCode: http.StatusOK,
    }
}

func (r *responseGRPC) GetHTTPResponse() pathhttp.HTTPResponse {
    return httpResponse{
        responsePayload: r.responseBz,
        httpStatusCode:  r.httpStatusCode,
    }
}

func (r *responseGRPC) GetObservation() qosobservations.CosmosEndpointObservation {
    return qosobservations.CosmosEndpointObservation{
        ResponseValidation: &qosobservations.ResponseValidation{
            IsValid:        true,
            ValidationType: qosobservations.CosmosResponseValidationType_COSMOS_RESPONSE_VALIDATION_TYPE_GRPC,
        },
    }
}
```

### Step 2.4: Update Request Validator Entry Point

File: `qos/cosmos/request_validator.go`

In `validateHTTPRequest()`, add gRPC detection before JSON-RPC/REST:

```go
func (rv *requestValidator) validateHTTPRequest(req *http.Request) (gateway.RequestQoSContext, bool) {
    // ... existing body reading code ...

    contentType := req.Header.Get("Content-Type")

    // Check for gRPC first (unambiguous by Content-Type)
    if isGRPCRequest(contentType) {
        return rv.validateGRPCRequest(req.URL, req.Method, body, req.Header, contentType)
    }

    // Existing JSON-RPC and REST detection follows...
}
```

### Step 2.5: Update Proto BackendServiceType

File: `proto/path/qos/cosmos_request.proto`

Add to enum:
```protobuf
enum BackendServiceType {
    BACKEND_SERVICE_TYPE_UNSPECIFIED = 0;
    BACKEND_SERVICE_TYPE_JSONRPC = 1;
    BACKEND_SERVICE_TYPE_REST = 2;
    BACKEND_SERVICE_TYPE_COMETBFT = 3;
    BACKEND_SERVICE_TYPE_GRPC = 4;
}
```

Add message:
```protobuf
message GRPCRequest {
    string service_path = 1;
    string method_name = 2;
    uint32 payload_length = 3;
    bool is_grpc_web = 4;
}
```

Update `CosmosRequestProfile` oneof to include `GRPCRequest grpc_request = X;`

Run: `make proto_generate` (or equivalent)

### Step 2.6: Update convertToProtoBackendServiceType

File: `qos/cosmos/request_validator_jsonrpc.go`

Find `convertToProtoBackendServiceType` function, add:
```go
case sharedtypes.RPCType_GRPC:
    return qosobservations.BackendServiceType_BACKEND_SERVICE_TYPE_GRPC
```

### Step 2.7: Update Protocol Endpoint Structure

File: `protocol/shannon/endpoint.go`

Add field to `protocolEndpoint`:
```go
type protocolEndpoint struct {
    supplier     string
    url          string
    websocketUrl string
    grpcUrl      string  // Add this
    session      sessiontypes.Session
}
```

Update `GetURL` method:
```go
func (e protocolEndpoint) GetURL(rpcType sharedtypes.RPCType) string {
    switch rpcType {
    case sharedtypes.RPCType_GRPC:
        if e.grpcUrl != "" {
            return e.grpcUrl
        }
        return e.url
    case sharedtypes.RPCType_WEBSOCKET:
        if e.websocketUrl != "" {
            return e.websocketUrl
        }
        return e.url
    default:
        return e.url
    }
}
```

In `endpointsFromSession`, add case for GRPC:
```go
case sharedtypes.RPCType_GRPC:
    endpoint.grpcUrl = supplierRPCTypeEndpoint.Endpoint().Url
```

### Step 2.8: Add gRPC Sanctioned Endpoints Store

File: `protocol/shannon/protocol.go`

Find where sanctioned endpoint stores are initialized, add:
```go
sanctionedEndpointsStores: map[sharedtypes.RPCType]*sanctionedEndpointsStore{
    sharedtypes.RPCType_JSON_RPC:  newSanctionedEndpointsStore(logger),
    sharedtypes.RPCType_WEBSOCKET: newSanctionedEndpointsStore(logger),
    sharedtypes.RPCType_GRPC:      newSanctionedEndpointsStore(logger),
},
```

### Step 2.9: Update Service Configuration

File: `config/service_qos_config.go`

Add `sharedtypes.RPCType_GRPC: {}` to Cosmos services. Example:

```go
cosmos.NewCosmosSDKServiceQoSConfig("pocket", "pocket", "", map[sharedtypes.RPCType]struct{}{
    sharedtypes.RPCType_REST:      {},
    sharedtypes.RPCType_COMET_BFT: {},
    sharedtypes.RPCType_GRPC:      {},
}),
```

Apply to: pocket, celestia, osmosis, cosmoshub, and other Cosmos chains as appropriate.

### Step 2.10: Update HTTP Client Content-Type Handling

File: `network/http/http_client.go`

Find where Content-Type is set. Change from:
```go
req.Header.Set("Content-Type", "application/json")
```

To:
```go
if _, hasContentType := headers["Content-Type"]; !hasContentType {
    req.Header.Set("Content-Type", "application/json")
}
```

This allows gRPC requests to preserve their Content-Type.

## Part 3: Server Streaming (Phase 2)

Create file: `qos/cosmos/grpc_stream.go`

This handles server-streaming gRPC responses. Implementation involves:
1. Detecting streaming methods (may need method list or heuristic)
2. Reading gRPC frames (5-byte header: 1 byte compressed flag, 4 bytes length)
3. Forwarding frames as they arrive
4. Handling final trailers

This is more complex; implement after unary calls work.

## Part 4: gRPC-Web (Phase 3)

Create file: `gateway/grpc_web.go`

gRPC-Web differences:
1. Trailers encoded in response body (not HTTP trailers)
2. May use base64 encoding (`application/grpc-web-text`)
3. Works over HTTP/1.1

Implementation involves detecting gRPC-Web specifically and handling format conversion.

## Testing

### Unit Tests

Create: `qos/cosmos/rpctype_grpc_test.go`
```go
func TestIsGRPCRequest(t *testing.T) {
    tests := []struct {
        contentType string
        expected    bool
    }{
        {"application/grpc", true},
        {"application/grpc+proto", true},
        {"application/grpc-web", true},
        {"application/grpc-web+proto", true},
        {"application/json", false},
        {"text/html", false},
    }
    // ...
}
```

Create: `qos/cosmos/request_validator_grpc_test.go`
Follow pattern from `request_validator_websocket_test.go`.

### Integration Test

Add to `e2e/service_cosmos_test.go`:
```go
case sharedtypes.RPCType_GRPC:
    targets, err := getGRPCVegetaTargets(ts, gatewayURL)
```

### Manual Test

```bash
# After implementation, test with:
grpcurl -plaintext -d '{"address":"cosmos1..."}' \
  localhost:3000 cosmos.bank.v1beta1.Query/Balance
```

## Verification Checklist

- [ ] poktroll: `GRPC` parses in supplier config
- [ ] PATH: gRPC request detected by Content-Type
- [ ] PATH: gRPC request creates correct Payload with RPCType_GRPC
- [ ] PATH: Protocol layer returns gRPC endpoint URL
- [ ] PATH: Response passes through correctly
- [ ] PATH: Observations include GRPC backend type
- [ ] PATH: Service config includes GRPC for Cosmos services
- [ ] Tests pass