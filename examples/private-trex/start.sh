#!/bin/bash
# Self-contained setup for the private-trex demo.
#
# Clones repos, generates ZKP artifacts, builds Docker image, deploys, runs.
#
# Usage:
#   ./start.sh              # Full setup from scratch → ready for demo
#   ./start.sh --demo       # Run the 17-step demo (after setup)
#   ./start.sh --clean      # Tear down everything
#
# Individual steps (for debugging / re-running):
#   ./start.sh --clone      # Clone zeto + paladin repos
#   ./start.sh --circuits   # Generate ZKP circuit artifacts
#   ./start.sh --build      # Build Docker image + TypeScript deps
#   ./start.sh --infra      # Start Besu + Postgres
#   ./start.sh --deploy     # Phase 1: deploy contracts
#   ./start.sh --start      # Phase 2: start Paladin with Zeto domain
#
# Environment variables:
#   WORKSPACE      Override workspace root (default: 3 levels up from this script)
#   ZETO_REPO      Override zeto git repo URL
#   ZETO_BRANCH    Override zeto branch
#   PALADIN_REPO   Override paladin git repo URL
#   PALADIN_BRANCH Override paladin branch

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------
ZETO_REPO="${ZETO_REPO:-https://github.com/NethermindEth/zeto.git}"
ZETO_BRANCH="${ZETO_BRANCH:-feat/aenknr-e-integration}"
PALADIN_REPO="${PALADIN_REPO:-https://github.com/NethermindEth/paladin.git}"
PALADIN_BRANCH="${PALADIN_BRANCH:-feat/trex-example}"

# Workspace root: expects paladin/ and zeto/ side by side
WORKSPACE="${WORKSPACE:-$(cd "$SCRIPT_DIR/../../.." && pwd)}"
ZETO_DIR="$WORKSPACE/zeto"
PALADIN_DIR="$WORKSPACE/paladin"

CONTAINER_NAME="paladin-node1"
POSTGRES_NAME="paladin-postgres"
DOCKER_IMAGE="paladin:test"
DOCKER_NETWORK="testinfra_default"
CONFIG_BASE="paladin-config-base.yaml"
CONFIG_FULL="paladin-config.yaml"
DEPLOY_DATA="data/deploy.json"
TESTINFRA_DIR="$PALADIN_DIR/testinfra"

AENKNRE_CIRCUITS=(
  anon_enc_nullifier_kyc_non_repudiation_enforced
  deposit_kyc_non_repudiation_enforced
  withdraw_nullifier_kyc_enforced
  forced_transfer_nullifier_kyc_enforced
)

log() { echo -e "\033[1;36m>>>\033[0m $1"; }
err() { echo -e "\033[1;31mERR:\033[0m $1" >&2; exit 1; }

# ---------------------------------------------------------------------------
# Clone repos (if not already present)
# ---------------------------------------------------------------------------
clone_repos() {
  log "=== Checking Repositories ==="

  if [ -d "$ZETO_DIR/.git" ]; then
    log "Zeto repo exists at $ZETO_DIR"
  else
    log "Cloning zeto ($ZETO_BRANCH)..."
    git clone --branch "$ZETO_BRANCH" --single-branch --depth 1 "$ZETO_REPO" "$ZETO_DIR"
  fi

  if [ -d "$PALADIN_DIR/.git" ]; then
    log "Paladin repo exists at $PALADIN_DIR"
  else
    log "Cloning paladin ($PALADIN_BRANCH)..."
    git clone --branch "$PALADIN_BRANCH" --single-branch --depth 1 "$PALADIN_REPO" "$PALADIN_DIR"
  fi

  log "Repos ready"
}

# ---------------------------------------------------------------------------
# Generate ZKP circuit artifacts (if not already present)
# ---------------------------------------------------------------------------
generate_circuits() {
  log "=== Checking Circuit Artifacts ==="

  local artifacts_dir="$ZETO_DIR/zkp/artifacts"
  local all_present=true

  for circuit in "${AENKNRE_CIRCUITS[@]}"; do
    if [ ! -f "$artifacts_dir/${circuit}.zkey" ] || \
       [ ! -f "$artifacts_dir/${circuit}-vkey.json" ] || \
       [ ! -d "$artifacts_dir/${circuit}_js" ]; then
      all_present=false
      break
    fi
  done

  if [ "$all_present" = true ]; then
    log "All circuit artifacts present — skipping generation"
    return 0
  fi

  log "Generating circuit artifacts (this takes 20-40 min on first run)..."
  log "Requires: circom (cargo install circom), Node.js"

  # Check circom is installed
  command -v circom >/dev/null 2>&1 || err "circom not found. Install: cargo install circom"

  # Install circuit build deps
  (cd "$ZETO_DIR/zkp/circuits" && npm install --silent)

  # Generate each circuit
  for circuit in "${AENKNRE_CIRCUITS[@]}"; do
    if [ -f "$artifacts_dir/${circuit}.zkey" ]; then
      log "  $circuit — already exists, skipping"
      continue
    fi
    log "  Generating $circuit..."
    (cd "$ZETO_DIR/zkp/circuits" && node scripts/gen.js -c "$circuit")
  done

  # Verify all artifacts were created
  for circuit in "${AENKNRE_CIRCUITS[@]}"; do
    [ -f "$artifacts_dir/${circuit}.zkey" ] || err "Failed to generate $circuit"
  done

  log "Circuit artifacts ready"
}

# ---------------------------------------------------------------------------
# Start infrastructure: Besu + Postgres
# ---------------------------------------------------------------------------
start_infra() {
  log "=== Starting Infrastructure ==="

  # Build besu_bootstrap if needed
  if ! docker image inspect paladin/besu_bootstrap >/dev/null 2>&1; then
    log "Building besu_bootstrap image..."
    docker build -t paladin/besu_bootstrap -f "$TESTINFRA_DIR/besu_bootstrap/Dockerfile" "$TESTINFRA_DIR"
  fi

  # Start Besu
  log "Starting Besu..."
  docker compose -f "$TESTINFRA_DIR/docker-compose-test.yml" up -d besu_free
  log "Waiting for Besu to be healthy..."
  for i in $(seq 1 30); do
    if docker compose -f "$TESTINFRA_DIR/docker-compose-test.yml" ps besu_free 2>/dev/null | grep -q healthy; then
      log "Besu healthy"
      break
    fi
    [ "$i" -eq 30 ] && err "Besu did not become healthy"
    sleep 2
  done

  # Start Postgres on the testinfra network
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
    sleep 3
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
# Phase 1: Deploy contracts (no zeto domain)
# ---------------------------------------------------------------------------
deploy_contracts() {
  log "=== Phase 1: Deploy Contracts ==="

  run_paladin "$CONFIG_BASE"

  log "Deploying Zeto AENKNR-E contracts..."
  npx ts-node src/deploy.ts --config config.json

  [ -f "$DEPLOY_DATA" ] || err "Deploy failed — $DEPLOY_DATA not created"

  local factory_addr
  factory_addr=$(python3 -c "import json; print(json.load(open('$DEPLOY_DATA'))['factoryAddress'])")
  log "Factory deployed: $factory_addr"

  stop_paladin

  log "Resetting database..."
  docker exec "$POSTGRES_NAME" psql -U postgres -d paladin_demo \
    -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"

  generate_config "$factory_addr"
  log "Phase 1 complete"
}

# ---------------------------------------------------------------------------
# Generate paladin-config.yaml with zeto domain
# ---------------------------------------------------------------------------
generate_config() {
  local factory_addr="$1"
  log "Generating $CONFIG_FULL (factory=$factory_addr)"

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
  fromBlock: latest

blockchain:
  http:
    url: http://besu_free:8545
  ws:
    url: ws://besu_free:8546
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
              inline: f970af3738d945e28b2caf245231b12ddfbf0aaa1ee276185f682a0d54924572

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

  [ -f "$SCRIPT_DIR/$CONFIG_FULL" ] || err "No $CONFIG_FULL — run --deploy first"

  run_paladin "$CONFIG_FULL"

  sleep 5
  docker logs "$CONTAINER_NAME" 2>&1 | grep -q "All DOMAIN plugins loaded" \
    && log "Zeto domain loaded" \
    || log "Warning: domain load not confirmed (check: docker logs $CONTAINER_NAME)"

  log "Paladin running at http://localhost:8548"
}

# ---------------------------------------------------------------------------
# Run demo
# ---------------------------------------------------------------------------
run_demo() {
  log "=== Running 17-Step Demo ==="
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
  docker compose -f "$TESTINFRA_DIR/docker-compose-test.yml" down -v 2>/dev/null || true
  rm -f "$SCRIPT_DIR/$CONFIG_FULL"
  log "Done"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
case "${1:-full}" in
  --clone)    clone_repos ;;
  --circuits) generate_circuits ;;
  --infra)    start_infra ;;
  --build)    build_all ;;
  --deploy)   deploy_contracts ;;
  --start)    start_with_domain ;;
  --demo)     run_demo ;;
  --clean)    clean ;;
  full|"")
    clone_repos
    generate_circuits
    start_infra
    build_all
    deploy_contracts
    start_with_domain
    log ""
    log "Ready! Run: ./start.sh --demo"
    ;;
  *)
    cat <<EOF
Usage: $0 [command]

  (no args)   Full setup from scratch → ready for demo
  --demo      Run the 17-step demo
  --clean     Tear down everything

Individual steps:
  --clone     Clone zeto + paladin repos (if not present)
  --circuits  Generate ZKP circuit artifacts (if not present)
  --infra     Start Besu + Postgres
  --build     Build Docker image + TypeScript deps
  --deploy    Phase 1: deploy contracts on-chain
  --start     Phase 2: start Paladin with Zeto domain

Environment:
  WORKSPACE=$WORKSPACE
  ZETO_REPO=$ZETO_REPO
  ZETO_BRANCH=$ZETO_BRANCH
  PALADIN_REPO=$PALADIN_REPO
  PALADIN_BRANCH=$PALADIN_BRANCH
EOF
    exit 1
    ;;
esac
