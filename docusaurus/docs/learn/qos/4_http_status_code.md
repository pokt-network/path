---
sidebar_position: 4
title: HTTP Status Codes
description: Opinionated Approach to HTTP Status Codes
---

## Table of Contents <!-- omit in toc -->

- [Opinionated Stance on HTTP Status Codes](#opinionated-stance-on-http-status-codes)
- [JSON-RPC to HTTP Status Code Mapping](#json-rpc-to-http-status-code-mapping)
- [Status Code Implementation](#status-code-implementation)
- [References](#references)

## Opinionated Stance on HTTP Status Codes

There is no official mapping of JSON-RPC error codes to HTTP status codes in the [JSON-RPC 2.0 specification](https://www.jsonrpc.org/specification). JSON-RPC 2.0 is transport-agnostic and does not mandate any particular use of HTTP status codes.

PATH takes an opinionated stance on mapping JSON-RPC errors to HTTP status codes. This is a common practice but not an industry standard.

## JSON-RPC to HTTP Status Code Mapping

It's common practice in JSON-RPC-over-HTTP implementations to map:

- `Client errors` (e.g., -32600 `Invalid Request`) to `4xx` HTTP statuses
- `Server errors` (e.g., -32603 `Internal error` or -32000 "Server error") to `5xx` HTTP statuses

PATH follows this practice and maps JSON-RPC errors to HTTP status codes as follows:

| JSON-RPC Error Code      | Common Meaning                   | HTTP Status Code          |
| ------------------------ | -------------------------------- | ------------------------- |
| **-32700**               | Parse error                      | **400** Bad Request       |
| **-32600**               | Invalid request                  | **400** Bad Request       |
| **-32601**               | Method not found                 | **404** Not Found         |
| **-32602**               | Invalid params                   | **400** Bad Request       |
| **-32603**               | Internal error                   | **500** Server Error      |
| **-32098**               | Timeout                          | **504** Gateway Timeout   |
| **-32097**               | Rate limited                     | **429** Too Many Requests |
| **-32000…-32099**        | Server error range               | **500** Server Error      |
| **> 0**                  | Application errors (client-side) | **400** Bad Request       |
| **< 0** (other negative) | Application errors (server-side) | **500** Server Error      |

## Status Code Implementation

PATH implements this mapping in the `Response.GetRecommendedHTTPStatusCode()` method:

```go
// GetRecommendedHTTPStatusCode maps a JSON-RPC error response code to an HTTP status code.
func (r Response) GetRecommendedHTTPStatusCode() int {
    // Return 200 OK if no error is present
    if r.Error == nil {
        return http.StatusOK
    }

    // Map standard JSON-RPC error codes to HTTP status codes
    switch r.Error.Code {
    case -32700: // Parse error
        return http.StatusBadRequest // 400
    case -32600: // Invalid request
        return http.StatusBadRequest // 400
    case -32601: // Method not found
        return http.StatusNotFound // 404
    case -32602: // Invalid params
        return http.StatusBadRequest // 400
    case -32603: // Internal error
        return http.StatusInternalServerError // 500
    case -32098: // Timeout (used by some providers)
        return http.StatusGatewayTimeout // 504
    case -32097: // Rate limited (used by some providers)
        return http.StatusTooManyRequests // 429
    }

    // Server error range (-32000 to -32099)
    if r.Error.Code >= -32099 && r.Error.Code <= -32000 {
        return http.StatusInternalServerError // 500
    }

    // Application-defined errors
    if r.Error.Code > 0 {
        // Positive error codes typically indicate client-side issues
        return http.StatusBadRequest // 400
    } else if r.Error.Code < 0 {
        // Other negative error codes typically indicate server-side issues
        return http.StatusInternalServerError // 500
    }

    // This should never be reached, but as a fallback return 500
    return http.StatusInternalServerError // 500
}
```

## References

- Original GitHub Issue can be found [here](https://github.com/pokt-network/path/issues/179)
- Original GitHub PR can be found [here](https://github.com/pokt-network/path/pull/186/files)
