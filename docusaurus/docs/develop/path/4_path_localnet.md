---
sidebar_position: 4
title: PATH Localnet
description: Development environment for the full PATH, GUARD & WATCH stack
---

- [Overview](#overview)
- [Quick Start](#quick-start)
  - [Prerequisites](#prerequisites)
    - [Install Docker](#install-docker)
    - [Prepare Configuration Files](#prepare-configuration-files)
  - [1. Download the shannon `.config.yaml`](#1-download-the-shannon-configyaml)
  - [2. Comment out unused config sections](#2-comment-out-unused-config-sections)
  - [3. Download the guard `.values.yaml`](#3-download-the-guard-valuesyaml)
  - [Start PATH Localnet](#start-path-localnet)
  - [Verify the Setup](#verify-the-setup)
    - [Example Relays](#example-relays)
  - [3. Access Development Tools](#3-access-development-tools)
- [Why PATH Localnet?](#why-path-localnet)
- [Architecture](#architecture)
  - [Components](#components)
- [Make Targets](#make-targets)
  - [Core Commands](#core-commands)
    - [`make path_up`](#make-path_up)
    - [`make path_up_local_helm`](#make-path_up_local_helm)
    - [`make path_down`](#make-path_down)
    - [`make build_and_push_localnet_image`](#make-build_and_push_localnet_image)
  - [Debugging Commands](#debugging-commands)
    - [`make localnet_k9s`](#make-localnet_k9s)
    - [`make localnet_exec`](#make-localnet_exec)
- [Container Environment](#container-environment)
  - [File Mounts](#file-mounts)
  - [Configuration Validation](#configuration-validation)
- [Development Workflow](#development-workflow)
  - [Hot Reloading](#hot-reloading)
  - [Viewing Logs](#viewing-logs)

## Overview

PATH Localnet is a containerized development environment that enables you to run the complete PATH stack locally: PATH (API Gateway), GUARD (Envoy Gateway), and WATCH (Observability).

It provides a fully isolated, reproducible development environment that requires only Docker on your host machine.

## Quick Start

### Prerequisites

Run the following command to install required tools:

```bash
make install_tools
```

#### Install Docker

```bash
docker --version  # Should output Docker version
docker ps         # Should list running containers (or be empty)
```

#### Prepare Configuration Files

For external contributors, you can generate starter configs:

```bash
make config_shannon_populate    # Generate .config.yaml
make configs_copy_values_yaml   # Copy default .values.yaml
```

- `./local/path/.config.yaml` - PATH gateway configuration
- `./local/path/.values.yaml` - Helm values override file

:::note 🌿 Are you a Grove employee 🌿?

<details>

<summary>Download your configs here</summary>

### 1. Download the shannon `.config.yaml`

For **MainNet**:

```bash
op item get 4ifsnkuifvaggwgptns6xyglsa --fields notesPlain --format json | jq -r '.value' > ./local/path/.config.yaml
```

For **Beta TestNet**:

```bash
op item get 3treknedz5q47rgwdbreluwffu --fields notesPlain --format json | jq -r '.value' > ./local/path/.config.yaml
```

### 2. Comment out unused config sections

In `./local/path/.config.yaml`:

1. Comment out the `owned_apps_private_keys_hex` you're not using for testing.
2. Comment out the `data_reporter_config` section:

   ```bash
   sed -i '' \
     -e 's/^[[:space:]]*data_reporter_config:/# data_reporter_config:/' \
     -e 's/^[[:space:]]*"target_url":/#   "target_url":/' \
     local/path/.config.yaml
   ```

### 3. Download the guard `.values.yaml`

```bash
op item get fkltz2wb7fegpumntqyo3w5qau --fields notesPlain --format json | jq -r '.value' > ./local/path/.values.yaml
```

</details>

:::

### Start PATH Localnet

```bash
# Start with remote Helm charts (recommended for most users)
make path_up

# Or start with local Helm charts (for Helm chart development)
make path_up_local_helm
```

The startup process will:

1. Validate your configuration files against the schema
2. Create a Kind Kubernetes cluster
3. Deploy PATH, GUARD, and WATCH using Helm
4. Start Tilt for orchestration and hot reloading

:::info
First-time startup may take 3-5 minutes as Docker pulls the required images.
:::

### Verify the Setup

Once started, you'll see:

```bash
🌿 PATH Localnet started successfully.
  🚀 Send relay requests to: http://localhost:3070/v1

🛠️  Development tools:
  🔧 Open container shell: make localnet_exec
  🔍 Launch k9s for debugging: make localnet_k9s
```

Test with a simple request:

```bash
curl http://localhost:3070/healthz
```

#### Example Relays

For more example relay requests, see [Example Relays](3_example_requests.md).

### 3. Access Development Tools

- **Tilt UI**: http://localhost:10350 - Monitor services, view logs, trigger rebuilds
- **Grafana**: http://localhost:3003 - View metrics and dashboards
- **PATH API**: http://localhost:3070 - Send relay requests

![Tilt Dashboard](../../../static/img/path-in-tilt.png)

## Why PATH Localnet?

The PATH Localnet development container exists to:

- **Minimize Host Dependencies**: Only Docker is required on your machine - no need to install Tilt, Helm, Kind, kubectl, or other tools locally
- **Ensure Consistency**: All developers work with the same tool versions and configurations
- **Enable Full Stack Development**: Run PATH (API Gateway), GUARD (Envoy Gateway), and WATCH (Observability) together
- **Support Hot Reloading**: Make code changes and see them reflected immediately without rebuilding containers
- **Simplify Onboarding**: New developers can get started in minutes with a single command

## Architecture

The PATH Localnet runs as a Docker container that internally manages a complete Kubernetes environment:

```bash
┌─────────────────────────────────────────────────────────────┐
│                    Host Machine (Your Computer)             │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐    │
│  │           Docker Desktop / Docker Engine            │    │
│  └─────────────────────────────────────────────────────┘    │
│                            │                                │
│  ┌─────────────────────────────────────────────────────┐    │
│  │         path-localnet Container (Docker-in-Docker)  │    │
│  │                                                     │    │
│  │  ┌────────────────────────────────────────────┐     │    │
│  │  │              Kind Kubernetes Cluster       │     │    │
│  │  │                                            │     │    │
│  │  │  ┌──────────┐  ┌──────────┐  ┌──────────┐  │     │    │
│  │  │  │   PATH   │  │  GUARD   │  │  WATCH   │  │     │    │
│  │  │  │   Pod    │  │  (Envoy) │  │ (Grafana)│  │     │    │
│  │  │  └──────────┘  └──────────┘  └──────────┘  │     │    │
│  │  └────────────────────────────────────────────┘     │    │
│  │                                                     │    │
│  │  ┌────────────────────────────────────────────┐     │    │
│  │  │              Tilt (Orchestrator)           │     │    │
│  │  └────────────────────────────────────────────┘     │    │
│  └─────────────────────────────────────────────────────┘    │
│                                                             │
│  Exposed Ports:                                             │
│  • 3070  → PATH API Gateway                                 │
│  • 10350 → Tilt UI                                          │
│  • 3003  → Grafana Dashboard                                │
└─────────────────────────────────────────────────────────────┘
```

### Components

- **PATH**: The API Gateway that handles relay requests
- **GUARD**: Envoy Gateway providing authentication, routing, and defense
- **WATCH**: Observability stack with Grafana, Prometheus, and metrics collection
- **Tilt**: Development orchestrator that manages hot reloading and service lifecycle
- **Kind**: Kubernetes-in-Docker providing the cluster environment

## Make Targets

The PATH Localnet provides several make targets for managing your development environment:

### Core Commands

#### `make path_up`

Starts the PATH Localnet environment using remote Helm charts from the official repository.

```bash
make path_up
```

This is the recommended way to start for most development tasks. The container will:

- Pull the latest `ghcr.io/buildwithgrove/path-localnet-env` image
- Mount your local PATH repository for hot reloading
- Use Helm charts from https://buildwithgrove.github.io/helm-charts/

#### `make path_up_local_helm`

Starts the PATH Localnet with local Helm charts, useful when developing Helm chart changes.

```bash
make path_up_local_helm
```

You'll be prompted for the path to your local `helm-charts` repository. The default is `../helm-charts`.

#### `make path_down`

Stops and removes the PATH Localnet container.

```bash
make path_down
```

This cleanly shuts down all services by stopping the localnet Docker container.

:::note 🌿 Grove employees only 🌿?

#### `make build_and_push_localnet_image`

If changes have been made to the localnet Dockerfile at `./local/Dockerfile.dev`, you can build and push the `path-localnet-env` image to the Grove GitHub Container Registry (GHCR) with the following command:

```bash
make build_and_push_localnet_image
```

This will build the image and push it to the GHCR repository `ghcr.io/buildwithgrove/path-localnet-env`.

:::

### Debugging Commands

#### `make localnet_k9s`

Launches [k9s](https://k9scli.io/), a terminal-based Kubernetes UI, inside the container.

```bash
make localnet_k9s
```

k9s provides an interactive way to:

- Navigate Kubernetes resources
- View and follow logs
- Execute into pods
- Edit resources
- Monitor resource usage

![k9s Dashboard](../../../static/img/k9s-localnet.png)
_k9s running inside the PATH localnet Docker container_

:::tip k9s Quick Commands

- `:pods` - List all pods
- `:svc` - List all services
- `l` - View logs for selected resource
- `d` - Describe selected resource
- `s` - Shell into selected pod
- `ctrl+a` - Show all namespaces
- `?` - Show help menu

:::

#### `make localnet_exec`

Opens an interactive shell inside the running PATH Localnet container.

```bash
make localnet_exec
```

Useful for:

- Running kubectl commands directly
- Inspecting logs and configurations
- Debugging networking issues
- Managing the Kind cluster

Example session:

```bash
$ make localnet_exec
root@path-localnet:/app# kubectl get pods
NAME                           READY   STATUS    RESTARTS   AGE
path-5f7b9c4d6f-abc12         1/1     Running   0          5m
envoy-gateway-xyz789          1/1     Running   0          5m
grafana-6d8f9c7b5-def45      1/1     Running   0          5m

root@path-localnet:/app# kubectl logs path-5f7b9c4d6f-abc12
```

## Container Environment

The PATH Localnet image includes all necessary development tools, meaning you can run PATH, GUARD, and WATCH locally without any additional dependencies.

- Image: `ghcr.io/buildwithgrove/path-localnet-env`
- [GHCR Repository](https://github.com/orgs/buildwithgrove/packages/container/package/path-localnet-env)

### File Mounts

The container mounts your local PATH repository at `/app`, enabling:

- Hot reloading of Go code changes
- Configuration file updates
- Access to test data and scripts

### Configuration Validation

On startup, the container validates your `./local/.config.yaml` against the [YAML schema](https://github.com/pokt-network/path/blob/main/config/config.schema.yaml) in the PATH repo.

## Development Workflow

### Hot Reloading

The PATH Localnet supports hot reloading for rapid development:

1. **Make code changes** in your local PATH repository
2. **Save the file**
3. **Tilt detects changes** and triggers a rebuild
4. **New binary is deployed** to the Kind cluster
5. **Service restarts** with your changes

### Viewing Logs

Multiple ways to view logs:

1. **Tilt UI** (http://localhost:10350):

- Real-time log streaming
- Filtered by service
- Search functionality

2. **Inside the container**:

   ```bash
   make localnet_exec
   kubectl logs -f deployment/path
   ```

3. **Using k9s**:

   ```bash
   make localnet_k9s
   # Navigate to pod and press 'l'
   ```
