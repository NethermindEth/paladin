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

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/domains/noto/internal/msgs"
	"github.com/LFDT-Paladin/paladin/domains/noto/pkg/types"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
)

// depositHandler shields (deposits) real SAC tokens, admitting new NotoCoin outputs - Stellar-only
// (soroban/contracts/snoto/src/lib.rs's `deposit`), no EVM equivalent exists. Modeled closely on
// mintHandler (a deposit is exactly a mint, off-chain, plus a real SAC pull on-chain instead of
// conjuring value): `from` is both the real on-chain SAC source and the off-chain recipient of the
// new outputs, mirroring mint's own `to`.
//
// UNLIKE every other write path in this file, SNoto's on-chain `deposit` requires TWO independent
// real signers - `from.require_auth()` (the depositor themselves) in addition to the usual
// `notary.require_auth()` (satisfied implicitly by the submitting channel account). Nothing in this
// codebase constructs a non-invoker SorobanAuthorizationEntry yet (chapter 14's own tracked
// follow-up work - see the plan's C3 workstream), so Prepare below builds a structurally correct
// SorobanInvoke but does NOT yet populate auth_entries_xdr for `from`'s signature: submitting a
// deposit today will fail on-chain with `from`'s require_auth() unsatisfied, until that lands.
type depositHandler struct {
	noto *Noto
}

func (h *depositHandler) ValidateParams(ctx context.Context, config *types.NotoParsedConfig, params string) (interface{}, error) {
	var p types.DepositParams
	err := json.Unmarshal([]byte(params), &p)
	if err == nil && p.From == "" {
		err = i18n.NewError(ctx, msgs.MsgParameterRequired, "from")
	}
	if err == nil && (p.Amount == nil || p.Amount.Int().Sign() != 1) {
		err = i18n.NewError(ctx, msgs.MsgParameterGreaterThanZero, "amount")
	}
	return &p, err
}

func (h *depositHandler) Init(ctx context.Context, tx *types.ParsedTransaction, req *prototk.InitTransactionRequest) (*prototk.InitTransactionResponse, error) {
	params := tx.Params.(*types.DepositParams)
	notary := tx.DomainConfig.NotaryLookup
	return &prototk.InitTransactionResponse{
		RequiredVerifiers: h.noto.ethAddressVerifiers(notary, tx.Transaction.From, params.From),
	}, nil
}

func (h *depositHandler) Assemble(ctx context.Context, tx *types.ParsedTransaction, req *prototk.AssembleTransactionRequest) (*prototk.AssembleTransactionResponse, error) {
	params := tx.Params.(*types.DepositParams)

	ids, err := resolveIdentities(ctx, h.noto, tx, req, params.From, "")
	if err != nil {
		return nil, err
	}
	notaryID, senderID, fromID := ids.notary, ids.sender, ids.from

	outputStates, err := h.noto.prepareOutputs(fromID, params.Amount, identityList{notaryID, fromID})
	if err != nil {
		return nil, err
	}
	infoDistribution := identityList{notaryID, senderID, fromID}
	infoStates, err := h.noto.prepareDataInfo(ctx, params.Data, tx.DomainConfig.Variant, infoDistribution.identities(), tx.Transaction, req.ResolvedVerifiers)
	if err != nil {
		return nil, err
	}

	encodedTransfer, err := h.noto.encodeTransferUnmasked(ctx, tx.ContractAddress, nil, outputStates.coins)
	if err != nil {
		return nil, err
	}

	manifestState, err := h.noto.newManifestBuilder().
		addOutputs(outputStates).
		addInfoStates(infoDistribution, infoStates...).
		buildManifest(ctx, req.StateQueryContext)
	if err != nil {
		return nil, err
	}
	infoStates = append([]*prototk.NewState{manifestState}, infoStates...)

	return &prototk.AssembleTransactionResponse{
		AssemblyResult: prototk.AssembleTransactionResponse_OK,
		AssembledTransaction: &prototk.AssembledTransaction{
			OutputStates: outputStates.states,
			InfoStates:   infoStates,
		},
		AttestationPlan: h.noto.buildEndorsePlan(tx.DomainConfig.NotaryLookup, req.Transaction.From, encodedTransfer),
	}, nil
}

func (h *depositHandler) Endorse(ctx context.Context, tx *types.ParsedTransaction, req *prototk.EndorseTransactionRequest) (*prototk.EndorseTransactionResponse, error) {
	params := tx.Params.(*types.DepositParams)

	outputs, err := h.noto.parseCoinList(ctx, "output", req.Outputs)
	if err != nil {
		return nil, err
	}
	if outputs.total.Cmp(params.Amount.Int()) != 0 {
		return nil, i18n.NewError(ctx, msgs.MsgInvalidAmount, "deposit", params.Amount.Int().Text(10), outputs.total.Text(10))
	}
	if err := h.noto.validateOwners(ctx, params.From, req.ResolvedVerifiers, outputs.coins, outputs.states); err != nil {
		return nil, err
	}

	encodedTransfer, err := h.noto.encodeTransferUnmasked(ctx, tx.ContractAddress, nil, outputs.coins)
	if err != nil {
		return nil, err
	}
	if err := h.noto.validateSignature(ctx, "sender", req.Signatures, encodedTransfer); err != nil {
		return nil, err
	}
	return &prototk.EndorseTransactionResponse{
		EndorsementResult: prototk.EndorseTransactionResponse_ENDORSER_SUBMIT,
	}, nil
}

func (h *depositHandler) Prepare(ctx context.Context, tx *types.ParsedTransaction, req *prototk.PrepareTransactionRequest) (*prototk.PrepareTransactionResponse, error) {
	if h.noto.getChainIO().ChainKind() != "stellar" {
		return nil, i18n.NewError(ctx, msgs.MsgOperationChainOnly, "deposit", "stellar")
	}
	params := tx.Params.(*types.DepositParams)

	fromID, err := h.noto.findEthAddressVerifier(ctx, "from", params.From, req.ResolvedVerifiers)
	if err != nil {
		return nil, err
	}
	txID, err := pldtypes.ParseBytes32Ctx(ctx, req.Transaction.TransactionId)
	if err != nil {
		return nil, err
	}
	outputs, err := parseBytes32List(ctx, endorsableStateIDs(ctx, req.OutputStates, false))
	if err != nil {
		return nil, err
	}
	data, err := h.noto.encodeTransactionData(ctx, tx.DomainConfig, req.Transaction, req.InfoStates)
	if err != nil {
		return nil, err
	}

	contractID := tx.Transaction.ContractInfo.ContractAddress
	argsXDR, argsJSON, err := encodeSNotoDepositArgs(txID, fromID.chainAddress.String(), params.Amount.Int(), outputs, data)
	if err != nil {
		return nil, err
	}

	// TODO (chapter 14 C3): populate SorobanInvoke.AuthEntriesXdr with `from`'s real Soroban
	// authorization entry once the attestation-plan/callback plumbing for it exists - without it,
	// this call will fail on-chain (from.require_auth() unsatisfied).
	return &prototk.PrepareTransactionResponse{
		ChainTransaction: &prototk.PreparedChainTransaction{
			Type: prototk.PreparedChainTransaction_PUBLIC,
			Payload: &prototk.PreparedChainTransaction_Soroban{
				Soroban: &prototk.SorobanInvoke{
					ContractId:   contractID,
					FunctionName: "deposit",
					ArgsXdr:      argsXDR,
					ArgsJson:     argsJSON,
				},
			},
		},
	}, nil
}
