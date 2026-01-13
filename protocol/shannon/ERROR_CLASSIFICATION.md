# Shannon Protocol Error Classification

This document defines how errors from suppliers (relay miners) are classified and mapped to reputation signals.

## Philosophy

**Errors are classified by fault responsibility:**
- If the supplier can fix it → penalize reputation
- If PATH caused it → don't penalize (or minor only)
- If we can't determine cause → penalize (conservative approach)

**All error classifications directly map to reputation signals** - there is no intermediate "sanction" layer.

## Error Categories

### 1. Supplier Service Misconfiguration (FATAL -50)
**Who's responsible:** Supplier
**Reputation impact:** Fatal error (-50 points)
**Recovery:** Supplier must fix configuration

Errors indicating the supplier's service is fundamentally misconfigured or not set up for this service.

**Error Types:**
- `SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_SERVICE_NOT_CONFIGURED`
  - Service not configured in relay miner
  - Example: "service eth not configured"
  - **File:** `protocol/shannon/error_classification.go` (FATAL_ERROR category)

**Why fatal:** Supplier cannot serve this service until they fix their configuration.

---

### 2. Supplier Protocol Violations (CRITICAL -25)
**Who's responsible:** Supplier
**Reputation impact:** Critical error (-25 points)
**Recovery:** Penalized until supplier fixes their relay miner

Errors indicating the supplier's relay miner is violating the Shannon protocol or returning invalid/malformed responses.

**Error Types:**

#### Signature & Validation Failures
- `SHANNON_ENDPOINT_ERROR_RESPONSE_VALIDATION_ERR`
  - Response failed basic validation
- `SHANNON_ENDPOINT_ERROR_RESPONSE_SIGNATURE_VALIDATION_ERR`
  - Signature verification failed
- `SHANNON_ENDPOINT_ERROR_RESPONSE_GET_PUBKEY_ERR`
  - Cannot fetch supplier's public key
- `SHANNON_ENDPOINT_ERROR_NIL_SUPPLIER_PUBKEY`
  - Supplier account not properly initialized (no public key)
- `SHANNON_ENDPOINT_ERROR_PAYLOAD_UNMARSHAL_ERR`
  - RelayResponse failed to unmarshal

**File:** `protocol/shannon/error_classification.go` (CRITICAL_ERROR category - signature/validation section)

#### Malformed Protocol Responses
- `SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_PROTOCOL_WIRE_TYPE`
  - Invalid protobuf wire type
- `SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_PROTOCOL_RELAY_REQUEST`
  - Malformed RelayRequest response

**File:** `protocol/shannon/error_classification.go:241-247` (protocol parsing section)

**Why critical:** These indicate serious issues with the relay miner implementation or potential malicious behavior.

---

### 3. Supplier Service Errors (CRITICAL -25)
**Who's responsible:** Supplier
**Reputation impact:** Critical error (-25 points)
**Recovery:** Penalized until supplier's backend stabilizes

Errors from the supplier's backend blockchain service returning errors.

**Error Types:**
- `SHANNON_ENDPOINT_ERROR_HTTP_NON_2XX_STATUS`
  - HTTP status not 2xx from relay miner
- `SHANNON_ENDPOINT_ERROR_HTTP_BAD_RESPONSE`
  - Malformed HTTP response
- `SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_5XX`
  - Relay miner backend returned 5xx error
- `SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_BACKEND_SERVICE`
  - Backend service error embedded in payload

**File:** `protocol/shannon/error_classification.go:255-257` (backend service section)

**Why critical:** HTTP 5xx errors are server-side failures - supplier's responsibility to fix.

---

### 4. Supplier Infrastructure Issues - Connection (MAJOR -10)
**Who's responsible:** Supplier (network/infrastructure)
**Reputation impact:** Major error (-10 points)
**Recovery:** Penalized until network issues resolve

Errors establishing or maintaining network connections to the supplier's endpoint.

**Error Types:**

#### Connection Establishment Failures
- `SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_REFUSED`
  - Connection refused (service not running/unreachable)
- `SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_RESET`
  - Connection reset by peer
- `SHANNON_ENDPOINT_ERROR_HTTP_NO_ROUTE_TO_HOST`
  - No network route to host
- `SHANNON_ENDPOINT_ERROR_HTTP_NETWORK_UNREACHABLE`
  - Network unreachable
- `SHANNON_ENDPOINT_ERROR_HTTP_BROKEN_PIPE`
  - Broken pipe (connection lost)
- `SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_CONNECTION_REFUSED`
  - Connection refused (from payload analysis)
- `SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_TCP_CONNECTION`
  - TCP connection error
- `SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_DNS_RESOLUTION`
  - DNS resolution failed
- `SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_TLS_HANDSHAKE`
  - TLS handshake failed

**File:** `protocol/shannon/error_classification.go:163-175` (connection establishment section)
**File:** `protocol/shannon/error_classification.go:276-288` (raw payload network section)

#### WebSocket Connection Failures
- `SHANNON_ENDPOINT_ERROR_WEBSOCKET_CONNECTION_FAILED`
  - WebSocket connection establishment failed

**File:** `protocol/shannon/error_classification.go:82-84` (websocket section)

**Why major:** Cannot establish connection = endpoint is unreachable or misconfigured.

---

### 5. Supplier Infrastructure Issues - Timeout (MAJOR -10)
**Who's responsible:** Supplier (performance/capacity)
**Reputation impact:** Major error (-10 points)
**Recovery:** Penalized until supplier improves performance

Errors where the supplier fails to respond within the timeout period.

**Error Types:**
- `SHANNON_ENDPOINT_ERROR_TIMEOUT`
  - Generic timeout (endpoint didn't respond)
- `SHANNON_ENDPOINT_ERROR_HTTP_IO_TIMEOUT`
  - I/O timeout during HTTP request
- `SHANNON_ENDPOINT_ERROR_HTTP_CONTEXT_DEADLINE_EXCEEDED`
  - Context deadline exceeded (timeout)
- `SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_TIMEOUT`
  - Timeout during connection establishment

**File:** `protocol/shannon/error_classification.go:110-113` (timeout section)
**File:** `protocol/shannon/error_classification.go:182-188` (transport timeout section)

**Why major:** Timeouts indicate the supplier is overloaded, slow, or has network issues.

---

### 6. Supplier Infrastructure Issues - Transport (MAJOR -10)
**Who's responsible:** Supplier (infrastructure)
**Reputation impact:** Major error (-10 points)
**Recovery:** Penalized until transport issues resolve

HTTP/network transport layer errors that aren't covered by specific categories above.

**Error Types:**
- `SHANNON_ENDPOINT_ERROR_HTTP_TRANSPORT_ERROR`
  - Generic transport error
- `SHANNON_ENDPOINT_ERROR_HTTP_INVALID_STATUS`
  - Invalid HTTP status line
- `SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_UNEXPECTED_EOF`
  - Unexpected EOF in response
- `SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_HTTP_TRANSPORT`
  - HTTP transport error from payload

**File:** `protocol/shannon/error_classification.go:197-210` (HTTP protocol/transport section)
**File:** `protocol/shannon/error_classification.go:249-252` (unexpected EOF section)

**Why major:** Transport errors indicate infrastructure problems on the supplier side.

---

### 7. Configuration Issues (MAJOR -10)
**Who's responsible:** Supplier (DNS, TLS cert, etc.)
**Reputation impact:** Major error (-10 points)
**Recovery:** Penalized until supplier fixes configuration

**Error Types:**
- `SHANNON_ENDPOINT_ERROR_CONFIG`
  - DNS lookup error, TLS certificate error, etc.

**File:** `protocol/shannon/error_classification.go:106-108` (config section)

**Why major:** Configuration errors prevent successful requests.

---

### 8. Not Supplier's Fault - Client Errors (MINOR -3)
**Who's responsible:** Client (PATH or end user)
**Reputation impact:** Minor error (-3 points)
**Recovery:** Immediate (not really the endpoint's fault)

Errors that indicate the client (PATH) sent a bad request, not a supplier issue.

**Error Types:**
- `SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_4XX`
  - HTTP 4xx error (client error - bad request from PATH)

**File:** `protocol/shannon/error_classification.go:120-121` (4xx section)

**Why minor:** HTTP 4xx means PATH sent a malformed request. We apply minor penalty to be conservative, but this isn't really the supplier's fault.

---

### 9. Not Supplier's Fault - PATH Internal (NO PENALTY, +1)
**Who's responsible:** PATH
**Reputation impact:** Success signal (+1) - neutral, not endpoint's fault
**Recovery:** N/A

Errors that are entirely on PATH's side.

**Error Types:**
- `SHANNON_ENDPOINT_REQUEST_CANCELED_BY_PATH`
  - PATH intentionally canceled the request

**File:** `protocol/shannon/error_classification.go:122-124` (cancellation section)

**Why success:** This is PATH's decision, not the endpoint's fault. Return a success signal to avoid penalizing.

---

### 10. Not Supplier's Fault - Transient/Normal Behavior (MINOR -3)
**Who's responsible:** Normal operation or unclear
**Reputation impact:** Minor error (-3 points)
**Recovery:** Quick (transient issues)

Errors that could be normal behavior or are unclear in fault responsibility.

**Error Types:**

#### WebSocket Transient Errors
- `SHANNON_ENDPOINT_ERROR_WEBSOCKET_REQUEST_SIGNING_FAILED`
  - WebSocket request signing failed (could be PATH issue)
- `SHANNON_ENDPOINT_ERROR_WEBSOCKET_RELAY_RESPONSE_VALIDATION_FAILED`
  - WebSocket relay response validation failed (could be transient)

**File:** `protocol/shannon/error_classification.go:123-126` (websocket validation section)

#### Response Size / Connection Management
- `SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_RESPONSE_SIZE_EXCEEDED`
  - Response size exceeded (could be legitimate large response)
- `SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_SERVER_CLOSED_CONNECTION`
  - Server closed idle connection (normal HTTP behavior)
- `SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_SUPPLIERS_NOT_REACHABLE`
  - Suppliers not reachable (could be temporary network issue)

**File:** `protocol/shannon/error_classification.go:259-273` (response size / connection section)

**Why minor:** These could be legitimate behavior or transient issues, not worth heavy penalty.

---

### 11. Unknown/Unclassified Errors (MINOR -3)
**Who's responsible:** Unknown
**Reputation impact:** Minor error (-3 points)
**Recovery:** Depends on root cause

Errors that don't match any known pattern.

**Error Types:**
- `SHANNON_ENDPOINT_ERROR_HTTP_UNKNOWN`
  - Unclassified HTTP error
- `SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_UNKNOWN`
  - Unclassified payload error
- `SHANNON_ENDPOINT_ERROR_UNKNOWN`
  - Generic unknown error

**File:** `protocol/shannon/error_classification.go:212-221` (unknown HTTP section)
**File:** `protocol/shannon/error_classification.go:295-302` (unknown payload section)

**Why minor:** Conservative penalty until we can classify the error better. These should trigger logging for investigation.

---

## Reputation Signal Severity Scale

| Signal Type        | Score Impact | Use Case                                         |
|--------------------|--------------|--------------------------------------------------|
| Success            | +1           | Successful request                               |
| Recovery Success   | +15          | Successful health check on low-scoring endpoint  |
| Slow Response      | -1           | Successful but slow (> 2s)                       |
| Very Slow Response | -3           | Successful but very slow (> 5s)                  |
| Minor Error        | -3           | Client errors, transient issues, unknown errors  |
| Major Error        | -10          | Timeouts, connection failures, transport errors  |
| Critical Error     | -25          | Service errors (5xx), protocol violations        |
| Fatal Error        | -50          | Service misconfiguration (cannot serve requests) |

**Score thresholds (configurable per service):**
- **Tier 1:** 80+ (best endpoints)
- **Tier 2:** 50-79 (acceptable endpoints)
- **Tier 3:** 30-49 (degraded endpoints)
- **Minimum:** 30 (below this = filtered out of regular relays)
- **Probation:** 30-50 (eligible for recovery traffic)

---

## Implementation Files

### Current Implementation
- `protocol/shannon/error_classification.go` - Direct error → signal mapping (implements all 11 categories)
- `protocol/shannon/reputation.go` - Reputation filtering logic (uses signals from error_classification.go)
- `proto/path/protocol/shannon.proto` - Error type definitions only (no sanction types)

---

## Decision Matrix

**When you see an error, ask:**

1. **Can the supplier fix it?** → Yes = Penalize
2. **Is it PATH's fault?** → Yes = Don't penalize (or minor only)
3. **Is it the backend chain's fault?** → Supplier's responsibility = Penalize
4. **Is it a network issue?** → Conservative approach = Penalize (we can't know if it's temporary or supplier's infrastructure)
5. **Is it a protocol violation?** → Yes = Heavy penalty
6. **Is it normal HTTP behavior?** → Minor penalty only (e.g., server closing idle connection)

**When in doubt: Apply minor penalty (-3) and log for investigation.**

---

## Migration Summary

**Sanction system has been completely removed:**
- Old system used `ShannonSanctionType` enum (SESSION, PERMANENT, DO_NOT_SANCTION) as intermediate layer
- Old: `Error → (ErrorType, SanctionType) → Signal` (two-step classification)
- **New:** `Error → ErrorType → Signal` (direct mapping based on fault responsibility)

**Changes implemented:**
- Deleted `protocol/shannon/sanctions.go` (350 lines)
- Removed `ShannonSanctionType` enum from shannon.proto
- Created `error_classification.go` with direct error → signal mapping
- Updated all call sites to use new classification functions
- All error types now map directly to one of 5 signal types: Fatal (-50), Critical (-25), Major (-10), Minor (-3), Success (+1)
