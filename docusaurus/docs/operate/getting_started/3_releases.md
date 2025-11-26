---
sidebar_position: 3
title: PATH Releases
---

## Table of Contents <!-- omit in toc -->

- [PATH Builds](#path-builds)
  - [PATH Build Resources](#path-build-resources)
  - [Example Usage](#example-usage)
- [Tagging a new release](#tagging-a-new-release)
  - [1. Clone the repository](#1-clone-the-repository)
  - [2. Create a release](#2-create-a-release)
  - [3. Draft a new GitHub release](#3-draft-a-new-github-release)

## PATH Builds

PATH builds provide a Docker image to quickly bootstrap your Path gateway without building your own image.

### PATH Build Resources

- [**Container Registry**](https://github.com/pokt-network/path/pkgs/container/path): Find all PATH Docker images
- [**Releases**](https://github.com/pokt-network/path/releases): Find the latest release and release notes
- [**Package Versions**](https://github.com/pokt-network/path/pkgs/container/path/versions): Find all available versions of the PATH Docker image

### Example Usage

```sh
docker pull ghcr.io/buildwithgrove/path
```

## Tagging a new release

### 1. Clone the repository

```bash
git clone git@github.com:buildwithgrove/path.git path
cd path
```

### 2. Create a release

Choose one of the following:

```bash
# Tag a new dev release (e.g. v1.0.1 -> v1.0.1-dev1, v1.0.1-dev1 -> v1.0.1-dev2)
make release_tag_dev

# Tag a new bug fix release (e.g. v1.0.1 -> v1.0.2)
make release_tag_bug_fix

# Tag a new minor release (e.g. v1.0.0 -> v1.1.0)
make release_tag_minor_release
```

Push the tag to GitHub:

```bash
git push origin $(git tag)
```

### 3. Draft a new GitHub release

Draft a new release at [buildwithgrove/path/releases/new](https://github.com/pokt-network/path/releases/new) using the tag (e.g. `v0.1.12-dev3`) created in the previous step.
