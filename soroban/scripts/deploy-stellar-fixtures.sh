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
# Also deploys a plain (non-AUTH_REQUIRED) test classic asset, wrapped as a SAC, standing in for
# a real institutional stablecoin (chapter 18's repo demo - "USDC-like", not actual Circle USDC,
# since this script has no access to Circle's issuer keys). Mirrors
# core/go/noderuntests/componenttest/stellar_asset_test.go's own generateAndFundIssuer/
# deploySACForAsset helpers, just as a persistent named CLI identity (like $deployer) instead of a
# throwaway keypair, so later steps (the demo harness funding a bank's test-asset balance) can
# reference it by name across runs.
#
# Writes artifacts/stellar-fixtures.json:
# {"saladinFactoryAddress", "notoSaladinFactoryAddress", "cashNotoSaladinFactoryAddress",
#  "snotoFactoryAddress", "snotoWasmHash", "senteFactoryAddress", "senteWasmHash",
#  "repoTermsSaladinFactoryAddress", "repoTermsFactoryAddress", "repoTermsWasmHash",
#  "testUsdcSacAddress", "testUsdcIssuerAddress"}
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
    # User-Agent is required: public Stellar testnet's RPC sits behind a WAF that returns a bare
    # HTTP 403 for urllib's default "Python-urllib/x.y" User-Agent specifically (confirmed - curl
    # and a browser-like UA both pass against the exact same endpoint/payload).
    headers={"Content-Type": "application/json", "User-Agent": "curl/8.5.0"},
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
# A THIRD dedicated SaladinFactory, distinct from both of the above - chapter 18's repo demo
# configures two separate "noto" domain instances on the same node (bond, with no real SAC; cash,
# with stellarSacAddress set) since DomainConfig.StellarSacAddress is fixed per domain-config, not
# overridable per deploy/constructor call (domains/noto/internal/noto/deploy_stellar.go's
# stellarPrepareDeploy reads it once from n.config) - and the one-dedicated-registry-per-domain
# constraint above means those two noto configs can't share a registry with each other either.
cash_noto_saladin_factory_address=$(stellar contract deploy --wasm "$artifacts_dir/factory.wasm" --source "$deployer" --network "$network" 2>/dev/null | tail -1)
# A FOURTH dedicated SaladinFactory, for the same one-dedicated-registry-per-domain reason as the
# three above - repo-terms (chapter 18) is its own independent domain, sharing a registry with any
# of noto/cash-noto/sente would misroute its own "reg" events the same way those three would
# misroute each other's.
repo_terms_saladin_factory_address=$(stellar contract deploy --wasm "$artifacts_dir/factory.wasm" --source "$deployer" --network "$network" 2>/dev/null | tail -1)
snoto_factory_address=$(stellar contract deploy --wasm "$artifacts_dir/snoto_factory.wasm" --source "$deployer" --network "$network" 2>/dev/null | tail -1)
snoto_wasm_hash=$(stellar contract upload --wasm "$artifacts_dir/snoto.wasm" --source "$deployer" --network "$network" 2>/dev/null | tail -1)
sente_factory_address=$(stellar contract deploy --wasm "$artifacts_dir/sente_factory.wasm" --source "$deployer" --network "$network" 2>/dev/null | tail -1)
sente_wasm_hash=$(stellar contract upload --wasm "$artifacts_dir/sente.wasm" --source "$deployer" --network "$network" 2>/dev/null | tail -1)
repo_terms_factory_address=$(stellar contract deploy --wasm "$artifacts_dir/repo_terms_factory.wasm" --source "$deployer" --network "$network" 2>/dev/null | tail -1)
repo_terms_wasm_hash=$(stellar contract upload --wasm "$artifacts_dir/repo_terms.wasm" --source "$deployer" --network "$network" 2>/dev/null | tail -1)

# Testnet has a real, shared "Test USDC" contract already live and in ongoing use -
# CAUGJT4GREIY3WHOUUU5RIUDGSPVREF5CDCYJOWMHOVT2GWQT5JEETGJ, confirmed for real (name()="Test
# USDC", symbol()="USDC") and confirmed genuinely permissionless (a real mint() call from an
# unrelated throwaway identity succeeded with no admin/require_auth gate at all). It's a native
# Soroban token contract, NOT a classic-asset-backed SAC - Stellar's classic trustline concept
# doesn't apply to it, so funding an account is a single direct mint() call, no issuer/trustline
# dance needed. Reusing it gives the demo a real, recognizable "USDC" story other testnet tooling
# already knows about, instead of a wrapped classic asset only this demo's own fixtures know of.
# No such shared official token exists on a private local quickstart chain, so that path keeps
# deploying its own throwaway classic asset + SAC wrapper exactly as before.
if [[ "$network" == "testnet" ]]; then
	test_usdc_sac_address="CAUGJT4GREIY3WHOUUU5RIUDGSPVREF5CDCYJOWMHOVT2GWQT5JEETGJ"
	# Empty (not a classic-asset issuer at all) is the signal TestInstitutionalRepoDemo.java's own
	# fundBankBWithTestUsdc reads to pick the direct-mint path over the trustline+payment one.
	test_usdc_issuer_address=""
else
	test_usdc_issuer="${STELLAR_FIXTURE_TEST_USDC_ISSUER:-stellar-fixtures-test-usdc-issuer}"
	stellar keys generate "$test_usdc_issuer" --network "$network" --fund --overwrite >/dev/null
	test_usdc_issuer_address=$(stellar keys address "$test_usdc_issuer")
	test_usdc_sac_address=$(stellar contract asset deploy --asset "TUSD:$test_usdc_issuer_address" --source "$test_usdc_issuer" --network "$network" 2>/dev/null | tail -1)
fi

# TTL management (only meaningful on a real, persistent network - quickstart's local chain is
# thrown away with its own docker container, so extending there is harmless but pointless).
# `stellar contract deploy`/`upload` only grant the protocol minimum TTL, not the extension's own
# max - confirmed empirically: a freshly-deployed fixture's TTL was found to have under 7 days left
# after sitting untouched for a few hours. `2500000` ledgers (~4.8 months at Stellar's ~5s ledger
# close) is comfortably below the network's own max-extension-per-call ceiling (~3.1M ledgers
# rejected outright as malformed; ~2.5M confirmed to succeed) - generous enough that a demo run
# every few months keeps these alive indefinitely, and re-extending an already-far-from-expiry
# entry is a confirmed no-op, not an error, so this is safe to run unconditionally on every
# deploy-stellar-fixtures.sh invocation, fresh deploy or not.
if [[ "$network" == "testnet" || "$network" == "futurenet" ]]; then
	extend_ttl() {
		local flag="$1" value="$2"
		stellar contract extend "$flag" "$value" --ledgers-to-extend 2500000 --source "$deployer" --network "$network" >/dev/null 2>&1 || true
	}
	extend_ttl --id "$saladin_factory_address"
	extend_ttl --id "$noto_saladin_factory_address"
	extend_ttl --id "$cash_noto_saladin_factory_address"
	extend_ttl --id "$snoto_factory_address"
	extend_ttl --wasm-hash "$snoto_wasm_hash"
	extend_ttl --id "$sente_factory_address"
	extend_ttl --wasm-hash "$sente_wasm_hash"
	extend_ttl --id "$repo_terms_saladin_factory_address"
	extend_ttl --id "$repo_terms_factory_address"
	extend_ttl --wasm-hash "$repo_terms_wasm_hash"
	# The real shared testnet Test USDC contract isn't ours to manage - its own TTL is whoever
	# operates it own responsibility, not this fixture set's.
	if [[ -n "$test_usdc_issuer_address" ]]; then
		extend_ttl --id "$test_usdc_sac_address"
	fi
	echo "Extended TTL (2500000 ledgers, ~4.8 months) on all fixture contracts/wasm entries."
fi

cat > "$fixtures_file" <<JSON
{
  "saladinFactoryAddress": "$saladin_factory_address",
  "notoSaladinFactoryAddress": "$noto_saladin_factory_address",
  "cashNotoSaladinFactoryAddress": "$cash_noto_saladin_factory_address",
  "snotoFactoryAddress": "$snoto_factory_address",
  "snotoWasmHash": "$snoto_wasm_hash",
  "senteFactoryAddress": "$sente_factory_address",
  "senteWasmHash": "$sente_wasm_hash",
  "repoTermsSaladinFactoryAddress": "$repo_terms_saladin_factory_address",
  "repoTermsFactoryAddress": "$repo_terms_factory_address",
  "repoTermsWasmHash": "$repo_terms_wasm_hash",
  "testUsdcSacAddress": "$test_usdc_sac_address",
  "testUsdcIssuerAddress": "$test_usdc_issuer_address"
}
JSON

echo "Wrote $fixtures_file:"
cat "$fixtures_file"
