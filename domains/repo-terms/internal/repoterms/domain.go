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

// Package repoterms implements the "repo-terms" Paladin domain plugin (book chapter 18): two banks
// (bank_a, bank_b) bilaterally agree on private repo-trade terms (rate_bps, maturity_ledger,
// haircut_bps) such that the real values are visible only to the two banks via Paladin's own
// private-state distribution - the public chain only ever sees an opaque 32-byte state ID. This
// mirrors domains/noto's own SNoto lock-info state-ID-echo pattern
// (domains/noto/internal/noto/handler_lock.go), but is drastically simpler: one output state, no
// inputs, one transaction type ("setTerms"), no amend/update path, Stellar-only (no chainIO
// abstraction - the three Stellar signing/verifier constants are hardcoded directly), and no
// nullifiers/merkle trees/coin-selection/lock lifecycle of any kind.
package repoterms

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/log"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/plugintk"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/hyperledger/firefly-signer/pkg/abi"
)

// RepoTerms is the domain plugin implementation, wired up by repo-terms.go's cgo entrypoint. It
// implements every plugintk.DomainAPI method directly (mirroring domains/noto's own Noto struct -
// no DomainAPIBase embedding), even though most of them are trivial stubs for functionality this
// domain doesn't use (calls, privacy groups, nullifiers, nested EVM transactions).
type RepoTerms struct {
	Callbacks plugintk.DomainCallbacks

	name                 string
	config               DomainConfig
	registryAddress      string
	networkPassphrase    string
	fixedSigningIdentity string
	repoTermsSchema      *prototk.StateSchema
}

func mustParseJSON[T any](obj T) string {
	parsed, err := json.Marshal(obj)
	if err != nil {
		panic(err)
	}
	return string(parsed)
}

func (r *RepoTerms) ConfigureDomain(ctx context.Context, req *prototk.ConfigureDomainRequest) (*prototk.ConfigureDomainResponse, error) {
	var config DomainConfig
	if err := json.Unmarshal([]byte(req.ConfigJson), &config); err != nil {
		return nil, err
	}
	// repo-terms is Stellar-only (no EVM/Solidity ABI concept at all) - require it explicitly
	// rather than branching, unlike Noto's chain-kind switch (which still supports EVM).
	if req.ChainInfo == nil || req.ChainInfo.ChainKind != "stellar" {
		return nil, fmt.Errorf("repo-terms is a Stellar-only domain, got chain kind %q", chainKindOf(req))
	}

	r.name = req.Name
	r.config = config
	r.registryAddress = req.RegistryContractAddress
	r.networkPassphrase = req.ChainInfo.NetworkId
	r.fixedSigningIdentity = req.FixedSigningIdentity

	return &prototk.ConfigureDomainResponse{
		DomainConfig: &prototk.DomainConfig{
			AbiStateSchemasJson: []string{mustParseJSON(RepoTermsV1ABI)},
			AbiEventsJson:       stellarEventsJSON,
			SigningAlgorithms:   map[string]int32{},
		},
		SupportedChainKinds: []string{"stellar"},
	}, nil
}

func chainKindOf(req *prototk.ConfigureDomainRequest) string {
	if req.ChainInfo == nil {
		return ""
	}
	return req.ChainInfo.ChainKind
}

func (r *RepoTerms) InitDomain(ctx context.Context, req *prototk.InitDomainRequest) (*prototk.InitDomainResponse, error) {
	if len(req.AbiStateSchemas) != 1 {
		return nil, fmt.Errorf("expected exactly one state schema, got %d", len(req.AbiStateSchemas))
	}
	r.repoTermsSchema = req.AbiStateSchemas[0]
	return &prototk.InitDomainResponse{}, nil
}

func (r *RepoTerms) InitContract(ctx context.Context, req *prototk.InitContractRequest) (*prototk.InitContractResponse, error) {
	ctx = log.WithComponent(ctx, "repoterms")
	ctx = log.WithLogField(ctx, "contract", req.ContractAddress)

	parsedConfig, err := decodeStellarConfig(ctx, req.ContractConfig)
	if err != nil {
		// This on-chain contract has invalid configuration - not an error in our process.
		log.L(ctx).Errorf("Error decoding config: %s", err)
		return &prototk.InitContractResponse{Valid: false}, nil
	}

	contractConfigJSON, err := json.Marshal(parsedConfig)
	if err != nil {
		return nil, err
	}

	return &prototk.InitContractResponse{
		Valid: true,
		ContractConfig: &prototk.ContractConfig{
			ContractConfigJson: string(contractConfigJSON),
			// Bilateral, not single-notary: both banks must independently endorse (see
			// handler.go's Assemble AttestationPlan, Threshold left unset so it defaults to
			// len(Parties)==2) - mirrors the only existing precedent for this shape,
			// core/go/noderuntests/pkg/domains/simple_domain.go's own PrivacyGroupEndorsement case.
			CoordinatorSelection:          prototk.ContractConfig_COORDINATOR_ENDORSER,
			SubmitterSelection:            prototk.ContractConfig_SUBMITTER_COORDINATOR,
			CoordinatorEndorserCandidates: []string{parsedConfig.BankALookup, parsedConfig.BankBLookup},
		},
	}, nil
}

// validateTransactionAndGetLogContext parses a TransactionSpecification into a ParsedTransaction -
// the repo-terms counterpart to domains/noto's own validateTransactionCommon, drastically
// simplified: there is exactly one function ("setTerms"), so no per-method handler lookup/dispatch
// table is needed at all.
func (r *RepoTerms) validateTransactionAndGetLogContext(ctx context.Context, tx *prototk.TransactionSpecification) (context.Context, *ParsedTransaction, error) {
	ctx = log.WithComponent(ctx, "repoterms")

	var functionABI abi.Entry
	if err := json.Unmarshal([]byte(tx.FunctionAbiJson), &functionABI); err != nil {
		return ctx, nil, err
	}
	if functionABI.Name != "setTerms" {
		return ctx, nil, fmt.Errorf("unknown function %q", functionABI.Name)
	}

	var domainConfig RepoTermsParsedConfig
	if err := json.Unmarshal([]byte(tx.ContractInfo.ContractConfigJson), &domainConfig); err != nil {
		return ctx, nil, err
	}

	var params SetTermsParams
	if err := json.Unmarshal([]byte(tx.FunctionParamsJson), &params); err != nil {
		return ctx, nil, err
	}

	contractAddress, err := parseContractAddress(tx.ContractInfo.ContractAddress)
	if err != nil {
		return ctx, nil, err
	}

	ctx = log.WithLogField(ctx, "tx", tx.TransactionId)
	ctx = log.WithLogField(ctx, "contract", tx.ContractInfo.ContractAddress)

	return ctx, &ParsedTransaction{
		Transaction:     tx,
		FunctionABI:     &functionABI,
		ContractAddress: contractAddress,
		DomainConfig:    &domainConfig,
		Params:          &params,
	}, nil
}

func (r *RepoTerms) InitTransaction(ctx context.Context, req *prototk.InitTransactionRequest) (*prototk.InitTransactionResponse, error) {
	ctx, tx, err := r.validateTransactionAndGetLogContext(ctx, req.Transaction)
	if err != nil {
		return nil, err
	}
	return r.setTermsHandler().Init(ctx, tx, req)
}

func (r *RepoTerms) AssembleTransaction(ctx context.Context, req *prototk.AssembleTransactionRequest) (*prototk.AssembleTransactionResponse, error) {
	ctx, tx, err := r.validateTransactionAndGetLogContext(ctx, req.Transaction)
	if err != nil {
		return nil, err
	}
	return r.setTermsHandler().Assemble(ctx, tx, req)
}

func (r *RepoTerms) EndorseTransaction(ctx context.Context, req *prototk.EndorseTransactionRequest) (*prototk.EndorseTransactionResponse, error) {
	ctx, tx, err := r.validateTransactionAndGetLogContext(ctx, req.Transaction)
	if err != nil {
		return nil, err
	}
	return r.setTermsHandler().Endorse(ctx, tx, req)
}

func (r *RepoTerms) PrepareTransaction(ctx context.Context, req *prototk.PrepareTransactionRequest) (*prototk.PrepareTransactionResponse, error) {
	ctx, tx, err := r.validateTransactionAndGetLogContext(ctx, req.Transaction)
	if err != nil {
		return nil, err
	}
	return r.setTermsHandler().Prepare(ctx, tx, req)
}

// allocateStateIDs sends newly-assembled states to Paladin to validate and generate their real
// state IDs, then writes those IDs back onto the very same objects - mirrors
// domains/noto/internal/noto/states.go's own allocateStateIDs exactly (copied rather than
// generalized/shared, since these are two independent domain-plugin binaries).
func (r *RepoTerms) allocateStateIDs(ctx context.Context, stateQueryContext string, states []*prototk.NewState) error {
	validated, err := r.Callbacks.ValidateStates(ctx, &prototk.ValidateStatesRequest{
		StateQueryContext: stateQueryContext,
		States:            states,
	})
	if err != nil {
		return err
	}
	for i, vs := range validated.States {
		generatedID := vs.Id
		states[i].Id = &generatedID
	}
	return nil
}

// The following are trivial stubs for plugintk.DomainAPI methods this domain never exercises -
// mirrors domains/noto/internal/noto/noto.go's own stub implementations for the same methods
// (Sign/GetVerifier are meaningful there only because of Noto's nullifier feature, which repo-terms
// has no equivalent of at all).

func (r *RepoTerms) Sign(ctx context.Context, req *prototk.SignRequest) (*prototk.SignResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r *RepoTerms) GetVerifier(ctx context.Context, req *prototk.GetVerifierRequest) (*prototk.GetVerifierResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r *RepoTerms) ValidateStateHashes(ctx context.Context, req *prototk.ValidateStateHashesRequest) (*prototk.ValidateStateHashesResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r *RepoTerms) InitCall(ctx context.Context, req *prototk.InitCallRequest) (*prototk.InitCallResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r *RepoTerms) ExecCall(ctx context.Context, req *prototk.ExecCallRequest) (*prototk.ExecCallResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r *RepoTerms) ConfigurePrivacyGroup(ctx context.Context, req *prototk.ConfigurePrivacyGroupRequest) (*prototk.ConfigurePrivacyGroupResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r *RepoTerms) InitPrivacyGroup(ctx context.Context, req *prototk.InitPrivacyGroupRequest) (*prototk.InitPrivacyGroupResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r *RepoTerms) WrapPrivacyGroupEVMTX(ctx context.Context, req *prototk.WrapPrivacyGroupEVMTXRequest) (*prototk.WrapPrivacyGroupEVMTXResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r *RepoTerms) IsBaseLedgerRevertRetryable(ctx context.Context, req *prototk.IsBaseLedgerRevertRetryableRequest) (*prototk.IsBaseLedgerRevertRetryableResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (r *RepoTerms) InvokeRPC(ctx context.Context, req *prototk.InvokeRPCRequest) (*prototk.InvokeRPCResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

// CheckStateCompletion is exercised by the framework when it detects states it doesn't yet have
// locally for a transaction (domains/noto's own manifest-based recovery mechanism). repo-terms has
// no manifest/info-state concept at all (a single output state per transaction, never chained), so
// there's nothing to recover beyond echoing back the framework's own pre-calculated
// FirstUnavailableId - mirrors the tail of domains/noto/internal/noto/noto.go's own
// CheckStateCompletion for the "no manifest available" case.
func (r *RepoTerms) CheckStateCompletion(ctx context.Context, req *prototk.CheckStateCompletionRequest) (*prototk.CheckStateCompletionResponse, error) {
	res := &prototk.CheckStateCompletionResponse{}
	if req.UnavailableStates != nil {
		res.NextMissingStateId = req.UnavailableStates.FirstUnavailableId
	}
	return res, nil
}
