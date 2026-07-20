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
	"math/big"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/domains/noto/internal/msgs"
	"github.com/LFDT-Paladin/paladin/domains/noto/pkg/types"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
)

// withdrawHandler unshields (withdraws) real SAC tokens to a real on-chain recipient, spending
// existing NotoCoin inputs - Stellar-only (soroban/contracts/snoto/src/lib.rs's `withdraw`), no EVM
// equivalent exists. Modeled closely on burnHandler/burnCommon (a withdraw is exactly a burn, off-
// chain, plus a real SAC unshield on-chain instead of simply vanishing): the withdrawing party
// (tx.Transaction.From, matching burn's own "no separate from param" convention) spends their own
// inputs; `withdraw` on-chain is notary-authorized only, same as transfer/mint/burn (no second-
// signer concern, unlike deposit - see handler_deposit.go's own doc comment).
type withdrawHandler struct {
	noto *Noto
}

func (h *withdrawHandler) ValidateParams(ctx context.Context, config *types.NotoParsedConfig, params string) (interface{}, error) {
	var p types.WithdrawParams
	err := json.Unmarshal([]byte(params), &p)
	if err == nil && (p.Amount == nil || p.Amount.Int().Sign() != 1) {
		err = i18n.NewError(ctx, msgs.MsgParameterGreaterThanZero, "amount")
	}
	if err == nil && p.Recipient == "" {
		err = i18n.NewError(ctx, msgs.MsgParameterRequired, "recipient")
	}
	return &p, err
}

func (h *withdrawHandler) Init(ctx context.Context, tx *types.ParsedTransaction, req *prototk.InitTransactionRequest) (*prototk.InitTransactionResponse, error) {
	params := tx.Params.(*types.WithdrawParams)
	notary := tx.DomainConfig.NotaryLookup
	return &prototk.InitTransactionResponse{
		RequiredVerifiers: h.noto.ethAddressVerifiers(notary, tx.Transaction.From, params.Recipient),
	}, nil
}

func (h *withdrawHandler) Assemble(ctx context.Context, tx *types.ParsedTransaction, req *prototk.AssembleTransactionRequest) (*prototk.AssembleTransactionResponse, error) {
	params := tx.Params.(*types.WithdrawParams)
	from := tx.Transaction.From

	ids, err := resolveIdentities(ctx, h.noto, tx, req, from, "")
	if err != nil {
		return nil, err
	}
	notaryID, senderID, fromID := ids.notary, ids.sender, ids.from
	useNullifiers := tx.DomainConfig.IsNullifierVariant()

	inputStates, revert, err := h.noto.prepareInputs(ctx, req.StateQueryContext, fromID, params.Amount, useNullifiers)
	if res, err := assembleRevertOrError(revert, err); res != nil || err != nil {
		return res, err
	}
	infoDistribution := identityList{notaryID, senderID, fromID}
	infoStates, err := h.noto.prepareDataInfo(ctx, params.Data, tx.DomainConfig.Variant, infoDistribution.identities(), tx.Transaction, req.ResolvedVerifiers)
	if err != nil {
		return nil, err
	}

	// The withdrawn value leaves the private pool entirely (unshielded to a real on-chain
	// recipient) - only any remainder above the requested amount comes back as a spendable output,
	// exactly mirroring burnCommon.assembleBurn's own remainder handling.
	outputs := &preparedOutputs{}
	if inputStates.total.Cmp(params.Amount.Int()) == 1 {
		remainder := big.NewInt(0).Sub(inputStates.total, params.Amount.Int())
		outputs, err = h.noto.prepareOutputs(fromID, (*pldtypes.HexUint256)(remainder), identityList{notaryID, senderID, fromID})
		if err != nil {
			return nil, err
		}
	}

	encodedTransfer, err := h.noto.encodeTransferUnmasked(ctx, tx.ContractAddress, inputStates.coins, outputs.coins)
	if err != nil {
		return nil, err
	}

	if !tx.DomainConfig.IsV0() {
		manifestState, err := h.noto.newManifestBuilder().
			addOutputs(outputs).
			addInfoStates(infoDistribution, infoStates...).
			buildManifest(ctx, req.StateQueryContext)
		if err != nil {
			return nil, err
		}
		infoStates = append([]*prototk.NewState{manifestState}, infoStates...)
	}

	return &prototk.AssembleTransactionResponse{
		AssemblyResult: prototk.AssembleTransactionResponse_OK,
		AssembledTransaction: &prototk.AssembledTransaction{
			InputStates:  inputStates.states,
			OutputStates: outputs.states,
			InfoStates:   infoStates,
		},
		AttestationPlan: h.noto.buildEndorsePlan(tx.DomainConfig.NotaryLookup, req.Transaction.From, encodedTransfer),
	}, nil
}

func (h *withdrawHandler) Endorse(ctx context.Context, tx *types.ParsedTransaction, req *prototk.EndorseTransactionRequest) (*prototk.EndorseTransactionResponse, error) {
	params := tx.Params.(*types.WithdrawParams)

	inputs, err := h.noto.parseCoinList(ctx, "input", req.Inputs)
	if err != nil {
		return nil, err
	}
	outputs, err := h.noto.parseCoinList(ctx, "output", req.Outputs)
	if err != nil {
		return nil, err
	}
	if len(inputs.coins) == 0 {
		return nil, i18n.NewError(ctx, msgs.MsgInvalidInputs, "withdraw", inputs.coins)
	}
	spent := big.NewInt(0).Sub(inputs.total, outputs.total)
	if spent.Cmp(params.Amount.Int()) != 0 {
		return nil, i18n.NewError(ctx, msgs.MsgInvalidAmount, "withdraw", params.Amount.Int().Text(10), spent.Text(10))
	}
	if err := h.noto.validateOwners(ctx, tx.Transaction.From, req.ResolvedVerifiers, inputs.coins, inputs.states); err != nil {
		return nil, err
	}
	if err := h.noto.validateOwners(ctx, tx.Transaction.From, req.ResolvedVerifiers, outputs.coins, outputs.states); err != nil {
		return nil, err
	}

	encodedTransfer, err := h.noto.encodeTransferUnmasked(ctx, tx.ContractAddress, inputs.coins, outputs.coins)
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

func (h *withdrawHandler) Prepare(ctx context.Context, tx *types.ParsedTransaction, req *prototk.PrepareTransactionRequest) (*prototk.PrepareTransactionResponse, error) {
	if h.noto.getChainIO().ChainKind() != "stellar" {
		return nil, i18n.NewError(ctx, msgs.MsgOperationChainOnly, "withdraw", "stellar")
	}
	params := tx.Params.(*types.WithdrawParams)

	recipientID, err := h.noto.findEthAddressVerifier(ctx, "recipient", params.Recipient, req.ResolvedVerifiers)
	if err != nil {
		return nil, err
	}
	txID, err := pldtypes.ParseBytes32Ctx(ctx, req.Transaction.TransactionId)
	if err != nil {
		return nil, err
	}
	inputStates := h.noto.filterSchema(req.InputStates, []string{h.noto.coinSchema.Id})
	inputs, err := parseBytes32List(ctx, endorsableStateIDs(ctx, inputStates, false))
	if err != nil {
		return nil, err
	}
	data, err := h.noto.encodeTransactionData(ctx, tx.DomainConfig, req.Transaction, req.InfoStates)
	if err != nil {
		return nil, err
	}

	contractID := tx.Transaction.ContractInfo.ContractAddress
	argsXDR, argsJSON, err := encodeSNotoWithdrawArgs(txID, recipientID.chainAddress.String(), params.Amount.Int(), inputs, data)
	if err != nil {
		return nil, err
	}

	return &prototk.PrepareTransactionResponse{
		ChainTransaction: &prototk.PreparedChainTransaction{
			Type: prototk.PreparedChainTransaction_PUBLIC,
			Payload: &prototk.PreparedChainTransaction_Soroban{
				Soroban: &prototk.SorobanInvoke{
					ContractId:   contractID,
					FunctionName: "withdraw",
					ArgsXdr:      argsXDR,
					ArgsJson:     argsJSON,
				},
			},
		},
	}, nil
}
