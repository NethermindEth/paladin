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
# with a distinguishable "Contract not found" error (case as actually emitted by the `stellar` CLI -
# matched case-insensitively below since the exact casing isn't a documented contract), a viable
# programmatic reset signal (as opposed to "function not found", which a genuinely live contract
# would return for the same made-up function name - so this can't false-positive on a live
# contract that simply doesn't have this function).
reset_detected() {
	local address="$1"
	local out
	if out=$(stellar contract invoke --id "$address" --network testnet --source "$deployer" --send=no -- __testnet_demo_reset_probe__ 2>&1); then
		return 1 # a real invoke succeeding at all means the contract exists
	fi
	if echo "$out" | grep -qi "contract not found"; then
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
	# -count=1: this test exercises live, mutable external state (a real blockchain) - Go's test
	# cache would otherwise skip re-running it on a second invocation with unchanged inputs/deps,
	# silently reporting a stale prior result instead of actually talking to testnet again.
	(cd ../core/go && \
		STELLAR_NODE_CONFIG_PREFIX=testnet \
		STELLAR_FIXTURES_FILE="$fixtures_file" \
		STELLAR_RPC_URL="$rpc_url" \
		STELLAR_NETWORK_PASSPHRASE="Test SDF Network ; September 2015" \
		STELLAR_FRIENDBOT_URL="$friendbot_url" \
		go test -tags stellar_quickstart ./noderuntests/componenttest/... -run TestStellarComponentTest -count=1 -timeout 20m -v)
}

run_sente() {
	echo "Running Sente 3-node testnet demo..."
	# NOTE on repeated runs against the SAME (unreset) testnet fixtures: the harness's 3 nodes
	# derive member1/2/3's keys deterministically (same wallet seeds, same fresh-per-run DB
	# allocation order every time), so `sha256(members)` - the deploy salt Sente's own
	# SenteFactory::deploy_group uses to let independently-assembling members agree on an address
	# with no prior coordination - is IDENTICAL every run too. The first successful run genuinely
	# deploys a group at that address; every subsequent run against the same fixtures correctly
	# fails with a Soroban `Storage::ExistingValue` trying to redeploy over it - this is the
	# address-collision-avoidance scheme working as intended, not a flaky test. A genuinely fresh
	# run needs either fresh fixtures (redeploy senteFactoryAddress) or a fresh reset of testnet.
	#
	# TestSenteThreeNodeHarness (not TestSenteRealTransition's single-JVM Testbed simulation) is
	# the genuine 3-separate-process demo this script's own header promises - channelAccountPoolSize/
	# StartingBalance are cut down the same way stellar.testnet.node1.config.yaml's Go counterpart
	# is (8->2, "5"->"3"): each channel account is a real on-chain create+fund against a real,
	# ~5s-ledger-close network, not quickstart's fast local one.
	#
	# -x :testinfra:startTestInfra -x :soroban:deployStellarFixtures: core/java/build.gradle's own
	# `test` task unconditionally depends on both. deployStellarFixtures defaults to local
	# quickstart (no STELLAR_FIXTURE_NETWORK env var reaches it here) and is upToDateWhen{false} -
	# it would unconditionally redeploy a FRESH set of quickstart fixtures and overwrite this
	# script's own already-validated testnet fixtures.json moments before the test runs otherwise.
	# This script's own reset-aware deploy above already handles fixture deployment correctly.
	#
	# --rerun: like SNoto's -count=1 above, this exercises live external state - Gradle's own
	# up-to-date check otherwise treats an unchanged set of -D system properties as "nothing to do"
	# and silently skips actually running the test a second time (confirmed: "Task :core:java:test
	# UP-TO-DATE", 3s total, no node processes ever started).
	(cd .. && ./gradlew :core:java:test --rerun -x :testinfra:startTestInfra -x :soroban:deployStellarFixtures \
		--tests "io.kaleido.paladin.TestSenteThreeNodeHarness" \
		-Dpaladin.test.stellar.rpcUrl="$rpc_url" \
		-Dpaladin.test.stellar.networkPassphrase="Test SDF Network ; September 2015" \
		-Dpaladin.test.stellar.friendbotUrl="$friendbot_url" \
		-Dpaladin.test.stellar.channelAccountPoolSize=2 \
		-Dpaladin.test.stellar.channelAccountStartingBalance=3 \
		-Dpaladin.test.stellar.pollIterations=360)
}

case "$target" in
snoto) run_snoto ;;
sente) run_sente ;;
all)
	run_snoto
	run_sente
	;;
esac
