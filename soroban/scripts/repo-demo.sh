#!/usr/bin/env bash
# Copyright © 2026 Kaleido, Inc.
#
# SPDX-License-Identifier: Apache-2.0
#
# One-command rebuild/run for the chapter 18 institutional repo demo (interbank repurchase
# agreement settling atomically across two SNoto instances - a bond and shielded cash - via one
# Sente bilateral group) against real Stellar testnet. Mirrors testnet-demo.sh's own structure
# (reset-detection against stale fixtures, friendbot funding, cache-busting for live-chain runs).
#
# Usage: ./repo-demo.sh [options]
#   --bond-amount N       Bond notional Bank A holds and repos to Bank B (default: 1000000)
#   --cash-amount N       Shielded cash notional Bank B pays Bank A for it (default: 500000)
#   --rate N              Repo rate in basis points, agreed privately (default: 500, i.e. 5.00%)
#   --maturity-days N     Days from now the repo matures, converted to a real future ledger
#                         sequence number (default: 7)
#   --haircut N           Repo haircut in basis points, agreed privately (default: 200, i.e. 2.00%)
#   --interactive         Pause with a prompt between the near leg and the far leg, so a live
#                         audience can inspect state before the repo matures (default: on)
#   --no-interactive      Run straight through with no pauses (e.g. for automated verification)
#   --log-level LEVEL     Verbosity of the demo JVM's own RPC-call log lines - trace/debug/info/
#                         warn/error (default: info, i.e. only the === narration and the real
#                         on-chain event dumps; use debug/trace to also see every raw
#                         ptx_sendTransaction/ptx_getTransactionReceipt request/response)
#
# Requires the `stellar` CLI and python3 on PATH, and this repo's artifacts/*.wasm already built
# (`./gradlew :soroban:build` or equivalent - this script only deploys, it doesn't compile).
set -euo pipefail

cd "$(dirname "$0")/.."

bond_amount="1000000"
cash_amount="500000"
rate_bps="500"
maturity_days="7"
haircut_bps="200"
interactive="true"
log_level="info"

while [[ $# -gt 0 ]]; do
	case "$1" in
	--bond-amount)
		bond_amount="$2"
		shift 2
		;;
	--cash-amount)
		cash_amount="$2"
		shift 2
		;;
	--rate)
		rate_bps="$2"
		shift 2
		;;
	--maturity-days)
		maturity_days="$2"
		shift 2
		;;
	--haircut)
		haircut_bps="$2"
		shift 2
		;;
	--interactive)
		interactive="true"
		shift
		;;
	--no-interactive)
		interactive="false"
		shift
		;;
	--log-level)
		log_level="$2"
		shift 2
		;;
	*)
		echo "usage: $0 [--bond-amount N] [--cash-amount N] [--rate N] [--maturity-days N] [--haircut N] [--interactive|--no-interactive] [--log-level LEVEL]" >&2
		exit 1
		;;
	esac
done

artifacts_dir="artifacts"
# Resolved to an absolute path up front (script cwd is this soroban/ directory at this point) -
# the Java test's own runtime working directory is core/java, not wherever this script was
# invoked from - only an absolute path survives that cwd change correctly.
fixtures_file_rel="${STELLAR_FIXTURES_FILE:-$artifacts_dir/stellar-fixtures.json}"
mkdir -p "$(dirname "$fixtures_file_rel")"
fixtures_file="$(cd "$(dirname "$fixtures_file_rel")" && pwd)/$(basename "$fixtures_file_rel")"
rpc_url="${STELLAR_FIXTURE_RPC_URL:-https://soroban-testnet.stellar.org/}"
friendbot_url="${STELLAR_FRIENDBOT_URL:-https://friendbot.stellar.org}"
deployer="${STELLAR_FIXTURE_DEPLOYER:-stellar-fixtures-deployer}"

# reset_detected checks whether a previously-deployed fixture address still resolves on testnet -
# see testnet-demo.sh's own copy of this function for the full rationale (testnet resets roughly
# quarterly per risk R19, and a stale stellar-fixtures.json left over from before a reset points at
# addresses that no longer exist).
reset_detected() {
	local address="$1"
	local out
	if out=$(stellar contract invoke --id "$address" --network testnet --source "$deployer" --send=no -- __repo_demo_reset_probe__ 2>&1); then
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
else
	# Reusing existing fixtures skips deploy-stellar-fixtures.sh entirely, so its own TTL-extension
	# step (see that script's own comment) never runs on this path - meaning every "still resolves,
	# skip redeploy" run would otherwise never refresh these entries' TTL at all, and they'd
	# eventually lapse into archival between repo-demo.sh runs spaced further apart than the last
	# extension's own window. Refresh it every time fixtures are reused, not just on fresh deploys.
	python3 -c "
import json, sys
f = json.load(open(sys.argv[1]))
for k in ('saladinFactoryAddress', 'notoSaladinFactoryAddress', 'cashNotoSaladinFactoryAddress', 'snotoFactoryAddress', 'senteFactoryAddress', 'repoTermsSaladinFactoryAddress', 'repoTermsFactoryAddress', 'testUsdcSacAddress'):
    print('id', f[k])
for k in ('snotoWasmHash', 'senteWasmHash', 'repoTermsWasmHash'):
    print('wasm-hash', f[k])
" "$fixtures_file" | while read -r flag value; do
		stellar contract extend --"$flag" "$value" --ledgers-to-extend 2500000 --source "$deployer" --network testnet >/dev/null 2>&1 || true
	done
	echo "Refreshed TTL (2500000 ledgers, ~4.8 months) on reused fixtures."
fi

# `stellar keys fund` is idempotent against an already-funded identity, so re-running this script
# against an unreset testnet is harmless too. The per-node identities (root/registrar/cashNotary/
# bankA/bankB) resolve and self-fund from inside the Java harness (NodeProcessHarness's own
# resolveAndFundVerifier), since those identities are only known once each node process starts.
stellar keys fund "$deployer" --network testnet

echo "Fixtures ready at $fixtures_file. Node-level identity funding happens inside the Java test"
echo "harness (NodeProcessHarness.resolveAndFundVerifier) as each identity resolves."
echo "Repo terms: bond=$bond_amount, cash=$cash_amount, rate=${rate_bps}bps, maturity=${maturity_days}d, haircut=${haircut_bps}bps, interactive=$interactive"

# --interactive's pause (between the near leg and the far leg) can't be a plain stdin readLine() in
# the test itself: Gradle's `Test` task (unlike `Exec`/`JavaExec`) has no standardInput property, so
# there's no supported way to forward this script's real terminal into the forked test JVM. Instead
# this is a file-signal handoff: TestInstitutionalRepoDemo.pauseForDemo drops a "waiting" marker in
# pause_dir and polls for "continue"; the watcher below (reading from /dev/tty directly, since the
# foreground gradle process below owns this script's own stdin/stdout) prints the real prompt and
# creates it once the presenter hits Enter.
pause_dir=""
watcher_pid=""
if [[ "$interactive" == "true" ]]; then
	pause_dir="$(mktemp -d)"
	(
		while [[ ! -f "$pause_dir/done" ]]; do
			if [[ -f "$pause_dir/waiting" ]]; then
				message="$(cat "$pause_dir/waiting" 2>/dev/null || true)"
				read -r -p ">>> ${message} (press Enter to continue) <<< " _ </dev/tty || true
				rm -f "$pause_dir/waiting"
				touch "$pause_dir/continue"
			fi
			sleep 0.3
		done
	) &
	watcher_pid=$!
fi
cleanup_pause() {
	if [[ -n "$pause_dir" ]]; then
		touch "$pause_dir/done"
		[[ -n "$watcher_pid" ]] && wait "$watcher_pid" 2>/dev/null || true
		rm -rf "$pause_dir"
	fi
}
trap cleanup_pause EXIT

# --rerun: this exercises live external state - Gradle's own up-to-date check otherwise treats an
# unchanged set of -D system properties as "nothing to do" and silently skips actually running the
# test a second time.
#
# -x :testinfra:startTestInfra -x :soroban:deployStellarFixtures: core/java/build.gradle's own
# `test` task unconditionally depends on both. deployStellarFixtures defaults to local quickstart
# (no STELLAR_FIXTURE_NETWORK env var reaches it here) and is upToDateWhen{false} - it would
# unconditionally redeploy a fresh set of quickstart fixtures and overwrite this script's own
# already-validated testnet fixtures.json moments before the test runs otherwise. startTestInfra's
# containers (postgres/besu/stellar_quickstart) are never touched by this test either - each node
# gets its own embedded SQLite database (see TestInstitutionalRepoDemo.java's buildNodeConfig).
(cd .. && ./gradlew :core:java:test --rerun -x :testinfra:startTestInfra -x :soroban:deployStellarFixtures \
	--tests "io.kaleido.paladin.TestInstitutionalRepoDemo" \
	-Dpaladin.test.stellar.rpcUrl="$rpc_url" \
	-Dpaladin.test.stellar.networkPassphrase="Test SDF Network ; September 2015" \
	-Dpaladin.test.stellar.friendbotUrl="$friendbot_url" \
	-Dpaladin.test.stellar.network=testnet \
	-Dpaladin.test.stellar.pollIterations=360 \
	-Dpaladin.demo.bondAmount="$bond_amount" \
	-Dpaladin.demo.cashAmount="$cash_amount" \
	-Dpaladin.demo.rateBps="$rate_bps" \
	-Dpaladin.demo.maturityDays="$maturity_days" \
	-Dpaladin.demo.haircutBps="$haircut_bps" \
	-Dpaladin.demo.interactive="$interactive" \
	-Dpaladin.demo.pauseDir="$pause_dir" \
	-Dpaladin.demo.logLevel="$log_level")
