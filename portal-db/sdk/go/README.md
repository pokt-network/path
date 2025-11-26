# Portal DB Go SDK

This Go SDK provides a type-safe client for the Portal DB API, generated using [oapi-codegen](https://github.com/oapi-codegen/oapi-codegen).

## Table of Contents

- [Portal DB Go SDK](#portal-db-go-sdk)
  - [Table of Contents](#table-of-contents)
  - [Installation](#installation)
  - [Quick Start](#quick-start)
  - [Authentication](#authentication)
  - [Query Features](#query-features)
    - [Filtering and Selection](#filtering-and-selection)
    - [Specific Resource Lookup](#specific-resource-lookup)
  - [RPC Functions](#rpc-functions)
  - [Error Handling](#error-handling)
  - [Complete Example](#complete-example)
  - [Development](#development)
  - [Generated Files](#generated-files)
  - [Related Documentation](#related-documentation)

## Installation

```bash
go get github.com/pokt-network/path/portal-db/sdk/go
```

## Quick Start

<details>
<summary>Click to expand Quick Start example</summary>

```go
package main

import (
    "context"
    "fmt"
    "log"
    
    "github.com/pokt-network/path/portal-db/sdk/go"
)

func main() {
    // Create a new client with typed responses
    client, err := portaldb.NewClientWithResponses("http://localhost:3000")
    if err != nil {
        log.Fatal(err)
    }
    
    ctx := context.Background()
    
    // Example: Get public data
    resp, err := client.GetNetworksWithResponse(ctx, &portaldb.GetNetworksParams{})
    if err != nil {
        log.Fatal(err)
    }
    
    if resp.StatusCode() == 200 && resp.JSON200 != nil {
        networks := *resp.JSON200
        fmt.Printf("Found %d networks\n", len(networks))
    }
}
```

</details>

## Authentication

For authenticated endpoints, add your JWT token to requests:

<details>
<summary>Click to expand Authentication example</summary>

```go
import (
    "context"
    "net/http"
    
    "github.com/pokt-network/path/portal-db/sdk/go"
)

func authenticatedExample() {
    client, err := portaldb.NewClientWithResponses("http://localhost:3000")
    if err != nil {
        log.Fatal(err)
    }
    
    // Add JWT token to requests
    token := "your-jwt-token-here"
    ctx := context.Background()
    
    // Use RequestEditorFn to add authentication header
    requestEditor := func(ctx context.Context, req *http.Request) error {
        req.Header.Set("Authorization", "Bearer "+token)
        return nil
    }
    
    // Make authenticated request
    resp, err := client.GetPortalAccountsWithResponse(
        ctx, 
        &portaldb.GetPortalAccountsParams{}, 
        requestEditor,
    )
    if err != nil {
        log.Fatal(err)
    }
    
    if resp.StatusCode() == 200 && resp.JSON200 != nil {
        accounts := *resp.JSON200
        fmt.Printf("Found %d accounts\n", len(accounts))
    } else {
        fmt.Printf("Authentication failed: %d\n", resp.StatusCode())
    }
}
```

</details>

## Query Features

The SDK supports PostgREST's powerful query features for filtering, selecting, and pagination:

### Filtering and Selection

<details>
<summary>Click to expand Filtering and Selection example</summary>

```go
// Filter active services with specific fields
resp, err := client.GetServicesWithResponse(ctx, &portaldb.GetServicesParams{
    Active: func() *string { s := "eq.true"; return &s }(),
    Select: func() *string { s := "service_id,service_name,active,network_id"; return &s }(),
    Limit:  func() *string { s := "3"; return &s }(),
})
if err != nil {
    log.Fatal(err)
}

if resp.StatusCode() == 200 && resp.JSON200 != nil {
    services := *resp.JSON200
    fmt.Printf("Found %d active services\n", len(services))
}
```

</details>

### Specific Resource Lookup

<details>
<summary>Click to expand Specific Resource Lookup example</summary>

```go
// Get a specific service by ID
resp, err := client.GetServicesWithResponse(ctx, &portaldb.GetServicesParams{
    ServiceId: func() *string { s := "eq.ethereum-mainnet"; return &s }(),
})
if err != nil {
    log.Fatal(err)
}

if resp.StatusCode() == 200 && resp.JSON200 != nil {
    services := *resp.JSON200
    fmt.Printf("Found service: %s\n", (*services)[0].ServiceName)
}
```

</details>

## RPC Functions

Access custom database functions via the RPC endpoint:

<details>
<summary>Click to expand RPC Functions example</summary>

```go
// Get current user info from JWT claims
resp, err := client.PostRpcMeWithResponse(
    ctx, 
    &portaldb.PostRpcMeParams{}, 
    portaldb.PostRpcMeJSONRequestBody{}, 
    requestEditor,
)
if err != nil {
    log.Fatal(err)
}

if resp.StatusCode() == 200 {
    fmt.Printf("User info: %s\n", string(resp.Body))
}
```

</details>

## Error Handling

<details>
<summary>Click to expand Error Handling example</summary>

```go
resp, err := client.GetNetworksWithResponse(ctx, &portaldb.GetNetworksParams{})
if err != nil {
    // Handle network/client errors
    log.Printf("Client error: %v", err)
    return
}

switch resp.StatusCode() {
case 200:
    // Success - access typed data
    if resp.JSON200 != nil {
        networks := *resp.JSON200
        fmt.Printf("Found %d networks\n", len(networks))
    }
case 401:
    // Unauthorized
    fmt.Println("Authentication required")
default:
    // Other status codes
    fmt.Printf("Unexpected status: %d\n", resp.StatusCode())
}
```

</details>

## Complete Example

A complete working example is available in the [`example/`](./example) directory:

- [`example/main.go`](./example/main.go) - Demonstrates basic SDK usage
- [`example/go.mod`](./example/go.mod) - Module configuration with local replace directive

**To run:**

```bash
# Make sure Portal DB API is running on http://localhost:3000
cd example
go run main.go
```

## Development

This SDK was generated from the OpenAPI specification served by PostgREST. 

To regenerate run the following make target while the PostgREST API is running:

```bash
# From the portal-db directory
make generate-all
```

## Generated Files

- `models.go` - Generated data models and type definitions
- `client.go` - Generated API client methods and HTTP logic
- `go.mod` - Go module definition
- `README.md` - This documentation

## Related Documentation

- **API Documentation**: [../../api/README.md](../../api/README.md)
- **OpenAPI Specification**: `../../api/openapi/openapi.json`
