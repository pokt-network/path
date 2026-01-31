#!/bin/bash
# Test script to verify shared state across PATH replicas via Redis
#
# This script:
# 1. Starts Redis in Docker
# 2. Starts 2 PATH instances with different ports
# 3. Waits for them to be ready
# 4. Checks /ready/<service>?detailed=true on both
# 5. Compares the results to verify shared state

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
REDIS_PORT=6379
PATH1_PORT=3069
PATH2_PORT=3070
PATH1_METRICS=9090
PATH2_METRICS=9091
PATH1_PPROF=6060
PATH2_PPROF=6061
SERVICE_ID="bsc"  # Use BSC since it's enabled in the config
WAIT_TIME=60      # Time to wait for health checks to run

CONFIG_DIR="/tmp/path-test-configs"
LOG_DIR="/tmp/path-test-logs"

cleanup() {
    echo -e "${YELLOW}Cleaning up...${NC}"
    # Kill PATH processes
    [ -n "$PATH1_PID" ] && kill $PATH1_PID 2>/dev/null || true
    [ -n "$PATH2_PID" ] && kill $PATH2_PID 2>/dev/null || true
    # Stop Redis container
    docker stop path-test-redis 2>/dev/null || true
    docker rm path-test-redis 2>/dev/null || true
    echo -e "${GREEN}Cleanup complete${NC}"
}

trap cleanup EXIT

echo -e "${YELLOW}=== PATH Shared State Test ===${NC}"

# Create directories
mkdir -p "$CONFIG_DIR" "$LOG_DIR"

# Step 1: Start Redis
echo -e "\n${YELLOW}[1/6] Starting Redis...${NC}"
docker run -d --name path-test-redis -p $REDIS_PORT:6379 redis:7-alpine
sleep 2

# Verify Redis is running
if ! docker exec path-test-redis redis-cli ping | grep -q PONG; then
    echo -e "${RED}Redis failed to start${NC}"
    exit 1
fi
echo -e "${GREEN}Redis is running${NC}"

# Clear any existing data
docker exec path-test-redis redis-cli FLUSHALL
echo "Redis data cleared"

# Step 2: Build PATH
echo -e "\n${YELLOW}[2/6] Building PATH...${NC}"
cd /home/overlordyorch/Development/path
make path_build

# Step 3: Create config for PATH instance 1
echo -e "\n${YELLOW}[3/6] Creating configs...${NC}"
cat > "$CONFIG_DIR/path1.yaml" << EOF
redis_config:
  address: "localhost:$REDIS_PORT"
  password: ""
  db: 0
  pool_size: 10

router_config:
  port: $PATH1_PORT

logger_config:
  level: "debug"

metrics_config:
  prometheus_addr: ":$PATH1_METRICS"
  pprof_addr: ":$PATH1_PPROF"

full_node_config:
  rpc_url: https://sauron-rpc.infra.pocket.network
  grpc_config:
    host_port: sauron-grpc.infra.pocket.network:443
    insecure: false
  lazy_mode: false
  session_rollover_blocks: 10
  cache_config:
    session_ttl: 30s

gateway_config:
  gateway_mode: "centralized"
  gateway_address: pokt1lf0kekv9zcv9v3wy4v6jx2wh7v4665s8e0sl9s
  gateway_private_key_hex: 5b54b850c483ba2e18a9ae0a6332f0da072c49a5f88dc9e8100495f9badaec93
  owned_apps_private_keys_hex:
    - 12038ff86c20b77a737901c9891fcf991cbf3d7c12f792718a141ec81a97ebbc # bsc

  reputation_config:
    enabled: true
    storage_type: "redis"
    key_granularity: "per-supplier"
    initial_score: 100
    min_threshold: 10
    sync_config:
      refresh_interval: 5s
      write_buffer_size: 1000
      flush_interval: 100ms

  active_health_checks:
    enabled: true
    max_workers: 50
    coordination:
      type: "leader_election"
      lease_duration: "15s"
      renew_interval: "5s"
      key: "path:health:leader"
    external:
      url: "https://raw.githubusercontent.com/pokt-network/pocket-network-resources/refs/heads/main/pocket-health-checks.yaml"
      refresh_interval: "5m"
      timeout: "30s"
    local: []

  observation_pipeline:
    enabled: true
    sample_rate: 1.0
    worker_count: 4
    queue_size: 1000

  services:
    - id: $SERVICE_ID
      type: "evm"
      rpc_types: ["json_rpc"]
      timeout_config:
        relay_timeout: 10s
EOF

# Create config for PATH instance 2 (different ports, same Redis)
cat > "$CONFIG_DIR/path2.yaml" << EOF
redis_config:
  address: "localhost:$REDIS_PORT"
  password: ""
  db: 0
  pool_size: 10

router_config:
  port: $PATH2_PORT

logger_config:
  level: "debug"

metrics_config:
  prometheus_addr: ":$PATH2_METRICS"
  pprof_addr: ":$PATH2_PPROF"

full_node_config:
  rpc_url: https://sauron-rpc.infra.pocket.network
  grpc_config:
    host_port: sauron-grpc.infra.pocket.network:443
    insecure: false
  lazy_mode: false
  session_rollover_blocks: 10
  cache_config:
    session_ttl: 30s

gateway_config:
  gateway_mode: "centralized"
  gateway_address: pokt1lf0kekv9zcv9v3wy4v6jx2wh7v4665s8e0sl9s
  gateway_private_key_hex: 5b54b850c483ba2e18a9ae0a6332f0da072c49a5f88dc9e8100495f9badaec93
  owned_apps_private_keys_hex:
    - 12038ff86c20b77a737901c9891fcf991cbf3d7c12f792718a141ec81a97ebbc # bsc

  reputation_config:
    enabled: true
    storage_type: "redis"
    key_granularity: "per-supplier"
    initial_score: 100
    min_threshold: 10
    sync_config:
      refresh_interval: 5s
      write_buffer_size: 1000
      flush_interval: 100ms

  active_health_checks:
    enabled: true
    max_workers: 50
    coordination:
      type: "leader_election"
      lease_duration: "15s"
      renew_interval: "5s"
      key: "path:health:leader"
    external:
      url: "https://raw.githubusercontent.com/pokt-network/pocket-network-resources/refs/heads/main/pocket-health-checks.yaml"
      refresh_interval: "5m"
      timeout: "30s"
    local: []

  observation_pipeline:
    enabled: true
    sample_rate: 1.0
    worker_count: 4
    queue_size: 1000

  services:
    - id: $SERVICE_ID
      type: "evm"
      rpc_types: ["json_rpc"]
      timeout_config:
        relay_timeout: 10s
EOF

echo -e "${GREEN}Configs created${NC}"

# Step 4: Start PATH instances
echo -e "\n${YELLOW}[4/6] Starting PATH instances...${NC}"

./bin/path -config "$CONFIG_DIR/path1.yaml" > "$LOG_DIR/path1.log" 2>&1 &
PATH1_PID=$!
echo "PATH instance 1 started (PID: $PATH1_PID, port: $PATH1_PORT)"

./bin/path -config "$CONFIG_DIR/path2.yaml" > "$LOG_DIR/path2.log" 2>&1 &
PATH2_PID=$!
echo "PATH instance 2 started (PID: $PATH2_PID, port: $PATH2_PORT)"

# Wait for both instances to be ready
echo -e "\n${YELLOW}Waiting for instances to be ready...${NC}"
for i in {1..30}; do
    PATH1_READY=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:$PATH1_PORT/healthz" 2>/dev/null || echo "000")
    PATH2_READY=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:$PATH2_PORT/healthz" 2>/dev/null || echo "000")

    if [ "$PATH1_READY" = "200" ] && [ "$PATH2_READY" = "200" ]; then
        echo -e "${GREEN}Both instances are healthy${NC}"
        break
    fi

    echo "  Waiting... (PATH1: $PATH1_READY, PATH2: $PATH2_READY)"
    sleep 2
done

# Step 5: Wait for health checks to run
echo -e "\n${YELLOW}[5/6] Waiting $WAIT_TIME seconds for health checks to run...${NC}"
echo "  (Leader election + health checks + Redis sync)"

for i in $(seq 1 $WAIT_TIME); do
    printf "\r  Progress: %d/%d seconds" $i $WAIT_TIME
    sleep 1
done
echo ""

# Step 6: Check detailed endpoint info
echo -e "\n${YELLOW}[6/6] Checking endpoint details...${NC}"

echo -e "\n${YELLOW}--- PATH Instance 1 (port $PATH1_PORT) ---${NC}"
PATH1_DETAILS=$(curl -s "http://localhost:$PATH1_PORT/ready/$SERVICE_ID?detailed=true")
echo "$PATH1_DETAILS" | jq '.' 2>/dev/null || echo "$PATH1_DETAILS"

echo -e "\n${YELLOW}--- PATH Instance 2 (port $PATH2_PORT) ---${NC}"
PATH2_DETAILS=$(curl -s "http://localhost:$PATH2_PORT/ready/$SERVICE_ID?detailed=true")
echo "$PATH2_DETAILS" | jq '.' 2>/dev/null || echo "$PATH2_DETAILS"

# Extract and compare key metrics
echo -e "\n${YELLOW}=== Comparison ===${NC}"

# Check if both see the same number of endpoints
PATH1_COUNT=$(echo "$PATH1_DETAILS" | jq ".services.$SERVICE_ID.endpoint_count" 2>/dev/null)
PATH2_COUNT=$(echo "$PATH2_DETAILS" | jq ".services.$SERVICE_ID.endpoint_count" 2>/dev/null)
echo "Endpoint counts: PATH1=$PATH1_COUNT, PATH2=$PATH2_COUNT"

# Check archival endpoints count
PATH1_ARCHIVAL=$(echo "$PATH1_DETAILS" | jq "[.services.$SERVICE_ID.endpoints[]? | select(.archival.is_archival == true)] | length" 2>/dev/null)
PATH2_ARCHIVAL=$(echo "$PATH2_DETAILS" | jq "[.services.$SERVICE_ID.endpoints[]? | select(.archival.is_archival == true)] | length" 2>/dev/null)
echo "Archival endpoints: PATH1=$PATH1_ARCHIVAL, PATH2=$PATH2_ARCHIVAL"

# Check perceived block height
PATH1_BLOCK=$(echo "$PATH1_DETAILS" | jq ".services.$SERVICE_ID.perceived_block_height" 2>/dev/null)
PATH2_BLOCK=$(echo "$PATH2_DETAILS" | jq ".services.$SERVICE_ID.perceived_block_height" 2>/dev/null)
echo "Perceived block height: PATH1=$PATH1_BLOCK, PATH2=$PATH2_BLOCK"

# Check Redis for stored data
echo -e "\n${YELLOW}=== Redis Data ===${NC}"
echo "Keys in Redis:"
docker exec path-test-redis redis-cli KEYS "*" | head -20

echo -e "\nPerceived block number for $SERVICE_ID:"
docker exec path-test-redis redis-cli GET "path:reputation:chain_state:$SERVICE_ID:perceived_block" || echo "(not set)"

echo -e "\nSample endpoint scores (first 3):"
KEYS=$(docker exec path-test-redis redis-cli KEYS "path:reputation:$SERVICE_ID:*" | head -3)
for key in $KEYS; do
    echo "  $key:"
    docker exec path-test-redis redis-cli HGETALL "$key" | head -10
done

# Final summary
echo -e "\n${YELLOW}=== Summary ===${NC}"

# Check archival status match
if [ "$PATH1_ARCHIVAL" = "$PATH2_ARCHIVAL" ] && [ "$PATH1_ARCHIVAL" != "null" ] && [ "$PATH1_ARCHIVAL" != "0" ]; then
    echo -e "${GREEN}SUCCESS: Both instances see the same archival endpoints ($PATH1_ARCHIVAL)${NC}"
else
    echo -e "${RED}MISMATCH: Archival endpoints differ (PATH1=$PATH1_ARCHIVAL, PATH2=$PATH2_ARCHIVAL)${NC}"
fi

# Check perceived block height match
if [ "$PATH1_BLOCK" = "$PATH2_BLOCK" ] && [ "$PATH1_BLOCK" != "null" ] && [ "$PATH1_BLOCK" != "0" ]; then
    echo -e "${GREEN}SUCCESS: Both instances see the same perceived block height ($PATH1_BLOCK)${NC}"
else
    echo -e "${YELLOW}NOTE: Perceived block heights differ (PATH1=$PATH1_BLOCK, PATH2=$PATH2_BLOCK)${NC}"
    echo "  This may be normal if replicas are still syncing"
fi

if [ "$PATH1_ARCHIVAL" != "$PATH2_ARCHIVAL" ] || [ "$PATH1_BLOCK" != "$PATH2_BLOCK" ]; then
    echo "Check logs for details:"
    echo "  tail -f $LOG_DIR/path1.log"
    echo "  tail -f $LOG_DIR/path2.log"
fi

echo -e "\n${YELLOW}Press Ctrl+C to stop and cleanup${NC}"
wait
