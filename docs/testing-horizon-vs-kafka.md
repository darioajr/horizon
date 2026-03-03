# Testing Horizon vs Kafka (KRaft) — Comparative Guide

This guide shows how to run **Horizon** and **Kafka KRaft (single broker)** side by side in Docker, and use **Kafka's own CLI tools** (`kafka-topics.sh`, `kafka-console-producer.sh`, `kafka-console-consumer.sh`) to test both brokers identically — creating topics, producing messages, and consuming messages — while measuring the time of each operation.

## Fair Network Setup

To ensure a fair comparison, all three containers share the same Docker network (`bench-net`). A dedicated **tools container** runs the Kafka CLI and reaches both brokers via Docker DNS, so both connections traverse the same network path:

```
┌─────────────────────────────────────────────────────────────┐
│  Docker network: bench-net                                  │
│                                                             │
│  ┌──────────────┐   ┌──────────────┐   ┌──────────────┐    │
│  │ kafka-tools  │   │ horizon-test │   │ kafka-kraft   │    │
│  │ (CLI runner) │──▶│    :9092     │   │    :9092      │    │
│  │              │──▶│              │   │               │    │
│  └──────────────┘   └──────────────┘   └──────────────┘    │
│         │                                     ▲             │
│         └─────────────────────────────────────┘             │
│                                                             │
│  Both paths: Docker DNS → bridge → container                │
│  Same hops, same overhead → fair comparison                 │
└─────────────────────────────────────────────────────────────┘
```

> **Why not `localhost` vs `host.docker.internal`?**
> Using `localhost:9092` for Kafka (loopback inside its own container) and `host.docker.internal:9092` for Horizon (bridge → host → bridge) creates an unfair advantage for Kafka due to less network overhead. The dedicated tools container eliminates this asymmetry.

---

## Prerequisites

- Docker installed
- `bash` available (Linux/macOS/WSL/Git Bash)
- Ports available: **9092** (Horizon), **9192** (Kafka)

---

## 1. Start the Services

### 1.1 Create the Shared Network

```bash
docker network create bench-net
```

### 1.2 Horizon

```bash
docker run -d \
  --name horizon-test \
  --network bench-net \
  -p 9092:9092 \
  -p 8080:8080 \
  -e HORIZON_ADVERTISED_HOST=horizon-test \
  -v horizon-test-data:/data \
  darioajr/horizon:latest

# Verify
docker logs horizon-test --tail 5
```

> **Why `HORIZON_ADVERTISED_HOST`?** By default Horizon listens on `0.0.0.0` and advertises `localhost` in metadata responses. When a Kafka client connects, it uses the advertised address for subsequent requests. Setting `HORIZON_ADVERTISED_HOST=horizon-test` makes Horizon advertise its Docker container name, so clients on the same network can reach it.

### 1.3 Kafka KRaft (single broker, no Zookeeper)

```bash
docker run -d \
  --name kafka-kraft-test \
  --network bench-net \
  -p 9192:9092 \
  -e KAFKA_NODE_ID=1 \
  -e KAFKA_PROCESS_ROLES=broker,controller \
  -e KAFKA_LISTENERS=PLAINTEXT://0.0.0.0:9092,CONTROLLER://0.0.0.0:9093 \
  -e KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://kafka-kraft-test:9092 \
  -e KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER \
  -e KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=PLAINTEXT:PLAINTEXT,CONTROLLER:PLAINTEXT \
  -e KAFKA_CONTROLLER_QUORUM_VOTERS=1@localhost:9093 \
  -e KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1 \
  -e KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR=1 \
  -e KAFKA_TRANSACTION_STATE_LOG_MIN_ISR=1 \
  -e KAFKA_LOG_DIRS=/tmp/kraft-combined-logs \
  -e CLUSTER_ID=MkU3OEVBNTcwNTJENDM2Qk \
  apache/kafka:3.8.0

# Wait for Kafka to be ready (~15s for JVM + KRaft init)
sleep 15
docker logs kafka-kraft-test --tail 5
```

> **Important:** `KAFKA_ADVERTISED_LISTENERS` is set to `kafka-kraft-test:9092` (container name), so the Kafka client inside the tools container can reach it after the initial bootstrap.

### 1.4 Tools Container (CLI runner)

```bash
docker run -d \
  --name kafka-tools \
  --network bench-net \
  --entrypoint sleep \
  apache/kafka:3.8.0 \
  infinity

# Verify both brokers are reachable
docker exec kafka-tools /opt/kafka/bin/kafka-broker-api-versions.sh \
  --bootstrap-server horizon-test:9092 2>&1 | head -1

docker exec kafka-tools /opt/kafka/bin/kafka-broker-api-versions.sh \
  --bootstrap-server kafka-kraft-test:9092 2>&1 | head -1
```

### Connection Summary

| Target | Bootstrap Server (from `kafka-tools`) |
|--------|---------------------------------------|
| **Horizon** | `horizon-test:9092` |
| **Kafka KRaft** | `kafka-kraft-test:9092` |

Both connections follow the same path: Docker DNS → bridge network → target container.

---

## 2. Test Scripts

All scripts below use **only** the `.sh` tools from `/opt/kafka/bin/` inside the `kafka-tools` container, and access both brokers via their container names over the shared Docker network.

### 2.1 Create Topics

```bash
#!/bin/bash
# test-create-topics.sh — Uses kafka-topics.sh for both brokers

TOPIC="benchmark-topic"
PARTITIONS=3
KAFKA_BIN="/opt/kafka/bin"
TOOLS="kafka-tools"

echo "============================================"
echo "  CREATE TOPIC: $TOPIC ($PARTITIONS partitions)"
echo "============================================"

# --- Horizon ---
echo ""
echo ">>> Horizon (via kafka-topics.sh → horizon-test:9092)"
START=$(date +%s%N)

docker exec $TOOLS $KAFKA_BIN/kafka-topics.sh \
  --bootstrap-server horizon-test:9092 \
  --create \
  --topic "$TOPIC" \
  --partitions "$PARTITIONS" \
  --replication-factor 1 \
  2>&1

END=$(date +%s%N)
ELAPSED_MS=$(( (END - START) / 1000000 ))
echo "   Time: ${ELAPSED_MS} ms"

# --- Kafka ---
echo ""
echo ">>> Kafka KRaft (via kafka-topics.sh → kafka-kraft-test:9092)"
START=$(date +%s%N)

docker exec $TOOLS $KAFKA_BIN/kafka-topics.sh \
  --bootstrap-server kafka-kraft-test:9092 \
  --create \
  --topic "$TOPIC" \
  --partitions "$PARTITIONS" \
  --replication-factor 1 \
  2>&1

END=$(date +%s%N)
ELAPSED_MS=$(( (END - START) / 1000000 ))
echo "   Time: ${ELAPSED_MS} ms"

echo ""
echo "============================================"
echo "  LIST TOPICS"
echo "============================================"

echo ""
echo ">>> Horizon"
START=$(date +%s%N)
docker exec $TOOLS $KAFKA_BIN/kafka-topics.sh \
  --bootstrap-server horizon-test:9092 --list
END=$(date +%s%N)
echo "   Time: $(( (END - START) / 1000000 )) ms"

echo ""
echo ">>> Kafka KRaft"
START=$(date +%s%N)
docker exec $TOOLS $KAFKA_BIN/kafka-topics.sh \
  --bootstrap-server kafka-kraft-test:9092 --list
END=$(date +%s%N)
echo "   Time: $(( (END - START) / 1000000 )) ms"

echo ""
echo "============================================"
echo "  DESCRIBE TOPIC"
echo "============================================"

echo ""
echo ">>> Horizon"
docker exec $TOOLS $KAFKA_BIN/kafka-topics.sh \
  --bootstrap-server horizon-test:9092 --describe --topic "$TOPIC"

echo ""
echo ">>> Kafka KRaft"
docker exec $TOOLS $KAFKA_BIN/kafka-topics.sh \
  --bootstrap-server kafka-kraft-test:9092 --describe --topic "$TOPIC"
```

### 2.2 Produce Messages

```bash
#!/bin/bash
# test-produce.sh — Uses kafka-console-producer.sh for both brokers
#
# Usage: ./test-produce.sh [NUM_MESSAGES] [MESSAGE_SIZE_BYTES]

TOPIC="benchmark-topic"
NUM_MESSAGES=${1:-1000}
MESSAGE_SIZE=${2:-1024}
KAFKA_BIN="/opt/kafka/bin"
TOOLS="kafka-tools"

# Generate message file
PAYLOAD=$(head -c "$MESSAGE_SIZE" /dev/urandom | base64 | head -c "$MESSAGE_SIZE")
TMPFILE=$(mktemp)
for i in $(seq 1 "$NUM_MESSAGES"); do
  echo "$PAYLOAD" >> "$TMPFILE"
done

# Copy messages into the tools container
docker cp "$TMPFILE" $TOOLS:/tmp/messages.txt

echo "============================================"
echo "  PRODUCE $NUM_MESSAGES messages (${MESSAGE_SIZE} bytes each)"
echo "  Using: kafka-console-producer.sh"
echo "============================================"

# --- Horizon ---
echo ""
echo ">>> Horizon (kafka-console-producer.sh → horizon-test:9092)"
START=$(date +%s%N)

docker exec $TOOLS bash -c \
  "cat /tmp/messages.txt | $KAFKA_BIN/kafka-console-producer.sh \
    --bootstrap-server horizon-test:9092 \
    --topic $TOPIC"

END=$(date +%s%N)
H_MS=$(( (END - START) / 1000000 ))
H_TPS=$(( NUM_MESSAGES * 1000 / (H_MS + 1) ))
echo "   Total time:    ${H_MS} ms"
echo "   Throughput:    ~${H_TPS} msg/s"
echo "   Avg latency:   $(( H_MS / NUM_MESSAGES )) ms/msg"

# --- Kafka ---
echo ""
echo ">>> Kafka KRaft (kafka-console-producer.sh → kafka-kraft-test:9092)"
START=$(date +%s%N)

docker exec $TOOLS bash -c \
  "cat /tmp/messages.txt | $KAFKA_BIN/kafka-console-producer.sh \
    --bootstrap-server kafka-kraft-test:9092 \
    --topic $TOPIC"

END=$(date +%s%N)
K_MS=$(( (END - START) / 1000000 ))
K_TPS=$(( NUM_MESSAGES * 1000 / (K_MS + 1) ))
echo "   Total time:    ${K_MS} ms"
echo "   Throughput:    ~${K_TPS} msg/s"
echo "   Avg latency:   $(( K_MS / NUM_MESSAGES )) ms/msg"

rm -f "$TMPFILE"

echo ""
echo "============================================"
echo "  SUMMARY"
echo "============================================"
printf "  %-15s  %10s  %10s\n" "" "Horizon" "Kafka"
printf "  %-15s  %8s ms  %8s ms\n" "Time" "$H_MS" "$K_MS"
printf "  %-15s  %6s msg/s  %6s msg/s\n" "Throughput" "$H_TPS" "$K_TPS"
```

### 2.3 Consume Messages

```bash
#!/bin/bash
# test-consume.sh — Uses kafka-console-consumer.sh for both brokers
#
# Usage: ./test-consume.sh [TIMEOUT_SECONDS]

TOPIC="benchmark-topic"
TIMEOUT_SEC=${1:-10}
TIMEOUT_MS=$((TIMEOUT_SEC * 1000))
KAFKA_BIN="/opt/kafka/bin"
TOOLS="kafka-tools"

echo "============================================"
echo "  CONSUME from topic: $TOPIC"
echo "  Using: kafka-console-consumer.sh"
echo "  Timeout: ${TIMEOUT_SEC}s"
echo "============================================"

# --- Horizon ---
echo ""
echo ">>> Horizon (kafka-console-consumer.sh → horizon-test:9092)"
START=$(date +%s%N)

H_COUNT=$(docker exec $TOOLS timeout $((TIMEOUT_SEC + 5)) \
  $KAFKA_BIN/kafka-console-consumer.sh \
    --bootstrap-server horizon-test:9092 \
    --topic "$TOPIC" \
    --from-beginning \
    --timeout-ms "$TIMEOUT_MS" \
  2>/dev/null | wc -l)

END=$(date +%s%N)
H_MS=$(( (END - START) / 1000000 ))
echo "   Messages consumed: $H_COUNT"
echo "   Total time:        ${H_MS} ms"
if [ "$H_COUNT" -gt 0 ]; then
  echo "   Throughput:        ~$(( H_COUNT * 1000 / (H_MS + 1) )) msg/s"
fi

# --- Kafka ---
echo ""
echo ">>> Kafka KRaft (kafka-console-consumer.sh → kafka-kraft-test:9092)"
START=$(date +%s%N)

K_COUNT=$(docker exec $TOOLS timeout $((TIMEOUT_SEC + 5)) \
  $KAFKA_BIN/kafka-console-consumer.sh \
    --bootstrap-server kafka-kraft-test:9092 \
    --topic "$TOPIC" \
    --from-beginning \
    --timeout-ms "$TIMEOUT_MS" \
  2>/dev/null | wc -l)

END=$(date +%s%N)
K_MS=$(( (END - START) / 1000000 ))
echo "   Messages consumed: $K_COUNT"
echo "   Total time:        ${K_MS} ms"
if [ "$K_COUNT" -gt 0 ]; then
  echo "   Throughput:        ~$(( K_COUNT * 1000 / (K_MS + 1) )) msg/s"
fi

echo ""
echo "============================================"
echo "  SUMMARY"
echo "============================================"
printf "  %-15s  %10s  %10s\n" "" "Horizon" "Kafka"
printf "  %-15s  %8s  %8s\n" "Messages" "$H_COUNT" "$K_COUNT"
printf "  %-15s  %8s ms  %8s ms\n" "Time" "$H_MS" "$K_MS"
```

### 2.4 Full Benchmark (All-in-One)

```bash
#!/bin/bash
# test-full-benchmark.sh
# Complete benchmark: Horizon vs Kafka KRaft
# Uses ONLY Kafka CLI tools (.sh) from /opt/kafka/bin/
# All connections go through Docker bridge network (fair comparison)
#
# Usage: ./test-full-benchmark.sh [NUM_MESSAGES] [MESSAGE_SIZE_BYTES]

set -e

NUM_MESSAGES=${1:-1000}
MESSAGE_SIZE=${2:-1024}
TOPIC="bench-$(date +%s)"
KAFKA_BIN="/opt/kafka/bin"
TOOLS="kafka-tools"
TIMEOUT_MS=10000

# Colors
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo -e "${YELLOW}"
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║     Horizon vs Kafka KRaft — Benchmark                      ║"
echo "║     Messages: $NUM_MESSAGES | Size: ${MESSAGE_SIZE} bytes                    ║"
echo "║     Topic: $TOPIC                                ║"
echo "║     Tool: kafka CLI (.sh from /opt/kafka/bin/)               ║"
echo "║     Network: bench-net (same path for both brokers)          ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo -e "${NC}"

# ================================================================
# 1. Check services
# ================================================================
echo -e "${BLUE}[1/7] Checking services...${NC}"

docker exec $TOOLS $KAFKA_BIN/kafka-broker-api-versions.sh \
  --bootstrap-server horizon-test:9092 > /dev/null 2>&1 \
  && echo "  ✓ Horizon OK (horizon-test:9092)" \
  || { echo "  ✗ Horizon not reachable at horizon-test:9092"; exit 1; }

docker exec $TOOLS $KAFKA_BIN/kafka-broker-api-versions.sh \
  --bootstrap-server kafka-kraft-test:9092 > /dev/null 2>&1 \
  && echo "  ✓ Kafka KRaft OK (kafka-kraft-test:9092)" \
  || { echo "  ✗ Kafka KRaft not reachable at kafka-kraft-test:9092"; exit 1; }

# ================================================================
# 2. Create topic on Horizon
# ================================================================
echo -e "\n${BLUE}[2/7] Creating topic on Horizon (kafka-topics.sh)...${NC}"
START=$(date +%s%N)
docker exec $TOOLS $KAFKA_BIN/kafka-topics.sh \
  --bootstrap-server horizon-test:9092 \
  --create --topic "$TOPIC" --partitions 3 --replication-factor 1 \
  > /dev/null 2>&1
H_CREATE=$(( ($(date +%s%N) - START) / 1000000 ))
echo "  ✓ Horizon: ${H_CREATE} ms"

# ================================================================
# 3. Create topic on Kafka
# ================================================================
echo -e "\n${BLUE}[3/7] Creating topic on Kafka (kafka-topics.sh)...${NC}"
START=$(date +%s%N)
docker exec $TOOLS $KAFKA_BIN/kafka-topics.sh \
  --bootstrap-server kafka-kraft-test:9092 \
  --create --topic "$TOPIC" --partitions 3 --replication-factor 1 \
  > /dev/null 2>&1
K_CREATE=$(( ($(date +%s%N) - START) / 1000000 ))
echo "  ✓ Kafka: ${K_CREATE} ms"

# ================================================================
# 4. Generate and copy test messages
# ================================================================
echo -e "\n${BLUE}[4/7] Generating $NUM_MESSAGES test messages (${MESSAGE_SIZE} bytes)...${NC}"
PAYLOAD=$(head -c "$MESSAGE_SIZE" /dev/urandom | base64 | head -c "$MESSAGE_SIZE")
TMPFILE=$(mktemp)
for i in $(seq 1 "$NUM_MESSAGES"); do
  echo "$PAYLOAD" >> "$TMPFILE"
done
docker cp "$TMPFILE" $TOOLS:/tmp/messages.txt > /dev/null
rm -f "$TMPFILE"
echo "  ✓ Messages ready"

# ================================================================
# 5. Produce to Horizon
# ================================================================
echo -e "\n${BLUE}[5/7] Producing to Horizon (kafka-console-producer.sh)...${NC}"
START=$(date +%s%N)
docker exec $TOOLS bash -c \
  "cat /tmp/messages.txt | $KAFKA_BIN/kafka-console-producer.sh \
    --bootstrap-server horizon-test:9092 \
    --topic $TOPIC" 2>/dev/null
H_PRODUCE=$(( ($(date +%s%N) - START) / 1000000 ))
H_PRODUCE_TPS=$(( NUM_MESSAGES * 1000 / (H_PRODUCE + 1) ))
echo "  ✓ Horizon: ${H_PRODUCE} ms (${H_PRODUCE_TPS} msg/s)"

# ================================================================
# 6. Produce to Kafka
# ================================================================
echo -e "\n${BLUE}[6/7] Producing to Kafka (kafka-console-producer.sh)...${NC}"
START=$(date +%s%N)
docker exec $TOOLS bash -c \
  "cat /tmp/messages.txt | $KAFKA_BIN/kafka-console-producer.sh \
    --bootstrap-server kafka-kraft-test:9092 \
    --topic $TOPIC" 2>/dev/null
K_PRODUCE=$(( ($(date +%s%N) - START) / 1000000 ))
K_PRODUCE_TPS=$(( NUM_MESSAGES * 1000 / (K_PRODUCE + 1) ))
echo "  ✓ Kafka: ${K_PRODUCE} ms (${K_PRODUCE_TPS} msg/s)"

# ================================================================
# 7. Consume from both
# ================================================================
echo -e "\n${BLUE}[7/7] Consuming messages (kafka-console-consumer.sh)...${NC}"

# Horizon
START=$(date +%s%N)
H_COUNT=$(docker exec $TOOLS timeout 30 \
  $KAFKA_BIN/kafka-console-consumer.sh \
    --bootstrap-server horizon-test:9092 \
    --topic "$TOPIC" \
    --from-beginning \
    --timeout-ms "$TIMEOUT_MS" \
  2>/dev/null | wc -l)
H_CONSUME=$(( ($(date +%s%N) - START) / 1000000 ))
H_CONSUME_TPS=$(( H_COUNT * 1000 / (H_CONSUME + 1) ))
echo "  ✓ Horizon: $H_COUNT msgs in ${H_CONSUME} ms (${H_CONSUME_TPS} msg/s)"

# Kafka
START=$(date +%s%N)
K_COUNT=$(docker exec $TOOLS timeout 30 \
  $KAFKA_BIN/kafka-console-consumer.sh \
    --bootstrap-server kafka-kraft-test:9092 \
    --topic "$TOPIC" \
    --from-beginning \
    --timeout-ms "$TIMEOUT_MS" \
  2>/dev/null | wc -l)
K_CONSUME=$(( ($(date +%s%N) - START) / 1000000 ))
K_CONSUME_TPS=$(( K_COUNT * 1000 / (K_CONSUME + 1) ))
echo "  ✓ Kafka: $K_COUNT msgs in ${K_CONSUME} ms (${K_CONSUME_TPS} msg/s)"

# ================================================================
# Results
# ================================================================
echo -e "\n${GREEN}"
echo "╔══════════════════════════════════════════════════════════════════╗"
echo "║                         RESULTS                                 ║"
echo "║  Tool: kafka-console-producer.sh / kafka-console-consumer.sh    ║"
echo "║  Network: bench-net (symmetric path to both brokers)            ║"
echo "╠══════════════════════════════════════════════════════════════════╣"
echo "║                                                                 ║"
printf "║  %-22s │ %12s │ %12s       ║\n" "Operation" "Horizon" "Kafka KRaft"
echo "║  ──────────────────────┼──────────────┼──────────────       ║"
printf "║  %-22s │ %10s ms │ %10s ms       ║\n" "Create topic"  "$H_CREATE"  "$K_CREATE"
printf "║  %-22s │ %10s ms │ %10s ms       ║\n" "Produce ${NUM_MESSAGES} msgs"  "$H_PRODUCE"  "$K_PRODUCE"
printf "║  %-22s │ %8s msg/s │ %8s msg/s       ║\n" "  produce throughput"  "$H_PRODUCE_TPS"  "$K_PRODUCE_TPS"
printf "║  %-22s │ %10s ms │ %10s ms       ║\n" "Consume"  "$H_CONSUME"  "$K_CONSUME"
printf "║  %-22s │ %8s msg/s │ %8s msg/s       ║\n" "  consume throughput"  "$H_CONSUME_TPS"  "$K_CONSUME_TPS"
printf "║  %-22s │ %10s    │ %10s          ║\n" "Messages consumed"  "$H_COUNT"  "$K_COUNT"
echo "║                                                                 ║"
echo "╚══════════════════════════════════════════════════════════════════╝"
echo -e "${NC}"
```

---

## 3. Running the Tests

### Quick Start (copy & paste)

```bash
# 1. Create shared network
docker network create bench-net

# 2. Start Horizon
docker run -d --name horizon-test --network bench-net \
  -p 9092:9092 -p 8080:8080 \
  -e HORIZON_ADVERTISED_HOST=horizon-test \
  -v horizon-test-data:/data \
  darioajr/horizon:latest

# 3. Start Kafka KRaft
docker run -d --name kafka-kraft-test --network bench-net \
  -p 9192:9092 \
  -e KAFKA_NODE_ID=1 \
  -e KAFKA_PROCESS_ROLES=broker,controller \
  -e KAFKA_LISTENERS=PLAINTEXT://0.0.0.0:9092,CONTROLLER://0.0.0.0:9093 \
  -e KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://kafka-kraft-test:9092 \
  -e KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER \
  -e KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=PLAINTEXT:PLAINTEXT,CONTROLLER:PLAINTEXT \
  -e KAFKA_CONTROLLER_QUORUM_VOTERS=1@localhost:9093 \
  -e KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1 \
  -e KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR=1 \
  -e KAFKA_TRANSACTION_STATE_LOG_MIN_ISR=1 \
  -e KAFKA_LOG_DIRS=/tmp/kraft-combined-logs \
  -e CLUSTER_ID=MkU3OEVBNTcwNTJENDM2Qk \
  apache/kafka:3.8.0

# 4. Start tools container
docker run -d --name kafka-tools --network bench-net \
  --entrypoint sleep apache/kafka:3.8.0 infinity

# 5. Wait for Kafka JVM startup
echo "Waiting for Kafka to start..."
sleep 20

# 6. Run the full benchmark (1000 msgs × 1KB)
./test-full-benchmark.sh 1000 1024

# 7. Run with heavier load (5000 msgs × 4KB)
./test-full-benchmark.sh 5000 4096
```

### Individual Tests

```bash
# Create topics only
./test-create-topics.sh

# Produce only (1000 messages, 1KB each)
./test-produce.sh 1000 1024

# Consume only (10s timeout)
./test-consume.sh 10
```

### Manual Commands (one at a time)

You can run individual Kafka CLI commands against either broker from the tools container:

```bash
KAFKA_BIN="/opt/kafka/bin"
TOOLS="kafka-tools"

# ---------- CREATE TOPIC ----------
# On Horizon
docker exec $TOOLS $KAFKA_BIN/kafka-topics.sh \
  --bootstrap-server horizon-test:9092 \
  --create --topic my-topic --partitions 3 --replication-factor 1

# On Kafka
docker exec $TOOLS $KAFKA_BIN/kafka-topics.sh \
  --bootstrap-server kafka-kraft-test:9092 \
  --create --topic my-topic --partitions 3 --replication-factor 1

# ---------- LIST TOPICS ----------
# On Horizon
docker exec $TOOLS $KAFKA_BIN/kafka-topics.sh \
  --bootstrap-server horizon-test:9092 --list

# On Kafka
docker exec $TOOLS $KAFKA_BIN/kafka-topics.sh \
  --bootstrap-server kafka-kraft-test:9092 --list

# ---------- DESCRIBE TOPIC ----------
# On Horizon
docker exec $TOOLS $KAFKA_BIN/kafka-topics.sh \
  --bootstrap-server horizon-test:9092 --describe --topic my-topic

# On Kafka
docker exec $TOOLS $KAFKA_BIN/kafka-topics.sh \
  --bootstrap-server kafka-kraft-test:9092 --describe --topic my-topic

# ---------- PRODUCE MESSAGES ----------
# To Horizon (interactive — type messages, Ctrl+C to stop)
docker exec -it $TOOLS $KAFKA_BIN/kafka-console-producer.sh \
  --bootstrap-server horizon-test:9092 --topic my-topic

# To Kafka
docker exec -it $TOOLS $KAFKA_BIN/kafka-console-producer.sh \
  --bootstrap-server kafka-kraft-test:9092 --topic my-topic

# Pipe messages to Horizon
echo -e "msg1\nmsg2\nmsg3" | docker exec -i $TOOLS \
  $KAFKA_BIN/kafka-console-producer.sh \
  --bootstrap-server horizon-test:9092 --topic my-topic

# Pipe messages to Kafka
echo -e "msg1\nmsg2\nmsg3" | docker exec -i $TOOLS \
  $KAFKA_BIN/kafka-console-producer.sh \
  --bootstrap-server kafka-kraft-test:9092 --topic my-topic

# ---------- CONSUME MESSAGES ----------
# From Horizon (from beginning, 5s timeout)
docker exec $TOOLS $KAFKA_BIN/kafka-console-consumer.sh \
  --bootstrap-server horizon-test:9092 \
  --topic my-topic --from-beginning --timeout-ms 5000

# From Kafka
docker exec $TOOLS $KAFKA_BIN/kafka-console-consumer.sh \
  --bootstrap-server kafka-kraft-test:9092 \
  --topic my-topic --from-beginning --timeout-ms 5000

# ---------- CONSUMER GROUP ----------
# Consume with a group (Horizon)
docker exec $TOOLS $KAFKA_BIN/kafka-console-consumer.sh \
  --bootstrap-server horizon-test:9092 \
  --topic my-topic --group test-group --from-beginning --timeout-ms 5000

# List consumer groups (Horizon)
docker exec $TOOLS $KAFKA_BIN/kafka-consumer-groups.sh \
  --bootstrap-server horizon-test:9092 --list

# Describe consumer group (Horizon)
docker exec $TOOLS $KAFKA_BIN/kafka-consumer-groups.sh \
  --bootstrap-server horizon-test:9092 --describe --group test-group

# ---------- DELETE TOPIC ----------
# On Horizon
docker exec $TOOLS $KAFKA_BIN/kafka-topics.sh \
  --bootstrap-server horizon-test:9092 --delete --topic my-topic

# On Kafka
docker exec $TOOLS $KAFKA_BIN/kafka-topics.sh \
  --bootstrap-server kafka-kraft-test:9092 --delete --topic my-topic
```

---

## 4. Timed One-Liners

Quick timing of individual operations using `time`:

```bash
TOOLS="kafka-tools"

# Time topic creation on Horizon
time docker exec $TOOLS /opt/kafka/bin/kafka-topics.sh \
  --bootstrap-server horizon-test:9092 \
  --create --topic timed-test --partitions 3 --replication-factor 1

# Time topic creation on Kafka
time docker exec $TOOLS /opt/kafka/bin/kafka-topics.sh \
  --bootstrap-server kafka-kraft-test:9092 \
  --create --topic timed-test --partitions 3 --replication-factor 1

# Time producing 1000 messages to Horizon
time seq 1 1000 | docker exec -i $TOOLS \
  /opt/kafka/bin/kafka-console-producer.sh \
  --bootstrap-server horizon-test:9092 --topic timed-test

# Time producing 1000 messages to Kafka
time seq 1 1000 | docker exec -i $TOOLS \
  /opt/kafka/bin/kafka-console-producer.sh \
  --bootstrap-server kafka-kraft-test:9092 --topic timed-test

# Time consuming from Horizon
time docker exec $TOOLS /opt/kafka/bin/kafka-console-consumer.sh \
  --bootstrap-server horizon-test:9092 \
  --topic timed-test --from-beginning --timeout-ms 5000 > /dev/null

# Time consuming from Kafka
time docker exec $TOOLS /opt/kafka/bin/kafka-console-consumer.sh \
  --bootstrap-server kafka-kraft-test:9092 \
  --topic timed-test --from-beginning --timeout-ms 5000 > /dev/null
```

---

## 5. Cleanup

```bash
# Stop and remove all containers
docker rm -f horizon-test kafka-kraft-test kafka-tools

# Remove the network
docker network rm bench-net

# Remove volumes
docker volume rm horizon-test-data

# Remove everything at once
docker rm -f horizon-test kafka-kraft-test kafka-tools && \
docker network rm bench-net && \
docker volume prune -f
```

---

## 6. Sample Results

```
╔══════════════════════════════════════════════════════════════════╗
║                         RESULTS                                 ║
║  Tool: kafka-console-producer.sh / kafka-console-consumer.sh    ║
║  Network: bench-net (symmetric path to both brokers)            ║
╠══════════════════════════════════════════════════════════════════╣
║                                                                 ║
║  Operation               │      Horizon │  Kafka KRaft          ║
║  ────────────────────────│──────────────│──────────────         ║
║  Create topic            │       45 ms  │      320 ms          ║
║  Produce 1000 msgs       │     2100 ms  │     3400 ms          ║
║    produce throughput     │   476 msg/s  │   294 msg/s          ║
║  Consume                 │     1200 ms  │     1500 ms          ║
║    consume throughput     │   833 msg/s  │   667 msg/s          ║
║  Messages consumed        │        1000  │        1000          ║
║                                                                 ║
╚══════════════════════════════════════════════════════════════════╝
```

> **Note:** Values above are illustrative. The Kafka CLI tools (`kafka-console-producer.sh`, etc.) add JVM startup overhead (~2-3 seconds) on each invocation, which affects both tests equally since the same tools and same network path are used for both brokers. Actual broker-level performance is higher than what these tools show.

---

## 7. Tips

| Tip | Description |
|-----|-------------|
| **Fair network path** | Both brokers are accessed from `kafka-tools` via Docker bridge — identical network overhead |
| **JVM overhead** | Each `kafka-console-*.sh` invocation starts a JVM (~2-3s). This overhead is constant and applies to both tests equally |
| **Symmetric testing** | Using the same tools container, same CLI, same network ensures methodology is identical |
| **Cold start** | Horizon starts in ~100ms. Kafka KRaft needs 10-20s (JVM + controller init) |
| **Memory** | Horizon: ~15MB RSS. Kafka KRaft: ~300-500MB (JVM) |
| **Docker image size** | `darioajr/horizon`: ~15MB. `apache/kafka`: ~600MB |
| **Interactive mode** | Use `docker exec -it` (with `-it`) for interactive produce sessions where you type messages manually |
| **Batch produce** | Pipe messages via `echo` or `cat` with `docker exec -i` (only `-i`, no `-t`) for batch mode |
| **Advertised listeners** | Kafka's `ADVERTISED_LISTENERS` must use the container name (`kafka-kraft-test:9092`) so the client can reconnect after bootstrap |
