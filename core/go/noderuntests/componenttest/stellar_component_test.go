//go:build stellar_quickstart

/*
 * Copyright © 2026 Kaleido, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with
 * the License. You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on
 * an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the
 * specific language governing permissions and limitations under the License.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package componenttest

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	testutils "github.com/LFDT-Paladin/paladin/core/noderuntests/pkg"
	"github.com/LFDT-Paladin/paladin/core/noderuntests/pkg/domains"
	"github.com/LFDT-Paladin/paladin/domains/noto/pkg/types"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldclient"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/algorithms"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/verifiers"
	"github.com/google/uuid"
	"github.com/hyperledger/firefly-signer/pkg/abi"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// notoStellarConstructorABI describes only the fields chapter 14 step 2's stellarPrepareDeploy
// actually reads (types.ConstructorParams's notary/notaryMode) - domains/noto/pkg/types doesn't
// export a constructor ABI of its own (only function ABIs), the same gap the EVM-side deploy
// helper (domains/integration-test/helpers/noto_helper.go) also works around by building its own
// inline shape rather than sourcing one from the domain package.
var notoStellarConstructorABI = &abi.ABI{{
	Type: abi.Constructor,
	Inputs: abi.ParameterArray{
		{Name: "notary", Type: "string"},
		{Name: "notaryMode", Type: "string"},
	},
}}

type stellarFixtures struct {
	SaladinFactoryAddress string `json:"saladinFactoryAddress"`
	SnotoFactoryAddress   string `json:"snotoFactoryAddress"`
	SnotoWasmHash         string `json:"snotoWasmHash"`
}

// loadStellarFixtures reads the addresses `./gradlew :soroban:deployStellarFixtures` deploys
// (soroban/scripts/deploy-stellar-fixtures.sh) - deployment happens at the Gradle/build layer,
// not inside this test, matching this repo's existing convention that Gradle/docker-compose
// provisions infrastructure and Go tests assume it's ready (testinfra:startTestInfra).
func loadStellarFixtures(t *testing.T) stellarFixtures {
	t.Helper()
	data, err := os.ReadFile("../../../../soroban/artifacts/stellar-fixtures.json")
	require.NoError(t, err, "run `./gradlew :soroban:deployStellarFixtures` first (chapter 14 step 6)")
	var f stellarFixtures
	require.NoError(t, json.Unmarshal(data, &f))
	require.NotEmpty(t, f.SaladinFactoryAddress)
	require.NotEmpty(t, f.SnotoFactoryAddress)
	require.NotEmpty(t, f.SnotoWasmHash)
	return f
}

// fundRootFunderViaFriendbot funds this node's own resolved "root" identity (config/stellar.node{1,2,3}.config.yaml's
// channelAccounts.funder) via the quickstart standalone network's friendbot. "root"'s configured
// static seed is the network ID hash (SHA-256 of the network passphrase, per keypair.Master's own
// derivation) - but Paladin's key manager always derives an HD child key from a configured seed,
// never the raw seed's own keypair directly, so "root" can never actually BE the network's literal
// genesis master account (confirmed empirically: they're different addresses). So, exactly like
// any other identity, "root" needs its own on-chain funding before it can act as a channel-account
// funder - friendbot is genuinely available on this network (despite it running with `--enable rpc`
// rather than `horizon`; friendbot answers on port 8000 regardless, confirmed empirically), the
// same mechanism §14.3's own Sente multi-member test already relies on for its own "root" identity.
func fundRootFunderViaFriendbot(t *testing.T, ctx context.Context, client pldclient.PaladinClient) {
	t.Helper()
	rootVerifier, err := client.PTX().ResolveVerifier(ctx, "root", algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS)
	require.NoError(t, err)

	resp, err := http.Get("http://localhost:8000/friendbot?addr=" + rootVerifier)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	alreadyFunded := resp.StatusCode == http.StatusBadRequest && strings.Contains(string(body), "already funded")
	require.True(t, resp.StatusCode == http.StatusOK || alreadyFunded,
		"failed to fund root funder verifier %s via friendbot: HTTP %d: %s", rootVerifier, resp.StatusCode, body)
}

// writePersistentNode3Config rewrites config/stellar.node3.config.yaml's `dsn: ":memory:"` to a
// real sqlite file under t.TempDir(), returning the path to the rewritten copy. party3 needs this
// for the restart/resync drill below: an in-memory DB is wiped on every Stop(), so a restarted
// node3 would have to re-index the *entire* chain history from the oldest available ledger (not
// just catch up on what it missed) before its "noto" event-stream even exists again to notice the
// transfer sent while it was down - unboundedly slower than any reasonable test timeout as the
// chain grows, and the reason a from-scratch rescan (not the reliable-message resend cycle, the
// first-suspected cause) was actually why this drill kept timing out even at 60s. coordinationtest
// avoids this entirely by using real postgres for every node (see its own config/*.yaml) rather
// than sqlite ":memory:" - switching this whole suite to postgres is out of scope here, so this
// keeps sqlite but makes party3's data specifically survive its own Stop()/Start() within this test.
func writePersistentNode3Config(t *testing.T) string {
	t.Helper()
	orig, err := os.ReadFile("config/stellar.node3.config.yaml")
	require.NoError(t, err)
	dbPath := filepath.Join(t.TempDir(), "stellar-node3.sqlite")
	rewritten := strings.Replace(string(orig), `dsn: ":memory:"`, `dsn: "`+dbPath+`"`, 1)
	require.NotEqual(t, string(orig), rewritten, "expected to find dsn: \":memory:\" in config/stellar.node3.config.yaml")
	newConfigPath := filepath.Join(t.TempDir(), "stellar.node3.config.yaml")
	require.NoError(t, os.WriteFile(newConfigPath, []byte(rewritten), 0600))
	return newConfigPath
}

func waitForSuccessfulReceipt(t *testing.T, ctx context.Context, client pldclient.PaladinClient, txID uuid.UUID, timeout time.Duration) {
	t.Helper()
	assert.Eventually(t, func() bool {
		receipt, err := client.PTX().GetTransactionReceipt(ctx, txID)
		require.NoError(t, err)
		return receipt != nil && receipt.Success
	}, timeout, 100*time.Millisecond, "transaction %s did not receive a successful receipt", txID)
}

// TestStellarComponentTest is chapter 14 step 6's 3-node Stellar acceptance flow: notary on
// node1, parties on node2/node3, real node processes (in-process ComponentManagers, real loopback
// gRPC between them - see testutils.NewInstanceForTesting) against the real local Stellar network
// (testinfra's stellar_quickstart). It deploys a real SNoto instance via the real
// SNotoFactory.deploy -> SaladinFactory.register flow (chapter 14 steps 2 and 5), mints and
// transfers to prove state distribution and receipts propagate correctly across nodes, then runs
// a restart/resync drill mirroring coordinationtest's own stop/sleep/restart pattern.
//
// The Gradle task wiring (core/go/build.gradle's componentTestStellarSQLite) already depends on
// :soroban:deployStellarFixtures and :testinfra:startTestInfra - running this test directly
// (rather than via that task) requires both to have already been run manually.
func TestStellarComponentTest(t *testing.T) {
	fixtures := loadStellarFixtures(t)
	ctx := t.Context()

	// The real SAC (Stellar Asset Contract) backing deposit/withdraw's shield/unshield must be
	// derived and set BEFORE any node starts: StellarSacAddress is a per-node domain-plugin config
	// value (domains/noto/pkg/types/config.go), applied to every SNoto instance that node ever
	// deploys - unlike RegistryAddress/SnotoFactoryAddress it has no per-deploy-transaction
	// equivalent a client could vary later. issuer/asset are otherwise entirely independent of any
	// Paladin node (see stellar_asset_test.go's own doc comments).
	issuer := generateAndFundIssuer(t)
	sacAddress := deploySACForAsset(t, ctx, issuer, "USDX")
	asset := &txnbuild.CreditAsset{Code: "USDX", Issuer: issuer.Address()}

	domainConfig := &domains.NotoStellarDomainConfig{
		RegistryAddress:     fixtures.SaladinFactoryAddress,
		SnotoFactoryAddress: fixtures.SnotoFactoryAddress,
		SnotoWasmHash:       fixtures.SnotoWasmHash,
		SacAddress:          sacAddress,
	}

	// domainRegistryAddress (nil here) is the EVM-shaped parameter every other domainConfig case
	// in NewInstanceForTesting's switch uses - irrelevant for NotoStellarDomainConfig, which
	// carries its own (Stellar strkey) RegistryAddress instead.
	notary := testutils.NewPartyForTestingWithNodeName(t, "notary", "node1", nil)
	party2 := testutils.NewPartyForTestingWithNodeName(t, "party2", "node2", nil)
	party3 := testutils.NewPartyForTestingWithNodeName(t, "party3", "node3", nil)

	notary.AddPeer(party2.GetNodeConfig(), party3.GetNodeConfig())
	party2.AddPeer(notary.GetNodeConfig(), party3.GetNodeConfig())
	party3.AddPeer(notary.GetNodeConfig(), party2.GetNodeConfig())

	// manualTestCleanup=true: this test stops/restarts party3 mid-test (the resync drill below),
	// so it manages Stop() itself rather than relying on a single t.Cleanup-registered stop.
	// party3 gets its own rewritten config (writePersistentNode3Config's own doc comment) so its
	// restart later doesn't wipe its DB - both Start() calls must use this same path.
	node3ConfigPath := writePersistentNode3Config(t)
	notary.Start(t, domainConfig, "config/stellar.node1.config.yaml", true)
	party2.Start(t, domainConfig, "config/stellar.node2.config.yaml", true)
	party3.Start(t, domainConfig, node3ConfigPath, true)
	t.Cleanup(func() {
		party3.Stop(t)
		party2.Stop(t)
		notary.Stop(t)
	})

	// Each node resolves its own "root" identity independently (own key manager, own DB), so each
	// needs its own on-chain funding before it can fund channel accounts from it - see
	// fundRootFunderViaFriendbot's own doc comment.
	fundRootFunderViaFriendbot(t, ctx, notary.GetClient())
	fundRootFunderViaFriendbot(t, ctx, party2.GetClient())
	fundRootFunderViaFriendbot(t, ctx, party3.GetClient())

	// Deploy a real SNoto instance: PrepareDeploy's Stellar branch (chapter 14 step 2) builds a
	// SorobanInvoke targeting SNotoFactory.deploy, which deploys+initializes the instance and
	// calls SaladinFactory.register in one atomic on-chain invocation (chapter 14 step 1,
	// soroban/contracts/snoto-factory) - domainmgr's event-stream consumer (chapter 14 step 5)
	// then trusts that registration and treats the instance as real.
	//
	// A longer timeout than later transactions in this test: this first transaction bootstraps
	// several *separate* 8-account channel-account pools (chapter 12 §12.2) - the notary's own
	// identity, plus a fresh per-deploy nonce identity - each one a sequence of CreateAccountOps
	// individually confirmed, currently paced by ~2s getTransaction polling per account. See
	// build.gradle's componentTestStellarSQLite task for the matching go test -timeout bump.
	deployTx := notary.GetClient().ForABI(ctx, *notoStellarConstructorABI).
		Private().
		Domain("noto").
		IdempotencyKey("deploy1").
		From(notary.GetIdentity()).
		Inputs(pldtypes.JSONString(&types.ConstructorParams{
			Notary:     notary.GetIdentityLocator(),
			NotaryMode: types.NotaryModeBasic,
		})).
		Send().Wait(480 * time.Second)
	require.NoError(t, deployTx.Error())
	contractAddress := deployTx.Receipt().ContractAddress
	require.NotNil(t, contractAddress, "deploy did not produce a contract address - is the SaladinFactory.register event being trusted/registered?")

	mintFn := types.NotoABI.Functions()["mint"]
	require.NotNil(t, mintFn)
	transferFn := types.NotoABI.Functions()["transfer"]
	require.NotNil(t, transferFn)

	// Mint to party2 - only the notary may mint in basic mode.
	mintTx := notary.GetClient().ForABI(ctx, abi.ABI{mintFn}).
		Private().
		Domain("noto").
		IdempotencyKey("mint1").
		From(notary.GetIdentity()).
		ToChainAddress(contractAddress).
		Function("mint").
		Inputs(pldtypes.RawJSON(`{
			"to": "` + party2.GetIdentityLocator() + `",
			"amount": "100",
			"data": "0x"
		}`)).
		Send().Wait(60 * time.Second)
	require.NoError(t, mintTx.Error())

	// Transfer from party2 to party3 - this can only succeed if party3's node received state
	// distribution for the mint output above via Paladin's own reliable-messaging layer (chain-
	// neutral - see chapter 14 step 6's design note on why this needed no changes for Stellar).
	transferTx := party2.GetClient().ForABI(ctx, abi.ABI{transferFn}).
		Private().
		Domain("noto").
		IdempotencyKey("transfer1").
		From(party2.GetIdentityLocator()).
		ToChainAddress(contractAddress).
		Function("transfer").
		Inputs(pldtypes.RawJSON(`{
			"to": "` + party3.GetIdentityLocator() + `",
			"amount": "40",
			"data": "0x"
		}`)).
		Send().Wait(60 * time.Second)
	require.NoError(t, transferTx.Error())

	// All three nodes should independently see a successful receipt - proving receipt
	// distribution, not just state distribution, propagated correctly.
	waitForSuccessfulReceipt(t, ctx, notary.GetClient(), transferTx.ID(), 30*time.Second)
	waitForSuccessfulReceipt(t, ctx, party2.GetClient(), transferTx.ID(), 30*time.Second)
	waitForSuccessfulReceipt(t, ctx, party3.GetClient(), transferTx.ID(), 30*time.Second)

	// Lock/prepareUnlock/delegateLock live check (chapter 14 step 7): proves
	// stellarBaseLedgerInvokePrepareUnlock's spend/cancel commitment digests and
	// stellarBaseLedgerInvokeDelegateLock's real on-chain call both encode correctly against SNoto's
	// actual `prepare_unlock`/`delegate_lock` functions - only verifiable live, same as every other
	// digest-matching risk this chapter has hit. Doesn't chain into an actual delegate-submitted
	// unlock afterward: once delegateLock hands the lock to party3, SNoto's on-chain `unlock` would
	// need party3.require_auth() satisfied by a non-invoker Soroban authorization entry - real
	// second-signer support that doesn't exist yet (tracked separately, alongside deposit's own
	// need for the same capability).
	lockFn := types.NotoABI.Functions()["lock"]
	require.NotNil(t, lockFn)
	prepareUnlockFn := types.NotoABI.Functions()["prepareUnlock"]
	require.NotNil(t, prepareUnlockFn)
	delegateLockFn := types.NotoABI.Functions()["delegateLock"]
	require.NotNil(t, delegateLockFn)

	lockTx := party2.GetClient().ForABI(ctx, abi.ABI{lockFn}).
		Private().
		Domain("noto").
		IdempotencyKey("lock1").
		From(party2.GetIdentityLocator()).
		ToChainAddress(contractAddress).
		Function("lock").
		Inputs(pldtypes.RawJSON(`{
			"amount": "20",
			"data": "0x"
		}`)).
		Send().Wait(60 * time.Second)
	require.NoError(t, lockTx.Error())
	waitForSuccessfulReceipt(t, ctx, notary.GetClient(), lockTx.ID(), 30*time.Second)
	waitForSuccessfulReceipt(t, ctx, party2.GetClient(), lockTx.ID(), 30*time.Second)

	lockDomainReceiptJSON, err := notary.GetClient().PTX().GetDomainReceipt(ctx, "noto", lockTx.ID())
	require.NoError(t, err)
	var lockDomainReceipt struct {
		States struct {
			LockedOutputs []struct {
				Data struct {
					LockID pldtypes.Bytes32 `json:"lockId"`
				} `json:"data"`
			} `json:"lockedOutputs"`
		} `json:"states"`
	}
	require.NoError(t, json.Unmarshal(lockDomainReceiptJSON, &lockDomainReceipt))
	require.Len(t, lockDomainReceipt.States.LockedOutputs, 1)
	lockID := lockDomainReceipt.States.LockedOutputs[0].Data.LockID
	require.False(t, lockID.IsZero())

	prepareUnlockTx := party2.GetClient().ForABI(ctx, abi.ABI{prepareUnlockFn}).
		Private().
		Domain("noto").
		IdempotencyKey("prepareUnlock1").
		From(party2.GetIdentityLocator()).
		ToChainAddress(contractAddress).
		Function("prepareUnlock").
		Inputs(pldtypes.RawJSON(`{
			"lockId": "` + lockID.String() + `",
			"from": "` + party2.GetIdentityLocator() + `",
			"recipients": [{"to": "` + party3.GetIdentityLocator() + `", "amount": "20"}],
			"unlockData": "0x",
			"data": "0x"
		}`)).
		Send().Wait(60 * time.Second)
	require.NoError(t, prepareUnlockTx.Error())
	waitForSuccessfulReceipt(t, ctx, notary.GetClient(), prepareUnlockTx.ID(), 30*time.Second)
	waitForSuccessfulReceipt(t, ctx, party2.GetClient(), prepareUnlockTx.ID(), 30*time.Second)

	delegateLockTx := party2.GetClient().ForABI(ctx, abi.ABI{delegateLockFn}).
		Private().
		Domain("noto").
		IdempotencyKey("delegateLock1").
		From(party2.GetIdentityLocator()).
		ToChainAddress(contractAddress).
		Function("delegateLock").
		Inputs(pldtypes.RawJSON(`{
			"lockId": "` + lockID.String() + `",
			"delegate": "` + party3.GetIdentityLocator() + `",
			"data": "0x"
		}`)).
		Send().Wait(60 * time.Second)
	require.NoError(t, delegateLockTx.Error())
	waitForSuccessfulReceipt(t, ctx, notary.GetClient(), delegateLockTx.ID(), 30*time.Second)
	waitForSuccessfulReceipt(t, ctx, party2.GetClient(), delegateLockTx.ID(), 30*time.Second)

	// cancelLock/cancel_unlock's Go+Rust wiring is implemented and unit-tested (handler_cancel_lock.go,
	// handler_cancel_lock_test.go), but - like this test's own unlock/delegateLock chain just above -
	// it can't be exercised live yet: SNoto's cancel_unlock requires lock.delegate.require_auth(),
	// and the lock's delegate is always the original party (e.g. party2), never the notary identity
	// that actually submits transactions on-chain. That needs the same real non-invoker Soroban
	// authorization capability deposit's own live test is blocked on (tracked separately as C3a/C3b).

	// Restart/resync drill (mirrors coordinationtest's own stop/sleep/restart pattern): stop
	// party3, send it a transfer while it's down, restart it, and confirm it catches up and gets
	// a receipt via the same reliable-messaging/state-distribution machinery, proving this works
	// across a real node restart, not just within one continuously-running process.
	party3.Stop(t)

	transferWhileDownTx := party2.GetClient().ForABI(ctx, abi.ABI{transferFn}).
		Private().
		Domain("noto").
		IdempotencyKey("transfer2-party3-down").
		From(party2.GetIdentityLocator()).
		ToChainAddress(contractAddress).
		Function("transfer").
		Inputs(pldtypes.RawJSON(`{
			"to": "` + party3.GetIdentityLocator() + `",
			"amount": "10",
			"data": "0x"
		}`)).
		Send().Wait(60 * time.Second)
	require.NoError(t, transferWhileDownTx.Error())

	time.Sleep(2 * time.Second)
	party3.Start(t, domainConfig, node3ConfigPath, true)

	// 60s: even with a persistent DB (writePersistentNode3Config) letting party3 resume from its
	// last checkpoint instead of re-indexing from genesis, node1's queued state-distribution
	// messages for party3 only get resent once its short initial retry burst (against the
	// just-stopped node) gives up and falls back to the next periodic full re-scan -
	// pldconf.TransportManagerDefaults.ReliableMessageResend, 30s by default and not overridden by
	// any node config here. A 30s wait would have ~zero margin against that same 30s timer;
	// TestTransactionSuccessIfOneRequiredVerifierStoppedDuringSubmission (coordinationtest) hits the
	// analogous race for EVM and handles it the same way - by giving the restarted node comfortably
	// more time than the internal resend interval, not by changing that interval itself.
	waitForSuccessfulReceipt(t, ctx, party3.GetClient(), transferWhileDownTx.ID(), 60*time.Second)

	// Custom asset trustlines live check (chapter 14 §14.1's deposit/withdraw prerequisite): proves
	// core/go/pkg/baseledger/stellar/classic_ops.go's BuildChangeTrustPayload/
	// EncodeClassicOperations/DecodeClassicOperations codec - fully implemented and unit-tested but,
	// until now, never actually called by anything - round-trips correctly against a real Stellar
	// classic ChangeTrust operation, signed by a genuine Paladin-managed identity via keymgr_sign
	// (the only way to get such a signature: Paladin's own domain-transaction pipeline never
	// constructs a classic operation itself). Deliberately runs LAST, after the restart/resync
	// drill: it adds real wall-clock time and extra indexed ledgers (3 parties' friendbot funding,
	// ChangeTrust, and Payment operations), which was found to erode the restart drill's own tight
	// timing margin (see that drill's own comment on ReliableMessageResend) when run beforehand.
	rpc, blClient := newStellarRPCClient(t, ctx)

	for _, party := range []struct {
		client   pldclient.PaladinClient
		identity string
	}{
		{notary.GetClient(), notary.GetIdentity()},
		{party2.GetClient(), party2.GetIdentity()},
		{party3.GetClient(), party3.GetIdentity()},
	} {
		trustorAddr, err := party.client.PTX().ResolveVerifier(ctx, party.identity, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS)
		require.NoError(t, err)
		chainAddr, err := pldtypes.NewStellarAccountAddress(trustorAddr)
		require.NoError(t, err)

		beforeStatus, err := blClient.CheckTrustline(ctx, chainAddr, asset)
		require.NoError(t, err)
		require.False(t, beforeStatus.Exists)

		establishTrustlineAndFund(t, ctx, rpc, blClient, party.client, party.identity, asset, issuer, "1000")

		afterStatus, err := blClient.CheckTrustline(ctx, chainAddr, asset)
		require.NoError(t, err)
		require.True(t, afterStatus.Exists)
		require.True(t, afterStatus.Authorized)
	}

	// withdraw's own live test (chapter 14 C2) is NOT included here yet. Chaining a further
	// Paladin-submitted base-ledger transaction (a second mint, or withdraw itself) after this
	// trustline loop repeatedly hit the same symptom: a transaction confirmed via direct
	// getTransaction RPC query against stellar_quickstart (status "SUCCESS") nonetheless never
	// gets recognized as confirmed by Paladin's own public-tx-manager tracking loop, sitting in
	// the "tracking" stage indefinitely (tested up to 3 minutes; the chain closes ledgers every
	// ~1s, so this isn't simple pacing). It first appeared to correlate specifically with the raw
	// classic ChangeTrust/Payment operations this loop submits directly via
	// baseledgerstellar.Client.Submit (bypassing Paladin's own publictxmgr) - but a later plain
	// rerun of this same test (unchanged, no new operations added) hit the identical symptom on
	// its own, so the true trigger is unconfirmed: it may be that interaction, or it may be
	// degradation on this session's local stellar_quickstart chain (177,000+ ledgers and growing
	// across many hours/reruns) unrelated to this test's own content. Either way, it's not
	// implicated in handler_withdraw.go/handler_deposit.go's own correctness - the transactions
	// that hung were pre-existing, untouched code paths (mint/the trustline loop's own classic
	// ops), never new deposit/withdraw code. Root-causing this (likely in
	// core/go/internal/publictxmgr's Stellar tracking/polling path, or possibly just needing a
	// fresh chain) is out of scope here - tracked as a separate follow-up, alongside the actual
	// withdraw/deposit live-test once it's understood.
}
