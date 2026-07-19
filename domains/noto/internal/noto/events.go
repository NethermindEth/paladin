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
	"context"
	"encoding/json"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/common/go/pkg/log"
	"github.com/LFDT-Paladin/paladin/common/go/pkg/pldmsgs"
	"github.com/LFDT-Paladin/paladin/domains/noto/internal/msgs"
	notosmt "github.com/LFDT-Paladin/paladin/domains/noto/internal/noto/smt"
	"github.com/LFDT-Paladin/paladin/domains/noto/pkg/types"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/solutils"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/smt"
	"github.com/LFDT-Paladin/smt/pkg/sparse-merkle-tree/core"
	"github.com/LFDT-Paladin/smt/pkg/sparse-merkle-tree/node"
	"github.com/LFDT-Paladin/smt/pkg/utxo"
)

func (n *Noto) HandleEventBatch(ctx context.Context, req *prototk.HandleEventBatchRequest) (*prototk.HandleEventBatchResponse, error) {
	var res prototk.HandleEventBatchResponse

	var variant pldtypes.HexUint64
	var domainConfig types.NotoParsedConfig
	if err := json.Unmarshal([]byte(req.ContractInfo.GetContractConfigJson()), &domainConfig); err == nil {
		if domainConfig.Variant != 0 {
			variant = domainConfig.Variant
		}
	}

	for _, ev := range req.Events {
		if variant == types.NotoVariantV0 {
			if err := n.handleV0Event(ctx, ev, &res, req); err != nil {
				log.L(ctx).Warnf("Error handling V0 event: %s", err)
				return nil, err
			}
		} else {
			if err := n.handleV1Event(ctx, ev, &res, req, variant == types.NotoVariantV2Nullifiers); err != nil {
				log.L(ctx).Warnf("Error handling V1 event: %s", err)
				return nil, err
			}
		}
	}
	return &res, nil
}

func (n *Noto) handleV1Event(ctx context.Context, ev *prototk.OnChainEvent, res *prototk.HandleEventBatchResponse, req *prototk.HandleEventBatchRequest, useNullifier bool) error {
	var smtForStates *smt.MerkleTreeSpec
	var err error
	if useNullifier {
		smtName := notosmt.MerkleTreeName(req.ContractInfo.ContractAddress)
		hasher := utxo.NewKeccak256Hasher()
		smtForStates, err = smt.NewMerkleTreeSpec(ctx, smtName, smt.StatesTree, notosmt.SMT_HEIGHT_UTXO, hasher, true, n.Callbacks, n.merkleTreeRootSchema.Id, n.merkleTreeNodeSchema.Id, req.StateQueryContext)
		if err != nil {
			return err
		}
	}

	// Stellar events carry no SoliditySignature at all (that field is populated by EVM log
	// decoding only) - dispatch by raw selector instead, mirroring how Sente's own Rust plugin
	// (domains/sente/crates/sente/src/domain.rs) matches its events. See events_stellar.go's own
	// doc comment for why this is a parallel dispatch path rather than a synthetic
	// SoliditySignature: the on-chain field layouts genuinely differ from EVM's, so the decode step
	// needs per-event-kind logic regardless.
	if n.getChainIO().ChainKind() == "stellar" {
		return n.handleStellarEventV1(ctx, ev, res, req, useNullifier, smtForStates)
	}

	switch ev.SoliditySignature {
	case eventSignatures[EventTransfer]:
		log.L(ctx).Infof("Processing '%s' event in batch %s", ev.SoliditySignature, req.BatchId)
		var transfer NotoTransfer_Event
		if err := json.Unmarshal([]byte(ev.DataJson), &transfer); err == nil {
			if err := n.applyTransferEvent(ctx, ev, &transfer, res, useNullifier, smtForStates); err != nil {
				return err
			}
		} else {
			log.L(ctx).Warnf("Ignoring malformed Transfer event in batch %s: %s", req.BatchId, err)
		}

	case eventSignatures[EventNotoLockCreated]:
		log.L(ctx).Infof("Processing '%s' event in batch %s", ev.SoliditySignature, req.BatchId)
		var lockCreated NotoLockCreated_Event
		if err := json.Unmarshal([]byte(ev.DataJson), &lockCreated); err == nil {
			if err := n.applyLockCreatedEvent(ctx, ev, &lockCreated, res); err != nil {
				return err
			}
		} else {
			log.L(ctx).Warnf("Ignoring malformed LockCreated event in batch %s: %s", req.BatchId, err)
		}

	case eventSignatures[EventNotoLockUpdated]:
		log.L(ctx).Infof("Processing '%s' event in batch %s", ev.SoliditySignature, req.BatchId)
		var lockUpdated NotoLockUpdated_Event
		if err := json.Unmarshal([]byte(ev.DataJson), &lockUpdated); err == nil {
			txData, err := n.decodeTransactionDataV1(ctx, lockUpdated.Data)
			if err != nil {
				return err
			}
			n.recordTransactionInfo(ev, lockUpdated.TxId, txData.InfoStates, res)
			res.ReadStates = append(res.ReadStates, n.parseStatesFromEvent(lockUpdated.TxId, lockUpdated.Contents)...)
			res.SpentStates = append(res.SpentStates, n.parseStatesFromEvent(lockUpdated.TxId, []pldtypes.Bytes32{lockUpdated.OldLockState})...)
			res.ConfirmedStates = append(res.ConfirmedStates, n.parseStatesFromEvent(lockUpdated.TxId, []pldtypes.Bytes32{lockUpdated.NewLockState})...)
		} else {
			log.L(ctx).Warnf("Ignoring malformed LockUpdated event in batch %s: %s", req.BatchId, err)
		}

	case eventSignatures[EventNotoLockSpent], eventSignatures[EventNotoLockCancelled]:
		log.L(ctx).Infof("Processing '%s' event in batch %s", ev.SoliditySignature, req.BatchId)
		var lockSpent NotoLockSpentOrCancelled_Event
		if err := json.Unmarshal([]byte(ev.DataJson), &lockSpent); err == nil {
			if err := n.applyLockSpentOrCancelledEvent(ctx, ev, &lockSpent, res, req); err != nil {
				return err
			}
		} else {
			log.L(ctx).Warnf("Ignoring malformed %s event in batch %s: %s", ev.SoliditySignature, req.BatchId, err)
		}

	case eventSignatures[EventNotoLockDelegated]:
		log.L(ctx).Infof("Processing '%s' event in batch %s", ev.SoliditySignature, req.BatchId)
		var lockDelegated NotoLockDelegated_Event
		if err := json.Unmarshal([]byte(ev.DataJson), &lockDelegated); err == nil {
			txData, err := n.decodeTransactionDataV1(ctx, lockDelegated.Data)
			if err != nil {
				return err
			}
			n.recordTransactionInfo(ev, lockDelegated.TxId, txData.InfoStates, res)
			res.SpentStates = append(res.SpentStates, n.parseStatesFromEvent(lockDelegated.TxId, []pldtypes.Bytes32{lockDelegated.OldLockState})...)
			res.ConfirmedStates = append(res.ConfirmedStates, n.parseStatesFromEvent(lockDelegated.TxId, []pldtypes.Bytes32{lockDelegated.NewLockState})...)
		} else {
			log.L(ctx).Warnf("Ignoring malformed LockDelegated event in batch %s: %s", req.BatchId, err)
		}
	default:
		log.L(ctx).Infof("Skipping '%s' event in batch %s", ev.SoliditySignature, req.BatchId)
	}

	// Handle new states representing new SMT nodes
	if useNullifier {
		newStatesForSMT, err := smtForStates.Storage.GetNewStates(ctx)
		if err != nil {
			log.L(ctx).Errorf("Failed to get new SMT states for tree %s: %s", smtForStates.Name, err)
			return nil
		}
		if len(newStatesForSMT) > 0 {
			res.NewStates = append(res.NewStates, newStatesForSMT...)
		}
	}

	return nil
}

// applyTransferEvent applies a decoded Transfer event to the batch response. Shared between the
// EVM SoliditySignature-dispatched path in handleV1Event and the Stellar raw-selector-dispatched
// path in handleStellarEventV1 - the decoded NotoTransfer_Event shape is identical either way,
// only how it gets populated (JSON unmarshal vs XDR decode) differs.
func (n *Noto) applyTransferEvent(ctx context.Context, ev *prototk.OnChainEvent, transfer *NotoTransfer_Event, res *prototk.HandleEventBatchResponse, useNullifier bool, smtForStates *smt.MerkleTreeSpec) error {
	txData, err := n.decodeTransactionDataV1(ctx, transfer.Data)
	if err != nil {
		return err
	}
	n.recordTransactionInfo(ev, transfer.TxId, txData.InfoStates, res)
	res.SpentStates = append(res.SpentStates, n.parseStatesFromEvent(transfer.TxId, transfer.Inputs)...)
	res.ConfirmedStates = append(res.ConfirmedStates, n.parseStatesFromEvent(transfer.TxId, transfer.Outputs)...)
	if useNullifier {
		if err := n.updateMerkleTree(ctx, smtForStates.Tree, smtForStates.Storage, transfer.TxId, convertToUint256(transfer.Outputs)); err != nil {
			return err
		}
	}
	return nil
}

// applyLockCreatedEvent applies a decoded LockCreated event to the batch response. Shared between
// the EVM and Stellar dispatch paths - see applyTransferEvent's comment for why the split exists.
//
// SNoto's on-chain Lock event has no equivalent of EVM's "Contents"/"NewLockState" fields (those
// come from EVM Noto's V1+ lock-info state model, which Stellar's lock design doesn't have), so a
// Stellar-decoded NotoLockCreated_Event leaves both zero-valued. Guarding on IsZero() here (rather
// than requiring Stellar's decoder to fabricate a placeholder ID) stops a bogus all-zero state ID
// from being recorded as a genuine confirmed state.
func (n *Noto) applyLockCreatedEvent(ctx context.Context, ev *prototk.OnChainEvent, lockCreated *NotoLockCreated_Event, res *prototk.HandleEventBatchResponse) error {
	txData, err := n.decodeTransactionDataV1(ctx, lockCreated.Data)
	if err != nil {
		return err
	}
	n.recordTransactionInfo(ev, lockCreated.TxId, txData.InfoStates, res)
	res.SpentStates = append(res.SpentStates, n.parseStatesFromEvent(lockCreated.TxId, lockCreated.Inputs)...)
	res.ConfirmedStates = append(res.ConfirmedStates, n.parseStatesFromEvent(lockCreated.TxId, lockCreated.Outputs)...)
	res.ConfirmedStates = append(res.ConfirmedStates, n.parseStatesFromEvent(lockCreated.TxId, lockCreated.Contents)...)
	if !lockCreated.NewLockState.IsZero() {
		res.ConfirmedStates = append(res.ConfirmedStates, n.parseStatesFromEvent(lockCreated.TxId, []pldtypes.Bytes32{lockCreated.NewLockState})...)
	}
	return nil
}

// applyLockSpentOrCancelledEvent applies a decoded LockSpent/LockCancelled event to the batch
// response. Shared between the EVM and Stellar dispatch paths - see applyTransferEvent's comment
// for why the split exists.
//
// The Pente-hooks branch below is EVM/Pente-only (NotaryModeHooks has no Stellar equivalent yet),
// but is safe to keep in this shared helper: domainConfig.NotaryMode will simply never equal
// NotaryModeHooks for a Stellar-configured domain, so the branch is inert on that path.
//
// OldLockState.IsZero() guards the same way applyLockCreatedEvent's NewLockState check does: SNoto's
// on-chain Unlock event has no equivalent of EVM's lock-info state ref, so a Stellar-decoded
// NotoLockSpentOrCancelled_Event leaves it zero-valued, and it must not be recorded as a genuine
// spent state.
func (n *Noto) applyLockSpentOrCancelledEvent(ctx context.Context, ev *prototk.OnChainEvent, lockSpent *NotoLockSpentOrCancelled_Event, res *prototk.HandleEventBatchResponse, req *prototk.HandleEventBatchRequest) error {
	txData, err := n.decodeTransactionDataV1(ctx, lockSpent.TxData)
	if err != nil {
		return err
	}
	n.recordTransactionInfo(ev, lockSpent.TxId, txData.InfoStates, res)
	res.SpentStates = append(res.SpentStates, n.parseStatesFromEvent(lockSpent.TxId, lockSpent.Inputs)...)
	if !lockSpent.OldLockState.IsZero() {
		res.SpentStates = append(res.SpentStates, n.parseStatesFromEvent(lockSpent.TxId, []pldtypes.Bytes32{lockSpent.OldLockState})...)
	}
	res.ConfirmedStates = append(res.ConfirmedStates, n.parseStatesFromEvent(lockSpent.TxId, lockSpent.Outputs)...)

	if req.ContractInfo != nil {
		var domainConfig *types.NotoParsedConfig
		if err := json.Unmarshal([]byte(req.ContractInfo.ContractConfigJson), &domainConfig); err != nil {
			return err
		}
		if domainConfig.IsNotary &&
			domainConfig.NotaryMode == types.NotaryModeHooks.Enum() &&
			!domainConfig.Options.Hooks.PublicAddress.Equals(lockSpent.Spender) {
			if err := n.handleNotaryPrivateUnlockV1(ctx, req.StateQueryContext, domainConfig, lockSpent); err != nil {
				log.L(ctx).Errorf("Failed to handle lock-spent-or-cancelled event in batch %s: %s", req.BatchId, err)
				return err
			}
		}
	}
	return nil
}

// When notary logic is implemented via Pente, unlock events from the base ledger must be propagated
// back to the Pente hooks
// TODO: this method should not be invoked directly on the event loop, but rather via a queue
func (n *Noto) handleNotaryPrivateUnlock(ctx context.Context, stateQueryContext string, domainConfig *types.NotoParsedConfig, lockedInputs []pldtypes.Bytes32, outputs []pldtypes.Bytes32, spender *pldtypes.EthAddress, data pldtypes.HexBytes, lockID pldtypes.Bytes32) error {

	lockedInputsStr := make([]string, len(lockedInputs))
	for i, input := range lockedInputs {
		lockedInputsStr[i] = input.String()
	}
	unlockedOutputsStr := make([]string, len(outputs))
	for i, output := range outputs {
		unlockedOutputsStr[i] = output.String()
	}

	lockStates, err := n.getStates(ctx, stateQueryContext, n.lockInfoSchemaV1.Id, lockedInputsStr)
	if err != nil {
		return err
	}
	inputStates, err := n.getStates(ctx, stateQueryContext, n.lockedCoinSchema.Id, lockedInputsStr)
	if err != nil {
		return err
	}
	if (len(inputStates) + len(lockStates)) != len(lockedInputsStr) {
		return i18n.NewError(ctx, msgs.MsgMissingStateData, lockedInputs)
	}

	outputStates, err := n.getStates(ctx, stateQueryContext, n.coinSchema.Id, unlockedOutputsStr)
	if err != nil {
		return err
	}
	if len(outputStates) != len(outputs) {
		return i18n.NewError(ctx, msgs.MsgMissingStateData, outputs)
	}

	recipients := make([]*ResolvedUnlockRecipient, len(outputStates))
	for i, state := range outputStates {
		coin, err := n.unmarshalCoin(state.DataJson)
		if err != nil {
			return err
		}
		// hooks.go (Pente-private-invoke) is EVM-only - ResolvedUnlockRecipient.To stays EthAddress.
		ownerAddr, err := coin.Owner.EthAddress()
		if err != nil {
			return err
		}
		recipients[i] = &ResolvedUnlockRecipient{
			To:     ownerAddr,
			Amount: coin.Amount,
		}
	}

	transactionType, functionABI, paramsJSON, err := n.wrapHookTransaction(
		domainConfig,
		solutils.MustLoadBuild(notoHooksJSON).ABI.Functions()["handleDelegateUnlock"],
		&DelegateUnlockHookParams{
			Sender:     spender,
			LockID:     lockID,
			Recipients: recipients,
			Data:       data,
		},
	)
	if err != nil {
		return err
	}
	functionABIJSON, err := json.Marshal(functionABI)
	if err != nil {
		return err
	}

	_, err = n.Callbacks.SendTransaction(ctx, &prototk.SendTransactionRequest{
		StateQueryContext: stateQueryContext,
		Transaction: &prototk.TransactionInput{
			Type:            mapSendTransactionType(transactionType),
			From:            domainConfig.NotaryLookup,
			ContractAddress: domainConfig.Options.Hooks.PublicAddress.String(),
			FunctionAbiJson: string(functionABIJSON),
			ParamsJson:      string(paramsJSON),
		},
	})
	return err
}

func (n *Noto) handleNotaryPrivateUnlockV1(ctx context.Context, stateQueryContext string, domainConfig *types.NotoParsedConfig, unlockEvent *NotoLockSpentOrCancelled_Event) error {
	// V1: lockId is in the event
	return n.handleNotaryPrivateUnlock(ctx, stateQueryContext, domainConfig, unlockEvent.Inputs, unlockEvent.Outputs, unlockEvent.Spender, unlockEvent.TxData, unlockEvent.LockID)
}

func (n *Noto) parseStatesFromEvent(txID pldtypes.Bytes32, states []pldtypes.Bytes32) []*prototk.StateUpdate {
	refs := make([]*prototk.StateUpdate, len(states))
	for i, state := range states {
		refs[i] = &prototk.StateUpdate{
			Id:            state.String(),
			TransactionId: txID.String(),
		}
	}
	return refs
}

func (n *Noto) recordTransactionInfo(ev *prototk.OnChainEvent, txID pldtypes.Bytes32, infoStates []pldtypes.Bytes32, res *prototk.HandleEventBatchResponse) {
	res.TransactionsComplete = append(res.TransactionsComplete, &prototk.CompletedTransaction{
		TransactionId: txID.String(),
		Location:      ev.Location,
	})
	for _, state := range infoStates {
		res.InfoStates = append(res.InfoStates, &prototk.StateUpdate{
			Id:            state.String(),
			TransactionId: txID.String(),
		})
	}
}

func (n *Noto) updateMerkleTree(ctx context.Context, tree core.SparseMerkleTree, storage smt.StatesStorage, txID pldtypes.Bytes32, outputs []pldtypes.HexUint256) error {
	storage.SetTransactionId(txID.HexString0xPrefix())
	for _, out := range outputs {
		if out.NilOrZero() {
			continue
		}
		err := n.addOutputToMerkleTree(ctx, tree, out)
		if err != nil {
			return err
		}
	}
	return nil
}

func (n *Noto) addOutputToMerkleTree(ctx context.Context, tree core.SparseMerkleTree, output pldtypes.HexUint256) error {
	idx, err := node.NewNodeIndexFromBigInt(output.Int(), notosmt.GetHasher())
	if err != nil {
		return i18n.NewError(ctx, pldmsgs.MsgErrorNewNodeIndex, output.String(), err)
	}
	nidx := node.NewIndexOnly(idx)
	leaf, err := node.NewLeafNode(nidx, nil)
	if err != nil {
		return i18n.NewError(ctx, pldmsgs.MsgErrorNewLeafNode, err)
	}
	err = tree.AddLeaf(ctx, leaf)
	if err != nil {
		return i18n.NewError(ctx, pldmsgs.MsgErrorAddLeafNode, err)
	}
	return nil
}

func convertToUint256(in []pldtypes.Bytes32) []pldtypes.HexUint256 {
	out := make([]pldtypes.HexUint256, len(in))
	for i, v := range in {
		out[i] = *pldtypes.MustParseHexUint256(v.String())
	}
	return out
}
