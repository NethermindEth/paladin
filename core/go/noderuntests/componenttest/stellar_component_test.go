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
	"os"
	"testing"
	"time"

	testutils "github.com/LFDT-Paladin/paladin/core/noderuntests/pkg"
	"github.com/LFDT-Paladin/paladin/core/noderuntests/pkg/domains"
	"github.com/LFDT-Paladin/paladin/domains/noto/pkg/types"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldclient"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/google/uuid"
	"github.com/hyperledger/firefly-signer/pkg/abi"
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

	domainConfig := &domains.NotoStellarDomainConfig{
		RegistryAddress:     fixtures.SaladinFactoryAddress,
		SnotoFactoryAddress: fixtures.SnotoFactoryAddress,
		SnotoWasmHash:       fixtures.SnotoWasmHash,
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
	notary.Start(t, domainConfig, "config/stellar.node1.config.yaml", true)
	party2.Start(t, domainConfig, "config/stellar.node2.config.yaml", true)
	party3.Start(t, domainConfig, "config/stellar.node3.config.yaml", true)
	t.Cleanup(func() {
		party3.Stop(t)
		party2.Stop(t)
		notary.Stop(t)
	})

	// Deploy a real SNoto instance: PrepareDeploy's Stellar branch (chapter 14 step 2) builds a
	// SorobanInvoke targeting SNotoFactory.deploy, which deploys+initializes the instance and
	// calls SaladinFactory.register in one atomic on-chain invocation (chapter 14 step 1,
	// soroban/contracts/snoto-factory) - domainmgr's event-stream consumer (chapter 14 step 5)
	// then trusts that registration and treats the instance as real.
	deployTx := notary.GetClient().ForABI(ctx, *notoStellarConstructorABI).
		Private().
		Domain("noto").
		IdempotencyKey("deploy1").
		From(notary.GetIdentity()).
		Inputs(pldtypes.JSONString(&types.ConstructorParams{
			Notary:     notary.GetIdentityLocator(),
			NotaryMode: types.NotaryModeBasic,
		})).
		Send().Wait(60 * time.Second)
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
	party3.Start(t, domainConfig, "config/stellar.node3.config.yaml", true)

	waitForSuccessfulReceipt(t, ctx, party3.GetClient(), transferWhileDownTx.ID(), 30*time.Second)
}
