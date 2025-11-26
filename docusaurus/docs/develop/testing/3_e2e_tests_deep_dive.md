---
sidebar_position: 3
title: E2E Tests Deep Dive (20+ min)
description: Deep dive into End-to-End Tests for PATH
---

:::tip Quickstart

⚠️ Make sure to visit the [E2E Tests Quickstart](1_e2e_tests_quickstart.md) to get started quickly.

:::

## Introduction

**The E2E tests verify:**

- Correct request routing
- Service responses (data + latency)
- System reliability under load
- Success metrics for Shannon protocol

**We use the [Vegeta library](https://github.com/tsenart/vegeta) for HTTP load testing:**

- Can generate thousands of requests/sec
- Collects detailed metrics including latency percentiles (p50, p95, p99)
- Supports custom configurations and attack parameters
- Validates JSON-RPC responses and success rates

<div align="center">
![Vegeta](../../../static/img/9000.png)
</div>

## E2E Test Mode

| Mode                                 | Make Targets                      | Purpose                                                                       |
| ------------------------------------ | --------------------------------- | ----------------------------------------------------------------------------- |
| **HTTP Test All Services**           | `make e2e_test_all`               | HTTP-only end-to-end testing that starts PATH in an isolated Docker container |
| **HTTP Test Specific Services**      | `make e2e_test eth,xrplevm`       | HTTP-only end-to-end testing that starts PATH in an isolated Docker container |
| **Websocket Test All Services**      | `make e2e_test_websocket_all`     | Websocket-only testing for all Websocket-compatible services                  |
| **Websocket Test Specific Services** | `make e2e_test_websocket xrplevm` | Websocket-only testing for specified Websocket-compatible services            |

What the above make target does:

1. Spins up PATH in a Docker container using Dockertest
2. Configures the gateway according to the `./e2e/config/.shannon.config.yaml` file
3. Runs tests according to the `./e2e/config/.e2e_load_test.config.yaml` file
4. Tears down container after the tests are done

## E2E Test Config Files

| Configuration File                        | Custom Config Required? |             Default available?              | Description                            | Command to create or customize                                                     |
| ----------------------------------------- | :---------------------: | :-----------------------------------------: | :------------------------------------- | :--------------------------------------------------------------------------------- |
| `./e2e/config/.shannon.config.yaml`       |            ✅            |                      ❌                      | Gateway service configuration for PATH | `make config_copy_path_local_config_shannon_e2e` OR `make config_shannon_populate` |
| `./e2e/config/.e2e_load_test.config.yaml` |            ❌            | `e2e/config/e2e_load_test.config.tmpl.yaml` | Custom configuration for E2E tests     | `make config_prepare_shannon_e2e`                                                  |

## Schema and Validation

The configuration uses a formal YAML schema with validation:

**Schema Location**: `./e2e/config/e2e_load_test.config.schema.yaml`

:::tip VSCode Validation

If you are using VSCode, we recommend using the [YAML Language Support](https://marketplace.visualstudio.com/items?itemName=redhat.vscode-yaml) extension for in-editor validation of the `.config.yaml` file.

Enable it by ensuring the following annotation is present at the top of your config file:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/pokt-network/path/refs/heads/main/e2e/config/e2e_load_test.config.schema.yaml
```

:::

## Supported Services in E2E Tests

To see the list of supported services for the tests, see the `test_cases` array in the [E2E Test Config](https://github.com/pokt-network/path/blob/main/e2e/config/e2e_load_test.config.default.yaml) file.

## Environment Variables

These environment variables are set by the test make targets, but if you wish to set them manually, see the table below:

<details>
<summary>Env Vars Table</summary>
| Variable         | Description                                                                                       | Values                              | Required |
| ---------------- | ------------------------------------------------------------------------------------------------- | ----------------------------------- | -------- |
| TEST_MODE        | Determines the test execution mode                                                                | `e2e`                               | Yes      |
| TEST_PROTOCOL    | Specifies which protocol to test                                                                  | `shannon`                           | Yes      |
| TEST_SERVICE_IDS | Specifies which service IDs to test. If not set, all service IDs for the protocol will be tested. | Comma-separated list of service IDs | No       |
| TEST_WEBSOCKETS  | Run only Websocket tests, skipping HTTP tests entirely                                            | `true` or `false`                   | No       |
</details>

## Extending/Updating/Adding EVM E2E Tests

To add new services or methods to the E2E tests, you will need to open a new PR to PATH's `main` branch.

1. **Add new service definitions** to the `services` array in the `e2e/config/services_shannon.yaml` configuration file
2. **Configure service parameters** including contract addresses, start blocks, and transaction hashes for archival tests

**Example new service configuration:**

```yaml
services:
  - name: "New Chain E2E Test"
    protocol: "shannon"
    service_id: "newchain"
    archival: true
    service_params:
      contract_address: "0x..."
      contract_start_block: 1000000
      transaction_hash: "0x..."
      call_data: "0x18160ddd"
```

## Test Metrics and Validation

:::warning Threshold Validation

Tests will **fail** if any configured thresholds are exceeded, ensuring consistent service quality and performance.

:::

The E2E tests collect and validate comprehensive metrics across multiple dimensions:

| **Category**              | **Metrics Collected**                                                                                                                                        |
| ------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **HTTP Metrics**          | - Success rates (HTTP 200) <br/> - Status code distribution <br/> - HTTP error categorization                                                                |
| **Latency Metrics**       | - P50, P95, P99 latency percentiles <br/> - Average latency <br/> - Per-method latency analysis                                                              |
| **JSON-RPC Validation**   | - Response unmarshaling success <br/> - JSON-RPC error field validation <br/> - Result field validation <br/> - Protocol-specific validation                 |
| **Service-Level Metrics** | - Per-service success aggregation <br/> - Cross-method performance comparison <br/> - Service reliability scoring <br/> - Error categorization and reporting |

## Websocket Testing

PATH E2E tests support Websocket testing for compatible services. Currently, XRPLEVM services are configured with Websocket support.

### Websocket Test Features

- **Transport-Agnostic Validation**: Uses the same JSON-RPC validation logic as HTTP tests
- **Real-time Connection**: Establishes persistent Websocket connections to test real-time communication
- **EVM JSON-RPC Support**: Tests all standard EVM JSON-RPC methods over Websocket
- **Separate from HTTP**: Websocket tests run independently from HTTP tests

### Websocket Test Modes

| Mode                       | Command                           | Description                                                |
| -------------------------- | --------------------------------- | ---------------------------------------------------------- |
| **HTTP Only**              | `make e2e_test xrplevm`           | Runs only HTTP tests (default behavior)                    |
| **Websocket Only**         | `make e2e_test_websocket xrplevm` | Runs only Websocket tests, skipping HTTP tests entirely    |
| **All Websocket Services** | `make e2e_test_websocket_all`     | Runs Websocket tests for all Websocket-compatible services |

### Service Configuration

To enable Websocket testing for a service, add `websockets: true` to the service configuration in `services_shannon.yaml`:

```yaml
- name: "Shannon - xrplevm (XRPL EVM MainNet) Test"
  service_id: "xrplevm" 
  service_type: "cosmos_sdk"
  websockets: true  # Enable Websocket testing
  supported_apis: ["json_rpc", "rest", "comet_bft", "websocket"]
  # ... rest of configuration
```

## Reviewing PATH Logs

In E2E test mode, logs may be written to `./path_log_e2e_test_{timestamp}.txt`.

**In order to enable this, set the log_to_file field:**

```bash
yq eval '.e2e_load_test_config.e2e_config.docker_config.log_to_file = true' -i ./e2e/config/.e2e_load_test.config.yaml
```

You should see the following log line at the bottom of the test summary:

```bash
===== 👀 LOGS 👀 =====

 ✍️ PATH container output logged to /tmp/path_log_e2e_test_1745527319.txt ✍️

===== 👀 LOGS 👀 =====

```

:::tip 🌿 Grove Employees Only 🌿

Review the [Anvil Shannon Beta TestNet Debugging Playbook](https://www.notion.so/buildwithgrove/Playbook-Debugging-Anvil-E2E-on-Beta-TestNet-177a36edfff6809c9f24e865ec5adbf8?pvs=4) if you believe the Anvil Supplier is broken.

:::
