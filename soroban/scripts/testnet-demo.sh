#!/usr/bin/env bash
# Copyright © 2026 Kaleido, Inc.
#
# SPDX-License-Identifier: Apache-2.0
#
# One-command rebuild/run for the 3-node SNoto/Sente demo against real Stellar testnet (chapter
# 14/15's "manual testnet demo" workstream). Unlike stellar_quickstart, testnet is a real public
# network that resets roughly quarterly (risk R19) - a stale artifacts/stellar-fixtures.json left
# over from before a reset points at addresses that no longer exist, so this script checks for that
# before doing anything else, and only redeploys when needed rather than burning testnet resources
# (and needing fresh friendbot funding for a brand new deployer) on every run.
#
# Usage: ./testnet-demo.sh [snoto|sente|all]   (defaults to "all")
#
# Requires the `stellar` CLI and python3 on PATH, and this repo's artifacts/*.wasm already built
# (`./gradlew :soroban:build` or equivalent - this script only deploys, it doesn't compile).
set -euo pipefail

cd "$(dirname "$0")/.."

target="${1:-all}"
case "$target" in
snoto | sente | all) ;;
*)
	echo "usage: $0 [snoto|sente|all]" >&2
	exit 1
	;;
esac

artifacts_dir="artifacts"
# Resolved to an absolute path up front (script cwd is this soroban/ directory at this point) -
# run_snoto below hands this same value to `go test`, whose actual runtime working directory is
# always the target package's own directory (core/go/noderuntests/componenttest), not wherever
# `go test` was invoked from - only an absolute path survives that cwd change correctly.
fixtures_file_rel="${STELLAR_FIXTURES_FILE:-$artifacts_dir/stellar-fixtures.json}"
mkdir -p "$(dirname "$fixtures_file_rel")"
fixtures_file="$(cd "$(dirname "$fixtures_file_rel")" && pwd)/$(basename "$fixtures_file_rel")"
rpc_url="${STELLAR_FIXTURE_RPC_URL:-https://soroban-testnet.stellar.org/}"
friendbot_url="${STELLAR_FRIENDBOT_URL:-https://friendbot.stellar.org}"
deployer="${STELLAR_FIXTURE_DEPLOYER:-stellar-fixtures-deployer}"

# reset_detected checks whether a previously-deployed fixture address still resolves on testnet -
# `stellar contract invoke` against a since-reset (nonexistent) contract fails during simulation
# with a distinguishable "contract not found" error, confirmed this session as a viable,
# programmatic reset signal (as opposed to "function not found", which a genuinely live contract
# would return for the same made-up function name - so this can't false-positive on a live
# contract that simply doesn't have this function).
reset_detected() {
	local address="$1"
	local out
	if out=$(stellar contract invoke --id "$address" --network testnet --source "$deployer" --send=no -- __testnet_demo_reset_probe__ 2>&1); then
		return 1 # a real invoke succeeding at all means the contract exists
	fi
	if echo "$out" | grep -q "contract not found"; then
		return 0
	fi
	return 1
}

needs_deploy=true
if [[ -f "$fixtures_file" ]]; then
	saladin_factory_address=$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['saladinFactoryAddress'])" "$fixtures_file")
	if ! reset_detected "$saladin_factory_address"; then
		echo "Existing fixtures at $fixtures_file still resolve on testnet - skipping redeploy."
		needs_deploy=false
	else
		echo "Existing fixtures at $fixtures_file no longer resolve on testnet (reset detected) - redeploying."
	fi
else
	echo "No fixtures file at $fixtures_file yet - deploying for the first time."
fi

if [[ "$needs_deploy" == "true" ]]; then
	STELLAR_FIXTURE_NETWORK=testnet STELLAR_FIXTURES_FILE="$fixtures_file" ./scripts/deploy-stellar-fixtures.sh
fi

# `stellar keys fund` is idempotent against an already-funded identity (no-ops rather than
# erroring), so re-running this script against an unreset testnet is harmless here too. The
# per-node identities the demo processes themselves resolve (root/notary/party2/party3/member1..N)
# are funded from inside each demo's own test harness instead (resolveAndFundVerifier on both the
# Go and Java sides), since those identities are only known once that process actually starts.
stellar keys fund "$deployer" --network testnet

echo "Fixtures ready at $fixtures_file. Node-level identity funding happens inside each demo's own"
echo "test harness (resolveAndFundVerifier on both the Go and Java sides) as each identity resolves,"
echo "since those identities are only known once the node/test process itself starts."

run_snoto() {
	echo "Running SNoto 3-node testnet demo..."
	(cd ../core/go && \
		STELLAR_NODE_CONFIG_PREFIX=testnet \
		STELLAR_FIXTURES_FILE="$fixtures_file" \
		STELLAR_RPC_URL="$rpc_url" \
		STELLAR_NETWORK_PASSPHRASE="Test SDF Network ; September 2015" \
		STELLAR_FRIENDBOT_URL="$friendbot_url" \
		go test -tags stellar_quickstart ./noderuntests/componenttest/... -run TestStellarComponentTest -timeout 20m -v)
}

run_sente() {
	echo "Running Sente real-transition testnet demo..."
	(cd .. && ./gradlew :core:java:test --tests "io.kaleido.paladin.TestSenteRealTransition" \
		-Dpaladin.test.stellar.rpcUrl="$rpc_url" \
		-Dpaladin.test.stellar.networkPassphrase="Test SDF Network ; September 2015" \
		-Dpaladin.test.stellar.friendbotUrl="$friendbot_url")
}

case "$target" in
snoto) run_snoto ;;
sente) run_sente ;;
all)
	run_snoto
	run_sente
	;;
esac
