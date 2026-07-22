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
	"github.com/hyperledger/firefly-signer/pkg/ethtypes"
)

type delegateLockHandler struct {
	noto *Noto
}

func (h *delegateLockHandler) ValidateParams(ctx context.Context, config *types.NotoParsedConfig, params string) (interface{}, error) {
	var delegateParams types.DelegateLockParams
	if err := json.Unmarshal([]byte(params), &delegateParams); err != nil {
		return nil, err
	}
	if delegateParams.LockID.IsZero() {
		return nil, i18n.NewError(ctx, msgs.MsgParameterRequired, "lockId")
	}
	if config.IsV0() && delegateParams.Unlock == nil {
		return nil, i18n.NewError(ctx, msgs.MsgParameterRequired, "unlock")
	}
	if delegateParams.Delegate == "" {
		return nil, i18n.NewError(ctx, msgs.MsgInvalidDelegate, delegateParams.Delegate)
	}
	return &delegateParams, nil
}

func (h *delegateLockHandler) Init(ctx context.Context, tx *types.ParsedTransaction, req *prototk.InitTransactionRequest) (*prototk.InitTransactionResponse, error) {
	params := tx.Params.(*types.DelegateLockParams)
	notary := tx.DomainConfig.NotaryLookup
	lookups := []string{notary, tx.Transaction.From}
	// A raw Stellar contract address needs no verifier resolution at all - a smart contract has
	// no private key for Paladin's key manager to resolve against (ch. 18's atomic
	// repo-settlement pattern: delegating to e.g. a Sente group's own contract address, unlocked
	// via invoker authorization when that contract calls `unlock` itself). Requesting a verifier
	// for it here would just fail identity resolution upstream in core for no reason - only
	// identity-locator delegates (the existing, unchanged path) need a verifier at all.
	if !isRawStellarContractDelegate(h.noto, params.Delegate) {
		lookups = append(lookups, params.Delegate)
	}
	return &prototk.InitTransactionResponse{
		RequiredVerifiers: h.noto.ethAddressVerifiers(lookups...),
	}, nil
}

// isRawStellarContractDelegate reports whether lookup is already a valid Stellar contract
// address (StrKey "C...") rather than a Paladin identity locator - Stellar-only, so the EVM path
// (evmChainIO) is completely unaffected by this check.
func isRawStellarContractDelegate(n *Noto, lookup string) bool {
	if n.getChainIO().ChainKind() != "stellar" {
		return false
	}
	_, err := pldtypes.NewStellarContractAddress(lookup)
	return err == nil
}

// resolveDelegate resolves the delegate for a delegate_lock call. A raw Stellar contract address
// is used directly, with no identity/verifier resolution at all. Everything else falls back to
// the existing identity-locator resolution, unchanged.
func (h *delegateLockHandler) resolveDelegate(ctx context.Context, lookup string, verifierList []*prototk.ResolvedVerifier) (*identityPair, error) {
	if isRawStellarContractDelegate(h.noto, lookup) {
		chainAddress, err := pldtypes.NewStellarContractAddress(lookup)
		if err != nil {
			return nil, err
		}
		return &identityPair{identifier: lookup, chainAddress: chainAddress}, nil
	}
	return h.noto.findEthAddressVerifier(ctx, "delegate", lookup, verifierList)
}

func (h *delegateLockHandler) Assemble(ctx context.Context, tx *types.ParsedTransaction, req *prototk.AssembleTransactionRequest) (*prototk.AssembleTransactionResponse, error) {
	params := tx.Params.(*types.DelegateLockParams)

	ids, err := resolveIdentities(ctx, h.noto, tx, req, "", "")
	if err != nil {
		return nil, err
	}
	notaryID, senderID := ids.notary, ids.sender

	delegateID, err := h.resolveDelegate(ctx, params.Delegate, req.ResolvedVerifiers)
	if err != nil {
		return nil, err
	}

	// Load the existing lock
	var lockedInputStates []*prototk.StateRef // V0 only
	var existingLock *loadedLockInfo
	if tx.DomainConfig.IsV0() {
		// In V0 at least one locked input was always present here, to confirm lock ownership - not required in V1 due to lock state check.
		lockedInputs, revert, err := h.noto.prepareLockedInputs(ctx, req.StateQueryContext, params.LockID, &senderID.chainAddress, big.NewInt(1), false)
		if res, err := assembleRevertOrError(revert, err); res != nil || err != nil {
			return res, err
		}
		lockedInputStates = lockedInputs.states
	} else {
		var revert bool
		existingLock, revert, err = h.noto.loadLockInfoV1(ctx, req.StateQueryContext, params.LockID)
		if res, err := assembleRevertOrError(revert, err); res != nil || err != nil {
			return res, err
		}
	}

	infoDistribution := identityList{notaryID, senderID}
	infoStates, err := h.noto.prepareDataInfo(ctx, params.Data, tx.DomainConfig.Variant, infoDistribution.identities(), tx.Transaction, req.ResolvedVerifiers)
	if err != nil {
		return nil, err
	}

	// Produce the new lock info
	var inputStates []*prototk.StateRef
	var outputStates []*prototk.NewState
	var lock *preparedLockInfo
	if tx.DomainConfig.IsV0() {
		lock, err = h.noto.prepareLockInfo_V0(params.LockID, &senderID.chainAddress, &delegateID.chainAddress, infoDistribution)
		if err == nil {
			infoStates = append(infoStates, lock.state) // in V0 lock states were just published as info
		}
	} else {
		newLock := *existingLock.lockInfo
		newLock.Salt = pldtypes.RandBytes32()
		newLock.Replaces = existingLock.id
		newLock.Spender = &delegateID.chainAddress
		lock, err = h.noto.prepareLockInfo_V1(&newLock, identityList{notaryID, senderID})
		if err == nil {
			inputStates = append(inputStates, existingLock.stateRef)
			outputStates = append(outputStates, lock.state) // as of V1 it is a first class transitioned state
		}
	}
	if err != nil {
		return nil, err
	}

	// This approval may leak the requesting signing identity on-chain, if the data is empty/static.
	// As apart from the 'data' (which is held off-chain in an info-state) all other parameters are written directly.
	encodedApproval, err := h.noto.encodeDelegateLock(ctx, tx.ContractAddress, params.LockID, &delegateID.chainAddress, params.Data)
	if err != nil {
		return nil, err
	}

	if !tx.DomainConfig.IsV0() {
		manifestState, err := h.noto.newManifestBuilder().
			addInfoStates(infoDistribution, infoStates...).
			addLockInfo(lock).
			buildManifest(ctx, req.StateQueryContext)
		if err != nil {
			return nil, err
		}
		infoStates = append([]*prototk.NewState{manifestState} /* manifest first */, infoStates...)
	}

	return &prototk.AssembleTransactionResponse{
		AssemblyResult: prototk.AssembleTransactionResponse_OK,
		AssembledTransaction: &prototk.AssembledTransaction{
			ReadStates:   lockedInputStates,
			InputStates:  inputStates,
			OutputStates: outputStates,
			InfoStates:   infoStates,
		},
		AttestationPlan: h.noto.buildEndorsePlan(tx.DomainConfig.NotaryLookup, req.Transaction.From, encodedApproval),
	}, nil
}

func (h *delegateLockHandler) Endorse(ctx context.Context, tx *types.ParsedTransaction, req *prototk.EndorseTransactionRequest) (*prototk.EndorseTransactionResponse, error) {
	params := tx.Params.(*types.DelegateLockParams)
	inputs, err := h.noto.parseCoinList(ctx, "read", req.Reads)
	if err != nil {
		return nil, err
	}

	if tx.DomainConfig.IsV0() {
		// Sender must specify at least one locked state, to show that they own the lock
		if len(inputs.lockedCoins) == 0 {
			return nil, i18n.NewError(ctx, msgs.MsgNoStatesSpecified)
		}
	} else {
		senderID, err := h.noto.findEthAddressVerifier(ctx, "sender", tx.Transaction.From, req.ResolvedVerifiers)
		if err != nil {
			return nil, err
		}

		// In V1 onwards the lock itself needs to be checked (which can be empty for a mint lock)
		_, err = h.noto.validateV1LockTransition(ctx, LOCK_UPDATE, senderID, &params.LockID, req.Inputs, req.Outputs)
		if err != nil {
			return nil, err
		}
	}

	if err := h.noto.validateLockOwners(ctx, tx.Transaction.From, req.ResolvedVerifiers, inputs.lockedCoins, inputs.lockedStates); err != nil {
		return nil, err
	}

	delegateID, err := h.resolveDelegate(ctx, params.Delegate, req.ResolvedVerifiers)
	if err != nil {
		return nil, err
	}

	// Notary checks the signature from the sender, then submits the transaction
	encodedApproval, err := h.noto.encodeDelegateLock(ctx, tx.ContractAddress, params.LockID, &delegateID.chainAddress, params.Data)
	if err != nil {
		return nil, err
	}
	if err := h.noto.validateSignature(ctx, "sender", req.Signatures, encodedApproval); err != nil {
		return nil, err
	}
	return &prototk.EndorseTransactionResponse{
		EndorsementResult: prototk.EndorseTransactionResponse_ENDORSER_SUBMIT,
	}, nil
}

func (h *delegateLockHandler) baseLedgerInvoke(ctx context.Context, tx *types.ParsedTransaction, req *prototk.PrepareTransactionRequest) (*TransactionWrapper, error) {
	inParams := tx.Params.(*types.DelegateLockParams)

	signature := domain.FindAttestation("sender", req.AttestationResult)
	if signature == nil {
		return nil, i18n.NewError(ctx, msgs.MsgAttestationNotFound, "sender")
	}

	txData, err := h.noto.encodeTransactionData(ctx, tx.DomainConfig, req.Transaction, req.InfoStates)
	if err != nil {
		return nil, err
	}

	delegateID, err := h.noto.findEthAddressVerifier(ctx, "delegate", inParams.Delegate, req.ResolvedVerifiers)
	if err != nil {
		return nil, err
	}

	var lt *lockTransition // v1 only
	if !tx.DomainConfig.IsV0() {
		senderID, err := h.noto.findEthAddressVerifier(ctx, "sender", tx.Transaction.From, req.ResolvedVerifiers)
		if err != nil {
			return nil, err
		}

		lt, err = h.noto.validateV1LockTransition(ctx, LOCK_UPDATE, senderID, &inParams.LockID, req.InputStates, req.OutputStates)
		if err != nil {
			return nil, err
		}
	}

	interfaceABI := h.noto.getInterfaceABI(tx.DomainConfig.Variant)
	functionName := "delegateLock"
	var paramsJSON []byte

	if tx.DomainConfig.IsV0() {
		// V0: delegateLock requires unlockHash
		var unlockHash ethtypes.HexBytes0xPrefix
		unlockHash, err = h.noto.unlockHashFromIDs_V0(ctx, tx.ContractAddress, inParams.Unlock.LockedInputs, inParams.Unlock.LockedOutputs, inParams.Unlock.Outputs, inParams.Unlock.Data)
		if err != nil {
			return nil, err
		}
		unlockHashBytes32 := pldtypes.Bytes32(unlockHash)
		paramsJSON, err = json.Marshal(&NotoDelegateLock_V0_Params{
			TxId:       req.Transaction.TransactionId,
			UnlockHash: &unlockHashBytes32,
			Delegate:   delegateID.address,
			Signature:  signature.Payload,
			Data:       txData,
		})
	} else if tx.DomainConfig.IsV1() || tx.DomainConfig.IsV2() {
		var delegateInputsEncoded pldtypes.HexBytes
		delegateInputsEncoded, err = h.noto.encodeNotoDelegateLockArgs(ctx, &types.NotoDelegateLockArgs{
			TxId:         req.Transaction.TransactionId,
			OldLockState: lt.prevLockStateID,
			NewLockState: lt.newLockStateID,
			Proof:        signature.Payload,
		})
		if err == nil {
			paramsJSON, err = json.Marshal(&DelegateLockParams{
				LockID:       inParams.LockID,
				DelegateArgs: delegateInputsEncoded,
				NewSpender:   delegateID.address,
				Data:         txData,
			})
		}
	} else {
		return nil, i18n.NewError(ctx, msgs.MsgUnknownDomainVariant, tx.DomainConfig.Variant)
	}
	if err != nil {
		return nil, err
	}
	return &TransactionWrapper{
		functionABI: interfaceABI.Functions()[functionName],
		paramsJSON:  paramsJSON,
	}, nil
}

func (h *delegateLockHandler) hookInvoke(ctx context.Context, tx *types.ParsedTransaction, req *prototk.PrepareTransactionRequest, baseTransaction *TransactionWrapper) (*TransactionWrapper, error) {
	inParams := tx.Params.(*types.DelegateLockParams)

	senderID, err := h.noto.findEthAddressVerifier(ctx, "sender", tx.Transaction.From, req.ResolvedVerifiers)
	if err != nil {
		return nil, err
	}
	delegateID, err := h.noto.findEthAddressVerifier(ctx, "delegate", inParams.Delegate, req.ResolvedVerifiers)
	if err != nil {
		return nil, err
	}

	encodedCall, err := baseTransaction.encode(ctx)
	if err != nil {
		return nil, err
	}
	params := &ApproveUnlockHookParams{
		Sender:   senderID.address,
		LockID:   inParams.LockID,
		Delegate: delegateID.address,
		Data:     inParams.Data,
		Prepared: PreparedTransaction{
			ContractAddress: (*pldtypes.EthAddress)(tx.ContractAddress),
			EncodedCall:     encodedCall,
		},
	}

	transactionType, functionABI, paramsJSON, err := h.noto.wrapHookTransaction(
		tx.DomainConfig,
		hooksBuild.ABI.Functions()["onDelegateLock"],
		params,
	)
	if err != nil {
		return nil, err
	}

	return &TransactionWrapper{
		transactionType: mapPrepareTransactionType(transactionType),
		functionABI:     functionABI,
		paramsJSON:      paramsJSON,
		contractAddress: tx.DomainConfig.Options.Hooks.PublicAddress,
	}, nil
}

// stellarBaseLedgerInvokeDelegateLock builds a real SorobanInvoke for SNoto's actual
// `delegate_lock(tx_id, lock_id, delegate, data)` (chapter 13 §13.2). Only the V1+ path is
// exercised, same as every other Stellar handler this phase - V0 never runs on Stellar
// (decodeStellarConfig always sets NotoVariantV1), so ValidateParams's V0-only `params.Unlock`
// requirement never applies here.
func (h *delegateLockHandler) stellarBaseLedgerInvokeDelegateLock(ctx context.Context, tx *types.ParsedTransaction, req *prototk.PrepareTransactionRequest) (*prototk.PrepareTransactionResponse, error) {
	inParams := tx.Params.(*types.DelegateLockParams)

	delegateID, err := h.resolveDelegate(ctx, inParams.Delegate, req.ResolvedVerifiers)
	if err != nil {
		return nil, err
	}

	txID, err := pldtypes.ParseBytes32Ctx(ctx, req.Transaction.TransactionId)
	if err != nil {
		return nil, err
	}
	data, err := h.noto.encodeTransactionData(ctx, tx.DomainConfig, req.Transaction, req.InfoStates)
	if err != nil {
		return nil, err
	}

	// The updated lockInfoV1 state's own ID - see encodeSNotoLockArgs's own doc comment. Mirrors
	// the EVM path's own validateV1LockTransition call (baseLedgerInvoke, above).
	lt, err := h.noto.validateV1LockTransition(ctx, LOCK_UPDATE, nil, &inParams.LockID, req.InputStates, req.OutputStates)
	if err != nil {
		return nil, err
	}

	realContractID := tx.Transaction.ContractInfo.ContractAddress
	argsXDR, argsJSON, err := encodeSNotoDelegateLockArgs(txID, inParams.LockID, delegateID.chainAddress.String(), data, lt.newLockStateID)
	if err != nil {
		return nil, err
	}

	return &prototk.PrepareTransactionResponse{
		ChainTransaction: &prototk.PreparedChainTransaction{
			Type: prototk.PreparedChainTransaction_PUBLIC,
			Payload: &prototk.PreparedChainTransaction_Soroban{
				Soroban: &prototk.SorobanInvoke{
					ContractId:   realContractID,
					FunctionName: "delegate_lock",
					ArgsXdr:      argsXDR,
					ArgsJson:     argsJSON,
				},
			},
		},
	}, nil
}

func (h *delegateLockHandler) Prepare(ctx context.Context, tx *types.ParsedTransaction, req *prototk.PrepareTransactionRequest) (*prototk.PrepareTransactionResponse, error) {
	if h.noto.getChainIO().ChainKind() == "stellar" {
		return h.stellarBaseLedgerInvokeDelegateLock(ctx, tx, req)
	}

	baseTransaction, err := h.baseLedgerInvoke(ctx, tx, req)
	if err != nil {
		return nil, err
	}

	if tx.DomainConfig.NotaryMode == types.NotaryModeHooks.Enum() {
		hookTransaction, err := h.hookInvoke(ctx, tx, req, baseTransaction)
		if err != nil {
			return nil, err
		}
		return hookTransaction.prepare()
	}

	return baseTransaction.prepare()
}
