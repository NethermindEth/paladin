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

package repoterms

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/algorithms"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/domain"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/signpayloads"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/verifiers"
)

// setTermsHandler implements the one and only transaction type this domain supports ("setTerms").
// Unlike domains/noto's own GetHandler dispatch table (a dozen-plus methods), there is exactly one
// handler here, so RepoTerms.validateTransactionAndGetLogContext (domain.go) calls it directly -
// no method-name switch is needed at all.
type setTermsHandler struct {
	rt *RepoTerms
}

func (r *RepoTerms) setTermsHandler() *setTermsHandler {
	return &setTermsHandler{rt: r}
}

// Init resolves the verifiers Assemble/Endorse will need: the sender (who signs), and both banks
// (who bilaterally endorse) - mirrors domains/noto/internal/noto/handler_lock.go's own Init.
func (h *setTermsHandler) Init(ctx context.Context, tx *ParsedTransaction, req *prototk.InitTransactionRequest) (*prototk.InitTransactionResponse, error) {
	return &prototk.InitTransactionResponse{
		RequiredVerifiers: []*prototk.ResolveVerifierRequest{
			{
				Lookup:       tx.Transaction.From,
				Algorithm:    algorithms.EDDSA_ED25519,
				VerifierType: verifiers.STELLAR_ADDRESS,
			},
			{
				Lookup:       tx.DomainConfig.BankALookup,
				Algorithm:    algorithms.EDDSA_ED25519,
				VerifierType: verifiers.STELLAR_ADDRESS,
			},
			{
				Lookup:       tx.DomainConfig.BankBLookup,
				Algorithm:    algorithms.EDDSA_ED25519,
				VerifierType: verifiers.STELLAR_ADDRESS,
			},
		},
	}, nil
}

// Assemble builds the single RepoTermsV1 output state (no inputs, no locked coins, no manifest -
// unlike domains/noto/internal/noto/handler_lock.go's own Assemble, which this mirrors in shape
// only), distributed off-chain to exactly the two banks via DistributionList, then builds the
// two-attestation plan: the sender signs the terms, and both banks bilaterally endorse them
// (Threshold left unset so it defaults to len(Parties)==2 - both required, per
// core/go/noderuntests/pkg/domains/simple_domain.go's own PrivacyGroupEndorsement precedent).
func (h *setTermsHandler) Assemble(ctx context.Context, tx *ParsedTransaction, req *prototk.AssembleTransactionRequest) (*prototk.AssembleTransactionResponse, error) {
	params, ok := tx.Params.(*SetTermsParams)
	if !ok {
		return nil, fmt.Errorf("internal error: unexpected params type %T", tx.Params)
	}

	terms := &RepoTermsV1{
		Salt:           pldtypes.RandBytes(32),
		BankA:          tx.DomainConfig.BankALookup,
		BankB:          tx.DomainConfig.BankBLookup,
		RateBps:        params.RateBps,
		MaturityLedger: params.MaturityLedger,
		HaircutBps:     params.HaircutBps,
	}
	stateDataJSON, err := json.Marshal(terms)
	if err != nil {
		return nil, err
	}

	newState := &prototk.NewState{
		SchemaId:         h.rt.repoTermsSchema.Id,
		StateDataJson:    string(stateDataJSON),
		DistributionList: []string{tx.DomainConfig.BankALookup, tx.DomainConfig.BankBLookup},
	}

	// Ask Paladin to validate the new state and allocate its real (final) state ID - the same
	// opaque ID that will be echoed on-chain by set_terms and confirmed back via HandleEventBatch.
	if err := h.rt.allocateStateIDs(ctx, req.StateQueryContext, []*prototk.NewState{newState}); err != nil {
		return nil, err
	}

	txID, err := pldtypes.ParseBytes32Ctx(ctx, req.Transaction.TransactionId)
	if err != nil {
		return nil, err
	}
	termsStateID, err := pldtypes.ParseBytes32Ctx(ctx, *newState.Id)
	if err != nil {
		return nil, err
	}
	signPayload, err := h.rt.encodeSetTermsSignPayload(ctx, tx.ContractAddress, txID, termsStateID)
	if err != nil {
		return nil, err
	}

	return &prototk.AssembleTransactionResponse{
		AssemblyResult: prototk.AssembleTransactionResponse_OK,
		AssembledTransaction: &prototk.AssembledTransaction{
			OutputStates: []*prototk.NewState{newState},
		},
		AttestationPlan: []*prototk.AttestationRequest{
			{
				Name:            "sender",
				AttestationType: prototk.AttestationType_SIGN,
				Algorithm:       algorithms.EDDSA_ED25519,
				VerifierType:    verifiers.STELLAR_ADDRESS,
				PayloadType:     signpayloads.OPAQUE_TO_EDDSA,
				Payload:         signPayload,
				Parties:         []string{req.Transaction.From},
			},
			{
				Name:            "bilateral",
				AttestationType: prototk.AttestationType_ENDORSE,
				Algorithm:       algorithms.EDDSA_ED25519,
				VerifierType:    verifiers.STELLAR_ADDRESS,
				PayloadType:     signpayloads.OPAQUE_TO_EDDSA,
				Parties:         []string{tx.DomainConfig.BankALookup, tx.DomainConfig.BankBLookup},
				// Threshold intentionally left nil/unset - defaults to len(Parties) == 2 (both
				// banks must endorse), per toolkit/proto/protos/to_domain.proto's own
				// AttestationRequest.threshold field comment.
			},
		},
	}, nil
}

// Endorse sanity-checks the assembled output state, then signs over the identical
// SALADIN_TYPED_DATA_V0 payload the sender itself signed (mirrors
// core/go/noderuntests/pkg/domains/simple_domain.go's own PrivacyGroupEndorsement case, which
// returns EndorsementResult_SIGN for its endorsers too - NOT Noto's single-notary
// ENDORSER_SUBMIT shortcut, which doesn't apply to a bilateral multi-party endorsement set).
func (h *setTermsHandler) Endorse(ctx context.Context, tx *ParsedTransaction, req *prototk.EndorseTransactionRequest) (*prototk.EndorseTransactionResponse, error) {
	if len(req.Outputs) != 1 {
		return nil, fmt.Errorf("expected exactly one output state, got %d", len(req.Outputs))
	}

	var terms RepoTermsV1
	if err := json.Unmarshal([]byte(req.Outputs[0].StateDataJson), &terms); err != nil {
		return nil, fmt.Errorf("invalid output state: %w", err)
	}
	if terms.BankA == "" || terms.BankA != tx.DomainConfig.BankALookup {
		return nil, fmt.Errorf("output state bankA %q does not match contract config bankA %q", terms.BankA, tx.DomainConfig.BankALookup)
	}
	if terms.BankB == "" || terms.BankB != tx.DomainConfig.BankBLookup {
		return nil, fmt.Errorf("output state bankB %q does not match contract config bankB %q", terms.BankB, tx.DomainConfig.BankBLookup)
	}

	txID, err := pldtypes.ParseBytes32Ctx(ctx, tx.Transaction.TransactionId)
	if err != nil {
		return nil, err
	}
	termsStateID, err := pldtypes.ParseBytes32Ctx(ctx, req.Outputs[0].Id)
	if err != nil {
		return nil, err
	}
	signPayload, err := h.rt.encodeSetTermsSignPayload(ctx, tx.ContractAddress, txID, termsStateID)
	if err != nil {
		return nil, err
	}

	return &prototk.EndorseTransactionResponse{
		EndorsementResult: prototk.EndorseTransactionResponse_SIGN,
		Payload:           signPayload,
	}, nil
}

// Prepare builds the real Soroban invoke against the genuine deployed instance address
// (tx.Transaction.ContractInfo.ContractAddress - NOT placeholderContractID's off-chain-only
// stand-in, mirroring handler_lock.go's own stellarBaseLedgerInvokeLock's identical comment) -
// calling repo-terms's real `set_terms(tx_id, terms_state_id)`.
func (h *setTermsHandler) Prepare(ctx context.Context, tx *ParsedTransaction, req *prototk.PrepareTransactionRequest) (*prototk.PrepareTransactionResponse, error) {
	sender := domain.FindAttestation("sender", req.AttestationResult)
	if sender == nil {
		return nil, fmt.Errorf("sender attestation not found")
	}
	if len(req.OutputStates) != 1 {
		return nil, fmt.Errorf("expected exactly one output state, got %d", len(req.OutputStates))
	}

	txID, err := pldtypes.ParseBytes32Ctx(ctx, req.Transaction.TransactionId)
	if err != nil {
		return nil, err
	}
	// req.OutputStates here are []*prototk.EndorsableState, whose Id is a plain string (already
	// confirmed/final by Prepare time), unlike AssembledTransaction.OutputStates' *string.
	termsStateID := pldtypes.MustParseBytes32(req.OutputStates[0].Id)

	argsXDR, argsJSON, err := encodeRepoTermsSetTermsArgs(txID, termsStateID)
	if err != nil {
		return nil, err
	}

	return &prototk.PrepareTransactionResponse{
		ChainTransaction: &prototk.PreparedChainTransaction{
			Type: prototk.PreparedChainTransaction_PUBLIC,
			Payload: &prototk.PreparedChainTransaction_Soroban{
				Soroban: &prototk.SorobanInvoke{
					ContractId:   tx.Transaction.ContractInfo.ContractAddress,
					FunctionName: "set_terms",
					ArgsXdr:      argsXDR,
					ArgsJson:     argsJSON,
				},
			},
		},
	}, nil
}
