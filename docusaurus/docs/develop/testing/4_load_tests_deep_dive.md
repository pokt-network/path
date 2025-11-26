---
sidebar_position: 4
title: Load Tests Deep Dive (20+ min)
description: Deep dive into Load Tests for PATH
---

:::tip Quickstart

⚠️ Make sure to visit the [Load Tests Quickstart](2_load_tests_quickstart.md) to get started quickly.

:::

## Overview

**Goal of this document**: Load testing to verify PATH works and scales under load.

**The load tests verify:**

- Service responses under load (data + latency)
- System reliability and performance
- Success metrics for Shannon protocol
- Scalability characteristics

**We use the [Vegeta library](https://github.com/tsenart/vegeta) for HTTP load testing:**

- Can generate thousands of requests/sec
- Collects detailed metrics including latency percentiles (p50, p95, p99)
- Supports custom configurations and attack parameters
- Validates JSON-RPC responses and success rates

<div align="center">
![Vegeta](../../../static/img/9000.png)
</div>

## Load Test Modes

PATH load tests support multiple modes of operation:

| Mode               | Purpose                                               | How it Works                                                                                                                                                                    | Use Cases                                                                                                           |
| ------------------ | ----------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| **Local PATH**     | HTTP performance testing against local PATH instances | 1. Requires completed [Getting Started](../path/1_getting_started.md) and [Pocket Cheat Sheet](../path/2_cheatsheet_pocket.md) setup <br/> 2. Tests against local PATH instance | - Local development testing <br/> - Feature validation <br/> - Development iteration                                |
| **Grove Portal**   | HTTP performance testing against Grove Portal         | 1. Sends HTTP requests to Grove Portal gateway URL <br/> 2. Requires Grove Portal credentials or pre-configured files                                                           | - Testing production gateway <br/> - Production performance validation <br/> - Benchmarking                         |
| **Websocket Only** | Websocket-specific performance testing                | 1. Tests only Websocket connections, skipping HTTP tests entirely <br/> 2. Available for Websocket-compatible services (e.g., XRPLEVM)                                          | - Websocket-specific performance validation <br/> - Real-time connection testing <br/> - Websocket scaling analysis |

### Local PATH Mode

For local PATH load testing, you need:

1. **Completed Setup**: Follow the [Getting Started](../path/1_getting_started.md) and [Pocket Cheat Sheet](../path/2_cheatsheet_pocket.md) guides
2. **Local PATH Instance**: Your local PATH instance should be running and configured
3. **Default Configuration**: The system automatically targets your local PATH instance

### Grove Portal Mode

You will need one of the following:

1. **Grove Employee Pre-configured Files**

   - Download from 1Password links above
   - Copy to `e2e/config/.grove.e2e_load_test.config.yaml`

2. **Custom Portal Access**
   - `gateway_url_override`: `https://rpc.grove.city/v1`
   - Get credentials from the [Grove Portal](https://www.portal.grove.city)
   - Use `make config_copy_e2e_load_test` to set up

### Websocket Mode

For Websocket load testing, you can run tests exclusively on Websocket-compatible services:

1. **Prerequisites**: Same as Local PATH or Grove Portal mode  
2. **Supported Services**: Currently XRPLEVM and XRPLEVM-testnet services
3. **Test Transport**: Uses Websocket connections instead of HTTP
4. **Commands**:
   - `make load_test_websocket xrplevm` - Test specific Websocket services
   - `make load_test_websocket_all` - Test all Websocket-compatible services

## Load Test Configuration

**Configuration files used:**

| Configuration File                              | Local PATH | Grove Portal |             Default Available?              |
| ----------------------------------------------- | :--------: | :----------: | :-----------------------------------------: |
| `./e2e/config/.grove.e2e_load_test.config.yaml` |     ❌      |      ✅       |                      ❌                      |
| `./e2e/config/.e2e_load_test.config.yaml`       |     ✅      |      ✅       | `e2e/config/e2e_load_test.config.tmpl.yaml` |

:::tip Populate Configs

You can use the following command to copy example configs and follow the instructions in your CLI:

- `make config_copy_e2e_load_test`

:::

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

## Supported Services in Load Tests

**All currently supported Grove Portal services are supported in the load tests.**

:::tip

To see the list of supported services for the tests, see the `test_cases` array in the [Load Test Config](https://github.com/pokt-network/path/blob/main/e2e/config/e2e_load_test.config.default.yaml) file.

:::

## Test Metrics and Validation

The load tests collect and validate comprehensive metrics across multiple dimensions:

| **Category**              | **Metrics Collected**                                                                                                                                        |
| ------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| **HTTP Metrics**          | - Success rates (HTTP 200) <br/> - Status code distribution <br/> - HTTP error categorization                                                                |
| **Latency Metrics**       | - P50, P95, P99 latency percentiles <br/> - Average latency <br/> - Per-method latency analysis                                                              |
| **JSON-RPC Validation**   | - Response unmarshaling success <br/> - JSON-RPC error field validation <br/> - Result field validation <br/> - Protocol-specific validation                 |
| **Service-Level Metrics** | - Per-service success aggregation <br/> - Cross-method performance comparison <br/> - Service reliability scoring <br/> - Error categorization and reporting |

:::important Threshold Validation

Tests will **fail** if any configured thresholds are exceeded, ensuring consistent service quality and performance.

:::

## Reviewing PATH Logs

When running against local PATH instances, logs may be written to `./path_log_load_test_{timestamp}.txt`.

**In order to enable this, set the log_to_file field:**

```bash
yq eval '.e2e_load_test_config.load_test_config.log_to_file = true' -i ./e2e/config/.e2e_load_test.config.yaml
```

You should see the following log line at the bottom of the test summary:

```bash
===== 👀 LOGS 👀 =====

 ✍️ PATH container output logged to /tmp/path_log_load_test_1745527319.txt ✍️

===== 👀 LOGS 👀 =====
```
