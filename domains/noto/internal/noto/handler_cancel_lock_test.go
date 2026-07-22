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

package noto

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/LFDT-Paladin/paladin/domains/noto/pkg/types"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/algorithms"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/verifiers"
	"github.com/hyperledger/firefly-signer/pkg/ethtypes"
	"github.com/hyperledger/firefly-signer/pkg/secp256k1"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCancelLock proves the full Init->Assemble->Endorse->Prepare flow for a lock with a real,
// already-committed cancel path (as createTransferLock/createMintLock/createBurnLock/
// prepareUnlock all set) - the exact gap this handler closes: cancelLock/cancel_unlock was
// previously unreachable on either chain even though both underlying contracts already support it.
func TestCancelLock(t *testing.T) {
	mockCallbacks := newMockCallbacks()
	n := &Noto{
		Callbacks:        mockCallbacks,
		coinSchema:       testSchema("coin"),
		lockedCoinSchema: testSchema("lockedCoin"),
		lockInfoSchemaV0: testSchema("lockInfo"),
		lockInfoSchemaV1: testSchema("lockInfo_v1"),
		dataSchemaV0:     testSchema("data"),
		dataSchemaV1:     testSchema("data_v1"),
		dataSchemaV2:     testSchema("data_v2"),
		manifestSchema:   testSchema("manifest"),
	}
	ctx := t.Context()
	fn := types.NotoABI.Functions()["cancelLock"]

	notaryAddress := "0x1000000000000000000000000000000000000000"
	senderKey, err := secp256k1.GenerateSecp256k1KeyPair()
	require.NoError(t, err)

	lockID := pldtypes.RandBytes32()
	spendTxId := pldtypes.RandBytes32()
	cancelData := pldtypes.HexBytes([]byte{0xca, 0x4c, 0xe1})

	inputCoin := &types.NotoLockedCoinState{
		ID: pldtypes.MustParseBytes32("0xe532ee16774660fceb6c941725d6045939d34263ce81cd17266e910ac0ec5277"),
		Data: types.NotoLockedCoin{
			Salt:   pldtypes.RandBytes32(),
			LockID: lockID,
			Owner:  evmChainAddressPtr((*pldtypes.EthAddress)(&senderKey.Address)),
			Amount: pldtypes.Int64ToInt256(100),
		},
	}
	cancelOutputCoin := &types.NotoCoin{
		Salt:   pldtypes.RandBytes32(),
		Owner:  evmChainAddressPtr((*pldtypes.EthAddress)(&senderKey.Address)),
		Amount: pldtypes.Int64ToInt256(100),
	}
	cancelOutputID := pldtypes.MustParseBytes32("0xf532ee16774660fceb6c941725d6045939d34263ce81cd17266e910ac0ec5278")

	inputLockInfo := &prototk.StoredState{
		Id:       pldtypes.RandBytes32().String(),
		SchemaId: hashName("lockInfo_v1"),
		DataJson: fmt.Sprintf(`{
			"lockId": "%s",
			"salt": "%s",
			"owner": "%s",
			"spender": "%s",
			"spendTxId": "%s",
			"cancelOutputs": ["%s"],
			"cancelData": "%s"
		}`, lockID, pldtypes.RandBytes32(), senderKey.Address, senderKey.Address, spendTxId, cancelOutputID, cancelData),
	}
	mockCallbacks.MockFindAvailableStates = func(ctx context.Context, req *prototk.FindAvailableStatesRequest) (*prototk.FindAvailableStatesResponse, error) {
		switch req.SchemaId {
		case hashName("lockInfo_v1"):
			return &prototk.FindAvailableStatesResponse{States: []*prototk.StoredState{inputLockInfo}}, nil
		case hashName("lockedCoin"):
			return &prototk.FindAvailableStatesResponse{
				States: []*prototk.StoredState{
					{
						Id:        inputCoin.ID.String(),
						SchemaId:  hashName("lockedCoin"),
						DataJson:  mustParseJSON(inputCoin.Data),
						CreatedAt: 1,
					},
				},
			}, nil
		}
		return nil, fmt.Errorf("unmocked query")
	}
	mockCallbacks.MockGetStatesByID = func(ctx context.Context, req *prototk.GetStatesByIDRequest) (*prototk.GetStatesByIDResponse, error) {
		require.Equal(t, hashName("coin"), req.SchemaId)
		require.Equal(t, []string{cancelOutputID.String()}, req.StateIds)
		return &prototk.GetStatesByIDResponse{
			States: []*prototk.StoredState{
				{
					Id:       cancelOutputID.String(),
					SchemaId: hashName("coin"),
					DataJson: mustParseJSON(cancelOutputCoin),
				},
			},
		}, nil
	}

	contractAddress := "0xf6a75f065db3cef95de7aa786eee1d0cb1aeafc3"
	tx := &prototk.TransactionSpecification{
		TransactionId: "0x015e1881f2ba769c22d05c841f06949ec6e1bd573f5e1e0328885494212f077d",
		From:          "sender@node1",
		ContractInfo: &prototk.ContractInfo{
			ContractAddress: contractAddress,
			ContractConfigJson: mustParseJSON(&types.NotoParsedConfig{
				NotaryLookup: "notary@node1",
				Variant:      types.NotoVariantV2,
			}),
		},
		FunctionAbiJson:   mustParseJSON(fn),
		FunctionSignature: fn.SolString(),
		FunctionParamsJson: fmt.Sprintf(`{
			"lockId": "%s",
			"from": "sender@node1",
			"data": "0x1234"
		}`, lockID),
	}

	initRes, err := n.InitTransaction(ctx, &prototk.InitTransactionRequest{Transaction: tx})
	require.NoError(t, err)
	require.Len(t, initRes.RequiredVerifiers, 2)
	assert.Equal(t, "notary@node1", initRes.RequiredVerifiers[0].Lookup)
	assert.Equal(t, "sender@node1", initRes.RequiredVerifiers[1].Lookup)

	verifierList := []*prototk.ResolvedVerifier{
		{
			Lookup:       "notary@node1",
			Algorithm:    algorithms.ECDSA_SECP256K1,
			VerifierType: verifiers.ETH_ADDRESS,
			Verifier:     notaryAddress,
		},
		{
			Lookup:       "sender@node1",
			Algorithm:    algorithms.ECDSA_SECP256K1,
			VerifierType: verifiers.ETH_ADDRESS,
			Verifier:     senderKey.Address.String(),
		},
	}

	assembleRes, err := n.AssembleTransaction(ctx, &prototk.AssembleTransactionRequest{
		Transaction:       tx,
		ResolvedVerifiers: verifierList,
	})
	require.NoError(t, err)
	require.Equal(t, prototk.AssembleTransactionResponse_OK, assembleRes.AssemblyResult)
	require.Len(t, assembleRes.AssembledTransaction.InputStates, 2) // locked coin and lock info
	require.Len(t, assembleRes.AssembledTransaction.OutputStates, 1)
	assert.Equal(t, inputCoin.ID.String(), assembleRes.AssembledTransaction.InputStates[0].Id)
	assert.Equal(t, inputLockInfo.Id, assembleRes.AssembledTransaction.InputStates[1].Id)

	outputCoinState := assembleRes.AssembledTransaction.OutputStates[0]
	require.Equal(t, cancelOutputID.String(), *outputCoinState.Id)
	outputCoin, err := n.unmarshalCoin(outputCoinState.StateDataJson)
	require.NoError(t, err)
	assert.Equal(t, senderKey.Address.String(), outputCoin.Owner.String())
	assert.Equal(t, "100", outputCoin.Amount.Int().String())

	lockState := assembleRes.AssembledTransaction.InputStates[1]

	encodedCancel, err := n.encodeUnlock(ctx, ethtypes.MustNewAddress(contractAddress), []*types.NotoLockedCoin{&inputCoin.Data}, nil, []*types.NotoCoin{outputCoin})
	require.NoError(t, err)
	signature, err := senderKey.SignDirect(encodedCancel)
	require.NoError(t, err)
	signatureBytes := pldtypes.HexBytes(signature.CompactRSV())

	inputStates := []*prototk.EndorsableState{
		{SchemaId: hashName("lockedCoin"), Id: inputCoin.ID.String(), StateDataJson: mustParseJSON(inputCoin.Data)},
		{SchemaId: lockState.SchemaId, Id: lockState.Id, StateDataJson: inputLockInfo.DataJson},
	}
	outputStates := []*prototk.EndorsableState{
		{SchemaId: outputCoinState.SchemaId, Id: *outputCoinState.Id, StateDataJson: outputCoinState.StateDataJson},
	}

	endorseRes, err := n.EndorseTransaction(ctx, &prototk.EndorseTransactionRequest{
		Transaction:       tx,
		ResolvedVerifiers: verifierList,
		Inputs:            inputStates,
		Outputs:           outputStates,
		EndorsementRequest: &prototk.AttestationRequest{
			Name: "notary",
		},
		Signatures: []*prototk.AttestationResult{
			{
				Name:     "sender",
				Verifier: &prototk.ResolvedVerifier{Verifier: senderKey.Address.String()},
				Payload:  signatureBytes,
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, prototk.EndorseTransactionResponse_ENDORSER_SUBMIT, endorseRes.EndorsementResult)

	prepareRes, err := n.PrepareTransaction(ctx, &prototk.PrepareTransactionRequest{
		Transaction:       tx,
		ResolvedVerifiers: verifierList,
		InputStates:       inputStates,
		OutputStates:      outputStates,
		AttestationResult: []*prototk.AttestationResult{
			{
				Name:     "sender",
				Verifier: &prototk.ResolvedVerifier{Verifier: senderKey.Address.String()},
				Payload:  signatureBytes,
			},
			{
				Name:     "notary",
				Verifier: &prototk.ResolvedVerifier{Lookup: "notary@node1"},
			},
		},
	})
	require.NoError(t, err)

	cancelLockABI := interfaceV2Build.ABI.Functions()["cancelLock"]
	expectedFunction := mustParseJSON(cancelLockABI)
	assert.JSONEq(t, expectedFunction, prepareRes.Transaction.FunctionAbiJson)

	params := decodeFnParams[CancelLockParams](t, cancelLockABI, prepareRes.Transaction.ParamsJson)
	require.Equal(t, lockID, params.LockID)

	notoParams := decodeSingleABITuple[types.NotoSpendLockArgs](t, types.NotoSpendLockArgsABI, params.CancelArgs)
	require.Equal(t, spendTxId.String(), notoParams.TxId)
	require.Equal(t, []string{inputCoin.ID.String()}, notoParams.Inputs)
	require.Equal(t, []string{cancelOutputID.String()}, notoParams.Outputs)
	require.Equal(t, cancelData, notoParams.Data)
	require.Equal(t, signatureBytes, notoParams.Proof)
}

// TestCancelLockNoCancelPath proves cancelLock refuses to invent a cancel path for a lock that
// never had one committed (e.g. a plain lock/createLock, whose CancelOutputs is always empty),
// rather than silently submitting an empty-outputs cancel that would burn the locked value.
func TestCancelLockNoCancelPath(t *testing.T) {
	mockCallbacks := newMockCallbacks()
	n := &Noto{
		Callbacks:        mockCallbacks,
		coinSchema:       testSchema("coin"),
		lockedCoinSchema: testSchema("lockedCoin"),
		lockInfoSchemaV1: testSchema("lockInfo_v1"),
	}
	ctx := t.Context()

	fn := types.NotoABI.Functions()["cancelLock"]
	notaryAddress := "0x1000000000000000000000000000000000000000"
	lockID := pldtypes.RandBytes32()
	senderKey, err := secp256k1.GenerateSecp256k1KeyPair()
	require.NoError(t, err)

	inputLockInfo := &prototk.StoredState{
		Id:       pldtypes.RandBytes32().String(),
		SchemaId: hashName("lockInfo_v1"),
		DataJson: fmt.Sprintf(`{
			"lockId": "%s",
			"salt": "%s",
			"owner": "%s",
			"spender": "%s",
			"spendTxId": "%s",
			"cancelOutputs": [],
			"cancelData": ""
		}`, lockID, pldtypes.RandBytes32(), senderKey.Address, senderKey.Address, pldtypes.RandBytes32()),
	}
	mockCallbacks.MockFindAvailableStates = func(ctx context.Context, req *prototk.FindAvailableStatesRequest) (*prototk.FindAvailableStatesResponse, error) {
		if req.SchemaId == hashName("lockInfo_v1") {
			return &prototk.FindAvailableStatesResponse{States: []*prototk.StoredState{inputLockInfo}}, nil
		}
		return nil, fmt.Errorf("unmocked query")
	}

	tx := &prototk.TransactionSpecification{
		TransactionId: "0x015e1881f2ba769c22d05c841f06949ec6e1bd573f5e1e0328885494212f077d",
		From:          "sender@node1",
		ContractInfo: &prototk.ContractInfo{
			ContractAddress: "0xf6a75f065db3cef95de7aa786eee1d0cb1aeafc3",
			ContractConfigJson: mustParseJSON(&types.NotoParsedConfig{
				NotaryLookup: "notary@node1",
				Variant:      types.NotoVariantV2,
			}),
		},
		FunctionAbiJson:    mustParseJSON(fn),
		FunctionSignature:  fn.SolString(),
		FunctionParamsJson: fmt.Sprintf(`{"lockId": "%s", "from": "sender@node1", "data": "0x"}`, lockID),
	}

	verifierList := []*prototk.ResolvedVerifier{
		{
			Lookup:       "notary@node1",
			Algorithm:    algorithms.ECDSA_SECP256K1,
			VerifierType: verifiers.ETH_ADDRESS,
			Verifier:     notaryAddress,
		},
		{
			Lookup:       "sender@node1",
			Algorithm:    algorithms.ECDSA_SECP256K1,
			VerifierType: verifiers.ETH_ADDRESS,
			Verifier:     senderKey.Address.String(),
		},
	}

	_, err = n.AssembleTransaction(ctx, &prototk.AssembleTransactionRequest{Transaction: tx, ResolvedVerifiers: verifierList})
	require.ErrorContains(t, err, "PD200047")
}

// TestCancelLock_Stellar proves stellarBaseLedgerInvokeCancelUnlock builds a real SorobanInvoke
// calling SNoto's cancel_unlock with the already-committed spendTxId/cancelData/cancelOutputs,
// mirroring TestUnlock_Stellar's own XDR-decode assertions.
func TestCancelLock_Stellar(t *testing.T) {
	mockCallbacks := newMockCallbacks()
	n := &Noto{
		Callbacks:        mockCallbacks,
		coinSchema:       testSchema("coin"),
		lockedCoinSchema: testSchema("lockedCoin"),
		lockInfoSchemaV0: testSchema("lockInfo"),
		lockInfoSchemaV1: testSchema("lockInfo_v1"),
		dataSchemaV0:     testSchema("data"),
		dataSchemaV1:     testSchema("data_v1"),
		dataSchemaV2:     testSchema("data_v2"),
		manifestSchema:   testSchema("manifest"),
		chainIO:          newStellarChainIO("Test Stellar Network ; 2026"),
	}
	ctx := t.Context()
	fn := types.NotoABI.Functions()["cancelLock"]

	notaryPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	notaryStellarAddress, err := strkey.Encode(strkey.VersionByteAccountID, notaryPub)
	require.NoError(t, err)

	senderPub, senderPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	senderAddress, err := strkey.Encode(strkey.VersionByteAccountID, senderPub)
	require.NoError(t, err)

	lockID := pldtypes.RandBytes32()
	spendTxId := pldtypes.RandBytes32()
	cancelData := pldtypes.HexBytes([]byte{0xca, 0x4c, 0xe1})

	inputCoin := &types.NotoLockedCoinState{
		ID: pldtypes.MustParseBytes32("0xe532ee16774660fceb6c941725d6045939d34263ce81cd17266e910ac0ec5277"),
		Data: types.NotoLockedCoin{
			Salt:   pldtypes.RandBytes32(),
			LockID: lockID,
			Owner:  pldtypes.MustParseChainAddress(senderAddress),
			Amount: pldtypes.Int64ToInt256(100),
		},
	}
	cancelOutputCoin := &types.NotoCoin{
		Salt:   pldtypes.RandBytes32(),
		Owner:  pldtypes.MustParseChainAddress(senderAddress),
		Amount: pldtypes.Int64ToInt256(100),
	}
	cancelOutputID := pldtypes.MustParseBytes32("0xf532ee16774660fceb6c941725d6045939d34263ce81cd17266e910ac0ec5278")

	inputLockInfo := &prototk.StoredState{
		Id:       pldtypes.RandBytes32().String(),
		SchemaId: hashName("lockInfo_v1"),
		DataJson: fmt.Sprintf(`{
			"lockId": "%s",
			"salt": "%s",
			"owner": "%s",
			"spender": "%s",
			"spendTxId": "%s",
			"cancelOutputs": ["%s"],
			"cancelData": "%s"
		}`, lockID, pldtypes.RandBytes32(), senderAddress, senderAddress, spendTxId, cancelOutputID, cancelData),
	}
	mockCallbacks.MockFindAvailableStates = func(ctx context.Context, req *prototk.FindAvailableStatesRequest) (*prototk.FindAvailableStatesResponse, error) {
		switch req.SchemaId {
		case hashName("lockInfo_v1"):
			return &prototk.FindAvailableStatesResponse{States: []*prototk.StoredState{inputLockInfo}}, nil
		case hashName("lockedCoin"):
			return &prototk.FindAvailableStatesResponse{
				States: []*prototk.StoredState{
					{
						Id:        inputCoin.ID.String(),
						SchemaId:  hashName("lockedCoin"),
						DataJson:  mustParseJSON(inputCoin.Data),
						CreatedAt: 1,
					},
				},
			}, nil
		}
		return nil, fmt.Errorf("unmocked query")
	}
	mockCallbacks.MockGetStatesByID = func(ctx context.Context, req *prototk.GetStatesByIDRequest) (*prototk.GetStatesByIDResponse, error) {
		return &prototk.GetStatesByIDResponse{
			States: []*prototk.StoredState{
				{Id: cancelOutputID.String(), SchemaId: hashName("coin"), DataJson: mustParseJSON(cancelOutputCoin)},
			},
		}, nil
	}

	contractAddress := "0xf6a75f065db3cef95de7aa786eee1d0cb1aeafc3" // placeholder - see placeholderContractID
	tx := &prototk.TransactionSpecification{
		TransactionId: "0x015e1881f2ba769c22d05c841f06949ec6e1bd573f5e1e0328885494212f077d",
		From:          "sender@node1",
		ContractInfo: &prototk.ContractInfo{
			ContractAddress:    contractAddress,
			ContractConfigJson: mustParseJSON(notoBasicConfigV1),
		},
		FunctionAbiJson:   mustParseJSON(fn),
		FunctionSignature: fn.SolString(),
		FunctionParamsJson: fmt.Sprintf(`{
			"lockId": "%s",
			"from": "sender@node1",
			"data": "0x1234"
		}`, lockID),
	}

	resolvedVerifiers := []*prototk.ResolvedVerifier{
		{Lookup: "notary@node1", Algorithm: algorithms.EDDSA_ED25519, VerifierType: verifiers.STELLAR_ADDRESS, Verifier: notaryStellarAddress},
		{Lookup: "sender@node1", Algorithm: algorithms.EDDSA_ED25519, VerifierType: verifiers.STELLAR_ADDRESS, Verifier: senderAddress},
	}

	assembleRes, err := n.AssembleTransaction(ctx, &prototk.AssembleTransactionRequest{
		Transaction:       tx,
		ResolvedVerifiers: resolvedVerifiers,
	})
	require.NoError(t, err)
	require.Equal(t, prototk.AssembleTransactionResponse_OK, assembleRes.AssemblyResult)
	require.Len(t, assembleRes.AssembledTransaction.InputStates, 2)
	require.Len(t, assembleRes.AssembledTransaction.OutputStates, 1)

	outputCoinState := assembleRes.AssembledTransaction.OutputStates[0]
	require.Equal(t, cancelOutputID.String(), *outputCoinState.Id)
	outputCoin, err := n.unmarshalCoin(outputCoinState.StateDataJson)
	require.NoError(t, err)

	lockState := assembleRes.AssembledTransaction.InputStates[1]

	encodedCancel, err := n.encodeUnlock(ctx, ethtypes.MustNewAddress(contractAddress), []*types.NotoLockedCoin{&inputCoin.Data}, nil, []*types.NotoCoin{outputCoin})
	require.NoError(t, err)
	signature := ed25519.Sign(senderPriv, encodedCancel)

	inputStates := []*prototk.EndorsableState{
		{SchemaId: hashName("lockedCoin"), Id: inputCoin.ID.String(), StateDataJson: mustParseJSON(inputCoin.Data)},
		{SchemaId: lockState.SchemaId, Id: lockState.Id, StateDataJson: inputLockInfo.DataJson},
	}
	outputStates := []*prototk.EndorsableState{
		{SchemaId: outputCoinState.SchemaId, Id: *outputCoinState.Id, StateDataJson: outputCoinState.StateDataJson},
	}

	endorseRes, err := n.EndorseTransaction(ctx, &prototk.EndorseTransactionRequest{
		Transaction:       tx,
		ResolvedVerifiers: resolvedVerifiers,
		Inputs:            inputStates,
		Outputs:           outputStates,
		EndorsementRequest: &prototk.AttestationRequest{
			Name: "notary",
		},
		Signatures: []*prototk.AttestationResult{
			{Name: "sender", Verifier: &prototk.ResolvedVerifier{Verifier: senderAddress}, Payload: signature},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, prototk.EndorseTransactionResponse_ENDORSER_SUBMIT, endorseRes.EndorsementResult)

	prepareRes, err := n.PrepareTransaction(ctx, &prototk.PrepareTransactionRequest{
		Transaction:       tx,
		ResolvedVerifiers: resolvedVerifiers,
		InputStates:       inputStates,
		OutputStates:      outputStates,
		AttestationResult: []*prototk.AttestationResult{
			{Name: "sender", Verifier: &prototk.ResolvedVerifier{Verifier: senderAddress}, Payload: signature},
			{Name: "notary", Verifier: &prototk.ResolvedVerifier{Lookup: "notary@node1"}},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, prepareRes.ChainTransaction)
	soroban, ok := prepareRes.ChainTransaction.Payload.(*prototk.PreparedChainTransaction_Soroban)
	require.True(t, ok)
	assert.Equal(t, "cancel_unlock", soroban.Soroban.FunctionName)

	var args xdr.ScVec
	_, err = xdr.Unmarshal(bytes.NewReader(soroban.Soroban.ArgsXdr), &args)
	require.NoError(t, err)
	require.Len(t, args, 5) // tx_id, lock_id, locked_inputs, cancel_outputs, data

	require.Equal(t, xdr.ScValTypeScvBytes, args[0].Type)
	assert.Equal(t, spendTxId[:], []byte(*args[0].Bytes), "tx_id must be the lock's already-committed spendTxId, not this invocation's own transaction ID")

	require.Equal(t, xdr.ScValTypeScvBytes, args[1].Type)
	assert.Equal(t, lockID[:], []byte(*args[1].Bytes))

	require.Equal(t, xdr.ScValTypeScvVec, args[2].Type)
	lockedInputsVec := **args[2].Vec
	require.Len(t, lockedInputsVec, 1)
	assert.Equal(t, inputCoin.ID[:], []byte(*lockedInputsVec[0].Bytes))

	require.Equal(t, xdr.ScValTypeScvVec, args[3].Type)
	cancelOutputsVec := **args[3].Vec
	require.Len(t, cancelOutputsVec, 1)
	assert.Equal(t, cancelOutputID[:], []byte(*cancelOutputsVec[0].Bytes))

	// The load-bearing assertion: "data" is exactly cancelData, matching what prepare_unlock
	// committed to on-chain (check_commitment, soroban/contracts/snoto/src/lib.rs) - not some
	// other, unrelated encoding. A plain non-empty check here would pass even with the wrong
	// value, since both the correct cancelData and the previous (buggy) transaction-data encoding
	// happen to be non-empty.
	require.Equal(t, xdr.ScValTypeScvBytes, args[4].Type)
	assert.Equal(t, []byte(cancelData), []byte(*args[4].Bytes))
}

// TestCancelLock_HooksModeNotSupported proves cancelLock errors clearly in hooks notary mode
// rather than silently invoking an EVM base-ledger call with no onCancelLock hook propagation
// (no such hook exists yet in INotoHooks.json).
func TestCancelLock_HooksModeNotSupported(t *testing.T) {
	mockCallbacks := newMockCallbacks()
	n := &Noto{
		Callbacks:        mockCallbacks,
		coinSchema:       testSchema("coin"),
		lockedCoinSchema: testSchema("lockedCoin"),
		lockInfoSchemaV1: testSchema("lockInfo_v1"),
	}
	ctx := t.Context()

	fn := types.NotoABI.Functions()["cancelLock"]
	lockID := pldtypes.RandBytes32()
	senderKey, err := secp256k1.GenerateSecp256k1KeyPair()
	require.NoError(t, err)
	hookAddress := "0x515fba7fe1d8b9181be074bd4c7119544426837c"

	tx := &prototk.TransactionSpecification{
		TransactionId: "0x015e1881f2ba769c22d05c841f06949ec6e1bd573f5e1e0328885494212f077d",
		From:          "sender@node1",
		ContractInfo: &prototk.ContractInfo{
			ContractAddress: "0xf6a75f065db3cef95de7aa786eee1d0cb1aeafc3",
			ContractConfigJson: mustParseJSON(&types.NotoParsedConfig{
				NotaryLookup: "notary@node1",
				NotaryMode:   types.NotaryModeHooks.Enum(),
				Variant:      types.NotoVariantV2,
				Options: types.NotoOptions{
					Hooks: &types.NotoHooksOptions{
						PublicAddress:     pldtypes.MustEthAddress(hookAddress),
						DevUsePublicHooks: true,
					},
				},
			}),
		},
		FunctionAbiJson:    mustParseJSON(fn),
		FunctionSignature:  fn.SolString(),
		FunctionParamsJson: fmt.Sprintf(`{"lockId": "%s", "from": "sender@node1", "data": "0x"}`, lockID),
	}

	_, err = n.PrepareTransaction(ctx, &prototk.PrepareTransactionRequest{
		Transaction: tx,
		AttestationResult: []*prototk.AttestationResult{
			{Name: "sender", Verifier: &prototk.ResolvedVerifier{Verifier: senderKey.Address.String()}},
			{Name: "notary", Verifier: &prototk.ResolvedVerifier{Lookup: "notary@node1"}},
		},
	})
	require.ErrorContains(t, err, "PD200048")
}
