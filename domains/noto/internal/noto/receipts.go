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
	"math/big"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/log"
	"github.com/LFDT-Paladin/paladin/domains/noto/pkg/types"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/google/uuid"
	"github.com/hyperledger/firefly-signer/pkg/abi"
)

func (n *Noto) BuildReceipt(ctx context.Context, req *prototk.BuildReceiptRequest) (res *prototk.BuildReceiptResponse, err error) {
	receipt := &types.NotoDomainReceipt{}

	infoStates := n.filterSchema(req.InfoStates, []string{n.dataSchemaV0.Id, n.dataSchemaV1.Id, n.dataSchemaV2.Id})
	var variant pldtypes.HexUint64
	if len(infoStates) > 0 {
		// For prepareUnlock we have two data states - one for the unlockData, and one for the prepareUnlock data.
		// So we take the last one in the list
		info, err := n.unmarshalInfo(infoStates[len(infoStates)-1].StateDataJson)
		if err != nil {
			return nil, err
		}
		receipt.Data = info.Data
		variant = info.Variant

		// Extract requester information from TransactionData info state
		if info.From != nil {
			receipt.Sender = info.From
		}
	}

	receipt.States.Inputs, err = n.receiptStates(ctx, n.filterSchema(req.InputStates, []string{n.coinSchema.Id}))
	if err == nil {
		receipt.States.LockedInputs, err = n.receiptStates(ctx, n.filterSchema(req.InputStates, []string{n.lockedCoinSchema.Id}))
	}
	if err == nil {
		receipt.States.Outputs, err = n.receiptStates(ctx, n.filterSchema(req.OutputStates, []string{n.coinSchema.Id}))
	}
	if err == nil {
		receipt.States.UpdatedLockInfo, err = n.receiptStates(ctx, n.filterSchema(req.OutputStates, []string{n.lockInfoSchemaV1.Id}))
	}
	if err == nil {
		receipt.States.LockedOutputs, err = n.receiptStates(ctx, n.filterSchema(req.OutputStates, []string{n.lockedCoinSchema.Id}))
	}
	if err == nil {
		receipt.States.ReadInputs, err = n.receiptStates(ctx, n.filterSchema(req.ReadStates, []string{n.coinSchema.Id}))
	}
	if err == nil {
		receipt.States.ReadLockedInputs, err = n.receiptStates(ctx, n.filterSchema(req.ReadStates, []string{n.lockedCoinSchema.Id}))
	}
	if err == nil {
		receipt.States.PreparedOutputs, err = n.receiptStates(ctx, n.filterSchema(req.InfoStates, []string{n.coinSchema.Id}))
	}
	if err == nil {
		receipt.States.PreparedLockedOutputs, err = n.receiptStates(ctx, n.filterSchema(req.InfoStates, []string{n.lockedCoinSchema.Id}))
	}
	if err != nil {
		return nil, err
	}

	// For prepareUnlock, createMintLock, and prepareBurnUnlock transactions, include the encoded "unlock"
	// call that can be used to unlock the coins.
	if variant == types.NotoVariantV0 {
		receipt.LockInfo, err = n.receiptLockInfoV0(ctx, req, &receipt.States, receipt.Data)
	} else if len(receipt.States.UpdatedLockInfo) > 0 {
		receipt.LockInfo, err = n.receiptLockInfoV1V2(ctx, req, variant)
	}
	if err == nil {
		receipt.Transfers, err = n.receiptTransfers(ctx, req)
	}
	var receiptJSON []byte
	if err == nil {
		receiptJSON, err = json.Marshal(receipt)
	}
	if err != nil {
		return nil, err
	}

	return &prototk.BuildReceiptResponse{
		ReceiptJson: string(receiptJSON),
	}, nil
}

func (n *Noto) receiptStates(ctx context.Context, states []*prototk.EndorsableState) ([]*types.ReceiptState, error) {
	coins := make([]*types.ReceiptState, len(states))
	for i, state := range states {
		id, err := pldtypes.ParseHexBytes(ctx, state.Id)
		if err != nil {
			return nil, err
		}
		schemaID, err := pldtypes.ParseBytes32Ctx(ctx, state.SchemaId)
		if err != nil {
			return nil, err
		}
		coins[i] = &types.ReceiptState{
			ID:     id,
			Schema: schemaID,
			Data:   pldtypes.RawJSON(state.StateDataJson),
		}
	}
	return coins, nil
}

func (n *Noto) receiptTransfers(ctx context.Context, req *prototk.BuildReceiptRequest) ([]*types.ReceiptTransfer, error) {
	inputCoins, err := n.parseCoinList(ctx, "inputs", n.filterSchema(req.InputStates, []string{n.coinSchema.Id, n.lockedCoinSchema.Id}))
	if err != nil {
		return nil, err
	}
	outputCoins, err := n.parseCoinList(ctx, "outputs", n.filterSchema(req.OutputStates, []string{n.coinSchema.Id, n.lockedCoinSchema.Id}))
	if err != nil {
		return nil, err
	}

	// Bookkeeping is chain-neutral (pldtypes.ChainAddress) since step 4 (NotoCoin.Owner) and the
	// lock/unlock phase (NotoLockedCoin.Owner) - both fields are ChainAddress now.
	var from *pldtypes.ChainAddress
	fromAmount := big.NewInt(0)
	to := make(map[pldtypes.ChainAddress]*big.Int)

	parseInput := func(owner pldtypes.ChainAddress, amount *big.Int) bool {
		if from == nil {
			from = &owner
		} else if !owner.Equals(from) {
			return false
		}
		fromAmount.Add(fromAmount, amount)
		return true
	}

	parseOutput := func(owner pldtypes.ChainAddress, amount *big.Int) bool {
		if owner.Equals(from) {
			fromAmount.Sub(fromAmount, amount)
		} else if toAmount, ok := to[owner]; ok {
			toAmount.Add(toAmount, amount)
		} else {
			to[owner] = amount
		}
		return true
	}

	parsedOK := true
	for _, coin := range inputCoins.coins {
		parsedOK = parsedOK && coin.Owner != nil && parseInput(*coin.Owner, coin.Amount.Int())
	}
	for _, coin := range inputCoins.lockedCoins {
		parsedOK = parsedOK && coin.Owner != nil && parseInput(*coin.Owner, coin.Amount.Int())
	}
	for _, coin := range outputCoins.coins {
		parsedOK = parsedOK && coin.Owner != nil && parseOutput(*coin.Owner, coin.Amount.Int())
	}
	for _, coin := range outputCoins.lockedCoins {
		parsedOK = parsedOK && coin.Owner != nil && parseOutput(*coin.Owner, coin.Amount.Int())
	}
	if !parsedOK {
		log.L(ctx).Warnf("Failed to parse transfer coins")
		return nil, nil
	}

	if len(to) == 0 && from != nil && fromAmount.BitLen() > 0 {
		// special case for burn (no recipients)
		return []*types.ReceiptTransfer{{
			From:   from,
			Amount: (*pldtypes.HexUint256)(fromAmount),
		}}, nil
	}

	transfers := make([]*types.ReceiptTransfer, 0, len(to))
	for owner, amount := range to {
		if amount.BitLen() > 0 {
			transfers = append(transfers, &types.ReceiptTransfer{
				From:   from,
				To:     &owner,
				Amount: (*pldtypes.HexUint256)(amount),
			})
		}
	}
	return transfers, nil
}

func (n *Noto) receiptLockInfoV0(ctx context.Context, req *prototk.BuildReceiptRequest, receiptStates *types.ReceiptStates, receiptData pldtypes.HexBytes) (lockInfo *types.ReceiptLockInfo, err error) {
	var unlockInterfaceABI abi.ABI
	var paramsJSON []byte
	lockInfoStates := n.filterSchema(req.InfoStates, []string{n.lockInfoSchemaV0.Id})
	if len(lockInfoStates) == 1 {
		var lock *types.NotoLockInfo_V0
		lock, err = n.unmarshalLockV0(lockInfoStates[0].StateDataJson)
		if err == nil {
			lockInfo = &types.ReceiptLockInfo{LockID: lock.LockID}
			// ReceiptLockInfo.Delegate is a receipt-facing type, deliberately left EVM-only -
			// unwrap the chain-neutral lock.Delegate back to an EthAddress; a Stellar delegate
			// has no representation here yet (receipt generation isn't Stellar-capable this
			// phase), so it's simply omitted rather than erroring the whole receipt.
			if lock.Delegate != nil && !lock.Delegate.IsZero() {
				if delegateAddr, addrErr := lock.Delegate.EthAddress(); addrErr == nil {
					lockInfo.Delegate = delegateAddr
				}
			}
		}
	}

	if lockInfo != nil && len(receiptStates.ReadLockedInputs) > 0 && len(receiptStates.PreparedOutputs) > 0 {
		// Old info-based decoding scheme
		var lockID *pldtypes.Bytes32
		var delegate *pldtypes.ChainAddress
		lockID, delegate, err = n.extractLockInfoV0(ctx, req.InfoStates, false)
		if err != nil {
			return nil, err
		}
		if lockID != nil {
			lockInfo = &types.ReceiptLockInfo{
				LockID: *lockID,
			}
			// delegate came directly from the info state in for V0 - unwrap to the receipt's
			// EVM-only Delegate field, same treatment as receiptLockInfoV0 above.
			if delegate != nil && !delegate.IsZero() {
				if delegateAddr, addrErr := delegate.EthAddress(); addrErr == nil {
					lockInfo.Delegate = delegateAddr
				}
			}

			unlockInterfaceABI = n.getInterfaceABI(types.NotoVariantV0)
			lockInfo.UnlockFunction = "unlock"
			lockInfo.UnlockParams = map[string]any{
				"txId":          pldtypes.Bytes32UUIDFirst16(uuid.New()).String(), // In V0 we generated a new UUID each time you request a receipt
				"lockedInputs":  endorsableStateIDs(ctx, n.filterSchema(req.ReadStates, []string{n.lockedCoinSchema.Id}), false),
				"lockedOutputs": endorsableStateIDs(ctx, n.filterSchema(req.InfoStates, []string{n.lockedCoinSchema.Id}), false),
				"outputs":       endorsableStateIDs(ctx, n.filterSchema(req.InfoStates, []string{n.coinSchema.Id}), false),
				"signature":     pldtypes.HexBytes{},
				"data":          receiptData, // for V0 we chose to pass the original "data" sent to "prepareUnlock", and decoded here from the info states
			}
			paramsJSON, err = json.Marshal(lockInfo.UnlockParams)
		}
	}
	if err == nil && unlockInterfaceABI != nil {
		unlockFunctionABI := unlockInterfaceABI.Functions()[lockInfo.UnlockFunction]
		lockInfo.UnlockCall, err = unlockFunctionABI.EncodeCallDataJSONCtx(ctx, paramsJSON)
	}
	return lockInfo, err

}

func (n *Noto) receiptLockInfoV1V2(ctx context.Context, req *prototk.BuildReceiptRequest, variant pldtypes.HexUint64) (lockInfo *types.ReceiptLockInfo, err error) {
	// Decode the lock transition
	lt, err := n.validateV1LockTransition(ctx, LOCK_DECODE_ANY, nil, nil, req.InputStates, req.OutputStates)
	if err == nil && lt.newLockState != nil {
		lockInfo = &types.ReceiptLockInfo{
			LockID: lt.newLockInfo.LockID,
		}
	}

	// Prepared locks have a spendTxId, and we add in extra info
	if err == nil && !lt.newLockInfo.SpendTxId.IsZero() {
		lockInfo.SpendTxId = &lt.newLockInfo.SpendTxId
		// ReceiptLockInfo.Delegate is EVM-only (receipt generation isn't Stellar-capable this
		// phase) - unwrap, same treatment as receiptLockInfoV0 above.
		if lt.newLockInfo.Spender != lt.newLockInfo.Owner {
			if delegateAddr, addrErr := lt.newLockInfo.Spender.EthAddress(); addrErr == nil {
				lockInfo.Delegate = delegateAddr
			}
		}

		var lockedInputIDs []string
		if lt.prevLockState == nil {
			// create lock: locked coins are in OutputStates (they were just created)
			lockedInputIDs = endorsableStateIDs(ctx, n.filterSchema(req.OutputStates, []string{n.lockedCoinSchema.Id}), false)
		} else {
			// prepare unlock: locked coins are in ReadStates (they were created by a prior lock transaction)
			lockedInputIDs = endorsableStateIDs(ctx, n.filterSchema(req.ReadStates, []string{n.lockedCoinSchema.Id}), false)
		}

		if n.getChainIO().ChainKind() == "stellar" {
			err = n.receiptLockInfoV1V2Stellar(ctx, lockInfo, lt, lockedInputIDs)
		} else {
			err = n.receiptLockInfoV1V2EVM(ctx, lockInfo, lt, lockedInputIDs, n.getInterfaceABI(variant))
		}
	}
	if err != nil {
		return nil, err
	}
	return lockInfo, nil
}

func (n *Noto) receiptLockInfoV1V2EVM(ctx context.Context, lockInfo *types.ReceiptLockInfo, lt *lockTransition, lockedInputIDs []string, unlockInterfaceABI abi.ABI) (err error) {
	// Encode the operation to spend the lock
	var spendLockArgs []byte
	var unlockParamsJSON []byte
	spendLockArgs, err = n.encodeNotoSpendLockArgs(ctx, &types.NotoSpendLockArgs{
		TxId:    lt.newLockInfo.SpendTxId.String(),
		Inputs:  lockedInputIDs,
		Outputs: stringIDs(lt.newLockInfo.SpendOutputs),
		Data:    lt.newLockInfo.SpendData,
		Proof:   pldtypes.HexBytes{}, // have to look back to the createLock/updateLock for the proof
	})
	if err == nil {
		lockInfo.UnlockFunction = "spendLock"
		lockInfo.UnlockParams = map[string]any{
			"lockId":    lockInfo.LockID,
			"spendArgs": pldtypes.HexBytes(spendLockArgs),
			"data":      lt.newLockInfo.SpendData,
		}
	}
	if err == nil {
		unlockParamsJSON, err = json.Marshal(lockInfo.UnlockParams)
	}
	if err == nil {
		unlockFunctionABI := unlockInterfaceABI.Functions()[lockInfo.UnlockFunction]
		lockInfo.UnlockCall, err = unlockFunctionABI.EncodeCallDataJSONCtx(ctx, unlockParamsJSON)
	}

	// Encode the operation to cancel the lock
	var cancelLockArgs []byte
	var cancelParamsJSON []byte
	if err == nil {
		cancelLockArgs, err = n.encodeNotoSpendLockArgs(ctx, &types.NotoSpendLockArgs{
			TxId:    lt.newLockInfo.SpendTxId.String(),
			Inputs:  lockedInputIDs,
			Outputs: stringIDs(lt.newLockInfo.CancelOutputs),
			Data:    lt.newLockInfo.CancelData,
			Proof:   pldtypes.HexBytes{}, // have to look back to the createLock/updateLock for the proof
		})
	}
	if err == nil {
		lockInfo.CancelFunction = "cancelLock"
		lockInfo.CancelParams = map[string]any{
			"lockId":     lockInfo.LockID,
			"cancelArgs": pldtypes.HexBytes(cancelLockArgs),
			"data":       lt.newLockInfo.CancelData,
		}
	}
	if err == nil {
		cancelParamsJSON, err = json.Marshal(lockInfo.CancelParams)
	}
	if err == nil {
		cancelFunctionABI := unlockInterfaceABI.Functions()[lockInfo.CancelFunction]
		lockInfo.CancelCall, err = cancelFunctionABI.EncodeCallDataJSONCtx(ctx, cancelParamsJSON)
	}
	return err
}

// receiptLockInfoV1V2Stellar is the Stellar counterpart of receiptLockInfoV1V2EVM - matching
// EVM's Atom-composition pattern (domains/integration-test/helpers/atom_helper.go,
// domains/integration-test/pvp_test.go's ReceiptLockInfo.UnlockCall), this is what makes SAtom's
// own atomic-composition helper possible on Stellar, the same way UnlockCall already does for
// EVM's Atom (ch. 13 §13.4).
//
// One real protocol difference from EVM, worth being explicit about: EVM's UnlockCall is a single,
// fully-encoded callData blob, ready for a raw `.call(callData)` - Soroban's `env.invoke_contract`
// has no equivalent "opaque packed call" primitive, it always needs a function name (Symbol) and
// a decomposed args vector. So UnlockCall/CancelCall here hold the XDR-encoded ARGS ONLY (an
// `ScVec`, exactly what encodeSNotoUnlockArgs/encodeSNotoCancelUnlockArgs already produce for the
// real submission path) - UnlockFunction/CancelFunction name the target function ("unlock"/
// "cancel_unlock", not EVM's "spendLock"/"cancelLock"), and the caller is expected to XDR-decode
// the ScVec back into individual entries when building an AtomOperation{contract, function, args}
// - standard XDR unmarshal, the same wire format every other part of this repo already treats as
// canonical, not a new encoding to invent.
func (n *Noto) receiptLockInfoV1V2Stellar(ctx context.Context, lockInfo *types.ReceiptLockInfo, lt *lockTransition, lockedInputIDs []string) (err error) {
	lockedInputsB32, err := parseBytes32List(ctx, lockedInputIDs)
	if err != nil {
		return err
	}

	// The exact same data-field correctness fix as stellarBaseLedgerInvokeUnlock/
	// stellarBaseLedgerInvokeCancelUnlock: SpendData/CancelData, not a fresh, unrelated encoding -
	// this receipt's whole purpose is to hand the caller args that will actually pass
	// check_commitment when submitted, so it must use the identical encoding the real
	// submission path uses.
	lockInfo.UnlockFunction = "unlock"
	lockInfo.UnlockCall, _, err = encodeSNotoUnlockArgs(lt.newLockInfo.SpendTxId, lockInfo.LockID, lockedInputsB32, lt.newLockInfo.SpendOutputs, lt.newLockInfo.SpendData)
	if err != nil {
		return err
	}
	// UnlockParams is the JSON-typed-args form (as opposed to UnlockCall's raw XDR), for callers
	// that can't submit raw XDR directly - Sente's `transition(external_calls, ...)` is the
	// motivating case: its own Rust plugin re-encodes JSON args itself (scval_json.rs's
	// encode_scval, {"type":..., "value":...} tagged values), it has no raw-args-passthrough
	// mode. Positional, matching unlock(tx_id, lock_id, locked_inputs, outputs, data)'s real Rust
	// arg order exactly - same values UnlockCall's own XDR encoding above uses, just JSON-shaped.
	lockInfo.UnlockParams = map[string]any{
		"args": []any{
			scvalJSONBytes(lt.newLockInfo.SpendTxId.String()),
			scvalJSONBytes(lockInfo.LockID.String()),
			scvalJSONVec(bytes32ListToScvalJSON(lockedInputsB32)),
			scvalJSONVec(bytes32ListToScvalJSON(lt.newLockInfo.SpendOutputs)),
			scvalJSONBytes(lt.newLockInfo.SpendData.String()),
		},
	}

	lockInfo.CancelFunction = "cancel_unlock"
	lockInfo.CancelCall, _, err = encodeSNotoCancelUnlockArgs(lt.newLockInfo.SpendTxId, lockInfo.LockID, lockedInputsB32, lt.newLockInfo.CancelOutputs, lt.newLockInfo.CancelData)
	if err != nil {
		return err
	}
	lockInfo.CancelParams = map[string]any{
		"args": []any{
			scvalJSONBytes(lt.newLockInfo.SpendTxId.String()),
			scvalJSONBytes(lockInfo.LockID.String()),
			scvalJSONVec(bytes32ListToScvalJSON(lockedInputsB32)),
			scvalJSONVec(bytes32ListToScvalJSON(lt.newLockInfo.CancelOutputs)),
			scvalJSONBytes(lt.newLockInfo.CancelData.String()),
		},
	}
	return nil
}

// scvalJSONBytes/scvalJSONVec/bytes32ListToScvalJSON build the {"type":..., "value":...} tagged
// JSON shape domains/sente/crates/sente/src/scval_json.rs's encode_scval expects - the receipt-
// level counterpart to this file's own scValBytes/scValVec XDR helpers, for callers (Sente's
// externalCalls) that need JSON args rather than raw XDR.
func scvalJSONBytes(hexValue string) map[string]any {
	return map[string]any{"type": "bytes", "value": hexValue}
}

func scvalJSONVec(items []any) map[string]any {
	return map[string]any{"type": "vec", "value": items}
}

func bytes32ListToScvalJSON(ids []pldtypes.Bytes32) []any {
	items := make([]any, len(ids))
	for i, id := range ids {
		items[i] = scvalJSONBytes(id.String())
	}
	return items
}
