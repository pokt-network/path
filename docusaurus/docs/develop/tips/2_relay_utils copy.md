---
sidebar_position: 2
title: Relay Utils
description: Relay Load Testing Tools
---

## Installation

```bash
make install_tools_optional
```

## Relay Util

:::tip

Easy to use make targets are provided in the [test requests Makefile](https://github.com/pokt-network/path/blob/main/makefiles/test_requests.mk).

You can run the following commands if you have a local PATH instance running pointing to Pocket Network.

```bash
relay-util \
    -u http://localhost:3070/v1 \
    -H "target-service-id: $${SERVICE_ID:-anvil}" \
    -H "authorization: test_api_key" \
		-d '{"jsonrpc":"2.0","method":"eth_blockNumber","id":1}' \
		-x 100 \
		-b
```

:::

The documentation below is taken from the [relay-util repo](https://github.com/commoddity/relay-util).

---

import RemoteMarkdown from '@site/src/components/RemoteMarkdown';

<RemoteMarkdown src="https://raw.githubusercontent.com/commoddity/relay-util/refs/heads/main/README.md" />
