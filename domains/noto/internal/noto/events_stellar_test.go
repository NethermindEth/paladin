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

package noto

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/scspec"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jsonTopicsDataStellar builds the {"topics":[...],"data":"0x.."} JSON shape
// stellarEventPayload.dataJSON() delivers (core/go/internal/ledgerindexer/stellar/
// event_payloads.go), mirroring core/go/internal/domainmgr/event_indexer_stellar_test.go's own
// jsonTopicsData helper - duplicated locally for the same cross-module reason
// stellarEventSelector/stellarEventPayload themselves are.
func jsonTopicsDataStellar(t *testing.T, topics []xdr.ScVal, data xdr.ScVal) string {
	t.Helper()
	topicStrs := make([]string, len(topics))
	for i, topic := range topics {
		b, err := topic.MarshalBinary()
		require.NoError(t, err)
		topicStrs[i] = pldtypes.HexBytes(b).String()
	}
	dataBytes, err := data.MarshalBinary()
	require.NoError(t, err)
	payload := stellarEventPayload{Topics: topicStrs, Data: pldtypes.HexBytes(dataBytes).String()}
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return string(b)
}

func scSymbol(sym string) xdr.ScVal {
	s := xdr.ScSymbol(sym)
	return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &s}
}

func scBytes32Val(b pldtypes.Bytes32) xdr.ScVal {
	bs := xdr.ScBytes(b[:])
	return xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &bs}
}

func scBytesVal(b []byte) xdr.ScVal {
	bs := xdr.ScBytes(b)
	return xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &bs}
}

func scBytes32VecVal(items ...pldtypes.Bytes32) xdr.ScVal {
	vec := make(xdr.ScVec, len(items))
	for i, b := range items {
		vec[i] = scBytes32Val(b)
	}
	vecPtr := &vec
	return xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vecPtr}
}

func scVecVal(items ...xdr.ScVal) xdr.ScVal {
	vec := xdr.ScVec(items)
	vecPtr := &vec
	return xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vecPtr}
}

func randBytes32(t *testing.T) pldtypes.Bytes32 {
	t.Helper()
	var b pldtypes.Bytes32
	copy(b[:], pldtypes.RandBytes(32))
	return b
}

func randStellarAddress(t *testing.T) string {
	t.Helper()
	addr, err := strkey.Encode(strkey.VersionByteAccountID, pldtypes.RandBytes(32))
	require.NoError(t, err)
	return addr
}

func scAddressVal(t *testing.T, strkeyAddr string) xdr.ScVal {
	t.Helper()
	addr, err := scspec.AddressFromStrkey(strkeyAddr)
	require.NoError(t, err)
	val, err := xdr.NewScVal(xdr.ScValTypeScvAddress, addr)
	require.NoError(t, err)
	return val
}

func TestStellarEventSelectorsMatchExpectedNames(t *testing.T) {
	// A regression guard, not a full re-derivation: confirms handleStellarEventV1's dispatch table
	// stays literally in sync with stellarEventsJSON's declared event names (noto.go) - if either
	// drifts, this test (rather than a silent live-network mismatch) is where it shows up.
	assert.Equal(t, stellarEventSelector("transfer").String(), stellarTransferSelector)
	assert.Equal(t, stellarEventSelector("lock").String(), stellarLockSelector)
	assert.Equal(t, stellarEventSelector("prepare_unlock").String(), stellarPrepareUnlockSelector)
	assert.Equal(t, stellarEventSelector("delegate_lock").String(), stellarDelegateLockSelector)
	assert.Equal(t, stellarEventSelector("unlock").String(), stellarUnlockSelector)
	assert.Equal(t, stellarEventSelector("cancel_unlock").String(), stellarCancelUnlockSelector)
	assert.NotEqual(t, stellarTransferSelector, stellarLockSelector)
	assert.NotEqual(t, stellarLockSelector, stellarUnlockSelector)
	assert.NotEqual(t, stellarPrepareUnlockSelector, stellarDelegateLockSelector)
	assert.NotEqual(t, stellarUnlockSelector, stellarCancelUnlockSelector)
}

func TestDecodeStellarTransferEvent(t *testing.T) {
	ctx := context.Background()
	txID := randBytes32(t)
	input := randBytes32(t)
	output := randBytes32(t)
	signature := []byte{0x01, 0x02, 0x03}
	data := []byte{0xaa, 0xbb}

	t.Run("success", func(t *testing.T) {
		ev := &prototk.OnChainEvent{DataJson: jsonTopicsDataStellar(t,
			[]xdr.ScVal{scSymbol("transfer"), scBytes32Val(txID)},
			scVecVal(scBytes32VecVal(input), scBytes32VecVal(output), scBytesVal(signature), scBytesVal(data)),
		)}
		got, err := decodeStellarTransferEvent(ctx, ev)
		require.NoError(t, err)
		assert.Equal(t, txID, got.TxId)
		assert.Nil(t, got.Operator)
		assert.Equal(t, []pldtypes.Bytes32{input}, got.Inputs)
		assert.Equal(t, []pldtypes.Bytes32{output}, got.Outputs)
		assert.Equal(t, pldtypes.HexBytes(signature), got.Proof)
		assert.Equal(t, pldtypes.HexBytes(data), got.Data)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		ev := &prototk.OnChainEvent{DataJson: `not json`}
		_, err := decodeStellarTransferEvent(ctx, ev)
		assert.ErrorContains(t, err, "invalid event payload")
	})

	t.Run("too few topics", func(t *testing.T) {
		ev := &prototk.OnChainEvent{DataJson: jsonTopicsDataStellar(t, []xdr.ScVal{scSymbol("transfer")}, scVecVal())}
		_, err := decodeStellarTransferEvent(ctx, ev)
		assert.ErrorContains(t, err, "expected at least 2 topics")
	})

	t.Run("tx_id topic not 32 bytes", func(t *testing.T) {
		ev := &prototk.OnChainEvent{DataJson: jsonTopicsDataStellar(t,
			[]xdr.ScVal{scSymbol("transfer"), scBytesVal([]byte{0x01})}, scVecVal())}
		_, err := decodeStellarTransferEvent(ctx, ev)
		assert.ErrorContains(t, err, "tx_id topic")
	})

	t.Run("data not a vec", func(t *testing.T) {
		ev := &prototk.OnChainEvent{DataJson: jsonTopicsDataStellar(t,
			[]xdr.ScVal{scSymbol("transfer"), scBytes32Val(txID)}, scBytesVal([]byte{0x01}))}
		_, err := decodeStellarTransferEvent(ctx, ev)
		assert.ErrorContains(t, err, "expected a Vec")
	})

	t.Run("data vec wrong length", func(t *testing.T) {
		ev := &prototk.OnChainEvent{DataJson: jsonTopicsDataStellar(t,
			[]xdr.ScVal{scSymbol("transfer"), scBytes32Val(txID)}, scVecVal(scBytesVal([]byte{0x01})))}
		_, err := decodeStellarTransferEvent(ctx, ev)
		assert.ErrorContains(t, err, "expected 4 elements")
	})
}

func TestDecodeStellarLockEvent(t *testing.T) {
	ctx := context.Background()
	lockID := randBytes32(t)
	input := randBytes32(t)
	lockedOutput := randBytes32(t)
	output := randBytes32(t)
	signature := []byte{0x04}
	data := []byte{0x05}

	ev := &prototk.OnChainEvent{DataJson: jsonTopicsDataStellar(t,
		[]xdr.ScVal{scSymbol("lock"), scBytes32Val(lockID)},
		scVecVal(scBytes32VecVal(input), scBytes32VecVal(lockedOutput), scBytes32VecVal(output), scBytesVal(signature), scBytesVal(data)),
	)}
	got, err := decodeStellarLockEvent(ctx, ev)
	require.NoError(t, err)
	assert.Equal(t, lockID, got.TxId, "lock_id doubles as tx_id in SNoto's design")
	assert.Equal(t, lockID, got.LockID)
	assert.Nil(t, got.Owner)
	assert.Equal(t, []pldtypes.Bytes32{input}, got.Inputs)
	assert.Equal(t, []pldtypes.Bytes32{output}, got.Outputs)
	assert.Equal(t, []pldtypes.Bytes32{lockedOutput}, got.Contents, "locked_outputs maps to the shared struct's Contents field")
	assert.True(t, got.NewLockState.IsZero(), "SNoto has no lock-info-state concept")
	assert.Equal(t, pldtypes.HexBytes(signature), got.Proof)
	assert.Equal(t, pldtypes.HexBytes(data), got.Data)

	t.Run("data vec wrong length", func(t *testing.T) {
		ev := &prototk.OnChainEvent{DataJson: jsonTopicsDataStellar(t,
			[]xdr.ScVal{scSymbol("lock"), scBytes32Val(lockID)}, scVecVal())}
		_, err := decodeStellarLockEvent(ctx, ev)
		assert.ErrorContains(t, err, "expected 5 elements")
	})
}

func TestDecodeStellarPrepareUnlockEvent(t *testing.T) {
	ctx := context.Background()
	lockID := randBytes32(t)
	txID := randBytes32(t)
	spendCommitment := randBytes32(t)
	cancelCommitment := randBytes32(t)
	data := []byte{0x07}

	ev := &prototk.OnChainEvent{DataJson: jsonTopicsDataStellar(t,
		[]xdr.ScVal{scSymbol("prepare_unlock"), scBytes32Val(lockID)},
		scVecVal(scBytes32Val(txID), scBytes32Val(spendCommitment), scBytes32Val(cancelCommitment), scBytesVal(data)),
	)}
	got, err := decodeStellarPrepareUnlockEvent(ctx, ev)
	require.NoError(t, err)
	assert.Equal(t, txID, got.TxId)
	assert.Equal(t, lockID, got.LockID)
	assert.Nil(t, got.Owner)
	assert.Empty(t, got.Contents)
	assert.True(t, got.OldLockState.IsZero(), "SNoto has no lock-info-state concept")
	assert.True(t, got.NewLockState.IsZero(), "SNoto has no lock-info-state concept")
	assert.Equal(t, pldtypes.HexBytes(data), got.Data)

	t.Run("data vec wrong length", func(t *testing.T) {
		ev := &prototk.OnChainEvent{DataJson: jsonTopicsDataStellar(t,
			[]xdr.ScVal{scSymbol("prepare_unlock"), scBytes32Val(lockID)}, scVecVal(scBytes32Val(txID)))}
		_, err := decodeStellarPrepareUnlockEvent(ctx, ev)
		assert.ErrorContains(t, err, "expected 4 elements")
	})

	t.Run("spend_commitment not 32 bytes", func(t *testing.T) {
		ev := &prototk.OnChainEvent{DataJson: jsonTopicsDataStellar(t,
			[]xdr.ScVal{scSymbol("prepare_unlock"), scBytes32Val(lockID)},
			scVecVal(scBytes32Val(txID), scBytesVal([]byte{0x01}), scBytes32Val(cancelCommitment), scBytesVal(data)),
		)}
		_, err := decodeStellarPrepareUnlockEvent(ctx, ev)
		assert.ErrorContains(t, err, "spend_commitment")
	})
}

func TestDecodeStellarDelegateLockEvent(t *testing.T) {
	ctx := context.Background()
	lockID := randBytes32(t)
	txID := randBytes32(t)
	delegate := randStellarAddress(t)
	data := []byte{0x08}

	ev := &prototk.OnChainEvent{DataJson: jsonTopicsDataStellar(t,
		[]xdr.ScVal{scSymbol("delegate_lock"), scBytes32Val(lockID)},
		scVecVal(scBytes32Val(txID), scAddressVal(t, delegate), scBytesVal(data)),
	)}
	got, err := decodeStellarDelegateLockEvent(ctx, ev)
	require.NoError(t, err)
	assert.Equal(t, txID, got.TxId)
	assert.Equal(t, lockID, got.LockID)
	assert.Nil(t, got.PreviousSpender)
	assert.Nil(t, got.NewSpender)
	assert.True(t, got.OldLockState.IsZero(), "SNoto has no lock-info-state concept")
	assert.True(t, got.NewLockState.IsZero(), "SNoto has no lock-info-state concept")
	assert.Equal(t, pldtypes.HexBytes(data), got.Data)

	t.Run("data vec wrong length", func(t *testing.T) {
		ev := &prototk.OnChainEvent{DataJson: jsonTopicsDataStellar(t,
			[]xdr.ScVal{scSymbol("delegate_lock"), scBytes32Val(lockID)}, scVecVal(scBytes32Val(txID)))}
		_, err := decodeStellarDelegateLockEvent(ctx, ev)
		assert.ErrorContains(t, err, "expected 3 elements")
	})

	t.Run("delegate not an address", func(t *testing.T) {
		ev := &prototk.OnChainEvent{DataJson: jsonTopicsDataStellar(t,
			[]xdr.ScVal{scSymbol("delegate_lock"), scBytes32Val(lockID)},
			scVecVal(scBytes32Val(txID), scBytesVal([]byte{0x01}), scBytesVal(data)),
		)}
		_, err := decodeStellarDelegateLockEvent(ctx, ev)
		assert.ErrorContains(t, err, "delegate")
	})
}

func TestDecodeStellarUnlockEvent(t *testing.T) {
	ctx := context.Background()
	txID := randBytes32(t)
	lockID := randBytes32(t)
	lockedInput := randBytes32(t)
	output := randBytes32(t)
	data := []byte{0x06}

	ev := &prototk.OnChainEvent{DataJson: jsonTopicsDataStellar(t,
		[]xdr.ScVal{scSymbol("unlock"), scBytes32Val(lockID)},
		scVecVal(scBytes32Val(txID), scBytes32VecVal(lockedInput), scBytes32VecVal(output), scBytesVal(data)),
	)}
	got, err := decodeStellarUnlockEvent(ctx, ev)
	require.NoError(t, err)
	assert.Equal(t, txID, got.TxId, "tx_id comes from the data vec, not the lock_id topic")
	assert.Equal(t, lockID, got.LockID)
	assert.Nil(t, got.Spender)
	assert.Equal(t, []pldtypes.Bytes32{lockedInput}, got.Inputs)
	assert.Equal(t, []pldtypes.Bytes32{output}, got.Outputs)
	assert.True(t, got.OldLockState.IsZero(), "SNoto has no lock-info-state concept")
	assert.Equal(t, pldtypes.HexBytes(data), got.TxData)

	t.Run("data vec wrong length", func(t *testing.T) {
		ev := &prototk.OnChainEvent{DataJson: jsonTopicsDataStellar(t,
			[]xdr.ScVal{scSymbol("unlock"), scBytes32Val(lockID)}, scVecVal(scBytes32Val(txID)))}
		_, err := decodeStellarUnlockEvent(ctx, ev)
		assert.ErrorContains(t, err, "expected 4 elements")
	})
}

func TestDecodeStellarCancelUnlockEvent(t *testing.T) {
	ctx := context.Background()
	txID := randBytes32(t)
	lockID := randBytes32(t)
	lockedInput := randBytes32(t)
	cancelOutput := randBytes32(t)
	data := []byte{0x09}

	ev := &prototk.OnChainEvent{DataJson: jsonTopicsDataStellar(t,
		[]xdr.ScVal{scSymbol("cancel_unlock"), scBytes32Val(lockID)},
		scVecVal(scBytes32Val(txID), scBytes32VecVal(lockedInput), scBytes32VecVal(cancelOutput), scBytesVal(data)),
	)}
	got, err := decodeStellarCancelUnlockEvent(ctx, ev)
	require.NoError(t, err)
	assert.Equal(t, txID, got.TxId, "tx_id comes from the data vec, not the lock_id topic")
	assert.Equal(t, lockID, got.LockID)
	assert.Nil(t, got.Spender)
	assert.Equal(t, []pldtypes.Bytes32{lockedInput}, got.Inputs)
	assert.Equal(t, []pldtypes.Bytes32{cancelOutput}, got.Outputs)
	assert.True(t, got.OldLockState.IsZero(), "SNoto has no lock-info-state concept")
	assert.Equal(t, pldtypes.HexBytes(data), got.TxData)

	t.Run("data vec wrong length", func(t *testing.T) {
		ev := &prototk.OnChainEvent{DataJson: jsonTopicsDataStellar(t,
			[]xdr.ScVal{scSymbol("cancel_unlock"), scBytes32Val(lockID)}, scVecVal(scBytes32Val(txID)))}
		_, err := decodeStellarCancelUnlockEvent(ctx, ev)
		assert.ErrorContains(t, err, "expected 4 elements")
	})
}

func TestHandleStellarEventV1UnknownSelectorSkipsWithoutError(t *testing.T) {
	n := &Noto{}
	ctx := context.Background()
	ev := &prototk.OnChainEvent{Signature: "0xnotarealselector"}
	res := &prototk.HandleEventBatchResponse{}
	req := &prototk.HandleEventBatchRequest{BatchId: "batch1"}

	err := n.handleStellarEventV1(ctx, ev, res, req, false, nil)
	require.NoError(t, err)
	assert.Empty(t, res.SpentStates)
	assert.Empty(t, res.ConfirmedStates)
	assert.Empty(t, res.TransactionsComplete)
}

func TestHandleStellarEventV1MalformedEventSkipsWithoutError(t *testing.T) {
	n := &Noto{}
	ctx := context.Background()
	ev := &prototk.OnChainEvent{Signature: stellarTransferSelector, DataJson: `not json`}
	res := &prototk.HandleEventBatchResponse{}
	req := &prototk.HandleEventBatchRequest{BatchId: "batch1"}

	err := n.handleStellarEventV1(ctx, ev, res, req, false, nil)
	require.NoError(t, err)
	assert.Empty(t, res.SpentStates)
	assert.Empty(t, res.ConfirmedStates)
}
