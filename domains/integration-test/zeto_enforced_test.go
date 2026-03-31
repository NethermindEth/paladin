/*
 * Copyright © 2024 Kaleido, Inc.
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

package integrationtest

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/log"
	"github.com/LFDT-Paladin/paladin/domains/integration-test/helpers"
	"github.com/LFDT-Paladin/paladin/domains/zeto/pkg/constants"
	"github.com/LFDT-Paladin/paladin/domains/zeto/pkg/types"
	"github.com/LFDT-Paladin/paladin/domains/zeto/pkg/zetosigner"
	"github.com/LFDT-Paladin/paladin/domains/zeto/pkg/zetosigner/zetosignerapi"
	"github.com/hyperledger-labs/zeto/go-sdk/pkg/crypto"
	smtcore "github.com/hyperledger-labs/zeto/go-sdk/pkg/sparse-merkle-tree/core"
	smtnode "github.com/hyperledger-labs/zeto/go-sdk/pkg/sparse-merkle-tree/node"
	smtsmt "github.com/hyperledger-labs/zeto/go-sdk/pkg/sparse-merkle-tree/smt"
	apicore "github.com/hyperledger-labs/zeto/go-sdk/pkg/utxo/core"
	"github.com/iden3/go-iden3-crypto/poseidon"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/query"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/algorithms"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/verifiers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

func TestEnforcedZetoDomainTestSuite(t *testing.T) {
	contractsFile = "./zeto/config-for-deploy-enforced.yaml"
	suite.Run(t, new(enforcedTestSuiteHelper))
}

type enforcedTestSuiteHelper struct {
	zetoDomainTestSuite
}

// TestZeto_AnonEncNullifierKycNonRepudiationEnforced exercises the full AENKNR-E
// enforced token lifecycle following the demo flow:
//
//  1. Setup: deploy contracts, register identities, configure compliance
//  2. Deposit: ERC20 → private UTXOs (Bank deposits into shielded pool)
//  3. Shielded transfer: Bank → Investor (private distribution)
//  4. Compliance: freeze investor, forced transfer (clawback)
func (s *enforcedTestSuiteHelper) TestZeto_AnonEncNullifierKycNonRepudiationEnforced() {
	t := s.T()
	ctx := context.Background()
	tokenName := constants.TOKEN_ANON_ENC_NULLIFIER_KYC_NON_REPUDIATION_ENFORCED
	algoSnarkBJJ := zetosignerapi.AlgoDomainZetoSnarkBJJ(s.domainName)

	// ──────────────────────────────────────────────────────────────────────
	// Phase 1 — Setup: deploy contracts, register identities, configure
	// ──────────────────────────────────────────────────────────────────────

	// Actors: Bank (controller), Alice (investor), Regulator (arbiter+enforcer for simplicity)
	bankName := controllerName
	aliceName := recipient1Name

	log.L(ctx).Infof("Deploying %s", tokenName)
	s.setupContractsAbi(t, ctx, tokenName)
	zeto := helpers.DeployZetoFungible(ctx, t, s.rpc, s.domainName, bankName, tokenName)
	log.L(ctx).Infof("Deployed to %s", zeto.Address)

	// Configure diamond-lite architecture (codec + facet)
	codecName, facetName := s.deployedContracts.GetCloneableContract(tokenName)
	if codecName != "" {
		codecAddr := s.deployedContracts.GetContractAddress(codecName)
		require.NotNil(t, codecAddr)
		zeto.SetCodec(ctx, s.tb, bankName, codecAddr)
	}
	if facetName != "" {
		facetAddr := s.deployedContracts.GetContractAddress(facetName)
		require.NotNil(t, facetAddr)
		zeto.SetTransferFacet(ctx, s.tb, bankName, facetAddr)
	}

	// Resolve BabyJubJub public keys for all actors
	resolveBJJ := func(name string) []*big.Int {
		var addr pldtypes.Bytes32
		rpcerr := s.rpc.CallRPC(ctx, &addr, "ptx_resolveVerifier", name, algoSnarkBJJ, zetosignerapi.IDEN3_PUBKEY_BABYJUBJUB_COMPRESSED_0X)
		require.Nil(t, rpcerr)
		pubKey, err := zetosigner.DecodeBabyJubJubPublicKey(addr.HexString())
		require.NoError(t, err)
		return []*big.Int{pubKey.X, pubKey.Y}
	}

	bankPubKey := resolveBJJ(bankName)
	alicePubKey := resolveBJJ(aliceName)

	// Register KYC identities (Bank + Alice are KYC'd)
	log.L(ctx).Info("Registering KYC identities: Bank + Alice")
	zeto.Register(ctx, s.tb, bankName, bankPubKey)
	zeto.Register(ctx, s.tb, bankName, alicePubKey)
	time.Sleep(5 * time.Second)

	// Set arbiter and enforcer (Bank acts as both for the demo)
	log.L(ctx).Info("Setting arbiter and enforcer to Bank")
	zeto.SetArbiter(ctx, s.tb, bankName, bankPubKey)
	zeto.SetEnforcer(ctx, s.tb, bankName, bankPubKey)

	// Set compliance root on-chain (all registered users ACTIVE)
	allActiveRoot := buildComplianceRoot(t, [][]*big.Int{bankPubKey, alicePubKey})
	log.L(ctx).Infof("Setting compliance root (all ACTIVE): 0x%s", allActiveRoot.Text(16))
	zeto.SetComplianceRoot(ctx, s.tb, bankName, allActiveRoot)

	// Deploy and link ERC20 (the public T-REX token analog)
	var bankEthAddr string
	rpcerr := s.rpc.CallRPC(ctx, &bankEthAddr, "ptx_resolveVerifier", bankName, algorithms.ECDSA_SECP256K1, verifiers.ETH_ADDRESS)
	require.Nil(t, rpcerr)

	erc20Address, err := helpers.DeployERC20(ctx, s.rpc, bankName, bankEthAddr)
	require.NoError(t, err)
	zeto.SetERC20(ctx, s.tb, bankName, erc20Address)

	jq := query.NewQueryBuilder().Limit(100).Equal("locked", false).Query()

	// ──────────────────────────────────────────────────────────────────────
	// Phase 2 — Deposit: Bank deposits ERC20 into the shielded pool
	// ──────────────────────────────────────────────────────────────────────

	log.L(ctx).Info("Bank minting 1000 ERC20 (treasury)")
	zeto.MintERC20(ctx, s.tb, *erc20Address, 1000, bankName, bankEthAddr)

	log.L(ctx).Info("Bank approving Zeto to spend 500 ERC20")
	zeto.ApproveERC20(ctx, s.tb, *erc20Address, 500, bankName)

	log.L(ctx).Info("Bank depositing 500 ERC20 into shielded pool")
	zeto.Deposit(ctx, 500).SignAndSend(bankName, true).Wait()

	findAvailableCoins(t, ctx, s.rpc, s.domain.Name(), s.domain.CoinSchemaID(), "pstate_queryContractNullifiers", zeto.Address, jq, func(coins []*types.ZetoCoinState) bool {
		return len(coins) >= 2
	})

	balanceOfResult := zeto.BalanceOf(ctx, bankName).SignAndCall(bankName).Wait()
	assert.Equal(t, "500", balanceOfResult["totalBalance"].(string))

	// ──────────────────────────────────────────────────────────────────────
	// Phase 3 — Shielded transfer: Bank distributes 200 to Alice (private)
	// ──────────────────────────────────────────────────────────────────────

	log.L(ctx).Info("Bank transfers 200 private tokens to Alice")
	zeto.Transfer(ctx, []string{aliceName}, []uint64{200}).SignAndSend(bankName, true).Wait()

	balanceOfResult = zeto.BalanceOf(ctx, bankName).SignAndCall(bankName).Wait()
	assert.Equal(t, "300", balanceOfResult["totalBalance"].(string))

	balanceOfResult = zeto.BalanceOf(ctx, aliceName).SignAndCall(aliceName).Wait()
	assert.Equal(t, "200", balanceOfResult["totalBalance"].(string))

	// ──────────────────────────────────────────────────────────────────────
	// Phase 4 — Compliance enforcement: freeze Alice, clawback via forced transfer
	// ──────────────────────────────────────────────────────────────────────

	// 4a. Update compliance root: freeze Alice
	log.L(ctx).Info("Freezing Alice in compliance tree")
	frozenRoot := buildComplianceRootWithStatus(t, [][2]interface{}{
		{bankPubKey, big.NewInt(1)},   // ACTIVE
		{alicePubKey, big.NewInt(2)},  // FROZEN
	})
	zeto.SetComplianceRoot(ctx, s.tb, bankName, frozenRoot)

	// 4b. Forced transfer: Bank (enforcer) claws back 150 from Alice
	//     Must seize less than total to produce a change output (circuit requires
	//     all outputs to have valid BabyJubJub keys — no zero-key fillers).
	log.L(ctx).Info("Clawback: seizing 150 of Alice's 200 tokens")
	zeto.ForcedTransfer(ctx, aliceName, []string{bankName}, []uint64{150}).SignAndSend(bankName, true).Wait()

	// Bank: 300 + 150 seized = 450
	balanceOfResult = zeto.BalanceOf(ctx, bankName).SignAndCall(bankName).Wait()
	assert.Equal(t, "450", balanceOfResult["totalBalance"].(string))

	// Alice: 200 total, but 200 enforcement-locked + 50 change from seizure = 250 visible
	// (enforcement nullifiers prevent the old 200 UTXO from being spent, but balanceOf
	// counts by owner nullifiers, so the enforcement-locked UTXO still appears)
	balanceOfResult = zeto.BalanceOf(ctx, aliceName).SignAndCall(aliceName).Wait()
	assert.Equal(t, "250", balanceOfResult["totalBalance"].(string))
}

// ──────────────────────────────────────────────────────────────────────
// Compliance tree helpers
// ──────────────────────────────────────────────────────────────────────

// buildComplianceRoot creates a compliance SMT with all identities ACTIVE (status=1).
func buildComplianceRoot(t *testing.T, pubKeys [][]*big.Int) *big.Int {
	entries := make([][2]interface{}, len(pubKeys))
	for i, pk := range pubKeys {
		entries[i] = [2]interface{}{pk, big.NewInt(1)}
	}
	return buildComplianceRootWithStatus(t, entries)
}

// buildComplianceRootWithStatus creates a compliance SMT with per-identity statuses.
// Each entry is {pubKey []*big.Int, status *big.Int}.
func buildComplianceRootWithStatus(t *testing.T, entries [][2]interface{}) *big.Int {
	storage := &memSMTStorage{
		hasher: crypto.NewPoseidonHasher(),
		nodes:  make(map[string]smtcore.Node),
	}
	tree, err := smtsmt.NewMerkleTree(storage, 64)
	require.NoError(t, err)

	for _, entry := range entries {
		pk := entry[0].([]*big.Int)
		status := entry[1].(*big.Int)
		key, err := poseidon.Hash([]*big.Int{pk[0], pk[1]})
		require.NoError(t, err)
		value, err := poseidon.Hash([]*big.Int{pk[0], pk[1], status})
		require.NoError(t, err)
		idx, err := smtnode.NewNodeIndexFromBigInt(key, crypto.NewPoseidonHasher())
		require.NoError(t, err)
		leaf, err := smtnode.NewLeafNode(smtnode.NewIndexOnly(idx), value)
		require.NoError(t, err)
		require.NoError(t, tree.AddLeaf(leaf))
	}
	return tree.Root().BigInt()
}

// memSMTStorage is a minimal in-memory Storage for building local compliance trees.
type memSMTStorage struct {
	hasher apicore.Hasher
	root   smtcore.NodeRef
	nodes  map[string]smtcore.Node
}

func (s *memSMTStorage) GetRootNodeRef() (smtcore.NodeRef, error) {
	if s.root != nil {
		return s.root, nil
	}
	return nil, smtcore.ErrNotFound
}
func (s *memSMTStorage) UpsertRootNodeRef(ref smtcore.NodeRef) error { s.root = ref; return nil }
func (s *memSMTStorage) GetNode(ref smtcore.NodeRef) (smtcore.Node, error) {
	if n, ok := s.nodes[ref.Hex()]; ok {
		return n, nil
	}
	return nil, smtcore.ErrNotFound
}
func (s *memSMTStorage) InsertNode(n smtcore.Node) error { s.nodes[n.Ref().Hex()] = n; return nil }
func (s *memSMTStorage) BeginTx() (smtcore.Transaction, error) { return s, nil }
func (s *memSMTStorage) Close()                                {}
func (s *memSMTStorage) GetHasher() apicore.Hasher             { return s.hasher }
func (s *memSMTStorage) Commit() error                         { return nil }
func (s *memSMTStorage) Rollback() error                       { return nil }
