---
sidebar_position: 1
title: Getting Started (< 5 min)
description: Intro and environment setup
---

<div align="center">
<h1>PATH API & Toolkit Harness</h1>
<img src="https://storage.googleapis.com/grove-brand-assets/Presskit/Logo%20Joined-2.png" alt="Grove logo" width="500"/>

</div>
<br/>

![Static Badge](https://img.shields.io/badge/Maintained_by-Grove-green)
![GitHub Actions Workflow Status](https://img.shields.io/github/actions/workflow/status/pokt-network/path/main-build.yml)
![GitHub last commit](https://img.shields.io/github/last-commit/buildwithgrove/path)
![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/buildwithgrove/path)
![GitHub Release](https://img.shields.io/github/v/release/buildwithgrove/path)
![GitHub Downloads (all assets, all releases)](https://img.shields.io/github/downloads/pokt-network/path/total)
![GitHub Issues or Pull Requests](https://img.shields.io/github/issues/buildwithgrove/path)
![GitHub Issues or Pull Requests](https://img.shields.io/github/issues-pr/buildwithgrove/path)
![GitHub Issues or Pull Requests](https://img.shields.io/github/issues-closed/buildwithgrove/path)

### 1. Clone the repository

```bash
git clone https://github.com/buildwithgrove/path.git
cd ./path
```

### 2. Install all dependencies

Required tools:

```bash
make install_tools
```

Optional but highly recommended tools:

```bash
make install_tools_optional
```

<details>
<summary>Details on Installed Tools</summary>

**Tools installed by `make install_tools`**:

- [**pocketd CLI**](https://dev.poktroll.com/category/pocketd-cli): CLI for interacting with Pocket Network's Shannon protocol
- [**Docker**](https://docs.docker.com/get-docker/): Container runtime

**Optional development tools** (`make install_tools_optional`):

- [**Websocket Load Test**](https://github.com/commoddity/websocket-load-test): Websocket load testing tool
- [**Relay Util**](https://github.com/commoddity/relay-util): Load testing tool for sending configurable batches of relays concurrently
- [**Graphviz**](https://graphviz.org): Required for generating profiling & debugging performance
- [**Uber Mockgen**](https://github.com/uber-go/mock): Mock interface generator for testing

</details>

### 3. Configure your PATH Gateway for Pocket Network

**[Pocket Network Cheat Sheet](2_cheatsheet_pocket.md)** - Configure PATH for the Shannon protocol
