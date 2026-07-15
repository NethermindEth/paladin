#!/usr/bin/env bash
# Copyright © 2026 Kaleido, Inc.
#
# SPDX-License-Identifier: Apache-2.0
#
# Deploys the shared Soroban infrastructure contracts (SaladinFactory + SNotoFactory) to the local
# stellar/quickstart standalone network (testinfra/docker-compose-test.yml), and uploads the SNoto
# Wasm - so core/go/noderuntests/componenttest's Stellar acceptance test (chapter 14 step 6) can
# register a real SNoto instance against them.
#
# This runs at the Gradle/build layer, not inside the Go test itself: this repo's existing
# convention is that Gradle/docker-compose provisions infrastructure and Go tests assume it's
# ready (testinfra:startTestInfra), rather than a Go test shelling out to external tools itself
# (no precedent for that anywhere in this repo's test suite).
#
# Writes artifacts/stellar-fixtures.json: {"saladinFactoryAddress", "snotoFactoryAddress", "snotoWasmHash"}
set -euo pipefail

cd "$(dirname "$0")/.."

artifacts_dir="artifacts"
fixtures_file="$artifacts_dir/stellar-fixtures.json"
network="stellar-quickstart-local"
rpc_url="http://localhost:8000/soroban/rpc"
# "Standalone Network ; February 2017" is the well-known passphrase for a stellar/quickstart
# `--local` network - see testinfra/docker-compose-test.yml's stellar_quickstart service comment.
network_passphrase="Standalone Network ; February 2017"
deployer="stellar-fixtures-deployer"

# Idempotent: safe to re-run against an already-running network (e.g. a second local build).
stellar network add "$network" --rpc-url "$rpc_url" --network-passphrase "$network_passphrase" >/dev/null 2>&1 || true
stellar keys generate "$deployer" --network "$network" --fund --overwrite >/dev/null

saladin_factory_address=$(stellar contract deploy --wasm "$artifacts_dir/factory.wasm" --source "$deployer" --network "$network" 2>/dev/null | tail -1)
snoto_factory_address=$(stellar contract deploy --wasm "$artifacts_dir/snoto_factory.wasm" --source "$deployer" --network "$network" 2>/dev/null | tail -1)
snoto_wasm_hash=$(stellar contract upload --wasm "$artifacts_dir/snoto.wasm" --source "$deployer" --network "$network" 2>/dev/null | tail -1)

cat > "$fixtures_file" <<JSON
{
  "saladinFactoryAddress": "$saladin_factory_address",
  "snotoFactoryAddress": "$snoto_factory_address",
  "snotoWasmHash": "$snoto_wasm_hash"
}
JSON

echo "Wrote $fixtures_file:"
cat "$fixtures_file"
