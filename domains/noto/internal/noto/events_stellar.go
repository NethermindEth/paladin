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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/log"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/scspec"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/smt"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// stellarEventSelector mirrors core/go/pkg/baseledger/stellar.ComputeEventSelector exactly:
// SHA-256("saladin:" + topic0Symbol + ":v0"). Duplicated here rather than imported - this Go
// module (domains/noto) is a standalone domain-plugin binary and doesn't otherwise depend on
// core's internal packages, and domains/sente's own Rust plugin duplicates this identical function
// for the same reason (domain.rs's stellar_event_selector - see its own doc comment). A tiny,
// stable, pure function, not worth a cross-module dependency.
func stellarEventSelector(topic0Symbol string) pldtypes.Bytes32 {
	return sha256.Sum256([]byte("saladin:" + topic0Symbol + ":v0"))
}

// stellarTransferSelector/stellarLockSelector/stellarUnlockSelector are what handleStellarEventV1
// matches a delivered event's raw ev.Signature against - the Stellar counterpart to EVM's
// eventSignatures[EventTransfer] etc lookups, since Stellar events carry no SoliditySignature at
// all (see handleV1Event's own doc comment on why this dispatch is by raw selector instead).
var (
	stellarTransferSelector      = stellarEventSelector("transfer").String()
	stellarLockSelector          = stellarEventSelector("lock").String()
	stellarPrepareUnlockSelector = stellarEventSelector("prepare_unlock").String()
	stellarDelegateLockSelector  = stellarEventSelector("delegate_lock").String()
	stellarUnlockSelector        = stellarEventSelector("unlock").String()
	stellarCancelUnlockSelector  = stellarEventSelector("cancel_unlock").String()
	stellarDepositSelector       = stellarEventSelector("deposit").String()
	stellarWithdrawSelector      = stellarEventSelector("withdraw").String()
)

// stellarEventPayload mirrors the JSON shape delivered in ev.DataJson for a Stellar event
// (core/go/internal/ledgerindexer/stellar/event_payloads.go's stellarEventPayload.dataJSON()):
// hex-encoded topics and data, since Stellar's event pipeline deliberately leaves Soroban event
// bodies XDR-encoded rather than ABI-decoding them into named fields the way EVM's blockindexer
// does. Duplicated locally for the same reason stellarEventSelector is -
// core/go/internal/domainmgr/event_indexer_stellar.go's own stellarRegistrationEventPayload does
// the same for its own (unrelated) Stellar events.
type stellarEventPayload struct {
	Topics []string `json:"topics"`
	Data   string   `json:"data"`
}

// handleStellarEventV1 is the Stellar counterpart to handleV1Event's EVM `switch
// ev.SoliditySignature` - dispatches by raw selector (ev.Signature) onto SNoto's on-chain events.
func (n *Noto) handleStellarEventV1(ctx context.Context, ev *prototk.OnChainEvent, res *prototk.HandleEventBatchResponse, req *prototk.HandleEventBatchRequest, useNullifier bool, smtForStates *smt.MerkleTreeSpec) error {
	switch ev.Signature {
	case stellarTransferSelector:
		transfer, err := decodeStellarTransferEvent(ctx, ev)
		if err != nil {
			log.L(ctx).Warnf("Ignoring malformed transfer event in batch %s: %s", req.BatchId, err)
			return nil
		}
		log.L(ctx).Infof("Processing 'transfer' event in batch %s", req.BatchId)
		return n.applyTransferEvent(ctx, ev, transfer, res, useNullifier, smtForStates)

	case stellarLockSelector:
		lockCreated, err := decodeStellarLockEvent(ctx, ev)
		if err != nil {
			log.L(ctx).Warnf("Ignoring malformed lock event in batch %s: %s", req.BatchId, err)
			return nil
		}
		log.L(ctx).Infof("Processing 'lock' event in batch %s", req.BatchId)
		return n.applyLockCreatedEvent(ctx, ev, lockCreated, res)

	case stellarPrepareUnlockSelector:
		lockUpdated, err := decodeStellarPrepareUnlockEvent(ctx, ev)
		if err != nil {
			log.L(ctx).Warnf("Ignoring malformed prepare_unlock event in batch %s: %s", req.BatchId, err)
			return nil
		}
		log.L(ctx).Infof("Processing 'prepare_unlock' event in batch %s", req.BatchId)
		return n.applyLockUpdatedEvent(ctx, ev, lockUpdated, res)

	case stellarDelegateLockSelector:
		lockDelegated, err := decodeStellarDelegateLockEvent(ctx, ev)
		if err != nil {
			log.L(ctx).Warnf("Ignoring malformed delegate_lock event in batch %s: %s", req.BatchId, err)
			return nil
		}
		log.L(ctx).Infof("Processing 'delegate_lock' event in batch %s", req.BatchId)
		return n.applyLockDelegatedEvent(ctx, ev, lockDelegated, res)

	case stellarUnlockSelector:
		lockSpent, err := decodeStellarUnlockEvent(ctx, ev)
		if err != nil {
			log.L(ctx).Warnf("Ignoring malformed unlock event in batch %s: %s", req.BatchId, err)
			return nil
		}
		log.L(ctx).Infof("Processing 'unlock' event in batch %s", req.BatchId)
		return n.applyLockSpentOrCancelledEvent(ctx, ev, lockSpent, res, req)

	case stellarCancelUnlockSelector:
		lockCancelled, err := decodeStellarCancelUnlockEvent(ctx, ev)
		if err != nil {
			log.L(ctx).Warnf("Ignoring malformed cancel_unlock event in batch %s: %s", req.BatchId, err)
			return nil
		}
		log.L(ctx).Infof("Processing 'cancel_unlock' event in batch %s", req.BatchId)
		return n.applyLockSpentOrCancelledEvent(ctx, ev, lockCancelled, res, req)

	case stellarDepositSelector:
		deposit, err := decodeStellarDepositEvent(ctx, ev)
		if err != nil {
			log.L(ctx).Warnf("Ignoring malformed deposit event in batch %s: %s", req.BatchId, err)
			return nil
		}
		log.L(ctx).Infof("Processing 'deposit' event in batch %s", req.BatchId)
		return n.applyStellarDepositEvent(ctx, ev, deposit, res)

	case stellarWithdrawSelector:
		withdraw, err := decodeStellarWithdrawEvent(ctx, ev)
		if err != nil {
			log.L(ctx).Warnf("Ignoring malformed withdraw event in batch %s: %s", req.BatchId, err)
			return nil
		}
		log.L(ctx).Infof("Processing 'withdraw' event in batch %s", req.BatchId)
		return n.applyStellarWithdrawEvent(ctx, ev, withdraw, res)

	default:
		log.L(ctx).Infof("Skipping '%s' event in batch %s", ev.Signature, req.BatchId)
		return nil
	}
}

// decodeStellarEventPayload unmarshals ev.DataJson into the topics/data hex-string pair every
// Stellar event delivery carries.
func decodeStellarEventPayload(ev *prototk.OnChainEvent) (*stellarEventPayload, error) {
	var payload stellarEventPayload
	if err := json.Unmarshal([]byte(ev.DataJson), &payload); err != nil {
		return nil, fmt.Errorf("invalid event payload: %w", err)
	}
	return &payload, nil
}

// decodeStellarScVal hex-decodes and XDR-unmarshals a single topic or data field (each is the full
// XDR encoding of one ScVal, per decodeContractEvent - core/go/pkg/baseledger/stellar/ingestor.go),
// mirroring decodeTopicScVal in core/go/internal/domainmgr/event_indexer_stellar.go.
func decodeStellarScVal(ctx context.Context, hexVal string) (xdr.ScVal, error) {
	raw, err := pldtypes.ParseHexBytes(ctx, hexVal)
	if err != nil {
		return xdr.ScVal{}, err
	}
	var val xdr.ScVal
	if _, err := xdr.Unmarshal(bytes.NewReader(raw), &val); err != nil {
		return xdr.ScVal{}, err
	}
	return val, nil
}

// scValToBytes32 expects a 32-byte BytesN ScVal (SNoto's tx_id/lock_id shape).
func scValToBytes32(val xdr.ScVal) (pldtypes.Bytes32, error) {
	if val.Type != xdr.ScValTypeScvBytes || val.Bytes == nil || len(*val.Bytes) != 32 {
		return pldtypes.Bytes32{}, fmt.Errorf("expected a 32-byte BytesN value")
	}
	var b32 pldtypes.Bytes32
	copy(b32[:], *val.Bytes)
	return b32, nil
}

// scValToBytes expects an arbitrary-length Bytes ScVal (SNoto's signature/data shape).
func scValToBytes(val xdr.ScVal) (pldtypes.HexBytes, error) {
	if val.Type != xdr.ScValTypeScvBytes || val.Bytes == nil {
		return nil, fmt.Errorf("expected a Bytes value")
	}
	return pldtypes.HexBytes(*val.Bytes), nil
}

// scValToBytes32Vec expects a Vec of 32-byte BytesN ScVals (SNoto's inputs/outputs/locked_outputs
// shape - always a Vec<BytesN<32>> of opaque state IDs, chapter 13 §13.2).
func scValToBytes32Vec(val xdr.ScVal) ([]pldtypes.Bytes32, error) {
	if val.Type != xdr.ScValTypeScvVec || val.Vec == nil || *val.Vec == nil {
		return nil, fmt.Errorf("expected a Vec value")
	}
	vec := **val.Vec
	result := make([]pldtypes.Bytes32, len(vec))
	for i, item := range vec {
		b32, err := scValToBytes32(item)
		if err != nil {
			return nil, fmt.Errorf("element %d: %w", i, err)
		}
		result[i] = b32
	}
	return result, nil
}

// scValToAddressString expects an Address ScVal (SNoto's delegate_lock "delegate" field shape),
// decoding it to its StrKey string form - the chain-neutral representation, matching
// stellarEventsJSON's "string" type declaration for this field (noto.go).
func scValToAddressString(val xdr.ScVal) (string, error) {
	if val.Type != xdr.ScValTypeScvAddress || val.Address == nil {
		return "", fmt.Errorf("expected an Address value")
	}
	return scspec.AddressToStrkey(*val.Address)
}

// decodeStellarEventDataVec XDR-decodes ev.DataJson's "data" field (the full data_format="vec"
// payload, everything but the topics) and checks it has exactly the expected number of positional
// elements for the event kind being decoded.
func decodeStellarEventDataVec(ctx context.Context, hexData string, expectedLen int) ([]xdr.ScVal, error) {
	val, err := decodeStellarScVal(ctx, hexData)
	if err != nil {
		return nil, fmt.Errorf("invalid event data XDR: %w", err)
	}
	if val.Type != xdr.ScValTypeScvVec || val.Vec == nil || *val.Vec == nil {
		return nil, fmt.Errorf("event data: expected a Vec")
	}
	vec := **val.Vec
	if len(vec) != expectedLen {
		return nil, fmt.Errorf("event data: expected %d elements, got %d", expectedLen, len(vec))
	}
	return vec, nil
}

// decodeStellarTransferEvent decodes SNoto's on-chain `transfer` event (topics = ["transfer",
// tx_id], data = vec![inputs, outputs, signature, data] - soroban/contracts/snoto/src/lib.rs's
// `Transfer` struct) into the same NotoTransfer_Event shape the EVM path produces via JSON
// unmarshal, so applyTransferEvent can be shared unchanged. SNoto's Transfer has no EVM-style
// "operator" concept (there's only ever the one fixed notary caller), so Operator is left nil -
// safe, since applyTransferEvent never reads it. The EVM ABI's "proof" field and SNoto's
// "signature" field occupy the same tuple position and serve the identical role (an opaque,
// off-chain-verified signature relayed through the event - see lib.rs's own doc comment), hence
// the signature -> Proof mapping below.
func decodeStellarTransferEvent(ctx context.Context, ev *prototk.OnChainEvent) (*NotoTransfer_Event, error) {
	payload, err := decodeStellarEventPayload(ev)
	if err != nil {
		return nil, err
	}
	if len(payload.Topics) < 2 {
		return nil, fmt.Errorf("expected at least 2 topics (symbol, tx_id), got %d", len(payload.Topics))
	}
	txIDVal, err := decodeStellarScVal(ctx, payload.Topics[1])
	if err != nil {
		return nil, fmt.Errorf("tx_id topic: %w", err)
	}
	txID, err := scValToBytes32(txIDVal)
	if err != nil {
		return nil, fmt.Errorf("tx_id topic: %w", err)
	}

	vec, err := decodeStellarEventDataVec(ctx, payload.Data, 4)
	if err != nil {
		return nil, err
	}
	inputs, err := scValToBytes32Vec(vec[0])
	if err != nil {
		return nil, fmt.Errorf("event data[0] (inputs): %w", err)
	}
	outputs, err := scValToBytes32Vec(vec[1])
	if err != nil {
		return nil, fmt.Errorf("event data[1] (outputs): %w", err)
	}
	signature, err := scValToBytes(vec[2])
	if err != nil {
		return nil, fmt.Errorf("event data[2] (signature): %w", err)
	}
	data, err := scValToBytes(vec[3])
	if err != nil {
		return nil, fmt.Errorf("event data[3] (data): %w", err)
	}

	return &NotoTransfer_Event{
		TxId:    txID,
		Inputs:  inputs,
		Outputs: outputs,
		Proof:   signature,
		Data:    data,
	}, nil
}

// decodeStellarLockEvent decodes SNoto's on-chain `lock` event (topics = ["lock", lock_id], data =
// vec![inputs, locked_outputs, outputs, signature, data, new_lock_state] -
// soroban/contracts/snoto/src/lib.rs's `Lock` struct) into the same NotoLockCreated_Event shape
// the EVM path produces, so applyLockCreatedEvent can be shared unchanged. lock_id doubles as
// tx_id (SNoto's lock_id = tx_id - lib.rs's own doc comment), so both fields get the same topic
// value. EVM's "contents" field (the locked coin states) corresponds to SNoto's "locked_outputs" -
// confirmed against handler_lock.go's own Contents: endorsableStateIDs(ctx, lockedOutputs, ...)
// construction. new_lock_state is the assembled lockInfoV1 state's own ID, echoed back by the
// contract (see encodeSNotoLockArgs's own doc comment) - populating NewLockState here is what lets
// applyLockCreatedEvent's IsZero() guard record it as a real confirmed state, fixing a real bug
// found live: without it, the lockInfoV1 state never gets confirmed, and prepareUnlock's own
// loadLockInfoV1 lookup permanently fails with "PD200028: Lock ID not found".
func decodeStellarLockEvent(ctx context.Context, ev *prototk.OnChainEvent) (*NotoLockCreated_Event, error) {
	payload, err := decodeStellarEventPayload(ev)
	if err != nil {
		return nil, err
	}
	if len(payload.Topics) < 2 {
		return nil, fmt.Errorf("expected at least 2 topics (symbol, lock_id), got %d", len(payload.Topics))
	}
	lockIDVal, err := decodeStellarScVal(ctx, payload.Topics[1])
	if err != nil {
		return nil, fmt.Errorf("lock_id topic: %w", err)
	}
	lockID, err := scValToBytes32(lockIDVal)
	if err != nil {
		return nil, fmt.Errorf("lock_id topic: %w", err)
	}

	vec, err := decodeStellarEventDataVec(ctx, payload.Data, 6)
	if err != nil {
		return nil, err
	}
	inputs, err := scValToBytes32Vec(vec[0])
	if err != nil {
		return nil, fmt.Errorf("event data[0] (inputs): %w", err)
	}
	lockedOutputs, err := scValToBytes32Vec(vec[1])
	if err != nil {
		return nil, fmt.Errorf("event data[1] (locked_outputs): %w", err)
	}
	outputs, err := scValToBytes32Vec(vec[2])
	if err != nil {
		return nil, fmt.Errorf("event data[2] (outputs): %w", err)
	}
	signature, err := scValToBytes(vec[3])
	if err != nil {
		return nil, fmt.Errorf("event data[3] (signature): %w", err)
	}
	data, err := scValToBytes(vec[4])
	if err != nil {
		return nil, fmt.Errorf("event data[4] (data): %w", err)
	}
	newLockState, err := scValToBytes32(vec[5])
	if err != nil {
		return nil, fmt.Errorf("event data[5] (new_lock_state): %w", err)
	}

	return &NotoLockCreated_Event{
		TxId:         lockID,
		LockID:       lockID,
		Inputs:       inputs,
		Outputs:      outputs,
		Contents:     lockedOutputs,
		Proof:        signature,
		Data:         data,
		NewLockState: newLockState,
	}, nil
}

// decodeStellarPrepareUnlockEvent decodes SNoto's on-chain `prepare_unlock` event (topics =
// ["prepare_unlock", lock_id], data = vec![tx_id, spend_commitment, cancel_commitment, data,
// old_lock_state, new_lock_state, contents] - soroban/contracts/snoto/src/lib.rs's
// `PrepareUnlock` struct) into the same NotoLockUpdated_Event shape the EVM path produces, so
// applyLockUpdatedEvent can be shared unchanged. spend_commitment/cancel_commitment are decoded
// (to validate the event's shape) but not carried into NotoLockUpdated_Event - that struct has no
// field for them, since off-chain confirmation bookkeeping never needs them (only
// unlock/cancel_unlock's own on-chain check_commitment recomputes and checks them later). Owner is
// left nil - an EVM-only field with no Stellar equivalent. old_lock_state/new_lock_state/Contents
// ARE populated (see decodeStellarLockEvent's own doc comment on why old/new_lock_state matters;
// Contents - the locked-coin state ID(s) - is what lets applyLockUpdatedEvent record them as a
// confirmed read state, which BuildReceipt's own lockedInputIDs extraction depends on to build the
// unlock/cancel externalCalls args - a real bug found live: without it, SNoto's own on-chain
// `unlock` traps because "locked_inputs" comes through empty).
func decodeStellarPrepareUnlockEvent(ctx context.Context, ev *prototk.OnChainEvent) (*NotoLockUpdated_Event, error) {
	payload, err := decodeStellarEventPayload(ev)
	if err != nil {
		return nil, err
	}
	if len(payload.Topics) < 2 {
		return nil, fmt.Errorf("expected at least 2 topics (symbol, lock_id), got %d", len(payload.Topics))
	}
	lockIDVal, err := decodeStellarScVal(ctx, payload.Topics[1])
	if err != nil {
		return nil, fmt.Errorf("lock_id topic: %w", err)
	}
	lockID, err := scValToBytes32(lockIDVal)
	if err != nil {
		return nil, fmt.Errorf("lock_id topic: %w", err)
	}

	vec, err := decodeStellarEventDataVec(ctx, payload.Data, 7)
	if err != nil {
		return nil, err
	}
	txID, err := scValToBytes32(vec[0])
	if err != nil {
		return nil, fmt.Errorf("event data[0] (tx_id): %w", err)
	}
	if _, err := scValToBytes32(vec[1]); err != nil {
		return nil, fmt.Errorf("event data[1] (spend_commitment): %w", err)
	}
	if _, err := scValToBytes32(vec[2]); err != nil {
		return nil, fmt.Errorf("event data[2] (cancel_commitment): %w", err)
	}
	data, err := scValToBytes(vec[3])
	if err != nil {
		return nil, fmt.Errorf("event data[3] (data): %w", err)
	}
	oldLockState, err := scValToBytes32(vec[4])
	if err != nil {
		return nil, fmt.Errorf("event data[4] (old_lock_state): %w", err)
	}
	newLockState, err := scValToBytes32(vec[5])
	if err != nil {
		return nil, fmt.Errorf("event data[5] (new_lock_state): %w", err)
	}
	contents, err := scValToBytes32Vec(vec[6])
	if err != nil {
		return nil, fmt.Errorf("event data[6] (contents): %w", err)
	}

	return &NotoLockUpdated_Event{
		TxId:         txID,
		LockID:       lockID,
		Contents:     contents,
		Data:         data,
		OldLockState: oldLockState,
		NewLockState: newLockState,
	}, nil
}

// decodeStellarDelegateLockEvent decodes SNoto's on-chain `delegate_lock` event (topics =
// ["delegate_lock", lock_id], data = vec![tx_id, delegate, data, old_lock_state, new_lock_state] -
// soroban/contracts/snoto/src/lib.rs's `DelegateLock` struct) into the same
// NotoLockDelegated_Event shape the EVM path produces, so applyLockDelegatedEvent can be shared
// unchanged. delegate is decoded (to validate the event's shape) but not carried into
// NotoLockDelegated_Event - PreviousSpender/NewSpender are EVM-only *pldtypes.EthAddress fields
// (chapter 14 step 7's Delegate migration keeps them that way, since they decode a real EVM event
// ABI); the Spender-field state transition already happened off-chain during Assemble.
// old_lock_state/new_lock_state ARE populated (see decodeStellarLockEvent's own doc comment on why
// this matters).
func decodeStellarDelegateLockEvent(ctx context.Context, ev *prototk.OnChainEvent) (*NotoLockDelegated_Event, error) {
	payload, err := decodeStellarEventPayload(ev)
	if err != nil {
		return nil, err
	}
	if len(payload.Topics) < 2 {
		return nil, fmt.Errorf("expected at least 2 topics (symbol, lock_id), got %d", len(payload.Topics))
	}
	lockIDVal, err := decodeStellarScVal(ctx, payload.Topics[1])
	if err != nil {
		return nil, fmt.Errorf("lock_id topic: %w", err)
	}
	lockID, err := scValToBytes32(lockIDVal)
	if err != nil {
		return nil, fmt.Errorf("lock_id topic: %w", err)
	}

	vec, err := decodeStellarEventDataVec(ctx, payload.Data, 5)
	if err != nil {
		return nil, err
	}
	txID, err := scValToBytes32(vec[0])
	if err != nil {
		return nil, fmt.Errorf("event data[0] (tx_id): %w", err)
	}
	if _, err := scValToAddressString(vec[1]); err != nil {
		return nil, fmt.Errorf("event data[1] (delegate): %w", err)
	}
	data, err := scValToBytes(vec[2])
	if err != nil {
		return nil, fmt.Errorf("event data[2] (data): %w", err)
	}
	oldLockState, err := scValToBytes32(vec[3])
	if err != nil {
		return nil, fmt.Errorf("event data[3] (old_lock_state): %w", err)
	}
	newLockState, err := scValToBytes32(vec[4])
	if err != nil {
		return nil, fmt.Errorf("event data[4] (new_lock_state): %w", err)
	}

	return &NotoLockDelegated_Event{
		TxId:         txID,
		LockID:       lockID,
		Data:         data,
		OldLockState: oldLockState,
		NewLockState: newLockState,
	}, nil
}

// decodeStellarUnlockEvent decodes SNoto's on-chain `unlock` event (topics = ["unlock", lock_id],
// data = vec![tx_id, locked_inputs, outputs, data, old_lock_state] -
// soroban/contracts/snoto/src/lib.rs's `Unlock` struct) into the same
// NotoLockSpentOrCancelled_Event shape the EVM path produces, so applyLockSpentOrCancelledEvent
// can be shared unchanged. tx_id lives in the data vec, not the topic (chapter 14's own tx_id
// addition to `unlock`, discovered necessary during this decoder's implementation - lock_id alone
// identifies the original lock-creation transaction, not this spend, so it can't serve Paladin's
// confirmation correlation). Spender is left nil (no EVM hooks-mode concept on Stellar) - the
// NotaryModeHooks gate on the Spender-comparing branch makes this safe. old_lock_state IS
// populated (see decodeStellarLockEvent's own doc comment on why this matters) - it's what lets
// applyLockSpentOrCancelledEvent record the lock's final state as spent.
func decodeStellarUnlockEvent(ctx context.Context, ev *prototk.OnChainEvent) (*NotoLockSpentOrCancelled_Event, error) {
	payload, err := decodeStellarEventPayload(ev)
	if err != nil {
		return nil, err
	}
	if len(payload.Topics) < 2 {
		return nil, fmt.Errorf("expected at least 2 topics (symbol, lock_id), got %d", len(payload.Topics))
	}
	lockIDVal, err := decodeStellarScVal(ctx, payload.Topics[1])
	if err != nil {
		return nil, fmt.Errorf("lock_id topic: %w", err)
	}
	lockID, err := scValToBytes32(lockIDVal)
	if err != nil {
		return nil, fmt.Errorf("lock_id topic: %w", err)
	}

	vec, err := decodeStellarEventDataVec(ctx, payload.Data, 5)
	if err != nil {
		return nil, err
	}
	txID, err := scValToBytes32(vec[0])
	if err != nil {
		return nil, fmt.Errorf("event data[0] (tx_id): %w", err)
	}
	lockedInputs, err := scValToBytes32Vec(vec[1])
	if err != nil {
		return nil, fmt.Errorf("event data[1] (locked_inputs): %w", err)
	}
	outputs, err := scValToBytes32Vec(vec[2])
	if err != nil {
		return nil, fmt.Errorf("event data[2] (outputs): %w", err)
	}
	data, err := scValToBytes(vec[3])
	if err != nil {
		return nil, fmt.Errorf("event data[3] (data): %w", err)
	}
	oldLockState, err := scValToBytes32(vec[4])
	if err != nil {
		return nil, fmt.Errorf("event data[4] (old_lock_state): %w", err)
	}

	return &NotoLockSpentOrCancelled_Event{
		TxId:         txID,
		LockID:       lockID,
		Inputs:       lockedInputs,
		Outputs:      outputs,
		TxData:       data,
		OldLockState: oldLockState,
	}, nil
}

// decodeStellarCancelUnlockEvent decodes SNoto's on-chain `cancel_unlock` event (topics =
// ["cancel_unlock", lock_id], data = vec![tx_id, locked_inputs, cancel_outputs, data,
// old_lock_state] - soroban/contracts/snoto/src/lib.rs's `CancelUnlock` struct) into the same
// NotoLockSpentOrCancelled_Event shape decodeStellarUnlockEvent produces, so
// applyLockSpentOrCancelledEvent can be shared unchanged - mirrors that decoder exactly (same
// reasoning for tx_id living in the data vec, not the topic; same Spender nil treatment; same
// old_lock_state population).
func decodeStellarCancelUnlockEvent(ctx context.Context, ev *prototk.OnChainEvent) (*NotoLockSpentOrCancelled_Event, error) {
	payload, err := decodeStellarEventPayload(ev)
	if err != nil {
		return nil, err
	}
	if len(payload.Topics) < 2 {
		return nil, fmt.Errorf("expected at least 2 topics (symbol, lock_id), got %d", len(payload.Topics))
	}
	lockIDVal, err := decodeStellarScVal(ctx, payload.Topics[1])
	if err != nil {
		return nil, fmt.Errorf("lock_id topic: %w", err)
	}
	lockID, err := scValToBytes32(lockIDVal)
	if err != nil {
		return nil, fmt.Errorf("lock_id topic: %w", err)
	}

	vec, err := decodeStellarEventDataVec(ctx, payload.Data, 5)
	if err != nil {
		return nil, err
	}
	txID, err := scValToBytes32(vec[0])
	if err != nil {
		return nil, fmt.Errorf("event data[0] (tx_id): %w", err)
	}
	lockedInputs, err := scValToBytes32Vec(vec[1])
	if err != nil {
		return nil, fmt.Errorf("event data[1] (locked_inputs): %w", err)
	}
	cancelOutputs, err := scValToBytes32Vec(vec[2])
	if err != nil {
		return nil, fmt.Errorf("event data[2] (cancel_outputs): %w", err)
	}
	data, err := scValToBytes(vec[3])
	if err != nil {
		return nil, fmt.Errorf("event data[3] (data): %w", err)
	}
	oldLockState, err := scValToBytes32(vec[4])
	if err != nil {
		return nil, fmt.Errorf("event data[4] (old_lock_state): %w", err)
	}

	return &NotoLockSpentOrCancelled_Event{
		TxId:         txID,
		LockID:       lockID,
		Inputs:       lockedInputs,
		Outputs:      cancelOutputs,
		TxData:       data,
		OldLockState: oldLockState,
	}, nil
}

// scValToI128 expects an I128 ScVal (SNoto's deposit/withdraw "amount" shape) - the inverse of
// scValI128 (chainio_stellar.go). Amounts in this codebase are always non-negative (validated
// on-chain via `amount <= 0` panics), so Hi is always itself non-negative.
func scValToI128(val xdr.ScVal) (*big.Int, error) {
	if val.Type != xdr.ScValTypeScvI128 || val.I128 == nil {
		return nil, fmt.Errorf("expected an I128 value")
	}
	result := new(big.Int).SetInt64(int64(val.I128.Hi))
	result.Lsh(result, 64)
	result.Or(result, new(big.Int).SetUint64(uint64(val.I128.Lo)))
	return result, nil
}

// stellarDepositEvent/stellarWithdrawEvent are decoded shapes local to this file - unlike
// transfer/lock/unlock/prepare_unlock/delegate_lock/cancel_unlock, deposit/withdraw have no EVM
// equivalent at all (Stellar-only real SAC shield/unshield), so there's no existing EVM event
// struct to decode into.
type stellarDepositEvent struct {
	TxId    pldtypes.Bytes32
	From    string
	Amount  *big.Int
	Outputs []pldtypes.Bytes32
	Data    pldtypes.HexBytes
}

type stellarWithdrawEvent struct {
	TxId      pldtypes.Bytes32
	Recipient string
	Amount    *big.Int
	Inputs    []pldtypes.Bytes32
	Data      pldtypes.HexBytes
}

// decodeStellarDepositEvent decodes SNoto's on-chain `deposit` event (topics = ["deposit", tx_id],
// data = vec![from, amount, outputs, data] - soroban/contracts/snoto/src/lib.rs's `Deposit`
// struct).
func decodeStellarDepositEvent(ctx context.Context, ev *prototk.OnChainEvent) (*stellarDepositEvent, error) {
	payload, err := decodeStellarEventPayload(ev)
	if err != nil {
		return nil, err
	}
	if len(payload.Topics) < 2 {
		return nil, fmt.Errorf("expected at least 2 topics (symbol, tx_id), got %d", len(payload.Topics))
	}
	txIDVal, err := decodeStellarScVal(ctx, payload.Topics[1])
	if err != nil {
		return nil, fmt.Errorf("tx_id topic: %w", err)
	}
	txID, err := scValToBytes32(txIDVal)
	if err != nil {
		return nil, fmt.Errorf("tx_id topic: %w", err)
	}

	vec, err := decodeStellarEventDataVec(ctx, payload.Data, 4)
	if err != nil {
		return nil, err
	}
	from, err := scValToAddressString(vec[0])
	if err != nil {
		return nil, fmt.Errorf("event data[0] (from): %w", err)
	}
	amount, err := scValToI128(vec[1])
	if err != nil {
		return nil, fmt.Errorf("event data[1] (amount): %w", err)
	}
	outputs, err := scValToBytes32Vec(vec[2])
	if err != nil {
		return nil, fmt.Errorf("event data[2] (outputs): %w", err)
	}
	data, err := scValToBytes(vec[3])
	if err != nil {
		return nil, fmt.Errorf("event data[3] (data): %w", err)
	}

	return &stellarDepositEvent{
		TxId:    txID,
		From:    from,
		Amount:  amount,
		Outputs: outputs,
		Data:    data,
	}, nil
}

// decodeStellarWithdrawEvent decodes SNoto's on-chain `withdraw` event (topics = ["withdraw",
// tx_id], data = vec![recipient, amount, inputs, data] - soroban/contracts/snoto/src/lib.rs's
// `Withdraw` struct).
func decodeStellarWithdrawEvent(ctx context.Context, ev *prototk.OnChainEvent) (*stellarWithdrawEvent, error) {
	payload, err := decodeStellarEventPayload(ev)
	if err != nil {
		return nil, err
	}
	if len(payload.Topics) < 2 {
		return nil, fmt.Errorf("expected at least 2 topics (symbol, tx_id), got %d", len(payload.Topics))
	}
	txIDVal, err := decodeStellarScVal(ctx, payload.Topics[1])
	if err != nil {
		return nil, fmt.Errorf("tx_id topic: %w", err)
	}
	txID, err := scValToBytes32(txIDVal)
	if err != nil {
		return nil, fmt.Errorf("tx_id topic: %w", err)
	}

	vec, err := decodeStellarEventDataVec(ctx, payload.Data, 4)
	if err != nil {
		return nil, err
	}
	recipient, err := scValToAddressString(vec[0])
	if err != nil {
		return nil, fmt.Errorf("event data[0] (recipient): %w", err)
	}
	amount, err := scValToI128(vec[1])
	if err != nil {
		return nil, fmt.Errorf("event data[1] (amount): %w", err)
	}
	inputs, err := scValToBytes32Vec(vec[2])
	if err != nil {
		return nil, fmt.Errorf("event data[2] (inputs): %w", err)
	}
	data, err := scValToBytes(vec[3])
	if err != nil {
		return nil, fmt.Errorf("event data[3] (data): %w", err)
	}

	return &stellarWithdrawEvent{
		TxId:      txID,
		Recipient: recipient,
		Amount:    amount,
		Inputs:    inputs,
		Data:      data,
	}, nil
}

// applyStellarDepositEvent applies a decoded Deposit event to the batch response - a mint-shaped
// confirmation (new outputs confirmed, nothing spent), no EVM counterpart to share with.
func (n *Noto) applyStellarDepositEvent(ctx context.Context, ev *prototk.OnChainEvent, deposit *stellarDepositEvent, res *prototk.HandleEventBatchResponse) error {
	txData, err := n.decodeTransactionDataV1(ctx, deposit.Data)
	if err != nil {
		return err
	}
	n.recordTransactionInfo(ev, deposit.TxId, txData.InfoStates, res)
	res.ConfirmedStates = append(res.ConfirmedStates, n.parseStatesFromEvent(deposit.TxId, deposit.Outputs)...)
	return nil
}

// applyStellarWithdrawEvent applies a decoded Withdraw event to the batch response - a burn-shaped
// confirmation (inputs spent, nothing new confirmed), no EVM counterpart to share with.
func (n *Noto) applyStellarWithdrawEvent(ctx context.Context, ev *prototk.OnChainEvent, withdraw *stellarWithdrawEvent, res *prototk.HandleEventBatchResponse) error {
	txData, err := n.decodeTransactionDataV1(ctx, withdraw.Data)
	if err != nil {
		return err
	}
	n.recordTransactionInfo(ev, withdraw.TxId, txData.InfoStates, res)
	res.SpentStates = append(res.SpentStates, n.parseStatesFromEvent(withdraw.TxId, withdraw.Inputs)...)
	return nil
}
