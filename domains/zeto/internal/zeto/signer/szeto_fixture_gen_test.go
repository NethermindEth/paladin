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

// This is a throwaway fixture-generation harness (saladin-book chapter 13, Phase B.2.2), NOT part
// of this package's "official" test suite. It reuses Zeto's own, completely unmodified real
// prover code path (the same `loadCircuit`/`calculateWitness`/`generateProof` helpers exercised
// by `TestSnarkProve` in `snark_prover_test.go`, just pointed at the real, checked-in
// `domains/zeto/zkp/anon_nullifier_transfer*` circuit artifacts instead of the fake
// circuitLoader/proofGenerator that `NewTestProver` installs for unit tests) to produce a real
// Groth16 proof of a real, value-conserving 2-input/2-output transfer, together with a real
// on-chain-shaped Merkle root/proof pair from the actual `github.com/LFDT-Paladin/smt` library.
//
// It must live under `domains/zeto` (not `soroban/spikes/`) purely because it needs direct access
// to this package's unexported helpers, which Go's `internal/` visibility rule only allows from
// within the `domains/zeto` module tree.
//
// Output: two committed JSON fixtures under `soroban/contracts/szeto/fixtures/`, consumed by a
// later Rust-side integration test (Phase B.2.3):
//   - real_anon_nullifier_transfer_proof.json: snarkjs-shaped {proof, pub_signals}, using the
//     exact json tags already defined upstream by go-rapidsnark's types.ZKProof/ProofData - no
//     manual re-shaping needed.
//   - real_anon_nullifier_transfer_seed_leaves.json: the plain index/value pairs for the 2 input
//     UTXO commitments inserted into the tree, in insertion order, so the Rust test can replay
//     the same 2 `tree::insert_leaf` calls to reach the same on-chain root before calling
//     `transfer()`.
package signer

import (
	"context"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/LFDT-Paladin/paladin/domains/zeto/internal/zeto/signer/common"
	pb "github.com/LFDT-Paladin/paladin/domains/zeto/pkg/proto"
	"github.com/LFDT-Paladin/paladin/domains/zeto/pkg/zetosigner/zetosignerapi"
	smtcore "github.com/LFDT-Paladin/smt/pkg/sparse-merkle-tree/core"
	smtnode "github.com/LFDT-Paladin/smt/pkg/sparse-merkle-tree/node"
	smttree "github.com/LFDT-Paladin/smt/pkg/sparse-merkle-tree/smt"
	utxopkg "github.com/LFDT-Paladin/smt/pkg/utxo"
	utxocore "github.com/LFDT-Paladin/smt/pkg/utxo/core"
	"github.com/hyperledger-labs/zeto/go-sdk/pkg/crypto"
	"github.com/hyperledger-labs/zeto/go-sdk/pkg/key-manager/key"
	"github.com/stretchr/testify/require"
)

// --- minimal in-memory core.Storage, mirroring soroban/spikes/m0-smt-parity/main.go's
// memStorage: no SQL/gorm needed just to seed a fresh tree for a fixture-generation run.

type fixtureGenMemStorage struct {
	hasher utxocore.Hasher
	root   smtcore.NodeRef
	nodes  map[string]smtcore.Node
}

func newFixtureGenMemStorage(hasher utxocore.Hasher) *fixtureGenMemStorage {
	return &fixtureGenMemStorage{hasher: hasher, nodes: map[string]smtcore.Node{}}
}

func (s *fixtureGenMemStorage) GetRootNodeRef(context.Context) (smtcore.NodeRef, error) {
	if s.root == nil {
		return nil, smtcore.ErrNotFound
	}
	return s.root, nil
}

func (s *fixtureGenMemStorage) UpsertRootNodeRef(_ context.Context, r smtcore.NodeRef) error {
	s.root = r
	return nil
}

func (s *fixtureGenMemStorage) GetNode(_ context.Context, key smtcore.NodeRef) (smtcore.Node, error) {
	n, ok := s.nodes[key.Hex()]
	if !ok {
		return nil, smtcore.ErrNotFound
	}
	return n, nil
}

func (s *fixtureGenMemStorage) InsertNode(_ context.Context, n smtcore.Node) error {
	s.nodes[n.Ref().Hex()] = n
	return nil
}

func (s *fixtureGenMemStorage) BeginTx(context.Context) (smtcore.Transaction, error) {
	return &fixtureGenMemTx{s}, nil
}

func (s *fixtureGenMemStorage) Close() {}

func (s *fixtureGenMemStorage) GetHasher() utxocore.Hasher { return s.hasher }

type fixtureGenMemTx struct{ s *fixtureGenMemStorage }

func (t *fixtureGenMemTx) UpsertRootNodeRef(ctx context.Context, r smtcore.NodeRef) error {
	return t.s.UpsertRootNodeRef(ctx, r)
}
func (t *fixtureGenMemTx) GetNode(ctx context.Context, key smtcore.NodeRef) (smtcore.Node, error) {
	return t.s.GetNode(ctx, key)
}
func (t *fixtureGenMemTx) InsertNode(ctx context.Context, n smtcore.Node) error {
	return t.s.InsertNode(ctx, n)
}
func (t *fixtureGenMemTx) Commit(context.Context) error   { return nil }
func (t *fixtureGenMemTx) Rollback(context.Context) error { return nil }

// seedLeaf mirrors the JSON shape the Rust-side test needs to replay the same 2
// tree::insert_leaf(&env, value.clone(), value) calls (index == value, decimal string, matching
// tree.rs's actual call site and the m0-smt-parity spike's confirmed parity).
type seedLeaf struct {
	Index string `json:"index"`
	Value string `json:"value"`
}

// repoPaths locates domains/zeto/zkp and soroban/contracts/szeto/fixtures relative to this
// source file, so the test works regardless of the `go test` invocation's working directory.
func repoPaths(t *testing.T) (zkpDir, fixturesDir string) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	signerDir := filepath.Dir(thisFile)
	// signerDir = .../domains/zeto/internal/zeto/signer
	zetoModuleDir := filepath.Join(signerDir, "..", "..", "..") // .../domains/zeto
	repoRoot := filepath.Join(zetoModuleDir, "..", "..")        // repo root
	return filepath.Join(zetoModuleDir, "zkp"), filepath.Join(repoRoot, "soroban", "contracts", "szeto", "fixtures")
}

// TestGenerateRealAnonNullifierTransferFixture is a throwaway fixture generator, not a
// regression test: it drives Zeto's real, unmodified `anon_nullifier_transfer` prover against a
// genuine 2-input/2-output value-conserving transfer, and writes the resulting real Groth16
// proof + the seed leaves used to build its Merkle root to soroban/contracts/szeto/fixtures/.
func TestGenerateRealAnonNullifierTransferFixture(t *testing.T) {
	ctx := context.Background()
	zkpDir, fixturesDir := repoPaths(t)

	require.NoError(t, os.MkdirAll(fixturesDir, 0o755))

	// --- 1. Keys: alice owns both real input UTXOs; bob and carol are the 2 recipients.
	alice := common.NewTestKeypair()
	bob := common.NewTestKeypair()
	carol := common.NewTestKeypair()

	hasher := utxopkg.NewPoseidonHasher()

	// --- 2. Two real input UTXOs (value-conserving: 10 + 20 = 30 in, 12 + 18 = 30 out).
	inputValues := []*big.Int{big.NewInt(10), big.NewInt(20)}
	outputValues := []*big.Int{big.NewInt(12), big.NewInt(18)}

	salt1 := crypto.NewSalt()
	salt2 := crypto.NewSalt()
	inputSalts := []*big.Int{salt1, salt2}

	input1UTXO := utxopkg.NewFungible(inputValues[0], alice.PublicKey, salt1, hasher)
	commitment1, err := input1UTXO.GetHash()
	require.NoError(t, err)
	input2UTXO := utxopkg.NewFungible(inputValues[1], alice.PublicKey, salt2, hasher)
	commitment2, err := input2UTXO.GetHash()
	require.NoError(t, err)
	inputCommitmentInts := []*big.Int{commitment1, commitment2}

	// --- 3. Insert both into a fresh, real SMT (SMT_HEIGHT_UTXO = 64, matching tree.rs's
	// MAX_SMT_DEPTH) to get a genuine root + Merkle proofs - the same library the m0-smt-parity
	// spike already confirmed agrees bit-for-bit with the Rust tree.rs port.
	storage := newFixtureGenMemStorage(hasher)
	tree, err := smttree.NewMerkleTree(ctx, storage, 64)
	require.NoError(t, err)

	seedLeaves := make([]seedLeaf, 0, 2)
	for i, commitment := range inputCommitmentInts {
		var ownerPubKey = alice.PublicKey
		var amount = inputValues[i]
		var salt = inputSalts[i]
		indexable := smtnode.NewFungible(amount, ownerPubKey, salt, hasher)
		leaf, err := smtnode.NewLeafNode(indexable, nil)
		require.NoError(t, err)
		require.NoError(t, tree.AddLeaf(ctx, leaf))
		seedLeaves = append(seedLeaves, seedLeaf{
			Index: commitment.Text(10),
			Value: commitment.Text(10),
		})
	}

	root := tree.Root()
	proofs, _, err := tree.GenerateProofs(ctx, inputCommitmentInts, root)
	require.NoError(t, err)
	require.Len(t, proofs, 2)

	merkleProofs := make([]*pb.MerkleProof, 0, 2)
	for i, proof := range proofs {
		cp, err := proof.ToCircomVerifierProof(inputCommitmentInts[i], inputCommitmentInts[i], root, 64)
		require.NoError(t, err)
		// Drop the last sibling, matching the real domain's generateMerkleProofs
		// (fungible/utils.go) - the circuit only wants the 64 sibling levels, not the trailing
		// root-adjacent element ToCircomVerifierProof appends.
		nodes := make([]string, len(cp.Siblings)-1)
		for j, s := range cp.Siblings[0 : len(cp.Siblings)-1] {
			nodes[j] = s.BigInt().Text(16)
		}
		merkleProofs = append(merkleProofs, &pb.MerkleProof{Nodes: nodes})
	}

	smtProof := &pb.MerkleProofObject{
		Root:         root.BigInt().Text(16),
		MerkleProofs: merkleProofs,
		Enabled:      []bool{true, true},
	}

	// --- 4. Two real output UTXOs: 12 to bob, 18 to carol - fresh salts, real recipient keys.
	outputSalt1 := crypto.NewSalt()
	outputSalt2 := crypto.NewSalt()

	bobPubKey := common.EncodeBabyJubJubPublicKey(bob.PublicKey)
	carolPubKey := common.EncodeBabyJubJubPublicKey(carol.PublicKey)

	tokenSecrets, err := json.Marshal(&pb.TokenSecrets_Fungible{
		InputValues:  []uint64{inputValues[0].Uint64(), inputValues[1].Uint64()},
		OutputValues: []uint64{outputValues[0].Uint64(), outputValues[1].Uint64()},
	})
	require.NoError(t, err)

	commonInputs := &pb.ProvingRequestCommon{
		InputCommitments: []string{commitment1.Text(16), commitment2.Text(16)},
		InputSalts:       []string{salt1.Text(16), salt2.Text(16)},
		InputOwner:       "alice/key0",
		OutputSalts:      []string{outputSalt1.Text(16), outputSalt2.Text(16)},
		OutputOwners:     []string{bobPubKey, carolPubKey},
		TokenSecrets:     tokenSecrets,
		TokenType:        pb.TokenType_fungible,
	}

	extras := &pb.ProvingRequestExtras_Nullifiers{SmtProof: smtProof}

	circuit := &zetosignerapi.Circuit{
		Name:           "anon_nullifier_transfer",
		Type:           zetosignerapi.Transfer,
		UsesNullifiers: true,
		UsesEncryption: false,
	}

	// --- 5. Real key entry for alice (the sender/input owner), matching how Sign() derives it
	// from raw private key bytes in the production code path.
	var keyBytes [32]byte
	copy(keyBytes[:], alice.PrivateKey[:])
	keyEntry := key.NewKeyEntryFromPrivateKeyBytes(keyBytes)

	// --- 6. Load the REAL circuit artifacts (unmodified loadCircuit) and compute the REAL
	// witness (unmodified calculateWitness), pointed at domains/zeto/zkp/anon_nullifier_transfer*.
	config := &zetosignerapi.SnarkProverConfig{
		CircuitsDir:    zkpDir,
		ProvingKeysDir: zkpDir,
	}
	witnessCalculator, provingKey, err := loadCircuit(ctx, circuit.Name, config)
	require.NoError(t, err, "failed to load real anon_nullifier_transfer circuit artifacts from %s", zkpDir)

	wtnsBin, err := calculateWitness(ctx, circuit, commonInputs, extras, keyEntry, witnessCalculator)
	require.NoError(t, err)

	// --- 7. Call the REAL, unmodified prover (go-rapidsnark's Groth16Prover under the hood).
	zkProof, err := generateProof(ctx, wtnsBin, provingKey)
	require.NoError(t, err)
	require.NotNil(t, zkProof.Proof)
	// nPublic: 7 (nullifiers x2 + root + enables x2 + outputs x2), matching the embedded VK
	// szeto's build.rs generates from anon_nullifier_transfer-vkey.json.
	require.Len(t, zkProof.PubSignals, 7, "unexpected public signal count for anon_nullifier_transfer")

	// --- 8. Write the snarkjs-shaped proof fixture. types.ZKProof/ProofData already carry the
	// exact json tags (proof/pi_a/pi_b/pi_c/protocol/pub_signals) real_zeto_fixtures.rs expects -
	// no manual re-shaping needed.
	proofBytes, err := json.MarshalIndent(zkProof, "", "  ")
	require.NoError(t, err)
	proofPath := filepath.Join(fixturesDir, "real_anon_nullifier_transfer_proof.json")
	require.NoError(t, os.WriteFile(proofPath, proofBytes, 0o644))

	seedLeavesBytes, err := json.MarshalIndent(seedLeaves, "", "  ")
	require.NoError(t, err)
	seedLeavesPath := filepath.Join(fixturesDir, "real_anon_nullifier_transfer_seed_leaves.json")
	require.NoError(t, os.WriteFile(seedLeavesPath, seedLeavesBytes, 0o644))

	t.Logf("wrote real anon_nullifier_transfer proof fixture to %s", proofPath)
	t.Logf("wrote seed leaves fixture to %s", seedLeavesPath)
	t.Logf("public signals: %v", zkProof.PubSignals)
}
