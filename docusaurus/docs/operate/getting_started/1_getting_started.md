---
sidebar_position: 1
title: Getting Started (<10 minutes)
description: Guide to get a PATH instance up and running.
---

This guide will help you set up and run PATH to serve requests using Pocket Network in under 10 minutes.

:::note No Authentication / Authorization

This guide covers running PATH without any authentication or authorization mechanisms. Work is underway to provide these capabilities.

See [PATH Guard documentation](https://path.grove.city/operate/helm/guard) for details.

:::

## Prerequisites

- Docker installed and running on your system

## 1. Prepare Your Configuration

First, prepare your configuration file by following [the PATH Config File instructions](https://path.grove.city/develop/configs/config_intro).

After you have your configuration file, you can proceed with the following steps:

1. Create a config directory for PATH:

   ```bash
   mkdir -p ./path/config
   ```

2. Copy your configuration file to the new directory with the correct name:

   ```bash
   export CONFIG_FILE=/PATH/TO/YOUR/CONFIG/FILE
   cp $CONFIG_FILE ./path/config/.config.yaml
   ```

## 2. Set Up the PATH Container

:::warning TODO

TODO_IMPROVE: Replace `main` with `latest` once the artifact release CI is complete.

:::

1. Set the PATH container image version from [the set of available tags](https://github.com/pokt-network/path/pkgs/container/path):

   ```bash
   export PATH_IMAGE_TAG='main'
   ```

2. Start the PATH container:

   ```bash
   docker run \
     -itd \
     --name path \
     -p 3069:3069 \
     -v ./path/config:/app/config \
     ghcr.io/buildwithgrove/path:$PATH_IMAGE_TAG
   ```

   Parameter explanation:

   - `-p 3069:3069`: Map port 3069 on host to port 3069 in container. PATH listens on port 3069 for requests.
   - `-v ./path/config:/app/config`: Mount the config directory. PATH expects its configuration file at `/app/config/.config.yaml`

## 3. Verify PATH is Running

1. Check the PATH container logs:

   ```bash
   docker logs path --follow --tail 100
   ```

2. Wait for PATH to be ready to serve requests:

   ```bash
   curl -s http://localhost:3069/healthz
   ```

   When PATH is ready, this command will return a `200 status code` and the response body will contain the JSON: `"status": "ready"`

   **Example response:**

   ```json
   {
     "status": "ready",
     "imageTag": "development",
     "readyStates": {
       "endpoint-hydrator": true,
       "eth": true
     },
     "configuredServiceIDs": ["anvil", "base", "eth", "pocket", "xrplevm"]
   }
   ```

## 4. Test Relays

```bash
curl http://localhost:3069/v1 \
 -H "Target-Service-Id: anvil" \
 -d '{"jsonrpc": "2.0", "id": 1, "method": "eth_blockNumber" }'
```

You should expect to see a response similar to the following:

```json
{ "id": 1, "jsonrpc": "2.0", "result": "0x2f01a" }
```

## Troubleshooting

If PATH doesn't show as ready after a few minutes:

- Check the logs for error messages: `docker logs path`
- Verify your configuration file is correct: `cat ./path/config/.config.yaml`
- Ensure the ports aren't already in use: `lsof -i :3069`

## Next Steps

Once PATH is running successfully:

- Configure your client applications to connect to PATH
- Monitor PATH's performance and logs as needed
- For more advanced configuration options, refer to the [full documentation](https://path.grove.city/develop/path)

:::warning TODO - Test instructions

TODO_IMPROVE(@adshmh): Add additional instructions on how to test this and improve next steps

:::
