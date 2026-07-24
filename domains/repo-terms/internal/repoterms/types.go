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

// This file holds the plain data shapes this domain plugin passes across its own JSON wire
// boundaries (deploy/function params, contract config, and the one private state). Mirrors
// domains/noto/pkg/types' layout, drastically simplified: repo-terms has exactly one config shape,
// one state, and one transaction type - unlike Noto's many notary modes/variants, there is nothing
// here to make generic, so it lives directly in the repoterms package rather than a separate
// sub-package.
package repoterms

import (
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/domain"
	"github.com/hyperledger/firefly-signer/pkg/abi"
)

// RepoTermsV1ABI describes the single private state this domain ever creates. AssembleTransaction
// (handler.go) produces exactly one, once, per contract instance - there is no amend/update path
// (chapter 18's own explicit v1 scope decision). Mirrors domains/noto's own NotoLockInfoABI_V1
// shape/conventions (domains/noto/pkg/types/states.go).
var RepoTermsV1ABI = &abi.Parameter{
	Name:         "RepoTerms_V1",
	Type:         "tuple",
	InternalType: "struct RepoTerms_V1",
	Components: abi.ParameterArray{
		{Name: "salt", Type: "bytes32"},
		{Name: "bankA", Type: "string", Indexed: true},
		{Name: "bankB", Type: "string", Indexed: true},
		{Name: "rateBps", Type: "uint32"},
		{Name: "maturityLedger", Type: "uint32"},
		{Name: "haircutBps", Type: "uint32"},
	},
}

// RepoTermsV1 is the actual private state content - the real bilateral repo-trade economics,
// visible only to bank_a/bank_b via this state's own DistributionList (handler.go's Assemble). The
// public chain only ever sees this state's own opaque 32-byte ID - repo-terms's own on-chain
// `set_terms` event echoes just that ID, mirroring SNoto's lock-info state-ID-echo pattern
// (domains/noto/internal/noto/handler_lock.go).
type RepoTermsV1 struct {
	Salt           pldtypes.HexBytes `json:"salt"`
	BankA          string            `json:"bankA"`
	BankB          string            `json:"bankB"`
	RateBps        uint32            `json:"rateBps"`
	MaturityLedger uint32            `json:"maturityLedger"`
	HaircutBps     uint32            `json:"haircutBps"`
}

// RepoTermsParsedConfig is this domain's one-and-only ContractConfigJson shape (InitContract) -
// unlike Noto's many notary modes/variants, repo-terms has exactly one shape: the two bilateral
// counterparties' fully-qualified Paladin identity locators, split back out of the combined
// "bankA@node|bankB@node" string carried on-chain (see domain.go's decodeStellarConfig).
type RepoTermsParsedConfig struct {
	BankALookup string `json:"bankALookup"`
	BankBLookup string `json:"bankBLookup"`
}

// ParsedTransaction aliases the shared generic toolkit type to this domain's own config shape,
// mirroring domains/noto/pkg/types/config.go's own aliasing pattern.
type ParsedTransaction = domain.ParsedTransaction[RepoTermsParsedConfig]

// DomainConfig is ConfigureDomain's own {config JSON} shape - the plugin-wide (not per-contract)
// static configuration, mirroring the Stellar-specific fields of domains/noto/pkg/types.DomainConfig.
type DomainConfig struct {
	StellarRepoTermsFactoryAddress string `json:"stellarRepoTermsFactoryAddress"`
	StellarRepoTermsWasmHash       string `json:"stellarRepoTermsWasmHash"`
}

// ConstructorParams is this domain's deploy-transaction ConstructorParamsJson shape (InitDeploy/
// PrepareDeploy) - two Paladin identity locators (not addresses), resolved to real Stellar
// addresses via RequiredVerifiers exactly like Noto resolves its own "notary".
type ConstructorParams struct {
	BankA string `json:"bankA"`
	BankB string `json:"bankB"`
}

// SetTermsParams is the one transaction type's own FunctionParamsJson shape - the banks are
// already fixed at deploy time (ConstructorParams/RepoTermsParsedConfig), so only the trade
// economics are specified per-call.
type SetTermsParams struct {
	RateBps        uint32 `json:"rateBps"`
	MaturityLedger uint32 `json:"maturityLedger"`
	HaircutBps     uint32 `json:"haircutBps"`
}
