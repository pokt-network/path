---
sidebar_position: 3
title: Websocket Utils
description: Websocket Load Testing Tools
---

## Installation

```bash
make install_tools_optional
```

## Websocket Load Test

:::tip

Easy to use make targets are provided in the [test load Makefile](https://github.com/pokt-network/path/blob/main/makefiles/test_load.mk).

You can run the following commands if you have a local PATH instance running pointing to Pocket Network.

````bash
 ```bash
   websocket-load-test \
   --service "xrplevm" \
   --app-id $GROVE_PORTAL_APP_ID \
   --api-key $GROVE_PORTAL_API_KEY \
   --subs "newHeads,newPendingTransactions" \
   --count 10 \
   --log
```

:::

The documentation below is taken from the [websocket-load-test repo](https://github.com/commoddity/websocket-load-test).

---

import RemoteMarkdown from '@site/src/components/RemoteMarkdown';

<RemoteMarkdown src="https://raw.githubusercontent.com/commoddity/websocket-load-test/refs/heads/main/README.md" />
```
````
