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

package fungible

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/domains/zeto/internal/msgs"
	"github.com/LFDT-Paladin/paladin/domains/zeto/internal/zeto/common"
	corepb "github.com/LFDT-Paladin/paladin/domains/zeto/pkg/proto"
	"github.com/LFDT-Paladin/paladin/domains/zeto/pkg/types"
	"github.com/LFDT-Paladin/paladin/domains/zeto/pkg/zetosigner/zetosignerapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/domain"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/plugintk"
	pb "github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/hyperledger/firefly-signer/pkg/abi"
	"google.golang.org/protobuf/proto"
)

var _ types.DomainHandler = &forcedTransferHandler{}

type forcedTransferHandler struct {
	baseHandler
	callbacks   plugintk.DomainCallbacks
	keyProvider common.AuthorityKeyProvider
}

var forcedTransferABI = &abi.Entry{
	Type: abi.Function,
	Name: types.METHOD_FORCED_TRANSFER,
	Inputs: abi.ParameterArray{
		{Name: "outputs", Type: "uint256[]"},
		{Name: "proof", Type: "tuple", InternalType: "struct Commonlib.Proof", Components: common.ProofComponents},
		{Name: "data", Type: "bytes"},
	},
}

var forcedTransferABI_enforced = &abi.Entry{
	Type: abi.Function,
	Name: types.METHOD_FORCED_TRANSFER,
	Inputs: abi.ParameterArray{
		{Name: "outputs", Type: "uint256[]"},
		{Name: "proof", Type: "bytes"},
		{Name: "data", Type: "bytes"},
	},
}

// Must match AENKNRECodec._decodeForcedTransfer
var enforcedForcedTransferProofABI = &abi.ParameterArray{
	{Name: "enforcementNullifiers", Type: "uint256[]"},
	{Name: "root", Type: "uint256"},
	{Name: "enabledInputs", Type: "uint256[]"},
	{Name: "encryptionNonce", Type: "uint256"},
	{Name: "ecdhPublicKey", Type: "uint256[2]"},
	{Name: "encryptedValuesForReceiver", Type: "uint256[]"},
	{Name: "encryptedValuesForArbiter", Type: "uint256[]"},
	{Name: "encryptedValuesForEnforcer", Type: "uint256[]"},
	{Name: "proof", Type: "tuple", InternalType: "struct Commonlib.Proof", Components: common.ProofComponents},
}

func NewForcedTransferHandler(name string, callbacks plugintk.DomainCallbacks, keyProvider common.AuthorityKeyProvider, coinSchema, merkleTreeRootSchema, merkleTreeNodeSchema, dataSchema *pb.StateSchema) *forcedTransferHandler {
	return &forcedTransferHandler{
		baseHandler: baseHandler{
			name: name,
			stateSchemas: &common.StateSchemas{
				CoinSchema:           coinSchema,
				MerkleTreeRootSchema: merkleTreeRootSchema,
				MerkleTreeNodeSchema: merkleTreeNodeSchema,
				DataSchema:           dataSchema,
			},
		},
		callbacks:   callbacks,
		keyProvider: keyProvider,
	}
}

func (h *forcedTransferHandler) ValidateParams(ctx context.Context, config *types.DomainInstanceConfig, params string) (interface{}, error) {
	var ftParams types.ForcedTransferParams
	if err := json.Unmarshal([]byte(params), &ftParams); err != nil {
		return nil, err
	}
	if ftParams.SeizedOwner == "" {
		return nil, i18n.NewError(ctx, msgs.MsgErrorNoSeizedOwner)
	}
	if err := validateTransferParams(ctx, ftParams.Transfers); err != nil {
		return nil, err
	}
	return &ftParams, nil
}

func (h *forcedTransferHandler) Init(ctx context.Context, tx *types.ParsedTransaction, req *pb.InitTransactionRequest) (*pb.InitTransactionResponse, error) {
	params := tx.Params.(*types.ForcedTransferParams)

	res := &pb.InitTransactionResponse{
		RequiredVerifiers: []*pb.ResolveVerifierRequest{
			// The enforcer (tx.Transaction.From = contract owner) signs the proof
			{
				Lookup:       tx.Transaction.From,
				Algorithm:    h.getAlgoZetoSnarkBJJ(),
				VerifierType: zetosignerapi.IDEN3_PUBKEY_BABYJUBJUB_COMPRESSED_0X,
			},
			// The seized owner — need their public key for the witness
			{
				Lookup:       params.SeizedOwner,
				Algorithm:    h.getAlgoZetoSnarkBJJ(),
				VerifierType: zetosignerapi.IDEN3_PUBKEY_BABYJUBJUB_COMPRESSED_0X,
			},
		},
	}
	for _, param := range params.Transfers {
		res.RequiredVerifiers = append(res.RequiredVerifiers, &pb.ResolveVerifierRequest{
			Lookup:       param.To,
			Algorithm:    h.getAlgoZetoSnarkBJJ(),
			VerifierType: zetosignerapi.IDEN3_PUBKEY_BABYJUBJUB_COMPRESSED_0X,
		})
	}
	return res, nil
}

func (h *forcedTransferHandler) Assemble(ctx context.Context, tx *types.ParsedTransaction, req *pb.AssembleTransactionRequest) (*pb.AssembleTransactionResponse, error) {
	params := tx.Params.(*types.ForcedTransferParams)

	resolvedSeizedOwner := domain.FindVerifier(params.SeizedOwner, h.getAlgoZetoSnarkBJJ(), zetosignerapi.IDEN3_PUBKEY_BABYJUBJUB_COMPRESSED_0X, req.ResolvedVerifiers)
	if resolvedSeizedOwner == nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorResolveVerifier, params.SeizedOwner)
	}

	useNullifiers := common.UseNullifierAvailability(tx.DomainConfig.TokenName)
	inputStates, expectedTotal, revert, err := prepareInputsForTransfer(ctx, h.callbacks, h.stateSchemas.CoinSchema, useNullifiers, req.StateQueryContext, resolvedSeizedOwner.Verifier, params.Transfers)
	if err != nil {
		if revert {
			message := err.Error()
			return &pb.AssembleTransactionResponse{
				AssemblyResult: pb.AssembleTransactionResponse_REVERT,
				RevertReason:   &message,
			}, nil
		}
		return nil, i18n.NewError(ctx, msgs.MsgErrorPrepTxInputs, err)
	}
	outputCoins, outputStates, err := prepareOutputsForTransfer(ctx, useNullifiers, params.Transfers, req.ResolvedVerifiers, h.stateSchemas.CoinSchema, h.name)
	if err != nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorPrepTxOutputs, err)
	}

	// Change goes back to the seized owner
	remainder := big.NewInt(0).Sub(inputStates.total, expectedTotal)
	if remainder.Sign() > 0 {
		remainderHex := pldtypes.HexUint256(*remainder)
		remainderParams := []*types.FungibleTransferParamEntry{
			{
				To:     params.SeizedOwner,
				Amount: &remainderHex,
			},
		}
		returnedCoins, returnedStates, err := prepareOutputsForTransfer(ctx, useNullifiers, remainderParams, req.ResolvedVerifiers, h.stateSchemas.CoinSchema, h.name)
		if err != nil {
			return nil, i18n.NewError(ctx, msgs.MsgErrorPrepTxChange, err)
		}
		outputCoins = append(outputCoins, returnedCoins...)
		outputStates = append(outputStates, returnedStates...)
	}

	infoStates := make([]*pb.NewState, 0, len(params.Transfers))
	for _, param := range params.Transfers {
		info, err := prepareTransactionInfoStates(ctx, param.Data, []string{tx.Transaction.From, param.To}, h.stateSchemas.DataSchema)
		if err != nil {
			return nil, err
		}
		infoStates = append(infoStates, info...)
	}

	contractAddress, err := pldtypes.ParseEthAddress(req.Transaction.ContractInfo.ContractAddress)
	if err != nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorDecodeContractAddress, err)
	}

	circuit := (*tx.DomainConfig.Circuits)[types.METHOD_FORCED_TRANSFER]
	payloadBytes, err := h.formatProvingRequest(ctx, inputStates.coins, outputCoins, circuit, tx.DomainConfig.TokenName, req.StateQueryContext, contractAddress)
	if err != nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorFormatProvingReq, err)
	}

	return &pb.AssembleTransactionResponse{
		AssemblyResult: pb.AssembleTransactionResponse_OK,
		AssembledTransaction: &pb.AssembledTransaction{
			InputStates:  inputStates.states,
			OutputStates: outputStates,
			InfoStates:   infoStates,
		},
		AttestationPlan: []*pb.AttestationRequest{
			{
				Name:            "sender",
				AttestationType: pb.AttestationType_SIGN,
				Algorithm:       h.getAlgoZetoSnarkBJJ(),
				VerifierType:    zetosignerapi.IDEN3_PUBKEY_BABYJUBJUB_COMPRESSED_0X,
				PayloadType:     zetosignerapi.PAYLOAD_DOMAIN_ZETO_SNARK,
				Payload:         payloadBytes,
				Parties:         []string{tx.Transaction.From},
			},
		},
	}, nil
}

func (h *forcedTransferHandler) Endorse(ctx context.Context, tx *types.ParsedTransaction, req *pb.EndorseTransactionRequest) (*pb.EndorseTransactionResponse, error) {
	return nil, nil
}

func (h *forcedTransferHandler) Prepare(ctx context.Context, tx *types.ParsedTransaction, req *pb.PrepareTransactionRequest) (*pb.PrepareTransactionResponse, error) {
	var proofRes corepb.ProvingResponse
	result := domain.FindAttestation("sender", req.AttestationResult)
	if result == nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorFindSenderAttestation)
	}
	if err := proto.Unmarshal(result.Payload, &proofRes); err != nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorUnmarshalProvingRes, err)
	}

	inputSize := common.GetInputSize(len(req.InputStates))
	outputs, err := utxosFromOutputStates(ctx, req.OutputStates, inputSize)
	if err != nil {
		return nil, err
	}

	// Encode input state IDs (commitment hashes) into the transaction data so all
	// nodes can mark the seized UTXOs as spent when processing the event.
	spentCommitments := make([]pldtypes.HexUint256, 0, len(req.InputStates))
	for _, state := range req.InputStates {
		parsed, err := pldtypes.ParseHexUint256(ctx, state.Id)
		if err == nil {
			spentCommitments = append(spentCommitments, *parsed)
		}
	}
	data, err := common.EncodeTransactionDataWithSpentCommitments(ctx, req.Transaction, req.InfoStates, spentCommitments)
	if err != nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorEncodeTxData, err)
	}

	params := map[string]any{
		"outputs": outputs,
		"data":    data,
	}
	ftFunction := forcedTransferABI
	if common.IsEnforcedToken(tx.DomainConfig.TokenName) {
		ftFunction = forcedTransferABI_enforced
		// enabledInputs: 1 for real inputs, 0 for fillers
		enabledInputs := make([]string, inputSize)
		for i := 0; i < inputSize; i++ {
			if i < len(req.InputStates) {
				enabledInputs[i] = "1"
			} else {
				enabledInputs[i] = "0"
			}
		}
		proofBytes, err := common.EncodeEnforcedProof(ctx, &proofRes, enforcedForcedTransferProofABI, map[string]interface{}{
			"enabledInputs": enabledInputs,
		})
		if err != nil {
			return nil, err
		}
		params["proof"] = "0x" + hex.EncodeToString(proofBytes)
	} else {
		params["proof"] = common.EncodeProof(proofRes.Proof)
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorMarshalPrepedParams, err)
	}
	functionJSON, err := json.Marshal(ftFunction)
	if err != nil {
		return nil, err
	}

	// onlyOwner: the contract owner must sign the on-chain transaction
	signer := req.Transaction.From
	return &pb.PrepareTransactionResponse{
		Transaction: &pb.PreparedTransaction{
			FunctionAbiJson: string(functionJSON),
			ParamsJson:      string(paramsJSON),
			RequiredSigner:  &signer,
		},
	}, nil
}

// formatProvingRequest builds a ProvingRequest with NonRepudiationEnforced extras.
func (h *forcedTransferHandler) formatProvingRequest(ctx context.Context, inputCoins, outputCoins []*types.ZetoCoin, circuit *zetosignerapi.Circuit, tokenName, stateQueryContext string, contractAddress *pldtypes.EthAddress) ([]byte, error) {
	inputSize := common.GetInputSize(len(inputCoins))
	inputCommitments := make([]string, inputSize)
	inputValueInts := make([]uint64, inputSize)
	inputSalts := make([]string, inputSize)
	inputOwner := inputCoins[0].Owner.String()
	for i := 0; i < inputSize; i++ {
		if i < len(inputCoins) {
			coin := inputCoins[i]
			hash, err := coin.Hash(ctx)
			if err != nil {
				return nil, i18n.NewError(ctx, msgs.MsgErrorHashInputState, err)
			}
			inputCommitments[i] = hash.Int().Text(16)
			inputValueInts[i] = coin.Amount.Int().Uint64()
			inputSalts[i] = coin.Salt.Int().Text(16)
		} else {
			inputCommitments[i] = "0"
			inputSalts[i] = "0"
		}
	}

	outputValueInts := make([]uint64, inputSize)
	outputSalts := make([]string, inputSize)
	outputOwners := make([]string, inputSize)
	for i := 0; i < inputSize; i++ {
		if i < len(outputCoins) {
			coin := outputCoins[i]
			outputValueInts[i] = coin.Amount.Int().Uint64()
			outputSalts[i] = coin.Salt.Int().Text(16)
			outputOwners[i] = coin.Owner.String()
		} else {
			outputSalts[i] = "0"
		}
	}

	// UTXO SMT proof
	smtUtxoProof, err := smtProofForInputs(ctx, h.callbacks, h.stateSchemas.MerkleTreeRootSchema, h.stateSchemas.MerkleTreeNodeSchema, tokenName, stateQueryContext, contractAddress, inputCoins, false, inputSize)
	if err != nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorGenerateMTP, err)
	}

	// KYC SMT proof (seized owner + output owners)
	smtKycProof, err := smtProofForOwners(ctx, h.callbacks, h.stateSchemas.MerkleTreeRootSchema, h.stateSchemas.MerkleTreeNodeSchema, tokenName, stateQueryContext, contractAddress, inputOwner, outputCoins, inputSize+1)
	if err != nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorGenerateMTP, err)
	}

	// Compliance SMT proof: build a fresh tree with seized owner FROZEN, others ACTIVE.
	// The forced transfer circuit verifies the seized owner has FROZEN status (2).
	identities := h.keyProvider.GetComplianceIdentities(contractAddress.String())
	smtComplianceProof, err := smtProofForForcedTransferCompliance(ctx, identities, inputOwner, inputOwner, outputCoins, inputSize+1)
	if err != nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorGenerateMTP, err)
	}

	extrasObj := &corepb.ProvingRequestExtras_NonRepudiationEnforced{
		SmtUtxoProof:       smtUtxoProof,
		SmtKycProof:        smtKycProof,
		SmtComplianceProof: smtComplianceProof,
	}
	if keys := h.keyProvider.GetAuthorityKeys(contractAddress.String()); keys != nil {
		extrasObj.ArbiterPublicKey = keys.ArbiterPublicKey[:]
		extrasObj.EnforcerPublicKey = keys.EnforcerPublicKey[:]
	}
	extras, err := proto.Marshal(extrasObj)
	if err != nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorMarshalExtraObj, err)
	}

	tokenSecrets, err := marshalTokenSecrets(inputValueInts, outputValueInts)
	if err != nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorMarshalValuesFungible, err)
	}

	payload := &corepb.ProvingRequest{
		Circuit: circuit.ToProto(),
		Common: &corepb.ProvingRequestCommon{
			InputCommitments: inputCommitments,
			InputSalts:       inputSalts,
			InputOwner:       inputOwner,
			OutputSalts:      outputSalts,
			OutputOwners:     outputOwners,
			TokenSecrets:     tokenSecrets,
			TokenType:        corepb.TokenType_fungible,
		},
		Extras: extras,
	}
	return proto.Marshal(payload)
}
