#!/bin/bash
# Sepolia testnet lifecycle for the private-trex demo.
#
# Connects to Sepolia via Alchemy (no local Besu). Still runs Postgres
# locally and uses the same Docker image as the Besu flow.
#
# Usage:
#   ./start-sepolia.sh              # First time: deploy infra contracts + start Paladin
#   ./start-sepolia.sh --demo       # Run the 17-step demo (repeatable)
#   ./start-sepolia.sh --start      # Restart Paladin with Zeto domain
#   ./start-sepolia.sh --clean      # Stop containers
#
# Individual steps (for debugging / re-running):
#   ./start-sepolia.sh --build      # Build Docker image + TypeScript deps
#   ./start-sepolia.sh --deploy     # Phase 1: deploy contracts (skips if already deployed)
#
# Requires .env with ALCHEMY_API_KEY and WALLET_SEED.

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

# ---------------------------------------------------------------------------
# Load .env
# ---------------------------------------------------------------------------
if [ ! -f "$SCRIPT_DIR/.env" ]; then
  echo "ERROR: .env not found. Copy .env.example to .env and fill in values." >&2
  exit 1
fi
set -a
source "$SCRIPT_DIR/.env"
set +a

if [ -z "${ALCHEMY_API_KEY:-}" ]; then
  echo "ERROR: ALCHEMY_API_KEY not set in .env" >&2
  exit 1
fi
if [ -z "${WALLET_SEED:-}" ]; then
  echo "ERROR: WALLET_SEED not set in .env" >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------
ALCHEMY_HTTP_URL="https://eth-sepolia.g.alchemy.com/v2/${ALCHEMY_API_KEY}"
ALCHEMY_WS_URL="wss://eth-sepolia.g.alchemy.com/v2/${ALCHEMY_API_KEY}"

# Workspace root: expects paladin/ and zeto/ side by side
WORKSPACE="${WORKSPACE:-$(cd "$SCRIPT_DIR/../../.." && pwd)}"
PALADIN_DIR="$WORKSPACE/paladin"

CONTAINER_NAME="paladin-node1"
POSTGRES_NAME="paladin-postgres"
DOCKER_IMAGE="paladin:test"
DOCKER_NETWORK="paladin-sepolia"
CONFIG_BASE="paladin-config-base-sepolia.yaml"
CONFIG_FULL="paladin-config.yaml"
DEPLOY_DATA="data/deploy.json"

log() { echo -e "\033[1;36m>>>\033[0m $1"; }
err() { echo -e "\033[1;31mERR:\033[0m $1" >&2; exit 1; }

# Verify required tools are available
for cmd in docker curl jq npx; do
  command -v "$cmd" >/dev/null 2>&1 || err "$cmd is required but not found"
done

# ---------------------------------------------------------------------------
# Start infrastructure: Postgres only (no Besu)
# ---------------------------------------------------------------------------
start_infra() {
  log "=== Starting Infrastructure (Postgres) ==="

  # Create network if it doesn't exist
  docker network inspect "$DOCKER_NETWORK" >/dev/null 2>&1 \
    || docker network create "$DOCKER_NETWORK"

  if docker ps -q -f "name=^${POSTGRES_NAME}$" | grep -q .; then
    log "Postgres already running"
  else
    docker rm -f "$POSTGRES_NAME" >/dev/null 2>&1 || true
    log "Starting Postgres..."
    docker run -d --name "$POSTGRES_NAME" \
      --network "$DOCKER_NETWORK" \
      -e POSTGRES_USER=postgres \
      -e POSTGRES_PASSWORD=my-secret \
      -p 5433:5432 \
      postgres:17.6 \
      -c 'max_connections=200'
    # Wait for Postgres to accept connections (not just container start)
    log "Waiting for Postgres to accept connections..."
    for i in $(seq 1 15); do
      docker exec "$POSTGRES_NAME" pg_isready -U postgres >/dev/null 2>&1 && break
      [ "$i" -eq 15 ] && err "Postgres did not become ready"
      sleep 1
    done
  fi

  # Create DB if needed
  docker exec "$POSTGRES_NAME" psql -U postgres -tc \
    "SELECT 1 FROM pg_database WHERE datname='paladin_demo'" | grep -q 1 \
    || docker exec "$POSTGRES_NAME" psql -U postgres -c "CREATE DATABASE paladin_demo;"

  log "Infrastructure ready"
}

# ---------------------------------------------------------------------------
# Build Docker image + TypeScript deps
# ---------------------------------------------------------------------------
build_all() {
  log "=== Building ==="

  if ! docker image inspect "$DOCKER_IMAGE" >/dev/null 2>&1; then
    log "Building $DOCKER_IMAGE from $WORKSPACE (10-20 min first build)..."
    docker build -t "$DOCKER_IMAGE" -f "$PALADIN_DIR/Dockerfile" "$WORKSPACE"
  else
    log "$DOCKER_IMAGE exists (docker rmi $DOCKER_IMAGE to rebuild)"
  fi

  log "Building TypeScript dependencies..."
  (cd "$PALADIN_DIR/sdk/typescript" && npm install --silent && npm run build)
  (cd "$PALADIN_DIR/examples/common" && npm install --silent && npm run build)
  (cd "$SCRIPT_DIR" && npm install --silent)

  log "Build complete"
}

# ---------------------------------------------------------------------------
# Paladin container helpers
# ---------------------------------------------------------------------------
wait_for_paladin() {
  log "Waiting for Paladin..."
  for i in $(seq 1 60); do
    if curl -sf http://127.0.0.1:8548/ -X POST \
       -H 'Content-Type: application/json' \
       -d '{"jsonrpc":"2.0","method":"ptx_getTransactionReceipt","params":["00000000-0000-0000-0000-000000000000"],"id":1}' \
       >/dev/null 2>&1; then
      log "Paladin ready (attempt $i)"
      return 0
    fi
    sleep 2
  done
  err "Paladin did not start within 120s"
}

stop_paladin() {
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
}

run_paladin() {
  local config_file="$1"
  stop_paladin
  log "Starting Paladin with $config_file"
  docker run -d --name "$CONTAINER_NAME" \
    --network "$DOCKER_NETWORK" \
    -p 8548:8548 -p 8549:8549 \
    -v "$SCRIPT_DIR/$config_file:/app/config.yaml:ro" \
    "$DOCKER_IMAGE" \
    /app/config.yaml engine
  wait_for_paladin
}

# ---------------------------------------------------------------------------
# Template a config file: replace placeholders with env values
# ---------------------------------------------------------------------------
template_config() {
  local input="$1"
  local output="$2"
  sed \
    -e "s|\${ALCHEMY_HTTP_URL}|${ALCHEMY_HTTP_URL}|g" \
    -e "s|\${ALCHEMY_WS_URL}|${ALCHEMY_WS_URL}|g" \
    -e "s|\${WALLET_SEED}|${WALLET_SEED}|g" \
    "$input" > "$output"
}

# ---------------------------------------------------------------------------
# Phase 1: Deploy contracts (no zeto domain)
# ---------------------------------------------------------------------------
deploy_contracts() {
  log "=== Phase 1: Deploy Contracts ==="

  if [ -f "$DEPLOY_DATA" ]; then
    local factory_addr
    factory_addr=$(jq -r '.factoryAddress' "$DEPLOY_DATA")
    log "Infrastructure already deployed (factory=$factory_addr)"
    log "To force redeploy: rm $DEPLOY_DATA"
    return 0
  fi

  # Template base config with Sepolia URLs + wallet seed
  local templated_base="paladin-config-base-sepolia-resolved.yaml"
  template_config "$CONFIG_BASE" "$templated_base"

  run_paladin "$templated_base"

  log "Deploying Zeto AENKNR-E contracts to Sepolia..."
  npx ts-node src/deploy.ts --config config.json

  [ -f "$DEPLOY_DATA" ] || err "Deploy failed — $DEPLOY_DATA not created"

  local factory_addr
  factory_addr=$(jq -r '.factoryAddress' "$DEPLOY_DATA")
  log "Factory deployed: $factory_addr"

  stop_paladin
  rm -f "$templated_base"

  log "Resetting database..."
  docker exec "$POSTGRES_NAME" psql -U postgres -d paladin_demo \
    -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"

  generate_config "$factory_addr"
  log "Phase 1 complete"
}

# ---------------------------------------------------------------------------
# Generate paladin-config.yaml with zeto domain (Sepolia URLs)
# ---------------------------------------------------------------------------
generate_config() {
  local factory_addr="$1"
  log "Generating $CONFIG_FULL (factory=$factory_addr)"

  # WARNING: This file contains the WALLET_SEED in cleartext. It is gitignored
  # but exists on disk while Paladin runs. This is inherent to how Paladin
  # consumes its config. Do not copy this file or leave it on shared filesystems.

  # Always use "latest" for fromBlock. Paladin discovers the Zeto token type
  # from newZeto() events at runtime — it does NOT need to replay the factory
  # registration from the deploy block. Using the deploy block forces the
  # indexer to catch up hundreds of blocks on every restart, which takes 15+
  # minutes on Alchemy's free tier due to rate limiting.
  #
  # The deployBlockNumber in deploy.json is preserved for reference but is
  # not used in the config.
  local from_block="latest"

  cat > "$SCRIPT_DIR/$CONFIG_FULL" <<YAML
nodeName: node1

db:
  type: postgres
  postgres:
    dsn: 'postgres://postgres:my-secret@paladin-postgres:5432/paladin_demo?sslmode=disable'
    autoMigrate: true
    migrationsDir: '/app/db/migrations/postgres'

rpcServer:
  http:
    port: 8548
    address: 0.0.0.0
    defaultRequestTimeout: 10m
  ws:
    port: 8549
    address: 0.0.0.0

blockIndexer:
  fromBlock: ${from_block}

blockchain:
  http:
    url: ${ALCHEMY_HTTP_URL}
  ws:
    url: ${ALCHEMY_WS_URL}
    initialConnectAttempts: 25

log:
  level: info

wallets:
  - name: wallet1
    keySelector: .*
    signer:
      keyDerivation:
        type: 'bip32'
      keyStore:
        type: 'static'
        static:
          keys:
            seed:
              encoding: hex
              inline: ${WALLET_SEED}

domains:
  zeto:
    plugin:
      type: c-shared
      library: /app/domains/libzeto.so
    registryAddress: '${factory_addr}'
    allowSigning: true
    config:
      domainContracts:
        implementations:
          - name: Zeto_AnonEncNullifierKycNonRepudiationEnforced
            circuits:
              deposit:
                name: deposit_kyc_non_repudiation_enforced
                usesNullifiers: true
                usesEncryption: true
                usesKyc: true
                usesNonRepudiation: true
                usesEnforcement: true
              withdraw:
                name: withdraw_nullifier_kyc_enforced
                usesNullifiers: true
                usesEncryption: true
                usesKyc: true
                usesNonRepudiation: true
                usesEnforcement: true
              transfer:
                name: anon_enc_nullifier_kyc_non_repudiation_enforced
                usesNullifiers: true
                usesEncryption: true
                usesKyc: true
                usesNonRepudiation: true
                usesEnforcement: true
              transferLocked:
                name: ""
              forcedTransfer:
                name: forced_transfer_nullifier_kyc_enforced
                usesNullifiers: true
                usesEncryption: true
                usesKyc: true
                usesNonRepudiation: true
                usesEnforcement: true
      snarkProver:
        circuitsDir: /app/domains/zeto/zkp
        provingKeysDir: /app/domains/zeto/zkp
YAML
}

# ---------------------------------------------------------------------------
# Phase 2: Start with zeto domain
# ---------------------------------------------------------------------------
start_with_domain() {
  log "=== Phase 2: Start with Zeto Domain ==="

  [ -f "$DEPLOY_DATA" ] || err "No $DEPLOY_DATA — run ./start-sepolia.sh first to deploy infrastructure"

  # Always regenerate config. This ensures fromBlock is "latest" (no stale
  # block numbers from a previous run) and Alchemy URLs reflect current .env.
  local factory_addr
  factory_addr=$(jq -r '.factoryAddress' "$DEPLOY_DATA")
  generate_config "$factory_addr"

  run_paladin "$CONFIG_FULL"

  sleep 5
  docker logs "$CONTAINER_NAME" 2>&1 | grep -q "All DOMAIN plugins loaded" \
    && log "Zeto domain loaded" \
    || log "Warning: domain load not confirmed (check: docker logs $CONTAINER_NAME)"

  log "Paladin running at http://localhost:8548"
}

# ---------------------------------------------------------------------------
# Ensure Paladin is running (start if not)
# ---------------------------------------------------------------------------
ensure_paladin() {
  if docker ps -q -f "name=^${CONTAINER_NAME}$" | grep -q .; then
    # Container exists — verify it's responsive
    if curl -sf http://127.0.0.1:8548/ -X POST \
       -H 'Content-Type: application/json' \
       -d '{"jsonrpc":"2.0","method":"ptx_getTransactionReceipt","params":["00000000-0000-0000-0000-000000000000"],"id":1}' \
       >/dev/null 2>&1; then
      log "Paladin already running"
      return 0
    fi
    log "Paladin container exists but not responding — restarting"
  fi

  start_infra
  start_with_domain
}

# ---------------------------------------------------------------------------
# Run demo
# ---------------------------------------------------------------------------
run_demo() {
  log "=== Running 17-Step Demo ==="
  ensure_paladin
  cd "$SCRIPT_DIR"
  npx ts-node src/index.ts --config config.json
}

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------
clean() {
  log "=== Cleaning Up ==="
  stop_paladin
  docker rm -f "$POSTGRES_NAME" >/dev/null 2>&1 || true
  docker network rm "$DOCKER_NETWORK" >/dev/null 2>&1 || true
  rm -f "$SCRIPT_DIR/$CONFIG_FULL"
  rm -f "$SCRIPT_DIR/paladin-config-base-sepolia-resolved.yaml"
  log "Done"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
case "${1:-full}" in
  --build)    build_all ;;
  --deploy)   start_infra; deploy_contracts ;;
  --start)    start_infra; start_with_domain ;;
  --demo)     run_demo ;;
  --clean)    clean ;;
  full|"")
    start_infra
    build_all
    deploy_contracts
    start_with_domain
    log ""
    log "Ready! Run: ./start-sepolia.sh --demo"
    ;;
  *)
    cat <<EOF
Usage: $0 [command]

  (no args)   First time: deploy infra contracts + start Paladin on Sepolia
  --demo      Run the 17-step demo (repeatable)
  --start     Restart Paladin with Zeto domain
  --clean     Stop containers

Individual steps:
  --build     Build Docker image + TypeScript deps
  --deploy    Phase 1: deploy contracts (skips if data/deploy.json exists)

Requires .env with:
  ALCHEMY_API_KEY   Alchemy API key for Sepolia
  WALLET_SEED       BIP32 seed (hex, 32 bytes) — fund derived root address first

Environment:
  WORKSPACE=$WORKSPACE
EOF
    exit 1
    ;;
esac
