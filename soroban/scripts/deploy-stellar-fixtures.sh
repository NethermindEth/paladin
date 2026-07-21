#!/usr/bin/env bash
# Copyright © 2026 Kaleido, Inc.
#
# SPDX-License-Identifier: Apache-2.0
#
# Deploys the shared Soroban infrastructure contracts (SaladinFactory + SNotoFactory + SenteFactory)
# to the local stellar/quickstart standalone network (testinfra/docker-compose-test.yml), and
# uploads the SNoto/Sente Wasm - so core/go/noderuntests/componenttest's Stellar acceptance test
# (chapter 14 step 6) can register real instances against them.
#
# This runs at the Gradle/build layer, not inside the Go test itself: this repo's existing
# convention is that Gradle/docker-compose provisions infrastructure and Go tests assume it's
# ready (testinfra:startTestInfra), rather than a Go test shelling out to external tools itself
# (no precedent for that anywhere in this repo's test suite).
#
# Sente's own instances (one `SentePrivacyGroup` per group) are deployed per-genesis, the same way
# SNoto instances are - so, like `snotoWasmHash`, only the *hash* is uploaded here, not an instance.
#
# Writes artifacts/stellar-fixtures.json:
# {"saladinFactoryAddress", "notoSaladinFactoryAddress", "snotoFactoryAddress", "snotoWasmHash",
#  "senteFactoryAddress", "senteWasmHash"}
#
# Two separate SaladinFactory instances are deployed (saladinFactoryAddress, notoSaladinFactoryAddress)
# rather than one shared one: domainmgr's registration-event routing (registrationIndexer's own
# getDomainByAddress) assumes one dedicated registry instance per domain - a design already
# documented in domain.go's processDomainConfig - so two domains sharing one registry in the same
# process (e.g. Sente + Noto configured together in one JVM Testbed) would have their "reg" events
# misrouted to the wrong domain.
set -euo pipefail

cd "$(dirname "$0")/.."

# Manual Stellar-testnet override points (chapter 14/15's "testnet manual demo" workstream): every
# value below defaults to exactly what this script has always hardcoded for local
# stellar_quickstart, so a plain `./deploy-stellar-fixtures.sh` (or the Gradle task that wraps it)
# is unaffected unless one of these env vars is explicitly set. For a manual testnet run:
#   STELLAR_FIXTURE_NETWORK=testnet ./deploy-stellar-fixtures.sh
# "testnet" is already a built-in `stellar` CLI network alias (see `stellar network ls --long`),
# carrying its own correct RPC URL/passphrase - STELLAR_FIXTURE_RPC_URL/STELLAR_FIXTURE_PASSPHRASE
# only matter for a genuinely custom network name the CLI doesn't already know about.
artifacts_dir="artifacts"
fixtures_file="${STELLAR_FIXTURES_FILE:-$artifacts_dir/stellar-fixtures.json}"
network="${STELLAR_FIXTURE_NETWORK:-stellar-quickstart-local}"
case "$network" in
testnet)
	default_rpc_url="https://soroban-testnet.stellar.org/"
	default_network_passphrase="Test SDF Network ; September 2015"
	;;
futurenet)
	default_rpc_url="https://rpc-futurenet.stellar.org/"
	default_network_passphrase="Test SDF Future Network ; October 2022"
	;;
*)
	default_rpc_url="http://localhost:8000/soroban/rpc"
	# "Standalone Network ; February 2017" is the well-known passphrase for a stellar/quickstart
	# `--local` network - see testinfra/docker-compose-test.yml's stellar_quickstart service comment.
	default_network_passphrase="Standalone Network ; February 2017"
	;;
esac
rpc_url="${STELLAR_FIXTURE_RPC_URL:-$default_rpc_url}"
network_passphrase="${STELLAR_FIXTURE_PASSPHRASE:-$default_network_passphrase}"
expected_protocol="${STELLAR_FIXTURE_PROTOCOL_VERSION:-27}"
deployer="${STELLAR_FIXTURE_DEPLOYER:-stellar-fixtures-deployer}"
validate_network="${STELLAR_FIXTURE_VALIDATE_NETWORK:-true}"

if [[ "$validate_network" != "false" ]]; then
	python3 - "$rpc_url" "$network_passphrase" "$expected_protocol" <<'PYVALIDATE'
import json
import sys
import urllib.request

rpc_url, expected_passphrase, expected_protocol = sys.argv[1], sys.argv[2], int(sys.argv[3])
req = urllib.request.Request(
    rpc_url,
    data=json.dumps({"jsonrpc": "2.0", "id": 1, "method": "getNetwork"}).encode(),
    headers={"Content-Type": "application/json"},
)
with urllib.request.urlopen(req, timeout=20) as res:
    payload = json.loads(res.read().decode())
result = payload.get("result") or {}
actual_passphrase = result.get("passphrase")
actual_protocol = result.get("protocolVersion")
if actual_passphrase != expected_passphrase:
    raise SystemExit(f"getNetwork passphrase mismatch: expected {expected_passphrase!r}, got {actual_passphrase!r}")
if int(actual_protocol) != expected_protocol:
    raise SystemExit(f"getNetwork protocol mismatch: expected {expected_protocol}, got {actual_protocol}")
friendbot = result.get("friendbotUrl") or ""
print(f"Validated Stellar network: protocol={actual_protocol} passphrase={actual_passphrase!r} friendbot={friendbot}")
PYVALIDATE
fi

# Only register a custom network alias for a genuinely custom network name - testnet/futurenet/
# mainnet are already built into the `stellar` CLI with their own correct RPC URL/passphrase, and
# overwriting one of those aliases with this script's (possibly stale, quickstart-defaulted)
# rpc_url/network_passphrase would corrupt it for every other use of that alias on this machine.
case "$network" in
testnet | futurenet | mainnet) ;;
*)
	# Idempotent: safe to re-run against an already-running network (e.g. a second local build).
	stellar network add "$network" --rpc-url "$rpc_url" --network-passphrase "$network_passphrase" >/dev/null 2>&1 || true
	;;
esac
stellar keys generate "$deployer" --network "$network" --fund --overwrite >/dev/null

saladin_factory_address=$(stellar contract deploy --wasm "$artifacts_dir/factory.wasm" --source "$deployer" --network "$network" 2>/dev/null | tail -1)
noto_saladin_factory_address=$(stellar contract deploy --wasm "$artifacts_dir/factory.wasm" --source "$deployer" --network "$network" 2>/dev/null | tail -1)
snoto_factory_address=$(stellar contract deploy --wasm "$artifacts_dir/snoto_factory.wasm" --source "$deployer" --network "$network" 2>/dev/null | tail -1)
snoto_wasm_hash=$(stellar contract upload --wasm "$artifacts_dir/snoto.wasm" --source "$deployer" --network "$network" 2>/dev/null | tail -1)
sente_factory_address=$(stellar contract deploy --wasm "$artifacts_dir/sente_factory.wasm" --source "$deployer" --network "$network" 2>/dev/null | tail -1)
sente_wasm_hash=$(stellar contract upload --wasm "$artifacts_dir/sente.wasm" --source "$deployer" --network "$network" 2>/dev/null | tail -1)

cat > "$fixtures_file" <<JSON
{
  "saladinFactoryAddress": "$saladin_factory_address",
  "notoSaladinFactoryAddress": "$noto_saladin_factory_address",
  "snotoFactoryAddress": "$snoto_factory_address",
  "snotoWasmHash": "$snoto_wasm_hash",
  "senteFactoryAddress": "$sente_factory_address",
  "senteWasmHash": "$sente_wasm_hash"
}
JSON

echo "Wrote $fixtures_file:"
cat "$fixtures_file"
