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

package zeto

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/LFDT-Paladin/paladin/domains/zeto/internal/zeto/common"
	"github.com/LFDT-Paladin/paladin/domains/zeto/internal/zeto/smt"
	"github.com/LFDT-Paladin/paladin/domains/zeto/pkg/types"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/domain"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordTransactionInfo(t *testing.T) {
	ev := &prototk.OnChainEvent{
		Location: &prototk.OnChainEventLocation{},
	}
	txData := &types.ZetoTransactionData_V0{
		TransactionID: pldtypes.MustParseBytes32("0x30e43028afbb41d6887444f4c2b4ed6d00000000000000000000000000000000"),
		InfoStates: []pldtypes.Bytes32{
			pldtypes.MustParseBytes32("0x0000000000000000000000000000000000000000000000000000000000001234"),
			pldtypes.MustParseBytes32("0x0000000000000000000000000000000000000000000000000000000000000000"),
		},
	}

	res := &prototk.HandleEventBatchResponse{}
	z := &Zeto{}
	z.recordTransactionInfo(ev, txData, res)
	assert.Len(t, res.InfoStates, 2)
	assert.Equal(t, "0x0000000000000000000000000000000000000000000000000000000000001234", res.InfoStates[0].Id)
	assert.Equal(t, "0x0000000000000000000000000000000000000000000000000000000000000000", res.InfoStates[1].Id)
}

func TestEncodeDecode(t *testing.T) {
	ctx := context.Background()
	encodedData, err := common.EncodeTransactionData(ctx, &prototk.TransactionSpecification{
		TransactionId: "0x30e43028afbb41d6887444f4c2b4ed6d00000000000000000000000000000000",
	}, nil)
	require.NoError(t, err)
	decodedData, err := decodeTransactionData(ctx, encodedData)
	require.NoError(t, err)
	assert.Equal(t, "0x30e43028afbb41d6887444f4c2b4ed6d00000000000000000000000000000000", decodedData.TransactionID.String())
}

func TestHandleMintEvent(t *testing.T) {
	z, testCallbacks := newTestZeto()
	storage := smt.NewStatesStorage(testCallbacks, "testToken1", "context1", "merkle_tree_root", "merkle_tree_node")
	merkleTree, err := smt.NewSmt(storage, smt.SMT_HEIGHT_UTXO)
	require.NoError(t, err)
	ctx := context.Background()

	ev := &prototk.OnChainEvent{
		DataJson:          "bad json",
		SoliditySignature: "event UTXOMint(uint256[] outputs, address indexed submitter, bytes data)",
	}

	smtSpec := &common.MerkleTreeSpec{Tree: merkleTree, Storage: storage}

	// bad transaction data for the mint event - should be logged and move on
	res := &prototk.HandleEventBatchResponse{}
	err = z.handleMintEvent(ctx, smtSpec, ev, "testToken1", res)
	assert.NoError(t, err)
	ev.DataJson = "{\"data\":\"0x0001\",\"outputs\":[\"7980718117603030807695495350922077879582656644717071592146865497574198464253\"],\"submitter\":\"0x74e71b05854ee819cb9397be01c82570a178d019\"}"
	res = &prototk.HandleEventBatchResponse{}
	err = z.handleMintEvent(ctx, smtSpec, ev, "testToken1", res)
	assert.NoError(t, err)
	ev.DataJson = "{\"data\":\"0x0001ffff\",\"outputs\":[\"7980718117603030807695495350922077879582656644717071592146865497574198464253\"],\"submitter\":\"0x74e71b05854ee819cb9397be01c82570a178d019\"}"
	res = &prototk.HandleEventBatchResponse{}
	err = z.handleMintEvent(ctx, smtSpec, ev, "testToken1", res)
	assert.NoError(t, err)

	encodedData, err := common.EncodeTransactionData(ctx, &prototk.TransactionSpecification{
		TransactionId: "0x30e43028afbb41d6887444f4c2b4ed6d00000000000000000000000000000000",
	}, nil)
	require.NoError(t, err)

	data, _ := json.Marshal(map[string]any{
		"data":      encodedData,
		"outputs":   []string{"7980718117603030807695495350922077879582656644717071592146865497574198464253"},
		"submitter": "0x74e71b05854ee819cb9397be01c82570a178d019",
	})
	ev.DataJson = string(data)
	res = &prototk.HandleEventBatchResponse{}
	err = z.handleMintEvent(ctx, smtSpec, ev, "testToken1", res)
	assert.NoError(t, err)
	assert.Len(t, res.TransactionsComplete, 1)
	assert.Equal(t, "0x30e43028afbb41d6887444f4c2b4ed6d00000000000000000000000000000000", res.TransactionsComplete[0].TransactionId)

	data, _ = json.Marshal(map[string]any{
		"data":      encodedData,
		"outputs":   []string{"0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"},
		"submitter": "0x74e71b05854ee819cb9397be01c82570a178d019",
	})
	ev.DataJson = string(data)
	res = &prototk.HandleEventBatchResponse{}
	err = z.handleMintEvent(ctx, smtSpec, ev, "Zeto_AnonNullifier", res)
	assert.ErrorContains(t, err, "PD210061: Failed to update merkle tree for the UTXOMint event. PD210056: Failed to create new node index from hash. ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")

	storage = smt.NewStatesStorage(testCallbacks, "testToken1", "context1", "merkle_tree_root", "merkle_tree_node")
	merkleTree, err = smt.NewSmt(storage, smt.SMT_HEIGHT_UTXO)
	require.NoError(t, err)
	smtSpec.Tree = merkleTree

	data, _ = json.Marshal(map[string]any{
		"data":      encodedData,
		"outputs":   []string{"7980718117603030807695495350922077879582656644717071592146865497574198464253"},
		"submitter": "0x74e71b05854ee819cb9397be01c82570a178d019",
	})
	ev.DataJson = string(data)
	res = &prototk.HandleEventBatchResponse{}
	err = z.handleMintEvent(ctx, smtSpec, ev, "Zeto_AnonNullifier", res)
	assert.NoError(t, err)
	assert.Len(t, res.TransactionsComplete, 1)
	newStates, err := storage.GetNewStates()
	require.NoError(t, err)
	assert.Len(t, newStates, 2)
	assert.Equal(t, "merkle_tree_root", newStates[0].SchemaId)
	assert.Equal(t, "merkle_tree_node", newStates[1].SchemaId)
}

func TestHandleTransferEvent(t *testing.T) {
	z, testCallbacks := newTestZeto()
	storage := smt.NewStatesStorage(testCallbacks, "testToken1", "context1", "merkle_tree_root", "merkle_tree_node")
	merkleTree, err := smt.NewSmt(storage, smt.SMT_HEIGHT_UTXO)
	require.NoError(t, err)
	ctx := context.Background()

	ev := &prototk.OnChainEvent{
		DataJson:          "bad json",
		SoliditySignature: "event UTXOTransfer(uint256[] inputs, uint256[] outputs, address indexed submitter, bytes data)",
	}

	smtSpec := &common.MerkleTreeSpec{Tree: merkleTree, Storage: storage}

	// bad data for the transfer event - should be logged and move on
	res := &prototk.HandleEventBatchResponse{}
	err = z.handleTransferEvent(ctx, smtSpec, ev, "Zeto_AnonNullifier", res)
	assert.NoError(t, err)
	ev.DataJson = "{\"data\":\"0x0001\",\"outputs\":[\"7980718117603030807695495350922077879582656644717071592146865497574198464253\"],\"submitter\":\"0x74e71b05854ee819cb9397be01c82570a178d019\"}"
	res = &prototk.HandleEventBatchResponse{}
	err = z.handleTransferEvent(ctx, smtSpec, ev, "Zeto_AnonNullifier", res)
	assert.NoError(t, err)
	ev.DataJson = "{\"data\":\"0x0001ffff\",\"outputs\":[\"7980718117603030807695495350922077879582656644717071592146865497574198464253\"],\"submitter\":\"0x74e71b05854ee819cb9397be01c82570a178d019\"}"
	res = &prototk.HandleEventBatchResponse{}
	err = z.handleTransferEvent(ctx, smtSpec, ev, "Zeto_AnonNullifier", res)
	assert.NoError(t, err)

	encodedData, err := common.EncodeTransactionData(ctx, &prototk.TransactionSpecification{
		TransactionId: "0x30e43028afbb41d6887444f4c2b4ed6d00000000000000000000000000000000",
	}, nil)
	require.NoError(t, err)

	data, _ := json.Marshal(map[string]any{
		"data":      encodedData,
		"outputs":   []string{"7980718117603030807695495350922077879582656644717071592146865497574198464253"},
		"submitter": "0x74e71b05854ee819cb9397be01c82570a178d019",
	})
	ev.DataJson = string(data)
	res = &prototk.HandleEventBatchResponse{}
	err = z.handleTransferEvent(ctx, smtSpec, ev, "Zeto_AnonNullifier", res)
	assert.NoError(t, err)
	assert.Len(t, res.TransactionsComplete, 1)
	assert.Equal(t, "0x30e43028afbb41d6887444f4c2b4ed6d00000000000000000000000000000000", res.TransactionsComplete[0].TransactionId)

	data, _ = json.Marshal(map[string]any{
		"data":      encodedData,
		"outputs":   []string{"0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"},
		"submitter": "0x74e71b05854ee819cb9397be01c82570a178d019",
	})
	ev.DataJson = string(data)
	res = &prototk.HandleEventBatchResponse{}
	err = z.handleTransferEvent(ctx, smtSpec, ev, "Zeto_AnonNullifier", res)
	assert.ErrorContains(t, err, "PD210061: Failed to update merkle tree for the UTXOTransfer event. PD210056: Failed to create new node index from hash. ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")

	storage = smt.NewStatesStorage(testCallbacks, "testToken1", "context1", "merkle_tree_root", "merkle_tree_node")
	merkleTree, err = smt.NewSmt(storage, smt.SMT_HEIGHT_UTXO)
	require.NoError(t, err)
	smtSpec.Tree = merkleTree

	data, _ = json.Marshal(map[string]any{
		"data":      encodedData,
		"outputs":   []string{"7980718117603030807695495350922077879582656644717071592146865497574198464253"},
		"submitter": "0x74e71b05854ee819cb9397be01c82570a178d019",
	})
	ev.DataJson = string(data)
	res = &prototk.HandleEventBatchResponse{}
	err = z.handleTransferEvent(ctx, smtSpec, ev, "Zeto_AnonNullifier", res)
	assert.NoError(t, err)
	assert.Len(t, res.TransactionsComplete, 1)
	// assert.Len(t, res6.NewStates, 2)
	// assert.Equal(t, "merkle_tree_root", res6.NewStates[0].SchemaId)
	// assert.Equal(t, "merkle_tree_node", res6.NewStates[1].SchemaId)
}

func TestHandleTransferWithEncryptionEvent(t *testing.T) {
	z, testCallbacks := newTestZeto()
	storage := smt.NewStatesStorage(testCallbacks, "testToken1", "context1", "merkle_tree_root", "merkle_tree_node")
	merkleTree, err := smt.NewSmt(storage, smt.SMT_HEIGHT_UTXO)
	require.NoError(t, err)
	ctx := context.Background()

	ev := &prototk.OnChainEvent{
		DataJson:          "bad json",
		SoliditySignature: "event UTXOTransferWithEncryptedValues(uint256[] inputs, uint256[] outputs, uint256 encryptionNonce, uint256[2] ecdhPublicKey, uint256[] encryptedValues, address indexed submitter, bytes data)",
	}

	smtSpec := &common.MerkleTreeSpec{Tree: merkleTree, Storage: storage}

	// bad data for the transfer event - should be logged and move on
	res := &prototk.HandleEventBatchResponse{}
	err = z.handleTransferWithEncryptionEvent(ctx, smtSpec, ev, "Zeto_AnonNullifier", res)
	assert.NoError(t, err)
	ev.DataJson = "{\"data\":\"0x0001\",\"outputs\":[\"7980718117603030807695495350922077879582656644717071592146865497574198464253\"],\"submitter\":\"0x74e71b05854ee819cb9397be01c82570a178d019\"}"
	res = &prototk.HandleEventBatchResponse{}
	err = z.handleTransferWithEncryptionEvent(ctx, smtSpec, ev, "Zeto_AnonNullifier", res)
	assert.NoError(t, err)
	ev.DataJson = "{\"data\":\"0x0001ffff\",\"outputs\":[\"7980718117603030807695495350922077879582656644717071592146865497574198464253\"],\"submitter\":\"0x74e71b05854ee819cb9397be01c82570a178d019\"}"
	res = &prototk.HandleEventBatchResponse{}
	err = z.handleTransferWithEncryptionEvent(ctx, smtSpec, ev, "Zeto_AnonNullifier", res)
	assert.NoError(t, err)

	encodedData, err := common.EncodeTransactionData(ctx, &prototk.TransactionSpecification{
		TransactionId: "0x30e43028afbb41d6887444f4c2b4ed6d00000000000000000000000000000000",
	}, nil)
	require.NoError(t, err)

	data, _ := json.Marshal(map[string]any{
		"data":      encodedData,
		"outputs":   []string{"7980718117603030807695495350922077879582656644717071592146865497574198464253"},
		"submitter": "0x74e71b05854ee819cb9397be01c82570a178d019",
	})
	ev.DataJson = string(data)
	res = &prototk.HandleEventBatchResponse{}
	err = z.handleTransferWithEncryptionEvent(ctx, smtSpec, ev, "Zeto_AnonNullifier", res)
	assert.NoError(t, err)
	assert.Equal(t, "0x30e43028afbb41d6887444f4c2b4ed6d00000000000000000000000000000000", res.TransactionsComplete[0].TransactionId)

	data, _ = json.Marshal(map[string]any{
		"data":      encodedData,
		"outputs":   []string{"0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"},
		"submitter": "0x74e71b05854ee819cb9397be01c82570a178d019",
	})
	ev.DataJson = string(data)
	res = &prototk.HandleEventBatchResponse{}
	err = z.handleTransferWithEncryptionEvent(ctx, smtSpec, ev, "Zeto_AnonNullifier", res)
	assert.ErrorContains(t, err, "PD210061: Failed to update merkle tree for the UTXOTransferWithEncryptedValues event. PD210056: Failed to create new node index from hash. ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")

	storage = smt.NewStatesStorage(testCallbacks, "testToken1", "context1", "merkle_tree_root", "merkle_tree_node")
	merkleTree, err = smt.NewSmt(storage, smt.SMT_HEIGHT_UTXO)
	require.NoError(t, err)
	smtSpec.Tree = merkleTree

	data, _ = json.Marshal(map[string]any{
		"data":      encodedData,
		"outputs":   []string{"7980718117603030807695495350922077879582656644717071592146865497574198464253"},
		"submitter": "0x74e71b05854ee819cb9397be01c82570a178d019",
	})
	ev.DataJson = string(data)
	res = &prototk.HandleEventBatchResponse{}
	err = z.handleTransferWithEncryptionEvent(ctx, smtSpec, ev, "Zeto_AnonNullifier", res)
	assert.NoError(t, err)
	assert.Equal(t, "0x30e43028afbb41d6887444f4c2b4ed6d00000000000000000000000000000000", res.TransactionsComplete[0].TransactionId)
}

func TestHandleLockedEvent(t *testing.T) {
	z, testCallbacks := newTestZeto()
	storage1 := smt.NewStatesStorage(testCallbacks, "testToken1", "context1", "merkle_tree_root", "merkle_tree_node")
	merkleTree1, err := smt.NewSmt(storage1, smt.SMT_HEIGHT_UTXO)
	require.NoError(t, err)
	storage2 := smt.NewStatesStorage(testCallbacks, "testToken2", "context1", "merkle_tree_root", "merkle_tree_node")
	merkleTree2, err := smt.NewSmt(storage2, smt.SMT_HEIGHT_UTXO)
	require.NoError(t, err)
	ctx := context.Background()

	ev := &prototk.OnChainEvent{
		DataJson:          "bad json",
		SoliditySignature: "event UTXOsLocked(uint256[] inputs, uint256[] outputs, uint256[] lockedOutputs, address indexed delegate, address indexed submitter, bytes data)",
	}
	res := &prototk.HandleEventBatchResponse{}

	smtSpec1 := &common.MerkleTreeSpec{Tree: merkleTree1, Storage: storage1}
	smtSpec2 := &common.MerkleTreeSpec{Tree: merkleTree2, Storage: storage2}

	// bad data for the locked event - should be logged and move on
	err = z.handleLockedEvent(ctx, smtSpec1, smtSpec2, ev, "Zeto_AnonNullifier", res)
	assert.NoError(t, err)
	ev.DataJson = "{\"data\":\"0x0001\",\"outputs\":[\"7980718117603030807695495350922077879582656644717071592146865497574198464253\"],\"submitter\":\"0x74e71b05854ee819cb9397be01c82570a178d019\"}"
	err = z.handleLockedEvent(ctx, smtSpec1, smtSpec2, ev, "Zeto_AnonNullifier", res)
	assert.NoError(t, err)
	ev.DataJson = "{\"data\":\"0x0001ffff\",\"outputs\":[\"7980718117603030807695495350922077879582656644717071592146865497574198464253\"],\"submitter\":\"0x74e71b05854ee819cb9397be01c82570a178d019\"}"
	err = z.handleLockedEvent(ctx, smtSpec1, smtSpec2, ev, "Zeto_AnonNullifier", res)
	assert.NoError(t, err)

	encodedData, err := common.EncodeTransactionData(ctx, &prototk.TransactionSpecification{
		TransactionId: "0x30e43028afbb41d6887444f4c2b4ed6d00000000000000000000000000000000",
	}, nil)
	require.NoError(t, err)

	data, _ := json.Marshal(map[string]any{
		"data":      encodedData,
		"outputs":   []string{"7980718117603030807695495350922077879582656644717071592146865497574198464253"},
		"submitter": "0x74e71b05854ee819cb9397be01c82570a178d019",
	})
	ev.DataJson = string(data)
	err = z.handleLockedEvent(ctx, smtSpec1, smtSpec2, ev, "Zeto_AnonNullifier", res)
	assert.NoError(t, err)
	assert.Len(t, res.TransactionsComplete, 1)
}

func TestUpdateMerkleTree(t *testing.T) {
	z, testCallbacks := newTestZeto()
	storage := smt.NewStatesStorage(testCallbacks, "testToken1", "context1", "merkle_tree_root", "merkle_tree_node")
	merkleTree, err := smt.NewSmt(storage, smt.SMT_HEIGHT_UTXO)
	require.NoError(t, err)

	ctx := context.Background()
	err = z.updateMerkleTree(ctx, merkleTree, storage, pldtypes.RandBytes32(), []pldtypes.HexUint256{*pldtypes.MustParseHexUint256("0x1234"), *pldtypes.MustParseHexUint256("0x0")})
	assert.NoError(t, err)
}

func TestHandleWithdrawEvent(t *testing.T) {
	z, testCallbacks := newTestZeto()
	storage := smt.NewStatesStorage(testCallbacks, "testToken1", "context1", "merkle_tree_root", "merkle_tree_node")
	merkleTree, err := smt.NewSmt(storage, smt.SMT_HEIGHT_UTXO)
	require.NoError(t, err)
	ctx := context.Background()

	ev := &prototk.OnChainEvent{
		DataJson:          "bad json",
		SoliditySignature: "event UTXOWithdraw(uint256 amount, uint256[] inputs, uint256 output, address indexed submitter, bytes data)",
	}

	smtSpec := &common.MerkleTreeSpec{Tree: merkleTree, Storage: storage}

	// bad data for the withdraw event - should be logged and move on
	res := &prototk.HandleEventBatchResponse{}
	err = z.handleWithdrawEvent(ctx, smtSpec, ev, "Zeto_Anon", res)
	assert.NoError(t, err)

	encodedData, err := common.EncodeTransactionData(ctx, &prototk.TransactionSpecification{
		TransactionId: "0x30e43028afbb41d6887444f4c2b4ed6d00000000000000000000000000000000",
	}, nil)
	require.NoError(t, err)

	data, _ := json.Marshal(map[string]any{
		"data":      encodedData,
		"inputs":    []string{"7980718117603030807695495350922077879582656644717071592146865497574198464253"},
		"output":    "7980718117603030807695495350922077879582656644717071592146865497574198464253",
		"submitter": "0x74e71b05854ee819cb9397be01c82570a178d019",
	})
	ev.DataJson = string(data)
	res = &prototk.HandleEventBatchResponse{}
	err = z.handleWithdrawEvent(ctx, smtSpec, ev, "Zeto_Anon", res)
	assert.NoError(t, err)

	data, _ = json.Marshal(map[string]any{
		"data":      encodedData,
		"inputs":    []string{"7980718117603030807695495350922077879582656644717071592146865497574198464253"},
		"output":    "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		"submitter": "0x74e71b05854ee819cb9397be01c82570a178d019",
	})
	ev.DataJson = string(data)
	res = &prototk.HandleEventBatchResponse{}
	err = z.handleWithdrawEvent(ctx, smtSpec, ev, "Zeto_Anon", res)
	assert.NoError(t, err)

	data, _ = json.Marshal(map[string]any{
		"data":      encodedData,
		"inputs":    []string{"7980718117603030807695495350922077879582656644717071592146865497574198464253"},
		"output":    "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		"submitter": "0x74e71b05854ee819cb9397be01c82570a178d019",
	})
	ev.DataJson = string(data)
	res = &prototk.HandleEventBatchResponse{}
	err = z.handleWithdrawEvent(ctx, smtSpec, ev, "Zeto_AnonNullifier", res)
	assert.NoError(t, err) // UTXO tree update errors are non-fatal (logged as warning)
}

func TestParseStatesFromEvent(t *testing.T) {
	txID := pldtypes.RandBytes32()
	states := parseStatesFromEvent(txID, []pldtypes.HexUint256{*pldtypes.MustParseHexUint256("0x1234"), *pldtypes.MustParseHexUint256("0x0")})
	assert.Len(t, states, 2)
	assert.Equal(t, "0000000000000000000000000000000000000000000000000000000000001234", states[0].Id)
	assert.Equal(t, "0000000000000000000000000000000000000000000000000000000000000000", states[1].Id)
}

func TestHandleIdentityRegisteredEvent(t *testing.T) {
	z, testCallbacks := newTestZeto()
	storage := smt.NewStatesStorage(testCallbacks, "testToken1", "context1", "merkle_tree_root", "merkle_tree_node")
	merkleTree, err := smt.NewSmt(storage, smt.SMT_HEIGHT_KYC)
	require.NoError(t, err)
	smtSpec := &common.MerkleTreeSpec{Tree: merkleTree, Storage: storage}

	count := 0
	data, _ := json.Marshal(map[string]string{"rootIndex": "0x1234567890123456789012345678901234567890123456789012345678901234"})
	errCallbacks := &domain.MockDomainCallbacks{
		MockFindAvailableStates: func() (*prototk.FindAvailableStatesResponse, error) {
			if count == 0 {
				count++
				return &prototk.FindAvailableStatesResponse{
					States: []*prototk.StoredState{
						{
							DataJson: string(data),
						},
					},
				}, nil
			}
			// Return error to simulate generateMerkleProofs failure, which allows us to test error handling
			return nil, errors.New("already exists")
		},
	}
	errStorage := smt.NewStatesStorage(errCallbacks, "testToken1", "context1", "merkle_tree_root", "merkle_tree_node")
	errMerkleTree, err := smt.NewSmt(errStorage, smt.SMT_HEIGHT_KYC)
	require.NoError(t, err)
	errSmtSpec := &common.MerkleTreeSpec{Tree: errMerkleTree, Storage: errStorage}

	ctx := context.Background()

	encodedData, _ := common.EncodeTransactionData(ctx, &prototk.TransactionSpecification{
		TransactionId: "0x30e43028afbb41d6887444f4c2b4ed6d00000000000000000000000000000000",
	}, nil)
	data, _ = json.Marshal(map[string]any{
		"data":      encodedData,
		"publicKey": []string{"7980718117603030807695495350922077879582656644717071592146865497574198464253", "7980718117603030807695495350922077879582656644717071592146865497574198464253"},
	})

	ev := &prototk.OnChainEvent{
		DataJson:          string(data),
		SoliditySignature: "event IdentityRegistered(uint256[] publicKey, bytes data)",
	}

	t.Run("valid data for the identity registered event", func(t *testing.T) {
		res := &prototk.HandleEventBatchResponse{}
		err = z.handleIdentityRegisteredEvent(ctx, smtSpec, nil, ev, "Zeto_AnonNullifierKyc", nil, res)
		assert.NoError(t, err)
	})

	t.Run("bad data for the identity registered event - should be logged and move on", func(t *testing.T) {
		ev.DataJson = "bad json"
		res := &prototk.HandleEventBatchResponse{}
		err = z.handleIdentityRegisteredEvent(ctx, smtSpec, nil, ev, "Zeto_AnonNullifierKyc", nil, res)
		assert.NoError(t, err)
	})

	t.Run("event with no data - generate one", func(t *testing.T) {
		data, _ := json.Marshal(map[string]any{
			"publicKey": []string{"7980718117603030807695495350922077879582656644717071592146865497574198464252", "7980718117603030807695495350922077879582656644717071592146865497574198464251"},
		})
		ev.DataJson = string(data)
		res := &prototk.HandleEventBatchResponse{}
		err = z.handleIdentityRegisteredEvent(ctx, smtSpec, nil, ev, "Zeto_AnonNullifierKyc", nil, res)
		assert.NoError(t, err)
	})

	t.Run("public key is not valid field - should return error", func(t *testing.T) {
		data, _ := json.Marshal(map[string]any{
			"data":      encodedData,
			"publicKey": []string{"0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"},
		})
		ev.DataJson = string(data)
		res := &prototk.HandleEventBatchResponse{}
		err = z.handleIdentityRegisteredEvent(ctx, smtSpec, nil, ev, "Zeto_AnonNullifierKyc", nil, res)
		assert.ErrorContains(t, err, "PD210020: Failed to handle events IdentityRegistered. inputs values not inside Finite Field")
	})

	t.Run("failed to update SMT - should return error", func(t *testing.T) {
		data, _ := json.Marshal(map[string]any{
			"data":      encodedData,
			"publicKey": []string{"7980718117603030807695495350922077879582656644717071592146865497574198464252", "7980718117603030807695495350922077879582656644717071592146865497574198464251"},
		})
		ev.DataJson = string(data)
		res := &prototk.HandleEventBatchResponse{}
		err = z.handleIdentityRegisteredEvent(ctx, errSmtSpec, nil, ev, "Zeto_AnonNullifierKyc", nil, res)
		assert.NoError(t, err)
	})
}

func TestEncodeDecodeWithSpentCommitments(t *testing.T) {
	ctx := context.Background()
	commitment1 := pldtypes.MustParseHexUint256("0x1111111111111111111111111111111111111111111111111111111111111111")
	commitment2 := pldtypes.MustParseHexUint256("0x2222222222222222222222222222222222222222222222222222222222222222")

	encodedData, err := common.EncodeTransactionDataWithSpentCommitments(ctx, &prototk.TransactionSpecification{
		TransactionId: "0x30e43028afbb41d6887444f4c2b4ed6d00000000000000000000000000000000",
	}, nil, []pldtypes.HexUint256{*commitment1, *commitment2})
	require.NoError(t, err)

	decodedData, err := decodeTransactionData(ctx, encodedData)
	require.NoError(t, err)
	assert.Equal(t, "0x30e43028afbb41d6887444f4c2b4ed6d00000000000000000000000000000000", decodedData.TransactionID.String())
	require.Len(t, decodedData.SpentCommitments, 2)
	assert.Equal(t, commitment1.String(), decodedData.SpentCommitments[0].String())
	assert.Equal(t, commitment2.String(), decodedData.SpentCommitments[1].String())
}

func TestEncodeDecodeEmptySpentCommitments(t *testing.T) {
	ctx := context.Background()

	// Standard EncodeTransactionData should produce empty SpentCommitments
	encodedData, err := common.EncodeTransactionData(ctx, &prototk.TransactionSpecification{
		TransactionId: "0x30e43028afbb41d6887444f4c2b4ed6d00000000000000000000000000000000",
	}, nil)
	require.NoError(t, err)

	decodedData, err := decodeTransactionData(ctx, encodedData)
	require.NoError(t, err)
	assert.Equal(t, "0x30e43028afbb41d6887444f4c2b4ed6d00000000000000000000000000000000", decodedData.TransactionID.String())
	assert.Empty(t, decodedData.SpentCommitments)
}

func TestHandleForcedTransferEnforcedEvent(t *testing.T) {
	z, testCallbacks := newTestZeto()
	storage := smt.NewStatesStorage(testCallbacks, "testToken1", "context1", "merkle_tree_root", "merkle_tree_node")
	merkleTree, err := smt.NewSmt(storage, smt.SMT_HEIGHT_UTXO)
	require.NoError(t, err)
	ctx := context.Background()
	smtSpec := &common.MerkleTreeSpec{Tree: merkleTree, Storage: storage}

	ev := &prototk.OnChainEvent{
		SoliditySignature: "event UTXOForcedTransferEnforced(uint256[] enforcementNullifiers, uint256[] outputs, uint256 encryptionNonce, uint256[2] ecdhPublicKey, uint256[] encryptedValuesForReceiver, uint256[] encryptedValuesForArbiter, uint256[] encryptedValuesForEnforcer, uint256 arbiterKeyId, address indexed submitter, bytes data)",
	}

	t.Run("bad json - should log and move on", func(t *testing.T) {
		ev.DataJson = "bad json"
		res := &prototk.HandleEventBatchResponse{}
		err = z.handleForcedTransferEnforcedEvent(ctx, smtSpec, ev, "Zeto_AnonEncNullifierKycNonRepudiationEnforced", res)
		assert.NoError(t, err)
		assert.Empty(t, res.SpentStates)
	})

	t.Run("with spent commitments - marks states as spent", func(t *testing.T) {
		commitment1 := pldtypes.MustParseHexUint256("0x1111111111111111111111111111111111111111111111111111111111111111")
		commitment2 := pldtypes.MustParseHexUint256("0x2222222222222222222222222222222222222222222222222222222222222222")

		encodedData, err := common.EncodeTransactionDataWithSpentCommitments(ctx, &prototk.TransactionSpecification{
			TransactionId: "0xaaaa0000bbbb11112222333344445555aaaa0000bbbb111122223333444455aa",
		}, nil, []pldtypes.HexUint256{*commitment1, *commitment2})
		require.NoError(t, err)

		output := pldtypes.MustParseHexUint256("0x119eda5a40724c3e976a8cfc74e1e853e2af34772a8cce1e5e1100afb5b6a8c5")
		data, _ := json.Marshal(map[string]any{
			"enforcementNullifiers":      []string{"123", "456"},
			"outputs":                    []string{output.Int().Text(10)},
			"encryptionNonce":            "0",
			"ecdhPublicKey":              []string{"0", "0"},
			"encryptedValuesForReceiver": []string{},
			"encryptedValuesForArbiter":  []string{},
			"encryptedValuesForEnforcer": []string{},
			"arbiterKeyId":               "0",
			"submitter":                  "0x74e71b05854ee819cb9397be01c82570a178d019",
			"data":                       encodedData,
		})
		ev.DataJson = string(data)
		res := &prototk.HandleEventBatchResponse{}
		err = z.handleForcedTransferEnforcedEvent(ctx, smtSpec, ev, "Zeto_AnonEncNullifierKycNonRepudiationEnforced", res)
		assert.NoError(t, err)

		// Verify transaction completed
		require.Len(t, res.TransactionsComplete, 1)
		assert.Equal(t, "0xaaaa0000bbbb11112222333344445555aaaa0000bbbb111122223333444455aa", res.TransactionsComplete[0].TransactionId)

		// Verify spent states from SpentCommitments
		require.Len(t, res.SpentStates, 2)
		assert.Equal(t, common.HexUint256To32ByteHexString(commitment1), res.SpentStates[0].Id)
		assert.Equal(t, common.HexUint256To32ByteHexString(commitment2), res.SpentStates[1].Id)

		// Verify confirmed states from outputs
		require.Len(t, res.ConfirmedStates, 1)
	})

	t.Run("without spent commitments - no spent states", func(t *testing.T) {
		// Fresh SMT to avoid "key already exists" from previous subtest
		storage2 := smt.NewStatesStorage(testCallbacks, "testToken2", "context2", "merkle_tree_root", "merkle_tree_node")
		merkleTree2, err := smt.NewSmt(storage2, smt.SMT_HEIGHT_UTXO)
		require.NoError(t, err)
		smtSpec = &common.MerkleTreeSpec{Tree: merkleTree2, Storage: storage2}

		encodedData, err := common.EncodeTransactionData(ctx, &prototk.TransactionSpecification{
			TransactionId: "0xbbbb0000cccc11112222333344445555bbbb0000cccc111122223333444455bb",
		}, nil)
		require.NoError(t, err)

		output := pldtypes.MustParseHexUint256("0x119eda5a40724c3e976a8cfc74e1e853e2af34772a8cce1e5e1100afb5b6a8c5")
		data, _ := json.Marshal(map[string]any{
			"enforcementNullifiers":      []string{"789"},
			"outputs":                    []string{output.Int().Text(10)},
			"encryptionNonce":            "0",
			"ecdhPublicKey":              []string{"0", "0"},
			"encryptedValuesForReceiver": []string{},
			"encryptedValuesForArbiter":  []string{},
			"encryptedValuesForEnforcer": []string{},
			"arbiterKeyId":               "0",
			"submitter":                  "0x74e71b05854ee819cb9397be01c82570a178d019",
			"data":                       encodedData,
		})
		ev.DataJson = string(data)
		res := &prototk.HandleEventBatchResponse{}
		err = z.handleForcedTransferEnforcedEvent(ctx, smtSpec, ev, "Zeto_AnonEncNullifierKycNonRepudiationEnforced", res)
		assert.NoError(t, err)

		// No spent states when SpentCommitments is empty
		assert.Empty(t, res.SpentStates)
		// But outputs are still confirmed
		require.Len(t, res.ConfirmedStates, 1)
	})
}
