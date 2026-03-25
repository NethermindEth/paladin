package fungible

import (
	"context"
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
	callbacks plugintk.DomainCallbacks
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

func NewForcedTransferHandler(name string, callbacks plugintk.DomainCallbacks, coinSchema, merkleTreeRootSchema, merkleTreeNodeSchema, dataSchema *pb.StateSchema) *forcedTransferHandler {
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
		callbacks: callbacks,
	}
}

func (h *forcedTransferHandler) ValidateParams(ctx context.Context, config *types.DomainInstanceConfig, params string) (interface{}, error) {
	var ftParams types.ForcedTransferParams
	if err := json.Unmarshal([]byte(params), &ftParams); err != nil {
		return nil, err
	}
	if ftParams.SeizedOwner == "" {
		return nil, i18n.NewError(ctx, msgs.MsgErrorNoTransferParams)
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

	useNullifiers := common.IsNullifiersToken(tx.DomainConfig.TokenName)
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

	data, err := common.EncodeTransactionData(ctx, req.Transaction, req.InfoStates)
	if err != nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorEncodeTxData, err)
	}

	params := map[string]any{
		"outputs": outputs,
		"proof":   common.EncodeProof(proofRes.Proof),
		"data":    data,
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorMarshalPrepedParams, err)
	}
	functionJSON, err := json.Marshal(forcedTransferABI)
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
// TODO(P11): compliance SMT proof generation — requires a compliance tree type in common.MerkleTreeType
//            and a corresponding smt.MerkleTreeNameForComplianceStates helper.
// TODO(P11): arbiter/enforcer public key retrieval — requires reading on-chain contract state
//            (arbiterPublicKey() and enforcerPublicKey() view calls).
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

	// TODO(P11): generate compliance SMT proof from on-chain compliance tree.
	// For now, use an empty proof — integration tests will provide the real implementation.
	smtComplianceProof := &corepb.MerkleProofObject{
		Root: "0",
	}

	// TODO(P11): fetch arbiter and enforcer public keys from on-chain contract state.
	// These are set via setArbiter()/setEnforcer() and exposed as view functions.
	arbiterPublicKey := []string{"0", "0"}
	enforcerPublicKey := []string{"0", "0"}

	extrasObj := &corepb.ProvingRequestExtras_NonRepudiationEnforced{
		SmtUtxoProof:       smtUtxoProof,
		SmtKycProof:        smtKycProof,
		SmtComplianceProof: smtComplianceProof,
		ArbiterPublicKey:   arbiterPublicKey,
		EnforcerPublicKey:  enforcerPublicKey,
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