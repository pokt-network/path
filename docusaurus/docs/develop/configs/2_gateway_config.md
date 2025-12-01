---
sidebar_position: 2
title: Gateway Config (`.config.yaml`)
description: PATH Gateway & Service Configurations
---

_tl;dr Configurations for Gateway operation & services support._

- [Example Configuration](#example-configuration)
- [Config File Location (Local Development)](#config-file-location-local-development)
- [Config File Validation](#config-file-validation)
- [`shannon_config` (required)](#shannon_config-required)
- [`hydrator_config` (optional)](#hydrator_config-optional)
- [`router_config` (optional)](#router_config-optional)
- [`logger_config` (optional)](#logger_config-optional)
- [`data_reporter_config` (optional)](#data_reporter_config-optional)

## Example Configuration

All configuration for the `PATH` gateway are defined in a single YAML file named `.config.yaml`.

Exactly one `shannon_config` **MUST** be provided. This field determines the protocol that the gateway will use.

<details>

<summary>Example **Shannon** Config (click to expand)</summary>

```yaml
# (Required) Shannon Protocol Configuration
shannon_config:
  full_node_config:
    rpc_url: https://shannon-grove-rpc.mainnet.poktroll.com
    grpc_config:
      host_port: shannon-grove-grpc.mainnet.poktroll.com:443
    lazy_mode: false

  gateway_config:
    gateway_mode: "centralized"
    gateway_address: pokt1up7zlytnmvlsuxzpzvlrta95347w322adsxslw
    gateway_private_key_hex: 40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388
    owned_apps_private_keys_hex:
      - 40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388
    service_fallback:
      - service_id: eth
        send_all_traffic: false
        fallback_urls:
          - "https://eth.rpc.grove.city/v1/1a2b3c4d"
          - "https://eth.rpc.grove.city/v1/5e6f7a8b"

# (Optional) Logger Configuration
logger_config:
  level: "info" # Valid values: debug, info, warn, error
```

</details>

## Config File Location (Local Development)

In local development mode, the config file must be located at:

```bash
./local/path/.config.yaml
```

## Config File Validation

:::tip VSCode Validation

If you are using VSCode, we recommend using the [YAML Language Support](https://marketplace.visualstudio.com/items?itemName=redhat.vscode-yaml) extension for in-editor validation of the `.config.yaml` file. Enable it by ensuring the following annotation is present at the top of your config file:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/pokt-network/path/refs/heads/main/config/config.schema.yaml
```

:::

## Protocol Configuration <!-- omit in toc -->

The config file **MUST contain EXACTLY one** top-level `shannon_config` section.

## `shannon_config` (required)

Configuration for the Shannon protocol gateway.

```yaml
shannon_config:
  full_node_config:
    lazy_mode: false
    rpc_url: "https://shannon-grove-rpc.mainnet.poktroll.com"
    grpc_config: # Required
      host_port: "shannon-grove-grpc.mainnet.poktroll.com:443" # Required: gRPC host and port

      # Optional backoff and keepalive configs...
      insecure: false # Optional: whether to use insecure connection
      backoff_base_delay: "1s" # Optional: initial backoff delay duration
      backoff_max_delay: "120s" # Optional: maximum backoff delay duration
      min_connect_timeout: "20s" # Optional: minimum timeout for connection attempts
      keep_alive_time: "20s" # Optional: frequency of keepalive pings
      keep_alive_timeout: "20s" # Optional: timeout for keepalive pings

  gateway_config: # Required
    gateway_mode: "centralized" # Required: centralized, delegated, or permissionless
    gateway_address: "pokt1up7zlytnmvlsuxzpzvlrta95347w322adsxslw" # Required: Bech32 address
    gateway_private_key_hex: "<64-char-hex>" # Required: Gateway private key
    owned_apps_private_keys_hex: # Required for centralized mode only
      - "<64-char-hex>" # Application private key
      - "<64-char-hex>" # Additional application private keys...
    service_fallback: # Optional: Fallback endpoints
      - service_id: eth
        send_all_traffic: false
        fallback_urls:
          - "https://eth.rpc.grove.city/v1/1a2b3c4d"
          - "https://eth.rpc.grove.city/v1/5e6f7a8b"
      - service_id: polygon
        send_all_traffic: false
        fallback_urls:
          - "https://polygon.rpc.grove.city/v1/9x8y7z6w"
```

**`full_node_config`**

| Field         | Type    | Required | Default | Description                                                     |
| ------------- | ------- | -------- | ------- | --------------------------------------------------------------- |
| `rpc_url`     | string  | Yes      | -       | URL of the Shannon RPC endpoint                                 |
| `grpc_config` | object  | Yes      | -       | gRPC connection configuration                                   |
| `lazy_mode`   | boolean | No       | false   | If true, disables caching of onchain data (e.g. apps, sessions) |

**`full_node_config.grpc_config`**

| Field                 | Type    | Required | Default | Description                             |
| --------------------- | ------- | -------- | ------- | --------------------------------------- |
| `host_port`           | string  | Yes      | -       | Host and port for gRPC connections      |
| `insecure`            | boolean | No       | false   | Whether to use insecure connection      |
| `backoff_base_delay`  | string  | No       | "1s"    | Initial backoff delay duration          |
| `backoff_max_delay`   | string  | No       | "120s"  | Maximum backoff delay duration          |
| `min_connect_timeout` | string  | No       | "20s"   | Minimum timeout for connection attempts |
| `keep_alive_time`     | string  | No       | "20s"   | Frequency of keepalive pings            |
| `keep_alive_timeout`  | string  | No       | "20s"   | Timeout for keepalive pings             |

**`gateway_config`**

| Field                         | Type     | Required                 | Default | Description                                                           |
| ----------------------------- | -------- | ------------------------ | ------- | --------------------------------------------------------------------- |
| `gateway_mode`                | string   | Yes                      | -       | Mode of operation: `centralized`, `delegated`, or `permissionless`    |
| `gateway_address`             | string   | Yes                      | -       | Bech32-formatted gateway address (starts with `pokt1`)                |
| `gateway_private_key_hex`     | string   | Yes                      | -       | 64-character hex-encoded `secp256k1` gateway private key              |
| `owned_apps_private_keys_hex` | string[] | Only in centralized mode | -       | List of 64-character hex-encoded `secp256k1` application private keys |
| `service_fallback`            | array    | No                       | -       | Array of service fallback configurations (see below for details)      |
| `sanction_config`             | object   | No                       | -       | Configuration for endpoint sanction system (see below for details)    |

**`service_fallback` (optional)**

Configures fallback endpoints that the gateway will use when all protocol endpoints for a service become unavailable (e.g., sanctioned or offline). This provides resilience by allowing the gateway to continue serving requests even when protocol endpoints are temporarily unavailable.

```yaml
service_fallback:
  - service_id: eth
    send_all_traffic: false
    fallback_urls:
      - "https://eth.rpc.grove.city/v1/1a2b3c4d"
      - "https://eth.rpc.grove.city/v1/5e6f7a8b"
  - service_id: poly
    send_all_traffic: false
    fallback_urls:
      - "https://poly.rpc.grove.city/v1/9x8y7z6w"
```

<!--
TODO_DOCUMENT(@adshmh): Update this section to clarify the request distribution when multiple fallback URLs are specified.
- This requires the distribution to be explicitly defined/implemented first.
-->

| Field              | Type     | Required | Default | Description                                                                                                                                                                   |
| ------------------ | -------- | -------- | ------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `service_id`       | string   | Yes      | -       | The service ID for this fallback configuration. **Must be a valid onchain Shannon service ID.** Each service_id must be unique within the service_fallback array.             |
| `fallback_urls`    | string[] | Yes      | -       | Array of fallback endpoint URLs for the specified service ID. Each URL must be a valid HTTP/HTTPS endpoint.                                                                   |
| `send_all_traffic` | boolean  | No       | false   | Whether to send all traffic to fallback endpoints for this service, regardless of protocol endpoint health. When true, bypasses protocol endpoints entirely for this service. |

**Key Features:**
- **Automatic failover**: Gateway automatically switches to fallback endpoints when protocol endpoints are unavailable
- **Send All
- **Protocol bypass**: Fallback endpoints bypass protocol-level validation and are sent directly to the configured URLs
- **Service-specific**: Each service ID can have its own set of fallback endpoints

**`sanction_config` (optional)**

Configures the endpoint sanction system parameters. The sanction system temporarily excludes misbehaving endpoints from selection. When an endpoint returns errors or behaves poorly, it receives a "session sanction" that prevents it from being selected for requests until the sanction expires.

```yaml
gateway_config:
  # ... other fields ...
  sanction_config:
    session_sanction_duration: "30m"  # How long session sanctions last
    cache_cleanup_interval: "5m"       # How often to purge expired sanctions
```

| Field                       | Type   | Required | Default | Description                                                                                              |
| --------------------------- | ------ | -------- | ------- | -------------------------------------------------------------------------------------------------------- |
| `session_sanction_duration` | string | No       | "1h"    | Duration that session-based sanctions remain active. Format: Go duration string (e.g., "30m", "1h", "2h") |
| `cache_cleanup_interval`    | string | No       | "10m"   | Interval for purging expired sanction entries from the cache. Format: Go duration string                 |

**Key Features:**
- **Automatic expiration**: Session sanctions automatically expire after the configured duration
- **Configurable timing**: Operators can tune sanction duration based on their network conditions
- **Memory efficient**: Expired sanctions are periodically cleaned up to prevent memory bloat

**Use Cases:**
- **Shorter durations** (e.g., `15m`): Use when endpoints frequently have temporary issues and you want faster recovery
- **Longer durations** (e.g., `2h`): Use when you want to more aggressively exclude problematic endpoints
- **Default** (`1h`): Balanced approach suitable for most deployments

---

## `hydrator_config` (optional)

:::info

For a full list of supported QoS service implementations, refer to the [QoS Documentation](../../learn/qos/1_supported_services.md).

:::

Configures the QoS hydrator. By default, all services configured in the `shannon_config` sections will have QoS checks run against them.

| Field                        | Type          | Required | Default   | Description                                                                                                                                                 |
| ---------------------------- | ------------- | -------- | --------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `run_interval_ms`            | string        | No       | "10000ms" | Interval at which the hydrator will run QoS checks                                                                                                          |
| `max_endpoint_check_workers` | integer       | No       | 100       | Maximum number of workers to run concurrent QoS checks against a service's endpoints                                                                        |
| `qos_disabled_service_ids`   | array[string] | No       | -         | List of service IDs to exclude from QoS checks. Will throw an error on startup if a service ID is provided that the PATH instance is not configured to use. |

### Manually Disable QoS Checks for a Service <!-- omit in toc -->

To manually disable QoS checks for a specific service, the `qos_disabled_service_ids` field may be specified in the `.config.yaml` file.

For example, to disable QoS checks for the Ethereum service on a Shannon PATH instance, the following configuration would be added to the `.config.yaml` file:

```yaml
hydrator_config:
  qos_disabled_service_ids:
    - "eth"
```

---

## `router_config` (optional)

**Enables configuring how incoming requests are handled.**

In particular, allows specifying server parameters for how the gateway handles incoming requests.

| Field                      | Type    | Required | Default           | Description                                     |
| -------------------------- | ------- | -------- | ----------------- | ----------------------------------------------- |
| `port`                     | integer | No       | 3069              | Port number on which the gateway server listens |
| `max_request_header_bytes` | integer | No       | 1MB               | Maximum request header size in bytes            |
| `read_timeout`             | string  | No       | "5000ms" (5s)     | Time limit for reading request data             |
| `write_timeout`            | string  | No       | "10000ms" (10s)   | Time limit for writing response data            |
| `idle_timeout`             | string  | No       | "120000ms" (120s) | Time limit for closing idle connections         |

---

## `logger_config` (optional)

Controls the logging behavior of the PATH gateway.

```yaml
logger_config:
  level: "warn"
```

| Field   | Type   | Required | Default | Description                                                           |
| ------- | ------ | -------- | ------- | --------------------------------------------------------------------- |
| `level` | string | No       | "info"  | Minimum log level. Valid values are: "debug", "info", "warn", "error" |

---

## `data_reporter_config` (optional)

Configures HTTP-based data reporting to external services like BigQuery via data pipelines (e.g., Fluentd with HTTP input and BigQuery output plugins).

```yaml
data_reporter_config:
  target_url: "https://fluentd-service.example.com/http-input"
  post_timeout_ms: 5000
```

| Field             | Type    | Required | Default | Description                                                                                       |
| ----------------- | ------- | -------- | ------- | ------------------------------------------------------------------------------------------------- |
| `target_url`      | string  | Yes      | -       | HTTP endpoint URL where data will be reported (must start with http:// or https://)               |
| `post_timeout_ms` | integer | No       | 10000   | Timeout in milliseconds for HTTP POST operations. If zero or negative, default of 10000ms is used |

:::info
Currently, only JSON-accepting data pipelines are supported as of PR #215.
:::
