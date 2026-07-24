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

package repoterms

import (
	"encoding/json"
	"testing"

	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/algorithms"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/verifiers"
	"github.com/hyperledger/firefly-signer/pkg/abi"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testContractAddress is a real (well-formed) Stellar contract StrKey address, standing in for a
// deployed repo-terms instance's own address - parseContractAddress (chainio_stellar.go) requires
// a genuine StrKey (or EVM hex) string, so an arbitrary literal like "CINSTANCE" won't parse.
var testContractAddress = func() string {
	addr, err := strkey.Encode(strkey.VersionByteContract, pldtypes.RandBytes(32))
	if err != nil {
		panic(err)
	}
	return addr
}()

// setTermsFunctionABI is the "setTerms" function's own FunctionAbiJson shape - just enough for
// validateTransactionAndGetLogContext to recover the function name.
var setTermsFunctionABI = &abi.Entry{
	Type: abi.Function,
	Name: "setTerms",
	Inputs: abi.ParameterArray{
		{Name: "rateBps", Type: "uint32"},
		{Name: "maturityLedger", Type: "uint32"},
		{Name: "haircutBps", Type: "uint32"},
	},
}

func testRepoTermsParsedConfig() *RepoTermsParsedConfig {
	return &RepoTermsParsedConfig{
		BankALookup: "bankA@node2",
		BankBLookup: "bankB@node3",
	}
}

func newSetTermsTransaction(t *testing.T, contractAddress string) *prototk.TransactionSpecification {
	t.Helper()
	configJSON, err := json.Marshal(testRepoTermsParsedConfig())
	require.NoError(t, err)
	return &prototk.TransactionSpecification{
		TransactionId: "0x015e1881f2ba769c22d05c841f06949ec6e1bd573f5e1e0328885494212f077d",
		From:          "bankA@node2",
		ContractInfo: &prototk.ContractInfo{
			ContractAddress:    contractAddress,
			ContractConfigJson: string(configJSON),
		},
		FunctionAbiJson: mustParseJSON(setTermsFunctionABI),
		FunctionParamsJson: `{
			"rateBps": 425,
			"maturityLedger": 123456,
			"haircutBps": 200
		}`,
	}
}

func newRepoTermsForHandlerTests() *RepoTerms {
	return &RepoTerms{
		Callbacks:         newMockCallbacks(),
		repoTermsSchema:   testSchema("repoTerms"),
		networkPassphrase: "Test Stellar Network ; 2026",
	}
}

func TestInitTransaction(t *testing.T) {
	r := newRepoTermsForHandlerTests()
	ctx := t.Context()

	res, err := r.InitTransaction(ctx, &prototk.InitTransactionRequest{
		Transaction: newSetTermsTransaction(t, testContractAddress),
	})
	require.NoError(t, err)
	require.Len(t, res.RequiredVerifiers, 3)
	for _, v := range res.RequiredVerifiers {
		assert.Equal(t, algorithms.EDDSA_ED25519, v.Algorithm)
		assert.Equal(t, verifiers.STELLAR_ADDRESS, v.VerifierType)
	}
}

func TestInitTransaction_UnknownFunction(t *testing.T) {
	r := newRepoTermsForHandlerTests()
	ctx := t.Context()

	tx := newSetTermsTransaction(t, testContractAddress)
	tx.FunctionAbiJson = mustParseJSON(&abi.Entry{Type: abi.Function, Name: "amendTerms"})

	_, err := r.InitTransaction(ctx, &prototk.InitTransactionRequest{Transaction: tx})
	assert.ErrorContains(t, err, "amendTerms")
}

// TestAssembleTransaction proves Assemble produces exactly one output state with the right
// schema/distribution list, and a two-item attestation plan (sender SIGN + bilateral ENDORSE,
// Parties=[bankA,bankB], Threshold=nil).
func TestAssembleTransaction(t *testing.T) {
	r := newRepoTermsForHandlerTests()
	ctx := t.Context()

	tx := newSetTermsTransaction(t, testContractAddress)
	res, err := r.AssembleTransaction(ctx, &prototk.AssembleTransactionRequest{
		Transaction: tx,
	})
	require.NoError(t, err)
	require.Equal(t, prototk.AssembleTransactionResponse_OK, res.AssemblyResult)
	require.NotNil(t, res.AssembledTransaction)
	require.Len(t, res.AssembledTransaction.OutputStates, 0+1)
	require.Empty(t, res.AssembledTransaction.InputStates)

	outputState := res.AssembledTransaction.OutputStates[0]
	assert.Equal(t, r.repoTermsSchema.Id, outputState.SchemaId)
	assert.Equal(t, []string{"bankA@node2", "bankB@node3"}, outputState.DistributionList)
	require.NotNil(t, outputState.Id)
	assert.NotEmpty(t, *outputState.Id)

	var terms RepoTermsV1
	require.NoError(t, json.Unmarshal([]byte(outputState.StateDataJson), &terms))
	assert.Equal(t, "bankA@node2", terms.BankA)
	assert.Equal(t, "bankB@node3", terms.BankB)
	assert.Equal(t, uint32(425), terms.RateBps)
	assert.Equal(t, uint32(123456), terms.MaturityLedger)
	assert.Equal(t, uint32(200), terms.HaircutBps)
	assert.NotEmpty(t, terms.Salt)

	require.Len(t, res.AttestationPlan, 2)

	sender := res.AttestationPlan[0]
	assert.Equal(t, "sender", sender.Name)
	assert.Equal(t, prototk.AttestationType_SIGN, sender.AttestationType)
	assert.Equal(t, algorithms.EDDSA_ED25519, sender.Algorithm)
	assert.Equal(t, verifiers.STELLAR_ADDRESS, sender.VerifierType)
	assert.Equal(t, []string{"bankA@node2"}, sender.Parties)
	assert.NotEmpty(t, sender.Payload)
	assert.Nil(t, sender.Threshold)

	bilateral := res.AttestationPlan[1]
	assert.Equal(t, "bilateral", bilateral.Name)
	assert.Equal(t, prototk.AttestationType_ENDORSE, bilateral.AttestationType)
	assert.Equal(t, algorithms.EDDSA_ED25519, bilateral.Algorithm)
	assert.Equal(t, verifiers.STELLAR_ADDRESS, bilateral.VerifierType)
	assert.Equal(t, []string{"bankA@node2", "bankB@node3"}, bilateral.Parties)
	assert.Nil(t, bilateral.Threshold, "threshold should default to len(Parties)==2")
}

func TestEndorseTransaction(t *testing.T) {
	r := newRepoTermsForHandlerTests()
	ctx := t.Context()

	tx := newSetTermsTransaction(t, testContractAddress)
	assembleRes, err := r.AssembleTransaction(ctx, &prototk.AssembleTransactionRequest{Transaction: tx})
	require.NoError(t, err)
	outputState := assembleRes.AssembledTransaction.OutputStates[0]

	endorseRes, err := r.EndorseTransaction(ctx, &prototk.EndorseTransactionRequest{
		Transaction: tx,
		Outputs: []*prototk.EndorsableState{
			{Id: *outputState.Id, SchemaId: outputState.SchemaId, StateDataJson: outputState.StateDataJson},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, prototk.EndorseTransactionResponse_SIGN, endorseRes.EndorsementResult)
	assert.NotEmpty(t, endorseRes.Payload)
}

func TestEndorseTransaction_BankMismatch(t *testing.T) {
	r := newRepoTermsForHandlerTests()
	ctx := t.Context()

	tx := newSetTermsTransaction(t, testContractAddress)
	badTerms := &RepoTermsV1{BankA: "wrongBank@node9", BankB: "bankB@node3"}
	badTermsJSON, err := json.Marshal(badTerms)
	require.NoError(t, err)

	_, err = r.EndorseTransaction(ctx, &prototk.EndorseTransactionRequest{
		Transaction: tx,
		Outputs: []*prototk.EndorsableState{
			{Id: "0x" + "11" /* placeholder, error should trip before ID is parsed */, SchemaId: r.repoTermsSchema.Id, StateDataJson: string(badTermsJSON)},
		},
	})
	assert.ErrorContains(t, err, "wrongBank@node9")
}

func TestPrepareTransaction(t *testing.T) {
	r := newRepoTermsForHandlerTests()
	ctx := t.Context()

	tx := newSetTermsTransaction(t, testContractAddress)
	assembleRes, err := r.AssembleTransaction(ctx, &prototk.AssembleTransactionRequest{Transaction: tx})
	require.NoError(t, err)
	outputState := assembleRes.AssembledTransaction.OutputStates[0]

	prepareRes, err := r.PrepareTransaction(ctx, &prototk.PrepareTransactionRequest{
		Transaction: tx,
		OutputStates: []*prototk.EndorsableState{
			{Id: *outputState.Id, SchemaId: outputState.SchemaId, StateDataJson: outputState.StateDataJson},
		},
		AttestationResult: []*prototk.AttestationResult{
			{Name: "sender", AttestationType: prototk.AttestationType_SIGN, Payload: []byte("sig")},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, prepareRes.ChainTransaction)
	assert.Equal(t, prototk.PreparedChainTransaction_PUBLIC, prepareRes.ChainTransaction.Type)

	invoke := prepareRes.ChainTransaction.GetSoroban()
	require.NotNil(t, invoke)
	assert.Equal(t, testContractAddress, invoke.ContractId)
	assert.Equal(t, "set_terms", invoke.FunctionName)
	assert.NotEmpty(t, invoke.ArgsXdr)
	assert.Contains(t, invoke.ArgsJson, *outputState.Id)
}

func TestPrepareTransaction_MissingSenderAttestation(t *testing.T) {
	r := newRepoTermsForHandlerTests()
	ctx := t.Context()

	tx := newSetTermsTransaction(t, testContractAddress)
	assembleRes, err := r.AssembleTransaction(ctx, &prototk.AssembleTransactionRequest{Transaction: tx})
	require.NoError(t, err)
	outputState := assembleRes.AssembledTransaction.OutputStates[0]

	_, err = r.PrepareTransaction(ctx, &prototk.PrepareTransactionRequest{
		Transaction: tx,
		OutputStates: []*prototk.EndorsableState{
			{Id: *outputState.Id, SchemaId: outputState.SchemaId, StateDataJson: outputState.StateDataJson},
		},
	})
	assert.ErrorContains(t, err, "sender attestation not found")
}
