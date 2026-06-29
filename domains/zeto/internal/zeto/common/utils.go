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

package common

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	corepb "github.com/LFDT-Paladin/paladin/domains/zeto/pkg/proto"
	"github.com/LFDT-Paladin/paladin/domains/zeto/pkg/types"
	"github.com/hyperledger-labs/zeto/go-sdk/pkg/sparse-merkle-tree/core"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/domains/zeto/internal/msgs"
	"github.com/LFDT-Paladin/paladin/domains/zeto/internal/zeto/smt"
	"github.com/LFDT-Paladin/paladin/domains/zeto/pkg/constants"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/plugintk"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/hyperledger/firefly-signer/pkg/abi"
	"github.com/iden3/go-iden3-crypto/babyjub"
)

type StateSchemas struct {
	CoinSchema           *prototk.StateSchema
	NftSchema            *prototk.StateSchema
	DataSchema           *prototk.StateSchema
	MerkleTreeRootSchema *prototk.StateSchema
	MerkleTreeNodeSchema *prototk.StateSchema
}

// AuthorityKeys holds the arbiter and enforcer BabyJubJub public keys for a contract instance.
type AuthorityKeys struct {
	ArbiterPublicKey  [2]string
	EnforcerPublicKey [2]string
}

// ComplianceIdentity holds a registered identity's BabyJubJub public key coordinates.
type ComplianceIdentity struct {
	PubKeyX *big.Int
	PubKeyY *big.Int
}

// AuthorityKeyProvider supplies per-contract authority keys, compliance identities,
// and transaction state tracking for the enforced token handlers.
type AuthorityKeyProvider interface {
	GetAuthorityKeys(contractAddress string) *AuthorityKeys
	GetComplianceIdentities(contractAddress string) []ComplianceIdentity
	TrackTxInputStates(txID string, commitmentHashes []pldtypes.HexUint256)
}

type MerkleTreeType int

const (
	StatesTree MerkleTreeType = iota
	LockedStatesTree
	KycStatesTree
	ComplianceStatesTree
)

type MerkleTreeSpec struct {
	Name       string
	Levels     int
	Type       MerkleTreeType
	Storage    smt.StatesStorage
	Tree       core.SparseMerkleTree
	EmptyProof *corepb.MerkleProof
}

func NewMerkleTreeSpec(ctx context.Context, name string, treeType MerkleTreeType, callbacks plugintk.DomainCallbacks, merkleTreeRootSchemaId, merkleTreeNodeSchemaId string, stateQueryContext string) (*MerkleTreeSpec, error) {
	var tree core.SparseMerkleTree
	var levels int
	switch treeType {
	case StatesTree:
		levels = smt.SMT_HEIGHT_UTXO
	case LockedStatesTree:
		levels = smt.SMT_HEIGHT_UTXO
	case KycStatesTree:
		levels = smt.SMT_HEIGHT_KYC
	case ComplianceStatesTree:
		levels = smt.SMT_HEIGHT_COMPLIANCE
	default:
		return nil, i18n.NewError(ctx, msgs.MsgUnknownSmtType, treeType)
	}
	storage := smt.NewStatesStorage(callbacks, name, stateQueryContext, merkleTreeRootSchemaId, merkleTreeNodeSchemaId)
	tree, err := smt.NewSmt(storage, levels)
	if err != nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorNewSmt, name, err)
	}
	var emptyProof *corepb.MerkleProof
	switch treeType {
	case KycStatesTree:
		emptyProof = &smt.Empty_Proof_kyc
	case ComplianceStatesTree:
		emptyProof = &smt.Empty_Proof_Compliance
	default:
		emptyProof = &smt.Empty_Proof_Utxos
	}
	return &MerkleTreeSpec{
		Name:       name,
		Levels:     levels,
		Type:       treeType,
		Storage:    storage,
		Tree:       tree,
		EmptyProof: emptyProof,
	}, nil
}

// MakeEmptyProof creates an empty merkle proof with the given number of zero nodes.
func MakeEmptyProof(levels int) *corepb.MerkleProof {
	nodes := make([]string, levels)
	for i := range nodes {
		nodes[i] = "0"
	}
	return &corepb.MerkleProof{Nodes: nodes}
}

const modulus = "21888242871839275222246405745257275088548364400416034343698204186575808495617"

func IsNullifiersToken(tokenName string) bool {
	return tokenName == constants.TOKEN_ANON_NULLIFIER || tokenName == constants.TOKEN_NF_ANON_NULLIFIER || tokenName == constants.TOKEN_ANON_NULLIFIER_KYC || tokenName == constants.TOKEN_ANON_ENC_NULLIFIER_KYC_NON_REPUDIATION_ENFORCED
}

func IsKycToken(tokenName string) bool {
	return tokenName == constants.TOKEN_ANON_NULLIFIER_KYC || tokenName == constants.TOKEN_ANON_ENC_NULLIFIER_KYC_NON_REPUDIATION_ENFORCED
}

func IsNonFungibleToken(tokenName string) bool {
	return tokenName == constants.TOKEN_NF_ANON || tokenName == constants.TOKEN_NF_ANON_NULLIFIER
}

func IsEncryptionToken(tokenName string) bool {
	return tokenName == constants.TOKEN_ANON_ENC || tokenName == constants.TOKEN_ANON_ENC_NULLIFIER_KYC_NON_REPUDIATION_ENFORCED
}

func IsNonRepudiationToken(tokenName string) bool {
	return tokenName == constants.TOKEN_ANON_ENC_NULLIFIER_KYC_NON_REPUDIATION_ENFORCED
}

func IsEnforcedToken(tokenName string) bool {
	return tokenName == constants.TOKEN_ANON_ENC_NULLIFIER_KYC_NON_REPUDIATION_ENFORCED
}

// UseNullifierAvailability was intended for forcedTransfer's SpentCommitments path
// but should NOT be used for balanceOf or coin selection — those must use
// IsNullifiersToken directly. Kept for backward compatibility with tests.
func UseNullifierAvailability(tokenName string) bool {
	return IsNullifiersToken(tokenName)
}

// the Zeto implementations support two input/output sizes for the circuits: 2 and 10,
// if the input or output size is larger than 2, then the batch circuit is used with
// input/output size 10
func GetInputSize(sizeOfEndorsableStates int) int {
	if sizeOfEndorsableStates <= 2 {
		return 2
	}
	return 10
}

func HexUint256To32ByteHexString(v *pldtypes.HexUint256) string {
	paddedBytes := IntTo32ByteSlice(v.Int())
	return hex.EncodeToString(paddedBytes)
}

func IntTo32ByteSlice(bigInt *big.Int) (res []byte) {
	return bigInt.FillBytes(make([]byte, 32))
}

func IntTo32ByteHexString(bigInt *big.Int) string {
	paddedBytes := bigInt.FillBytes(make([]byte, 32))
	return hex.EncodeToString(paddedBytes)
}

func LoadBabyJubKey(payload []byte) (*babyjub.PublicKey, error) {
	var keyCompressed babyjub.PublicKeyComp
	if err := keyCompressed.UnmarshalText(payload); err != nil {
		return nil, err
	}
	return keyCompressed.Decompress()
}

func EncodeTransactionData(ctx context.Context, transaction *prototk.TransactionSpecification, infoStates []*prototk.EndorsableState) (pldtypes.HexBytes, error) {
	return EncodeTransactionDataWithSpentCommitments(ctx, transaction, infoStates, nil)
}

// EncodeTransactionDataWithSpentCommitments encodes transaction data including spent commitment
// hashes. Used by forcedTransfer so all nodes can mark seized UTXOs as spent.
func EncodeTransactionDataWithSpentCommitments(ctx context.Context, transaction *prototk.TransactionSpecification, infoStates []*prototk.EndorsableState, spentCommitments []pldtypes.HexUint256) (pldtypes.HexBytes, error) {
	var err error
	stateIDs := make([]pldtypes.Bytes32, len(infoStates))
	for i, state := range infoStates {
		stateIDs[i], err = pldtypes.ParseBytes32Ctx(ctx, state.Id)
		if err != nil {
			return nil, err
		}
	}

	transactionID, err := pldtypes.ParseBytes32Ctx(ctx, transaction.TransactionId)
	if err != nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorParseTxId, err)
	}
	if spentCommitments == nil {
		spentCommitments = []pldtypes.HexUint256{}
	}
	dataValues := &types.ZetoTransactionData_V0{
		TransactionID:    transactionID,
		InfoStates:       stateIDs,
		SpentCommitments: spentCommitments,
	}

	var dataJSON []byte
	var dataABI []byte
	var data []byte
	dataJSON, err = json.Marshal(dataValues)
	if err == nil {
		dataABI, err = types.ZetoTransactionDataABI_V0.EncodeABIDataJSONCtx(ctx, dataJSON)
		if err == nil {
			data = append(data, types.ZetoTransactionDataID_V0...)
			data = append(data, dataABI...)
		}
	}

	return data, err
}

// DecodeTransactionData decodes the ABI-encoded transaction data from an on-chain event's data field.
func DecodeTransactionData(ctx context.Context, data pldtypes.HexBytes) (*types.ZetoTransactionData_V0, error) {
	if len(data) < 4 {
		return nil, nil
	}
	dataPrefix := data[0:4]
	if dataPrefix.String() != types.ZetoTransactionDataID_V0.String() {
		return nil, nil
	}

	var dataValues types.ZetoTransactionData_V0
	dataDecoded, err := types.ZetoTransactionDataABI_V0.DecodeABIDataCtx(ctx, data, 4)
	if err == nil {
		var dataJSON []byte
		dataJSON, err = dataDecoded.JSON()
		if err == nil {
			err = json.Unmarshal(dataJSON, &dataValues)
		}
	}
	if err != nil {
		return nil, err
	}
	return &dataValues, nil
}

func EncodeProof(proof *corepb.SnarkProof) map[string]interface{} {
	// Convert the proof json to the format that the Solidity verifier expects
	return map[string]interface{}{
		"pA": []string{proof.A[0], proof.A[1]},
		"pB": [][]string{
			{proof.B[0].Items[1], proof.B[0].Items[0]},
			{proof.B[1].Items[1], proof.B[1].Items[0]},
		},
		"pC": []string{proof.C[0], proof.C[1]},
	}
}

// EncodeEnforcedProof builds the ABI-encoded proof bytes blob for AENKNR-E circuits.
// Fields are populated from PublicInputs, extraFields, or the Groth16 proof struct.
func EncodeEnforcedProof(ctx context.Context, proofRes *corepb.ProvingResponse, proofABI *abi.ParameterArray, extraFields map[string]interface{}) ([]byte, error) {
	proofData := make(map[string]interface{})
	for _, param := range *proofABI {
		if param.Name == "proof" {
			proofData["proof"] = EncodeProof(proofRes.Proof)
		} else if extra, ok := extraFields[param.Name]; ok {
			proofData[param.Name] = extra
		} else if val, ok := proofRes.PublicInputs[param.Name]; ok {
			if param.Type == "uint256[]" || param.Type == "uint256[2]" {
				proofData[param.Name] = strings.Split(val, ",")
			} else {
				proofData[param.Name] = val
			}
		}
	}
	proofJSON, err := json.Marshal(proofData)
	if err != nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorMarshalPrepedParams, err)
	}
	return proofABI.EncodeABIDataJSONCtx(ctx, proofJSON)
}

// Generate a random 256-bit integer
func CryptoRandBN254() (*big.Int, error) {
	// The BN254 field modulus.
	fieldModulus, ok := new(big.Int).SetString(modulus, 10)
	if !ok {
		return nil, fmt.Errorf("failed to parse field modulus")
	}

	// Generate a random number in [0, fieldModulus).
	tokenValue, err := rand.Int(rand.Reader, fieldModulus)
	if err != nil {
		return nil, err
	}
	return tokenValue, nil
}
