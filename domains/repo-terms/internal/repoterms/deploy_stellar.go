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
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/verifiers"
	"github.com/google/uuid"
)

func validateDeploy(tx *prototk.DeployTransactionSpecification) (*ConstructorParams, error) {
	var params ConstructorParams
	if err := json.Unmarshal([]byte(tx.ConstructorParamsJson), &params); err != nil {
		return nil, err
	}
	if params.BankA == "" {
		return nil, fmt.Errorf("bankA is required")
	}
	if params.BankB == "" {
		return nil, fmt.Errorf("bankB is required")
	}
	return &params, nil
}

// InitDeploy resolves both banks' verifiers - the two Paladin identity locators supplied in
// ConstructorParams ({"bankA": "...", "bankB": "..."}), NOT addresses - mirrors
// domains/noto/internal/noto/noto.go's own InitDeploy resolving "notary".
func (r *RepoTerms) InitDeploy(ctx context.Context, req *prototk.InitDeployRequest) (*prototk.InitDeployResponse, error) {
	params, err := validateDeploy(req.Transaction)
	if err != nil {
		return nil, err
	}
	return &prototk.InitDeployResponse{
		RequiredVerifiers: []*prototk.ResolveVerifierRequest{
			{
				Lookup:       params.BankA,
				Algorithm:    algorithms.EDDSA_ED25519,
				VerifierType: verifiers.STELLAR_ADDRESS,
			},
			{
				Lookup:       params.BankB,
				Algorithm:    algorithms.EDDSA_ED25519,
				VerifierType: verifiers.STELLAR_ADDRESS,
			},
		},
	}, nil
}

// PrepareDeploy builds a real PreparedChainTransaction.soroban (SorobanInvoke) targeting the
// domain's configured repo-terms-factory instance's `deploy` function
// (soroban/contracts/repo-terms-factory) - the repo-terms counterpart to
// domains/noto/internal/noto/deploy_stellar.go's own stellarPrepareDeploy, simplified: no
// config/sac args (repo-terms-factory's own deploy signature has neither - see
// encodeRepoTermsFactoryDeployArgs' own doc comment), and always Stellar (no EVM branch at all).
func (r *RepoTerms) PrepareDeploy(ctx context.Context, req *prototk.PrepareDeployRequest) (*prototk.PrepareDeployResponse, error) {
	params, err := validateDeploy(req.Transaction)
	if err != nil {
		return nil, err
	}
	if r.config.StellarRepoTermsFactoryAddress == "" {
		return nil, fmt.Errorf("stellarRepoTermsFactoryAddress is required")
	}
	if r.config.StellarRepoTermsWasmHash == "" {
		return nil, fmt.Errorf("stellarRepoTermsWasmHash is required")
	}
	if r.registryAddress == "" {
		return nil, fmt.Errorf("registryContractAddress is required")
	}

	bankAVerifier := domain.FindVerifier(params.BankA, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, req.ResolvedVerifiers)
	if bankAVerifier == nil {
		return nil, fmt.Errorf("failed to resolve verifier for bankA %q", params.BankA)
	}
	bankBVerifier := domain.FindVerifier(params.BankB, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, req.ResolvedVerifiers)
	if bankBVerifier == nil {
		return nil, fmt.Errorf("failed to resolve verifier for bankB %q", params.BankB)
	}

	localNodeName, err := r.Callbacks.LocalNodeName(ctx, &prototk.LocalNodeNameRequest{})
	if err != nil {
		return nil, err
	}
	bankAQualified, err := pldtypes.PrivateIdentityLocator(params.BankA).FullyQualified(ctx, localNodeName.Name)
	if err != nil {
		return nil, err
	}
	bankBQualified, err := pldtypes.PrivateIdentityLocator(params.BankB).FullyQualified(ctx, localNodeName.Name)
	if err != nil {
		return nil, err
	}
	// The combined "bankA@node|bankB@node" string repo-terms-factory's own `deploy` just passes
	// through untouched to SaladinFactory::register - see that factory's own doc comment. Split
	// back apart by this plugin's own decodeStellarConfig (chainio_stellar.go).
	identityLookup := bankAQualified.String() + "|" + bankBQualified.String()

	txID, err := pldtypes.ParseBytes32Ctx(ctx, req.Transaction.TransactionId)
	if err != nil {
		return nil, err
	}
	wasmHash, err := pldtypes.ParseBytes32Ctx(ctx, r.config.StellarRepoTermsWasmHash)
	if err != nil {
		return nil, err
	}

	// saladin_factory is the domain's own registry (RegistryContractAddress, ConfigureDomain) -
	// the per-domain SaladinFactory instance domainmgr's event-stream trusts - NOT
	// StellarRepoTermsFactoryAddress (that's the contract being invoked below, a different
	// instance).
	argsXDR, argsJSON, err := encodeRepoTermsFactoryDeployArgs(wasmHash, bankAVerifier.Verifier, bankBVerifier.Verifier, r.registryAddress, txID, identityLookup)
	if err != nil {
		return nil, err
	}

	signer := r.fixedSigningIdentity
	if signer == "" {
		// Use a random key to deploy if no default signing identity is set - mirrors
		// domains/noto/internal/noto/deploy_stellar.go's own stellarPrepareDeploy.
		signer = fmt.Sprintf("%s.deploy.%s", r.name, uuid.New())
	}

	return &prototk.PrepareDeployResponse{
		ChainTransaction: &prototk.PreparedChainTransaction{
			Type: prototk.PreparedChainTransaction_PUBLIC,
			Payload: &prototk.PreparedChainTransaction_Soroban{
				Soroban: &prototk.SorobanInvoke{
					ContractId:   r.config.StellarRepoTermsFactoryAddress,
					FunctionName: "deploy",
					ArgsXdr:      argsXDR,
					ArgsJson:     argsJSON,
				},
			},
		},
		Signer: &signer,
	}, nil
}
