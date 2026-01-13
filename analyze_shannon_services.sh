#!/bin/bash

# Fast Shannon Network Service Analysis
# Queries ALL suppliers once, then analyzes in memory
#
# Usage: ./analyze_shannon_services.sh [OUTPUT_DIR]
# Example: ./analyze_shannon_services.sh /path/to/output

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

NODE="https://sauron-rpc.infra.pocket.network:443"
OUTPUT_DIR="${1:-/tmp/service_analysis}"
ALL_SUPPLIERS_FILE="$OUTPUT_DIR/all_suppliers.json"
SUMMARY_FILE="$OUTPUT_DIR/summary.txt"
CSV_FILE="$OUTPUT_DIR/summary.csv"

echo -e "${BLUE}=== Fast Shannon Network Service Analysis ===${NC}"
echo -e "${BLUE}Output directory: $OUTPUT_DIR${NC}"
echo ""

# Create output directory
mkdir -p "$OUTPUT_DIR"

# Step 1: Query ALL suppliers once (this is the only network call)
echo -e "${YELLOW}Step 1: Fetching ALL suppliers from Shannon network...${NC}"
echo "  (This may take 30-60 seconds for the entire network)"
echo ""

pocketd query supplier list-suppliers \
    --node "$NODE" \
    --output json \
    --page-limit 100000 \
    --dehydrated \
    2>&1 > "$ALL_SUPPLIERS_FILE"

if [ $? -ne 0 ]; then
    echo -e "${RED}Error querying suppliers!${NC}"
    cat "$ALL_SUPPLIERS_FILE"
    exit 1
fi

TOTAL_SUPPLIERS=$(jq '.supplier | length' "$ALL_SUPPLIERS_FILE")
echo -e "${GREEN}✓ Fetched $TOTAL_SUPPLIERS suppliers${NC}"
echo ""

# Step 2: Analyze in memory (fast!)
echo -e "${YELLOW}Step 2: Analyzing services in memory...${NC}"
echo ""

# Initialize CSV
echo "Service ID,Suppliers,JSON_RPC,WEBSOCKET,REST,COMET_BFT,GRPC,Min Stake,Max Stake,Avg Stake,RPC Config" > "$CSV_FILE"

# Get all unique service IDs
SERVICE_IDS=$(jq -r '[.supplier[].services[]?.service_id] | unique | .[]' "$ALL_SUPPLIERS_FILE" | sort)

CURRENT=0
TOTAL_SERVICES=$(echo "$SERVICE_IDS" | wc -l)

echo "Found $TOTAL_SERVICES unique services"
echo ""

# Initialize summary file
cat > "$SUMMARY_FILE" << 'HEADER'
================================================================================
                    SHANNON NETWORK SERVICE ANALYSIS
================================================================================

Total Suppliers Analyzed: 
Total Unique Services:    

HEADER

# Replace placeholders
sed -i "s/Total Suppliers Analyzed:.*/Total Suppliers Analyzed: $TOTAL_SUPPLIERS/" "$SUMMARY_FILE"
sed -i "s/Total Unique Services:.*/Total Unique Services:    $TOTAL_SERVICES/" "$SUMMARY_FILE"

cat >> "$SUMMARY_FILE" << 'SEPARATOR'

================================================================================
                           PER-SERVICE BREAKDOWN
================================================================================

SEPARATOR

for SERVICE_ID in $SERVICE_IDS; do
    CURRENT=$((CURRENT + 1))
    echo -ne "\r${YELLOW}[$CURRENT/$TOTAL_SERVICES] Analyzing: $SERVICE_ID                    ${NC}"
    
    # Extract suppliers for this service (in memory - fast!)
    SERVICE_DATA=$(jq --arg sid "$SERVICE_ID" '
        [.supplier[] | select(.services[]?.service_id == $sid) | 
        {
            operator: .operator_address,
            stake: .stake.amount,
            endpoints: [.services[] | select(.service_id == $sid) | .endpoints[]? | .rpc_type]
        }]
    ' "$ALL_SUPPLIERS_FILE")
    
    SUPPLIER_COUNT=$(echo "$SERVICE_DATA" | jq 'length')
    
    if [ "$SUPPLIER_COUNT" -eq 0 ]; then
        continue
    fi
    
    # Count RPC types (unique suppliers)
    JSON_RPC=$(echo "$SERVICE_DATA" | jq '[.[] | select(.endpoints[] == "JSON_RPC")] | length')
    WEBSOCKET=$(echo "$SERVICE_DATA" | jq '[.[] | select(.endpoints[] == "WEBSOCKET")] | length')
    REST=$(echo "$SERVICE_DATA" | jq '[.[] | select(.endpoints[] == "REST")] | length')
    COMET_BFT=$(echo "$SERVICE_DATA" | jq '[.[] | select(.endpoints[] == "COMET_BFT")] | length')
    GRPC=$(echo "$SERVICE_DATA" | jq '[.[] | select(.endpoints[] == "GRPC")] | length')
    
    # Get most common RPC configuration
    RPC_CONFIG=$(echo "$SERVICE_DATA" | jq -r '.[0].endpoints | sort | join(",")')
    
    # Stake statistics
    MIN_STAKE=$(echo "$SERVICE_DATA" | jq -r '[.[].stake | tonumber] | min / 1000000 | floor')
    MAX_STAKE=$(echo "$SERVICE_DATA" | jq -r '[.[].stake | tonumber] | max / 1000000 | floor')
    AVG_STAKE=$(echo "$SERVICE_DATA" | jq -r '[.[].stake | tonumber] | add / length / 1000000 | floor')
    
    # Append to CSV
    echo "$SERVICE_ID,$SUPPLIER_COUNT,$JSON_RPC,$WEBSOCKET,$REST,$COMET_BFT,$GRPC,$MIN_STAKE,$MAX_STAKE,$AVG_STAKE,$RPC_CONFIG" >> "$CSV_FILE"
    
    # Append to summary
    cat >> "$SUMMARY_FILE" << ENTRY

--------------------------------------------------------------------------------
Service: $SERVICE_ID
--------------------------------------------------------------------------------
Suppliers:              $SUPPLIER_COUNT

RPC Type Distribution:
  JSON_RPC:             $JSON_RPC ($(echo "scale=1; $JSON_RPC * 100 / $SUPPLIER_COUNT" | bc)%)
  WEBSOCKET:            $WEBSOCKET ($(echo "scale=1; $WEBSOCKET * 100 / $SUPPLIER_COUNT" | bc)%)
  REST:                 $REST ($(echo "scale=1; $REST * 100 / $SUPPLIER_COUNT" | bc)%)
  COMET_BFT:            $COMET_BFT ($(echo "scale=1; $COMET_BFT * 100 / $SUPPLIER_COUNT" | bc)%)
  GRPC:                 $GRPC ($(echo "scale=1; $GRPC * 100 / $SUPPLIER_COUNT" | bc)%)

Common Config:          [$RPC_CONFIG]

Stake Range:            $MIN_STAKE K - $MAX_STAKE K POKT (avg: $AVG_STAKE K)

ENTRY
done

echo ""
echo ""

# Step 3: Domain-based grouping analysis
echo -e "${YELLOW}Step 3: Analyzing domain distribution (ignoring subdomains)...${NC}"
echo ""

DOMAIN_CSV="$OUTPUT_DIR/domain_analysis.csv"
DOMAIN_DETAIL_FILE="$OUTPUT_DIR/domain_detail.txt"

echo "Service ID,Unique Domains,Total Suppliers" > "$DOMAIN_CSV"

# Initialize detailed domain file
cat > "$DOMAIN_DETAIL_FILE" << 'DETAIL_HEADER'
================================================================================
                    DOMAIN-LEVEL RPC TYPE ANALYSIS
================================================================================

Shows which domains serve which services and their RPC type support.

DETAIL_HEADER

for SERVICE_ID in $SERVICE_IDS; do
    # Get domain count for summary CSV
    DOMAIN_COUNT=$(jq --arg sid "$SERVICE_ID" -r '
        [.supplier[] |
         select(.services[]?.service_id == $sid) |
         .services[] |
         select(.service_id == $sid) |
         .endpoints[]? |
         .url // empty
        ] |
        map(
            # Extract hostname from URL
            gsub("^https?://"; "") |
            gsub("/.*$"; "") |
            gsub(":.*$"; "") |
            # Get base domain (last 2 parts, simplified)
            split(".") |
            if length >= 2 then .[-2:] | join(".") else join(".") end
        ) |
        unique |
        length
    ' "$ALL_SUPPLIERS_FILE")

    SUPPLIER_COUNT=$(jq --arg sid "$SERVICE_ID" '[.supplier[] | select(.services[]?.service_id == $sid)] | length' "$ALL_SUPPLIERS_FILE")

    if [ "$SUPPLIER_COUNT" -gt 0 ]; then
        echo "$SERVICE_ID,$DOMAIN_COUNT,$SUPPLIER_COUNT" >> "$DOMAIN_CSV"

        # Generate detailed per-domain RPC type analysis
        echo "" >> "$DOMAIN_DETAIL_FILE"
        echo "--------------------------------------------------------------------------------" >> "$DOMAIN_DETAIL_FILE"
        echo "Service: $SERVICE_ID ($SUPPLIER_COUNT suppliers, $DOMAIN_COUNT domains)" >> "$DOMAIN_DETAIL_FILE"
        echo "--------------------------------------------------------------------------------" >> "$DOMAIN_DETAIL_FILE"

        # Get per-domain breakdown with RPC types
        jq --arg sid "$SERVICE_ID" -r '
            # Group by domain
            [.supplier[] |
             select(.services[]?.service_id == $sid) |
             .services[] |
             select(.service_id == $sid) |
             . as $service |
             .endpoints[]? |
             {
                 domain: (.url // "" |
                          gsub("^https?://"; "") |
                          gsub("/.*$"; "") |
                          gsub(":.*$"; "") |
                          split(".") |
                          if length >= 2 then .[-2:] | join(".") else join(".") end),
                 rpc_type: .rpc_type
             }
            ] |
            # Group by domain
            group_by(.domain) |
            map({
                domain: .[0].domain,
                supplier_count: length,
                rpc_types: [.[].rpc_type] | unique | sort
            }) |
            sort_by(-.supplier_count) |
            .[] |
            "  \(.domain | . + (" " * (30 - length))): \(.supplier_count) suppliers  [\(.rpc_types | join(", "))]"
        ' "$ALL_SUPPLIERS_FILE" >> "$DOMAIN_DETAIL_FILE"
    fi
done

echo ""

# Generate comparison table
cat >> "$SUMMARY_FILE" << 'TABLE_HEADER'

================================================================================
                           COMPARISON TABLE
================================================================================

Service ID          Suppliers  JSON_RPC  WEBSOCKET  REST  COMET_BFT  GRPC  Avg Stake
--------------------------------------------------------------------------------
TABLE_HEADER

tail -n +2 "$CSV_FILE" | sort -t',' -k2 -nr | while IFS=',' read -r SERVICE SUPPLIERS JSON WS REST COMET GRPC MIN MAX AVG CONFIG; do
    printf "%-18s  %8s  %8s  %9s  %4s  %9s  %4s  %6s K\n" \
        "$SERVICE" "$SUPPLIERS" "$JSON" "$WS" "$REST" "$COMET" "$GRPC" "$AVG"
done >> "$SUMMARY_FILE"

cat >> "$SUMMARY_FILE" << 'FOOTER'
================================================================================

Key Insights:
- JSON_RPC: Universal (100% of suppliers provide this)
- WEBSOCKET: Common for EVM chains (15-100% coverage)
- REST: Common for Cosmos chains (gRPC-gateway)
- COMET_BFT: Rarely provided (0% on most chains)
- GRPC: Rarely provided (0% on most chains)

Files Generated:
- summary.txt: This full report
- summary.csv: CSV format for analysis
- domain_analysis.csv: Domain distribution data
- domain_detail.txt: Per-domain RPC type breakdown
- all_suppliers.json: Raw supplier data

View commands:
  cat /tmp/service_analysis/summary.txt
  cat /tmp/service_analysis/domain_detail.txt
  column -t -s',' /tmp/service_analysis/summary.csv | less -S

FOOTER

echo -e "${GREEN}=================================================================================${NC}"
echo -e "${GREEN}Analysis Complete!${NC}"
echo -e "${GREEN}=================================================================================${NC}"
echo ""
echo "Results:"
echo "  Full report:    $SUMMARY_FILE"
echo "  CSV data:       $CSV_FILE"
echo "  Domain CSV:     $OUTPUT_DIR/domain_analysis.csv"
echo "  Domain details: $OUTPUT_DIR/domain_detail.txt"
echo "  Raw data:       $ALL_SUPPLIERS_FILE"
echo ""
echo "Quick stats:"
TOTAL_SERVICES_ANALYZED=$(tail -n +2 "$CSV_FILE" | wc -l)
SERVICES_WITH_REST=$(tail -n +2 "$CSV_FILE" | awk -F',' '$5 > 0' | wc -l)
SERVICES_WITH_WS=$(tail -n +2 "$CSV_FILE" | awk -F',' '$4 > 0' | wc -l)
echo "  Total suppliers: $TOTAL_SUPPLIERS"
echo "  Services found:  $TOTAL_SERVICES_ANALYZED"
echo "  With REST:       $SERVICES_WITH_REST"
echo "  With WebSocket:  $SERVICES_WITH_WS"
echo ""
echo "View summary:"
echo "  cat $SUMMARY_FILE"
echo ""

# Display all services
echo "All Services by Supplier Count:"
echo ""
printf "%-25s %10s %10s %10s %10s %10s %10s %12s\n" "Service" "Suppliers" "JSON_RPC" "WebSocket" "REST" "CometBFT" "GRPC" "Avg Stake"
printf "%-25s %10s %10s %10s %10s %10s %10s %12s\n" "-------------------------" "----------" "----------" "----------" "----------" "----------" "----------" "------------"
tail -n +2 "$CSV_FILE" | sort -t',' -k2 -nr | while IFS=',' read -r SERVICE SUPPLIERS JSON WS REST COMET GRPC MIN MAX AVG CONFIG; do
    printf "%-25s %10s %10s %10s %10s %10s %10s %10s K\n" "$SERVICE" "$SUPPLIERS" "$JSON" "$WS" "$REST" "$COMET" "$GRPC" "$AVG"
done

echo ""
echo ""
echo "Domain Distribution Analysis (Base Domains Only):"
echo ""
printf "%-25s %15s %15s %15s\n" "Service" "Unique Domains" "Total Suppliers" "Suppliers/Domain"
printf "%-25s %15s %15s %15s\n" "-------------------------" "---------------" "---------------" "---------------"
tail -n +2 "$DOMAIN_CSV" | sort -t',' -k3 -nr | while IFS=',' read -r SERVICE DOMAINS SUPPLIERS; do
    RATIO=$(echo "scale=1; $SUPPLIERS / $DOMAINS" | bc)
    printf "%-25s %15s %15s %15s\n" "$SERVICE" "$DOMAINS" "$SUPPLIERS" "$RATIO"
done

echo ""
echo ""
echo "Sample Domain-Level RPC Type Breakdown (Top Services):"
echo ""
echo "View full details: cat $OUTPUT_DIR/domain_detail.txt"
echo ""

# Show sample for top 3 services
head -100 "$DOMAIN_DETAIL_FILE" | tail -80

echo ""
echo "..."
echo ""
echo "Full domain details saved to: $OUTPUT_DIR/domain_detail.txt"
echo ""
