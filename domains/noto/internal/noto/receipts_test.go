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
	"encoding/json"
	"fmt"
	"testing"

	"github.com/LFDT-Paladin/paladin/domains/noto/pkg/types"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/domain"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/google/uuid"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newNotoFullSchemaSet(t *testing.T) (context.Context, *domain.MockDomainCallbacks, *Noto) {
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
	return t.Context(), mockCallbacks, n
}

func TestReceiptTransfers(t *testing.T) {
	mockCallbacks := newMockCallbacks()
	n := &Noto{
		Callbacks:        mockCallbacks,
		coinSchema:       testSchema("coin"),
		lockedCoinSchema: testSchema("lockedCoin"),
	}
	ctx := t.Context()

	transfers, err := n.receiptTransfers(ctx, &prototk.BuildReceiptRequest{
		InputStates:  []*prototk.EndorsableState{},
		OutputStates: []*prototk.EndorsableState{},
	})
	require.NoError(t, err)
	require.Len(t, transfers, 0)

	owner1 := pldtypes.MustParseChainAddress("0xbb2b99dde4ca2d4c99f149d13cd55a9edada69eb")
	owner2 := pldtypes.MustParseChainAddress("0x3008ee73a70cd1cc57647c7d253a48defe86dd9b")
	owner3 := pldtypes.MustParseChainAddress("0xea82df8cb94b9fa1a0a711d75a7f7dfdec2ef53c")

	// Simple mint
	transfers, err = n.receiptTransfers(ctx, &prototk.BuildReceiptRequest{
		InputStates: []*prototk.EndorsableState{},
		OutputStates: []*prototk.EndorsableState{{
			Id:       "1",
			SchemaId: hashName("coin"),
			StateDataJson: fmt.Sprintf(`{
				"amount": 1,
				"owner": "%s"
			}`, owner1),
		}},
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []*types.ReceiptTransfer{{
		From:   nil,
		To:     owner1,
		Amount: pldtypes.Int64ToInt256(1),
	}}, transfers)

	// Simple burn
	transfers, err = n.receiptTransfers(ctx, &prototk.BuildReceiptRequest{
		InputStates: []*prototk.EndorsableState{{
			Id:       "1",
			SchemaId: hashName("coin"),
			StateDataJson: fmt.Sprintf(`{
				"amount": 1,
				"owner": "%s"
			}`, owner1),
		}},
		OutputStates: []*prototk.EndorsableState{},
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []*types.ReceiptTransfer{{
		From:   owner1,
		To:     nil,
		Amount: pldtypes.Int64ToInt256(1),
	}}, transfers)

	// Burn with returned remainder
	transfers, err = n.receiptTransfers(ctx, &prototk.BuildReceiptRequest{
		InputStates: []*prototk.EndorsableState{{
			Id:       "1",
			SchemaId: hashName("coin"),
			StateDataJson: fmt.Sprintf(`{
				"amount": 10,
				"owner": "%s"
			}`, owner1),
		}},
		OutputStates: []*prototk.EndorsableState{{
			Id:       "2",
			SchemaId: hashName("coin"),
			StateDataJson: fmt.Sprintf(`{
				"amount": 8,
				"owner": "%s"
			}`, owner1),
		}},
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []*types.ReceiptTransfer{{
		From:   owner1,
		To:     nil,
		Amount: pldtypes.Int64ToInt256(2),
	}}, transfers)

	// Simple transfer
	transfers, err = n.receiptTransfers(ctx, &prototk.BuildReceiptRequest{
		InputStates: []*prototk.EndorsableState{{
			Id:       "1",
			SchemaId: hashName("coin"),
			StateDataJson: fmt.Sprintf(`{
				"amount": 1,
				"owner": "%s"
			}`, owner1),
		}},
		OutputStates: []*prototk.EndorsableState{{
			Id:       "2",
			SchemaId: hashName("coin"),
			StateDataJson: fmt.Sprintf(`{
				"amount": 1,
				"owner": "%s"
			}`, owner2),
		}},
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []*types.ReceiptTransfer{{
		From:   owner1,
		To:     owner2,
		Amount: pldtypes.Int64ToInt256(1),
	}}, transfers)

	// Unlock to multiple recipients, with locked remainder
	transfers, err = n.receiptTransfers(ctx, &prototk.BuildReceiptRequest{
		InputStates: []*prototk.EndorsableState{{
			Id:       "1",
			SchemaId: hashName("lockedCoin"),
			StateDataJson: fmt.Sprintf(`{
				"amount": 10,
				"owner": "%s"
			}`, owner1),
		}},
		OutputStates: []*prototk.EndorsableState{{
			Id:       "2",
			SchemaId: hashName("coin"),
			StateDataJson: fmt.Sprintf(`{
				"amount": 1,
				"owner": "%s"
			}`, owner2),
		}, {
			Id:       "3",
			SchemaId: hashName("coin"),
			StateDataJson: fmt.Sprintf(`{
				"amount": 1,
				"owner": "%s"
			}`, owner3),
		}, {
			Id:       "4",
			SchemaId: hashName("coin"),
			StateDataJson: fmt.Sprintf(`{
				"amount": 1,
				"owner": "%s"
			}`, owner3),
		}, {
			Id:       "5",
			SchemaId: hashName("lockedCoin"),
			StateDataJson: fmt.Sprintf(`{
				"amount": 7,
				"owner": "%s"
			}`, owner1),
		}},
	})
	require.NoError(t, err)
	assert.ElementsMatch(t, []*types.ReceiptTransfer{{
		From:   owner1,
		To:     owner2,
		Amount: pldtypes.Int64ToInt256(1),
	}, {
		From:   owner1,
		To:     owner3,
		Amount: pldtypes.Int64ToInt256(2),
	}}, transfers)
}

func TestBuildReceiptBadDataState(t *testing.T) {
	ctx, _, n := newNotoFullSchemaSet(t)

	_, err := n.BuildReceipt(ctx, &prototk.BuildReceiptRequest{
		TransactionId: uuid.New().String(),
		InfoStates: []*prototk.EndorsableState{
			{
				Id:            pldtypes.RandBytes32().String(),
				SchemaId:      n.dataSchemaV1.Id,
				StateDataJson: `{! bad data`,
			},
		},
	})
	require.Error(t, err)
}

func TestBuildReceiptBadCoinSchemaId(t *testing.T) {
	n := Noto{
		dataSchemaV0: testSchema("data"),
		dataSchemaV1: testSchema("data_v1"),
		dataSchemaV2: testSchema("data_v2"),
		coinSchema:   &prototk.StateSchema{Id: "not_b32"},
	}

	_, err := n.BuildReceipt(t.Context(), &prototk.BuildReceiptRequest{
		TransactionId: uuid.New().String(),
		InputStates: []*prototk.EndorsableState{
			{
				Id:            pldtypes.RandBytes32().String(),
				SchemaId:      "not_b32",
				StateDataJson: `{}`,
			},
		},
	})
	require.Error(t, err)
}

func TestBuildReceiptBadV0LockInfo(t *testing.T) {
	ctx, _, n := newNotoFullSchemaSet(t)

	_, err := n.BuildReceipt(ctx, &prototk.BuildReceiptRequest{
		TransactionId: uuid.New().String(),
		InfoStates: []*prototk.EndorsableState{
			{
				Id:       pldtypes.RandBytes32().String(),
				SchemaId: n.dataSchemaV0.Id,
				StateDataJson: `{
				  "variant": "0x00"
				}`,
			},
			{
				Id:            pldtypes.RandBytes32().String(),
				SchemaId:      n.lockInfoSchemaV0.Id,
				StateDataJson: `{! bad data`,
			},
		},
		ReadStates: []*prototk.EndorsableState{
			{
				Id:            pldtypes.RandBytes32().String(),
				SchemaId:      n.lockedCoinSchema.Id,
				StateDataJson: `{}`,
			},
		},
	})
	require.Error(t, err)
}

func testGetDomainReceipt(t *testing.T, n *Noto, req *prototk.BuildReceiptRequest) *types.NotoDomainReceipt {
	// Schemas all need mapping

	res, err := n.BuildReceipt(t.Context(), req)
	require.NoError(t, err)

	var notoReceipt types.NotoDomainReceipt
	err = json.Unmarshal([]byte(res.ReceiptJson), &notoReceipt)
	require.NoError(t, err)

	return &notoReceipt
}

// TestBuildReceipt_Stellar_LockInfo proves the new Stellar dispatch in receiptLockInfoV1V2 (ch. 18's
// SAtom atomic-composition support): BuildReceipt's LockInfo.UnlockCall/CancelCall are populated
// with real, correctly-encoded Soroban args - the same encoding stellarBaseLedgerInvokeUnlock/
// stellarBaseLedgerInvokeCancelUnlock use for genuine submission (chapter 13 §13.2's
// check_commitment needs byte-for-byte the same "data" value both places) - not the EVM-only ABI
// encoding this receipt used to always produce regardless of chain kind.
func TestBuildReceipt_Stellar_LockInfo(t *testing.T) {
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

	ownerPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	ownerAddress, err := strkey.Encode(strkey.VersionByteAccountID, ownerPub)
	require.NoError(t, err)

	lockID := pldtypes.RandBytes32()
	spendTxID := pldtypes.RandBytes32()
	lockedCoinID := pldtypes.RandBytes32()
	spendOutputID := pldtypes.RandBytes32()
	cancelOutputID := pldtypes.RandBytes32()
	spendData := "0xcafe01"
	cancelData := "0xcafe02"

	res, err := n.BuildReceipt(ctx, &prototk.BuildReceiptRequest{
		TransactionId: uuid.New().String(),
		InfoStates: []*prototk.EndorsableState{
			{
				Id:       pldtypes.RandBytes32().String(),
				SchemaId: n.dataSchemaV1.Id,
				StateDataJson: fmt.Sprintf(`{
					"salt": "%s",
					"data": "0x",
					"variant": "0x0001"
				}`, pldtypes.RandBytes32()),
			},
		},
		OutputStates: []*prototk.EndorsableState{
			{
				Id:       lockedCoinID.String(),
				SchemaId: n.lockedCoinSchema.Id,
				StateDataJson: fmt.Sprintf(`{
					"lockId": "%s",
					"owner": "%s",
					"amount": "100"
				}`, lockID, ownerAddress),
			},
			{
				Id:       pldtypes.RandBytes32().String(),
				SchemaId: n.lockInfoSchemaV1.Id,
				StateDataJson: fmt.Sprintf(`{
					"lockId": "%s",
					"salt": "%s",
					"owner": "%s",
					"spender": "%s",
					"spendTxId": "%s",
					"spendOutputs": ["%s"],
					"spendData": "%s",
					"cancelOutputs": ["%s"],
					"cancelData": "%s"
				}`, lockID, pldtypes.RandBytes32(), ownerAddress, ownerAddress, spendTxID, spendOutputID, spendData, cancelOutputID, cancelData),
			},
		},
	})
	require.NoError(t, err)

	var receipt types.NotoDomainReceipt
	require.NoError(t, json.Unmarshal([]byte(res.ReceiptJson), &receipt))
	require.NotNil(t, receipt.LockInfo)

	assert.Equal(t, "unlock", receipt.LockInfo.UnlockFunction)
	var unlockArgs xdr.ScVec
	_, err = xdr.Unmarshal(bytes.NewReader(receipt.LockInfo.UnlockCall), &unlockArgs)
	require.NoError(t, err)
	require.Len(t, unlockArgs, 5) // tx_id, lock_id, locked_inputs, outputs, data
	assert.Equal(t, spendTxID[:], []byte(*unlockArgs[0].Bytes))
	assert.Equal(t, lockID[:], []byte(*unlockArgs[1].Bytes))
	assert.Equal(t, pldtypes.MustParseHexBytes(spendData), pldtypes.HexBytes(*unlockArgs[4].Bytes))

	assert.Equal(t, "cancel_unlock", receipt.LockInfo.CancelFunction)
	var cancelArgs xdr.ScVec
	_, err = xdr.Unmarshal(bytes.NewReader(receipt.LockInfo.CancelCall), &cancelArgs)
	require.NoError(t, err)
	require.Len(t, cancelArgs, 5)
	assert.Equal(t, pldtypes.MustParseHexBytes(cancelData), pldtypes.HexBytes(*cancelArgs[4].Bytes))

	// UnlockParams/CancelParams are the JSON-typed-args counterpart to UnlockCall/CancelCall's raw
	// XDR - Sente's externalCalls mechanism (scval_json.rs) needs these instead, since it has no
	// raw-args-passthrough mode. Same positional values, {"type":"bytes"/"vec", "value":...} shaped.
	unlockArgsJSON, ok := receipt.LockInfo.UnlockParams["args"].([]any)
	require.True(t, ok)
	require.Len(t, unlockArgsJSON, 5)
	assert.Equal(t, map[string]any{"type": "bytes", "value": spendTxID.String()}, unlockArgsJSON[0])
	assert.Equal(t, map[string]any{"type": "bytes", "value": lockID.String()}, unlockArgsJSON[1])
	assert.Equal(t, map[string]any{"type": "vec", "value": []any{map[string]any{"type": "bytes", "value": lockedCoinID.String()}}}, unlockArgsJSON[2])
	assert.Equal(t, map[string]any{"type": "vec", "value": []any{map[string]any{"type": "bytes", "value": spendOutputID.String()}}}, unlockArgsJSON[3])
	assert.Equal(t, map[string]any{"type": "bytes", "value": spendData}, unlockArgsJSON[4])

	cancelArgsJSON, ok := receipt.LockInfo.CancelParams["args"].([]any)
	require.True(t, ok)
	require.Len(t, cancelArgsJSON, 5)
	assert.Equal(t, map[string]any{"type": "vec", "value": []any{map[string]any{"type": "bytes", "value": cancelOutputID.String()}}}, cancelArgsJSON[3])
	assert.Equal(t, map[string]any{"type": "bytes", "value": cancelData}, cancelArgsJSON[4])
}
