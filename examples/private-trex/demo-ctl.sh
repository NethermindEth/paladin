#!/bin/bash
# demo-ctl.sh — lifecycle control for the private-trex demo stack.
#
# The demo is four independently-launched pieces:
#   1. Besu + Postgres   — testinfra docker-compose ("testinfra" project)
#   2. paladin-node1      — bare `docker run` container
#   3. Block explorer     — Chainlens/Epirus docker-compose ("docker-compose" project, 6 services)
#   4. API server         — host process: `ts-node api/server.ts` on $API_PORT
#
# Left running, Besu mints empty blocks forever (xemptyblockperiodseconds),
# growing the chain DB / explorer index / RAM until the host OOMs and the
# kernel kills Besu (Exited 137). This script lets you PARK the demo when idle
# (stop = zero blocks, ~6-7 GB RAM freed, state preserved) and bring it back on
# demand — and it pins memory limits + restart policy so a live demo can't OOM.
#
# Usage:
#   ./demo-ctl.sh status     # what's up / down + memory
#   ./demo-ctl.sh start      # bring the whole stack up (in dependency order)
#   ./demo-ctl.sh stop       # park the stack (preserves all state, frees RAM)
#   ./demo-ctl.sh restart    # stop then start
#   ./demo-ctl.sh logs [svc] # tail logs (besu|paladin|api|explorer)
#
# `stop`/`start` use `docker stop`/`docker start` (NOT `down`/`rm`) so the chain,
# Postgres DB, and explorer index all survive a park. Use start.sh --clean for a
# full teardown.
#
# Env overrides:
#   CHAINLENS_DIR   explorer compose dir (default: /root/chainlens-free/docker-compose)
#   API_PORT        API server port (default: 3001)
#   NO_EXPLORER=1   skip the explorer (start/stop only besu+paladin+api)
#   NO_HARDEN=1     skip applying mem-limits / restart-policy on start

set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------
PALADIN_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
TESTINFRA_DIR="$PALADIN_DIR/testinfra"
TESTINFRA_COMPOSE="$TESTINFRA_DIR/docker-compose-test.yml"

CHAINLENS_DIR="${CHAINLENS_DIR:-/root/chainlens-free/docker-compose}"
CHAINLENS_COMPOSE=(-f "$CHAINLENS_DIR/docker-compose.yml")
[ -f "$CHAINLENS_DIR/chainlens-extensions/docker-compose-quorum-dev-quickstart.yml" ] && \
  CHAINLENS_COMPOSE+=(-f "$CHAINLENS_DIR/chainlens-extensions/docker-compose-quorum-dev-quickstart.yml")

PALADIN_CONTAINER="paladin-node1"
POSTGRES_CONTAINER="paladin-postgres"
BESU_SERVICE="besu_free"
DOCKER_NETWORK="testinfra_default"
CONFIG_FULL="paladin-config.yaml"
API_PORT="${API_PORT:-3001}"
API_LOG="/tmp/api-besu.log"

# Per-container memory ceilings so no single container can take the whole host.
# Besu is the heap+RocksDB hog; the rest are comfortably under these.
BESU_MEM="3g"
PALADIN_MEM="1536m"
POSTGRES_MEM="512m"
EXPLORER_MEM="768m"   # applied to each explorer container

c1='\033[1;36m'; c2='\033[1;33m'; c3='\033[1;31m'; c0='\033[0m'
log()  { echo -e "${c1}>>>${c0} $1"; }
warn() { echo -e "${c2}WARN:${c0} $1" >&2; }
err()  { echo -e "${c3}ERR:${c0} $1" >&2; exit 1; }

dc_testinfra() { docker compose -f "$TESTINFRA_COMPOSE" "$@"; }
dc_explorer()  { docker compose "${CHAINLENS_COMPOSE[@]}" "$@"; }

container_exists() { docker inspect "$1" >/dev/null 2>&1; }
container_running() { [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = "true" ]; }

# ---------------------------------------------------------------------------
# status
# ---------------------------------------------------------------------------
cmd_status() {
  log "=== Demo stack status ==="
  printf '%-26s %-12s %s\n' "PIECE" "STATE" "DETAIL"
  printf '%-26s %-12s %s\n' "-----" "-----" "------"

  _row() { # name  running?  detail
    local state; container_running "$1" && state="up" || { container_exists "$1" && state="stopped" || state="absent"; }
    printf '%-26s %-12s %s\n' "$2" "$state" "$3"
  }
  _row "$BESU_SERVICE"      "Besu"              "$(docker inspect -f '{{.State.Status}} (exit {{.State.ExitCode}}, OOMKilled={{.State.OOMKilled}})' "$BESU_SERVICE" 2>/dev/null || echo absent)"
  _row "$POSTGRES_CONTAINER" "Postgres"         ""
  _row "$PALADIN_CONTAINER"  "Paladin node"     ""
  for s in mongodb redis ingestion api web nginx; do
    local cn="docker-compose-${s}-1"; container_exists "$cn" && _row "$cn" "Explorer/$s" ""
  done

  local api_state="down"
  if lsof -ti:"$API_PORT" -sTCP:LISTEN >/dev/null 2>&1 || ss -ltn 2>/dev/null | grep -q ":$API_PORT "; then api_state="up"; fi
  printf '%-26s %-12s %s\n' "API server" "$api_state" "port $API_PORT"

  echo
  free -h | head -2
  echo
  log "Besu chain height:"
  curl -s -X POST http://127.0.0.1:8545 -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' 2>/dev/null || echo "  (besu not reachable)"
  echo
}

# ---------------------------------------------------------------------------
# hardening — pin restart policy + memory ceilings on the live containers
# ---------------------------------------------------------------------------
apply_hardening() {
  [ "${NO_HARDEN:-0}" = "1" ] && { log "Skipping hardening (NO_HARDEN=1)"; return 0; }
  log "Applying restart policy + memory limits..."
  _update() { # container mem
    container_exists "$1" || return 0
    if docker update --restart unless-stopped --memory "$2" --memory-swap "$2" "$1" >/dev/null 2>&1; then
      log "  $1: restart=unless-stopped mem=$2"
    else
      warn "  $1: mem-limit update failed (kernel may lack swap accounting) — setting restart policy only"
      docker update --restart unless-stopped "$1" >/dev/null 2>&1 || true
    fi
  }
  _update "$BESU_SERVICE"       "$BESU_MEM"
  _update "$POSTGRES_CONTAINER" "$POSTGRES_MEM"
  _update "$PALADIN_CONTAINER"  "$PALADIN_MEM"
  if [ "${NO_EXPLORER:-0}" != "1" ]; then
    for s in mongodb redis ingestion api web nginx; do _update "docker-compose-${s}-1" "$EXPLORER_MEM"; done
  fi
}

# ---------------------------------------------------------------------------
# start
# ---------------------------------------------------------------------------
wait_besu_healthy() {
  log "Waiting for Besu to be healthy..."
  for i in $(seq 1 30); do
    dc_testinfra ps "$BESU_SERVICE" 2>/dev/null | grep -q healthy && { log "Besu healthy"; return 0; }
    # fall back to RPC probe if no healthcheck reports
    curl -sf -X POST http://127.0.0.1:8545 -H 'Content-Type: application/json' \
      -d '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' >/dev/null 2>&1 && { log "Besu responding"; return 0; }
    sleep 2
  done
  warn "Besu did not report healthy within 60s — continuing anyway"
}

wait_paladin_ready() {
  log "Waiting for Paladin..."
  for i in $(seq 1 60); do
    curl -sf http://127.0.0.1:8548/ -X POST -H 'Content-Type: application/json' \
      -d '{"jsonrpc":"2.0","method":"ptx_getTransactionReceipt","params":["00000000-0000-0000-0000-000000000000"],"id":1}' \
      >/dev/null 2>&1 && { log "Paladin ready (attempt $i)"; return 0; }
    sleep 2
  done
  warn "Paladin not ready within 120s"
}

start_or_run_paladin() {
  if container_exists "$PALADIN_CONTAINER"; then
    docker start "$PALADIN_CONTAINER" >/dev/null && log "paladin-node1 started"
  else
    [ -f "$SCRIPT_DIR/$CONFIG_FULL" ] || err "No $CONFIG_FULL — run start.sh --deploy first"
    log "paladin-node1 absent — creating from $CONFIG_FULL"
    docker run -d --name "$PALADIN_CONTAINER" --network "$DOCKER_NETWORK" \
      -p 8548:8548 -p 8549:8549 \
      -v "$SCRIPT_DIR/$CONFIG_FULL:/app/config.yaml:ro" \
      paladin:test /app/config.yaml engine >/dev/null
  fi
}

start_api() {
  if lsof -ti:"$API_PORT" -sTCP:LISTEN >/dev/null 2>&1; then log "API already listening on :$API_PORT"; return 0; fi
  command -v npx >/dev/null 2>&1 || { warn "npx not found — start the API manually"; return 0; }
  log "Starting API server (ts-node api/server.ts) on :$API_PORT → $API_LOG"
  ( cd "$SCRIPT_DIR" && nohup npx ts-node api/server.ts > "$API_LOG" 2>&1 & )
  for i in $(seq 1 20); do
    lsof -ti:"$API_PORT" -sTCP:LISTEN >/dev/null 2>&1 && { log "API up on :$API_PORT"; return 0; }
    sleep 1
  done
  warn "API did not bind :$API_PORT within 20s — check $API_LOG"
}

cmd_start() {
  log "=== Starting demo stack ==="
  # 1. Besu + Postgres
  log "Starting Besu + Postgres (testinfra)..."
  dc_testinfra up -d "$BESU_SERVICE"
  if container_exists "$POSTGRES_CONTAINER"; then docker start "$POSTGRES_CONTAINER" >/dev/null 2>&1 || true
  else warn "$POSTGRES_CONTAINER absent — run start.sh --infra to create it"; fi
  wait_besu_healthy
  # 2. Paladin
  start_or_run_paladin
  wait_paladin_ready
  # 3. Explorer
  if [ "${NO_EXPLORER:-0}" != "1" ]; then
    if [ -f "$CHAINLENS_DIR/docker-compose.yml" ]; then
      log "Starting block explorer (chainlens)..."
      dc_explorer start 2>/dev/null || dc_explorer up -d
    else warn "Explorer compose not found at $CHAINLENS_DIR — skipping"; fi
  else log "Skipping explorer (NO_EXPLORER=1)"; fi
  # 4. API
  start_api
  # 5. Hardening
  apply_hardening
  echo; log "Stack up."; cmd_status
}

# ---------------------------------------------------------------------------
# stop  (reverse dependency order; preserves all state)
# ---------------------------------------------------------------------------
stop_api() {
  local pids; pids="$(lsof -ti:"$API_PORT" -sTCP:LISTEN 2>/dev/null)"
  if [ -n "$pids" ]; then log "Stopping API server (:$API_PORT)"; kill $pids 2>/dev/null; sleep 1; kill -9 $pids 2>/dev/null || true
  else log "API not running"; fi
}

cmd_stop() {
  log "=== Parking demo stack (state preserved) ==="
  stop_api
  if [ "${NO_EXPLORER:-0}" != "1" ] && [ -f "$CHAINLENS_DIR/docker-compose.yml" ]; then
    log "Stopping block explorer..."; dc_explorer stop 2>/dev/null || true
  fi
  container_running "$PALADIN_CONTAINER" && { log "Stopping paladin-node1..."; docker stop "$PALADIN_CONTAINER" >/dev/null; }
  log "Stopping Besu + Postgres..."
  dc_testinfra stop "$BESU_SERVICE" 2>/dev/null || true
  container_running "$POSTGRES_CONTAINER" && docker stop "$POSTGRES_CONTAINER" >/dev/null || true
  echo; log "Parked. Besu is no longer producing blocks; RAM freed. 'start' to resume."; echo
  free -h | head -2
}

# ---------------------------------------------------------------------------
# logs
# ---------------------------------------------------------------------------
cmd_logs() {
  case "${1:-}" in
    besu)     dc_testinfra logs -f --tail 100 "$BESU_SERVICE" ;;
    paladin)  docker logs -f --tail 100 "$PALADIN_CONTAINER" ;;
    explorer) dc_explorer logs -f --tail 100 ;;
    api)      tail -f "$API_LOG" ;;
    *)        err "logs needs one of: besu | paladin | explorer | api" ;;
  esac
}

# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------
case "${1:-status}" in
  status)  cmd_status ;;
  start)   cmd_start ;;
  stop)    cmd_stop ;;
  restart) cmd_stop; echo; cmd_start ;;
  logs)    shift; cmd_logs "$@" ;;
  *) cat <<EOF
Usage: $0 {status|start|stop|restart|logs <svc>}

  status    show what's up/down + memory + chain height
  start     bring up besu -> paladin -> explorer -> api (+ mem limits, restart policy)
  stop      park the stack (docker stop; preserves chain/db/index, frees RAM)
  restart   stop then start
  logs      tail logs: besu | paladin | explorer | api

Env: CHAINLENS_DIR=$CHAINLENS_DIR  API_PORT=$API_PORT  NO_EXPLORER=  NO_HARDEN=
EOF
    exit 1 ;;
esac
