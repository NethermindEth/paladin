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

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/domains/noto/internal/msgs"
	"github.com/LFDT-Paladin/paladin/domains/noto/pkg/types"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/domain"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
)

// cancelLockHandler implements EVM's cancelLock/Stellar's cancel_unlock: invoking a lock's
// already-committed cancel path. Both on-chain contracts fix a lock's spendCommitment/
// cancelCommitment (Solidity)/spend_commitment/cancel_commitment (Rust) at createLock/updateLock
// time - see Noto.sol's _spendLock, which only hash-checks a spendLock/cancelLock call against the
// PRE-EXISTING commitment, never recomputing outputs itself. So unlike unlockHandler (which always
// computes fresh outputs from a caller-supplied Recipients list, using functionName "spendLock" but
// with a brand new commitment implied by fresh args), cancelLockHandler never computes new outputs
// at all - it replays the exact already-committed cancelOutputs/cancelData fixed when the lock was
// created (createTransferLock/createMintLock/createBurnLock) or later replaced (prepareUnlock),
// reconstructed from the pre-allocated state IDs stored in the lock's own NotoLockInfo_V1
// (locks.go), fetched via Noto.getStates (the same mechanism events.go's
// handleNotaryPrivateUnlock uses to reconstruct known state data from known IDs).
type cancelLockHandler struct {
	lockCommon
}

func (h *cancelLockHandler) ValidateParams(ctx context.Context, config *types.NotoParsedConfig, params string) (interface{}, error) {
	var cancelParams types.CancelLockParams
	if err := json.Unmarshal([]byte(params), &cancelParams); err != nil {
		return nil, err
	}
	if cancelParams.LockID.IsZero() {
		return nil, i18n.NewError(ctx, msgs.MsgParameterRequired, "lockId")
	}
	if len(cancelParams.From) == 0 {
		return nil, i18n.NewError(ctx, msgs.MsgParameterRequired, "from")
	}
	return &cancelParams, nil
}

func (h *cancelLockHandler) Init(ctx context.Context, tx *types.ParsedTransaction, req *prototk.InitTransactionRequest) (*prototk.InitTransactionResponse, error) {
	params := tx.Params.(*types.CancelLockParams)
	if err := h.checkAllowed(ctx, tx, params.From); err != nil {
		return nil, err
	}
	notary := tx.DomainConfig.NotaryLookup
	return &prototk.InitTransactionResponse{
		RequiredVerifiers: h.noto.ethAddressVerifiers(notary, tx.Transaction.From, params.From),
	}, nil
}

// loadCancelOutputStates reconstructs the exact coin states committed as a lock's cancel path.
// These were only ever distributed off-chain as info states when the lock was created/prepared
// (e.g. handler_prepare_unlock.go's assembledTransaction.InfoStates append) - they have no on-chain
// existence yet, so cancelLock is what first reveals them as real, confirmed output states, reusing
// their already-fixed IDs (not allocating new ones - those IDs are exactly what the on-chain
// cancelCommitment/cancel_commitment hash already covers).
func (h *cancelLockHandler) loadCancelOutputStates(ctx context.Context, stateQueryContext string, cancelOutputIDs []pldtypes.Bytes32, distribution identityList) ([]*prototk.NewState, []*types.NotoCoin, error) {
	ids := stringIDs(cancelOutputIDs)
	stored, err := h.noto.getStates(ctx, stateQueryContext, h.noto.coinSchema.Id, ids)
	if err != nil {
		return nil, nil, err
	}
	if len(stored) != len(ids) {
		return nil, nil, i18n.NewError(ctx, msgs.MsgMissingStateData, ids)
	}
	distributionList := distribution.identities()
	states := make([]*prototk.NewState, len(stored))
	coins := make([]*types.NotoCoin, len(stored))
	for i, state := range stored {
		coin, err := h.noto.unmarshalCoin(state.DataJson)
		if err != nil {
			return nil, nil, err
		}
		id := state.Id
		states[i] = &prototk.NewState{
			Id:               &id,
			SchemaId:         state.SchemaId,
			StateDataJson:    state.DataJson,
			DistributionList: distributionList,
		}
		coins[i] = coin
	}
	return states, coins, nil
}

func (h *cancelLockHandler) Assemble(ctx context.Context, tx *types.ParsedTransaction, req *prototk.AssembleTransactionRequest) (*prototk.AssembleTransactionResponse, error) {
	params := tx.Params.(*types.CancelLockParams)

	if tx.DomainConfig.IsV0() {
		return nil, i18n.NewError(ctx, msgs.MsgCancelLockNotSupportedV0)
	}

	ids, err := resolveIdentities(ctx, h.noto, tx, req, params.From, "")
	if err != nil {
		return nil, err
	}
	notaryID, senderID, fromID := ids.notary, ids.sender, ids.from

	existingLock, revert, err := h.noto.loadLockInfoV1(ctx, req.StateQueryContext, params.LockID)
	if res, err := assembleRevertOrError(revert, err); res != nil || err != nil {
		return res, err
	}
	if len(existingLock.lockInfo.CancelOutputs) == 0 {
		return nil, i18n.NewError(ctx, msgs.MsgCancelLockNoCancelPath, params.LockID)
	}

	// The locked-coin pool for a given lockId is fixed once created (only ever fully consumed by
	// spendLock/cancelLock, never partially drained and refilled) - selectAll=true with a
	// zero-amount floor deterministically returns the exact same set that was already locked when
	// the cancel commitment was computed, exactly like unlockHandler's own prepareLockedInputs call.
	lockedInputs, revert, err := h.noto.prepareLockedInputs(ctx, req.StateQueryContext, params.LockID, &fromID.chainAddress, big.NewInt(0), true)
	if res, err := assembleRevertOrError(revert, err); res != nil || err != nil {
		return res, err
	}

	distribution := identityList{notaryID, senderID, fromID}
	cancelOutputStates, cancelCoins, err := h.loadCancelOutputStates(ctx, req.StateQueryContext, existingLock.lockInfo.CancelOutputs, distribution)
	if err != nil {
		return nil, err
	}

	encodedCancel, err := h.noto.encodeUnlock(ctx, tx.ContractAddress, lockedInputs.coins, nil, cancelCoins)
	if err != nil {
		return nil, err
	}

	return &prototk.AssembleTransactionResponse{
		AssemblyResult: prototk.AssembleTransactionResponse_OK,
		AssembledTransaction: &prototk.AssembledTransaction{
			InputStates:  append(lockedInputs.states, existingLock.stateRef),
			OutputStates: cancelOutputStates,
		},
		AttestationPlan: h.noto.buildEndorsePlan(tx.DomainConfig.NotaryLookup, req.Transaction.From, encodedCancel),
	}, nil
}

func (h *cancelLockHandler) Endorse(ctx context.Context, tx *types.ParsedTransaction, req *prototk.EndorseTransactionRequest) (*prototk.EndorseTransactionResponse, error) {
	params := tx.Params.(*types.CancelLockParams)

	senderID, err := h.noto.findEthAddressVerifier(ctx, "sender", tx.Transaction.From, req.ResolvedVerifiers)
	if err != nil {
		return nil, err
	}
	// The lock itself needs to be checked - cancelLock, like unlock, always fully consumes it
	// (LOCK_SPEND: one input lock-info state, zero output lock-info states).
	if _, err := h.noto.validateV1LockTransition(ctx, LOCK_SPEND, senderID, &params.LockID, req.Inputs, req.Outputs); err != nil {
		return nil, err
	}

	if err := h.checkAllowed(ctx, tx, params.From); err != nil {
		return nil, err
	}

	inputs, err := h.noto.parseCoinList(ctx, "input", req.Inputs)
	if err != nil {
		return nil, err
	}
	outputs, err := h.noto.parseCoinList(ctx, "output", req.Outputs)
	if err != nil {
		return nil, err
	}

	if err := h.noto.validateUnlockAmounts(ctx, tx, inputs, outputs); err != nil {
		return nil, err
	}
	if err := h.noto.validateLockOwners(ctx, params.From, req.ResolvedVerifiers, inputs.lockedCoins, inputs.lockedStates); err != nil {
		return nil, err
	}
	if err := h.noto.validateOwners(ctx, params.From, req.ResolvedVerifiers, outputs.coins, outputs.states); err != nil {
		return nil, err
	}

	encodedCancel, err := h.noto.encodeUnlock(ctx, tx.ContractAddress, inputs.lockedCoins, nil, outputs.coins)
	if err != nil {
		return nil, err
	}
	if err := h.noto.validateSignature(ctx, "sender", req.Signatures, encodedCancel); err != nil {
		return nil, err
	}
	return &prototk.EndorseTransactionResponse{
		EndorsementResult: prototk.EndorseTransactionResponse_ENDORSER_SUBMIT,
	}, nil
}

func (h *cancelLockHandler) baseLedgerInvoke(ctx context.Context, tx *types.ParsedTransaction, req *prototk.PrepareTransactionRequest) (*TransactionWrapper, error) {
	inParams := tx.Params.(*types.CancelLockParams)

	senderID, err := h.noto.findEthAddressVerifier(ctx, "sender", tx.Transaction.From, req.ResolvedVerifiers)
	if err != nil {
		return nil, err
	}
	lt, err := h.noto.validateV1LockTransition(ctx, LOCK_SPEND, senderID, &inParams.LockID, req.InputStates, req.OutputStates)
	if err != nil {
		return nil, err
	}

	signature := domain.FindAttestation("sender", req.AttestationResult)
	if signature == nil {
		return nil, i18n.NewError(ctx, msgs.MsgAttestationNotFound, "sender")
	}

	txData, err := h.noto.encodeTransactionData(ctx, tx.DomainConfig, req.Transaction, req.InfoStates)
	if err != nil {
		return nil, err
	}

	// Locked-coin state IDs are always referenced literally here, never by nullifier (matching
	// buildCreateLockParams/buildPrepareUnlockParams's own convention for the exact same reason:
	// locked outputs are spent by UTXO ID rather than nullifier, per handler_lock.go's own comment)
	// - this must byte-for-byte match what was hashed into the already-committed cancelCommitment/
	// cancel_commitment at create/prepare time, unlike unlockHandler's own Inputs (which uses
	// useNullifiers, but never matters there since a plain unlock's commitment is always zero/
	// unrestricted and so never hash-checked on-chain).
	lockedCoinInputs := h.noto.filterSchema(req.InputStates, []string{h.noto.lockedCoinSchema.Id})
	outputs, _ := h.noto.splitStates(req.OutputStates)

	cancelArgs, err := h.noto.encodeNotoSpendLockArgs(ctx, &types.NotoSpendLockArgs{
		TxId:    lt.prevLockInfo.SpendTxId.String(),
		Inputs:  endorsableStateIDs(ctx, lockedCoinInputs, false),
		Outputs: endorsableStateIDs(ctx, outputs, false),
		Data:    lt.prevLockInfo.CancelData,
		Proof:   signature.Payload,
	})
	if err != nil {
		return nil, err
	}
	paramsJSON, err := json.Marshal(&CancelLockParams{
		LockID:     lt.prevLockInfo.LockID,
		CancelArgs: cancelArgs,
		Data:       txData,
	})
	if err != nil {
		return nil, err
	}

	interfaceABI := h.noto.getInterfaceABI(tx.DomainConfig.Variant)
	return &TransactionWrapper{
		functionABI: interfaceABI.Functions()["cancelLock"],
		paramsJSON:  paramsJSON,
	}, nil
}

// stellarBaseLedgerInvokeCancelUnlock builds a real SorobanInvoke for SNoto's actual
// `cancel_unlock(tx_id, lock_id, locked_inputs, cancel_outputs, data)`, replaying the exact
// already-committed cancel path via the already-verified-correct encodeSNotoCancelUnlockArgs
// (cross-checked byte-for-byte against soroban/contracts/snoto/src/lib.rs's check_commitment).
// txID is the lock's own stored SpendTxId (the commitment nonce shared by both the spend and
// cancel paths, fixed at createLock/prepare_unlock time) - not req.Transaction.TransactionId,
// which is this cancelLock invocation's own (different) transaction ID and would never match the
// already-computed on-chain commitment.
func (h *cancelLockHandler) stellarBaseLedgerInvokeCancelUnlock(ctx context.Context, tx *types.ParsedTransaction, req *prototk.PrepareTransactionRequest) (*prototk.PrepareTransactionResponse, error) {
	inParams := tx.Params.(*types.CancelLockParams)

	senderID, err := h.noto.findEthAddressVerifier(ctx, "sender", tx.Transaction.From, req.ResolvedVerifiers)
	if err != nil {
		return nil, err
	}
	lt, err := h.noto.validateV1LockTransition(ctx, LOCK_SPEND, senderID, &inParams.LockID, req.InputStates, req.OutputStates)
	if err != nil {
		return nil, err
	}

	// The on-chain "data" argument must be byte-for-byte identical to what prepare_unlock already
	// committed to for the "cancel" purpose (UnlockHashFromIDsV1's own "data" param, check_commitment
	// on the Rust side, soroban/contracts/snoto/src/lib.rs) - see stellarBaseLedgerInvokeUnlock's
	// own doc comment for the full explanation of why a freshly re-encoded transaction-data blob
	// (this used to compute here) is a different, wrong value. CancelData is already loaded onto
	// lt.prevLockInfo by validateV1LockTransition above, so no extra state lookup is needed here.
	data := lt.prevLockInfo.CancelData

	lockedInputStates := h.noto.filterSchema(req.InputStates, []string{h.noto.lockedCoinSchema.Id})
	lockedInputs, err := parseBytes32List(ctx, endorsableStateIDs(ctx, lockedInputStates, false))
	if err != nil {
		return nil, err
	}
	outputStates, _ := h.noto.splitStates(req.OutputStates)
	cancelOutputs, err := parseBytes32List(ctx, endorsableStateIDs(ctx, outputStates, false))
	if err != nil {
		return nil, err
	}

	contractID := tx.Transaction.ContractInfo.ContractAddress
	argsXDR, argsJSON, err := encodeSNotoCancelUnlockArgs(lt.prevLockInfo.SpendTxId, lt.prevLockInfo.LockID, lockedInputs, cancelOutputs, data)
	if err != nil {
		return nil, err
	}

	return &prototk.PrepareTransactionResponse{
		ChainTransaction: &prototk.PreparedChainTransaction{
			Type: prototk.PreparedChainTransaction_PUBLIC,
			Payload: &prototk.PreparedChainTransaction_Soroban{
				Soroban: &prototk.SorobanInvoke{
					ContractId:   contractID,
					FunctionName: "cancel_unlock",
					ArgsXdr:      argsXDR,
					ArgsJson:     argsJSON,
				},
			},
		},
	}, nil
}

func (h *cancelLockHandler) Prepare(ctx context.Context, tx *types.ParsedTransaction, req *prototk.PrepareTransactionRequest) (*prototk.PrepareTransactionResponse, error) {
	endorsement := domain.FindAttestation("notary", req.AttestationResult)
	if endorsement == nil || endorsement.Verifier.Lookup != tx.DomainConfig.NotaryLookup {
		return nil, i18n.NewError(ctx, msgs.MsgAttestationNotFound, "notary")
	}

	if h.noto.getChainIO().ChainKind() == "stellar" {
		return h.stellarBaseLedgerInvokeCancelUnlock(ctx, tx, req)
	}

	// Hooks-mode notary re-propagation (mirroring unlockHandler's hookInvoke) needs a new
	// onCancelLock hook that does not exist yet in INotoHooks.json - out of scope for this pass.
	if tx.DomainConfig.NotaryMode == types.NotaryModeHooks.Enum() {
		return nil, i18n.NewError(ctx, msgs.MsgCancelLockHooksNotSupported)
	}

	baseTransaction, err := h.baseLedgerInvoke(ctx, tx, req)
	if err != nil {
		return nil, err
	}
	return baseTransaction.prepare()
}
