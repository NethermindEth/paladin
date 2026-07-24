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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/saladintypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/scspec"
	"github.com/hyperledger/firefly-signer/pkg/ethtypes"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// parseContractAddress parses a TransactionSpecification's ContractInfo.ContractAddress into the
// EVM-shaped 20-byte address ParsedTransaction.ContractAddress carries (a shared toolkit type,
// used by every domain regardless of chain kind - see domains/noto/internal/noto/noto.go's own
// parseContractAddress for the identical rationale). For a Stellar StrKey address (not hex at all)
// pldtypes.EthAddressOrPlaceholder derives a deterministic stand-in rather than failing.
func parseContractAddress(s string) (*ethtypes.Address0xHex, error) {
	ethAddr, err := pldtypes.EthAddressOrPlaceholder(s)
	if err != nil {
		return nil, err
	}
	return ethAddr.Address0xHex(), nil
}

// placeholderContractID derives a deterministic, StrKey-encoded Soroban contract ID from the
// EVM-shaped 20-byte address ParsedTransaction.ContractAddress carries - the same off-chain-only
// stand-in domains/noto/internal/noto/chainio_stellar.go's own placeholderContractID uses, for the
// same reason (that toolkit-wide generalization is out of scope for this domain too). Used only to
// compute the SALADIN_TYPED_DATA_V0 digest for the off-chain "sender"/"bilateral" attestation
// payloads - the real on-chain invoke target for PrepareTransaction uses
// tx.Transaction.ContractInfo.ContractAddress directly (handler.go's Prepare), never this stand-in.
func placeholderContractID(contract *ethtypes.Address0xHex) (string, error) {
	var padded [32]byte
	if contract != nil {
		copy(padded[12:], contract[:])
	}
	return strkey.Encode(strkey.VersionByteContract, padded[:])
}

// scValBytes/scValString/scValVec/scValAddress/marshalScVal build xdr.ScVal values by hand for the
// small, known-shape payloads this file needs - copied from
// domains/noto/internal/noto/chainio_stellar.go's own identical helpers (no shared module to put
// them in - these are two independent domain-plugin binaries).
func scValBytes(b []byte) (xdr.ScVal, error) {
	return xdr.NewScVal(xdr.ScValTypeScvBytes, xdr.ScBytes(b))
}

func scValString(str string) (xdr.ScVal, error) {
	return xdr.NewScVal(xdr.ScValTypeScvString, xdr.ScString(str))
}

func scValVec(items []xdr.ScVal) (xdr.ScVal, error) {
	vec := xdr.ScVec(items)
	return xdr.NewScVal(xdr.ScValTypeScvVec, &vec)
}

// scValAddress builds an Address SCVal from a strkey string (account "G..." or contract "C...").
func scValAddress(strkeyAddr string) (xdr.ScVal, error) {
	addr, err := scspec.AddressFromStrkey(strkeyAddr)
	if err != nil {
		return xdr.ScVal{}, err
	}
	return xdr.NewScVal(xdr.ScValTypeScvAddress, addr)
}

func marshalScVal(v xdr.ScVal) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := xdr.Marshal(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encodeSetTermsSignPayload builds the off-chain SALADIN_TYPED_DATA_V0 digest the "sender" SIGN
// attestation and the "bilateral" ENDORSE attestation both sign over (handler.go's Assemble/
// Endorse) - the repo-terms counterpart to domains/noto/internal/noto/chainio_stellar.go's own
// EncodeTransferUnmasked. contractID uses placeholderContractID (off-chain-only stand-in, as there
// - the real deployed instance address only matters for the actual on-chain invoke in Prepare).
func (r *RepoTerms) encodeSetTermsSignPayload(ctx context.Context, contract *ethtypes.Address0xHex, txID, termsStateID pldtypes.Bytes32) (ethtypes.HexBytes0xPrefix, error) {
	contractID, err := placeholderContractID(contract)
	if err != nil {
		return nil, err
	}
	txIDVal, err := scValBytes(txID[:])
	if err != nil {
		return nil, err
	}
	termsStateIDVal, err := scValBytes(termsStateID[:])
	if err != nil {
		return nil, err
	}
	payload, err := scValVec([]xdr.ScVal{txIDVal, termsStateIDVal})
	if err != nil {
		return nil, err
	}
	payloadXDR, err := marshalScVal(payload)
	if err != nil {
		return nil, err
	}
	digest, err := saladintypes.DigestXDR(r.networkPassphrase, contractID, "repoterms.SetTerms", payloadXDR)
	if err != nil {
		return nil, err
	}
	return ethtypes.HexBytes0xPrefix(digest[:]), nil
}

// encodeRepoTermsSetTermsArgs builds the real Soroban call args for repo-terms's
// `set_terms(tx_id, terms_state_id)` (soroban/contracts/repo-terms/src/lib.rs) - the only on-chain
// call this domain ever makes on its deployed instance (there is deliberately no signature/proof
// argument: the contract does zero on-chain signature verification at all, see set_terms's own doc
// comment - the trust boundary is Paladin's own off-chain bilateral ENDORSE/threshold=2 attestation
// plan, already enforced before Prepare is ever called).
func encodeRepoTermsSetTermsArgs(txID, termsStateID pldtypes.Bytes32) (argsXDR []byte, argsJSON string, err error) {
	txIDVal, err := scValBytes(txID[:])
	if err != nil {
		return nil, "", err
	}
	termsStateIDVal, err := scValBytes(termsStateID[:])
	if err != nil {
		return nil, "", err
	}

	args := xdr.ScVec{txIDVal, termsStateIDVal}
	var buf bytes.Buffer
	if _, err := xdr.Marshal(&buf, args); err != nil {
		return nil, "", err
	}

	argsJSONBytes, err := json.Marshal(map[string]any{
		"tx_id":          txID.String(),
		"terms_state_id": termsStateID.String(),
	})
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), string(argsJSONBytes), nil
}

// encodeRepoTermsFactoryDeployArgs builds the real Soroban call args for repo-terms-factory's
// `deploy(wasm_hash, bank_a, bank_b, saladin_factory, tx_id, identity_lookup)`
// (soroban/contracts/repo-terms-factory/src/lib.rs) - argument order must match the Rust signature
// exactly. Simpler than SNoto's own encodeSNotoFactoryDeployArgs: no separate config/sac args (see
// that factory's own doc comment on why an empty config rides through instead). identityLookup is
// the combined "bankA@node|bankB@node" string (deploy_stellar.go's PrepareDeploy) - the factory
// contract passes it through to SaladinFactory::register untouched; splitting it back apart is
// this Go plugin's own job (decodeStellarConfig).
func encodeRepoTermsFactoryDeployArgs(wasmHash pldtypes.Bytes32, bankA, bankB, saladinFactory string, txID pldtypes.Bytes32, identityLookup string) (argsXDR []byte, argsJSON string, err error) {
	wasmHashVal, err := scValBytes(wasmHash[:])
	if err != nil {
		return nil, "", err
	}
	bankAVal, err := scValAddress(bankA)
	if err != nil {
		return nil, "", err
	}
	bankBVal, err := scValAddress(bankB)
	if err != nil {
		return nil, "", err
	}
	saladinFactoryVal, err := scValAddress(saladinFactory)
	if err != nil {
		return nil, "", err
	}
	txIDVal, err := scValBytes(txID[:])
	if err != nil {
		return nil, "", err
	}
	identityLookupVal, err := scValString(identityLookup)
	if err != nil {
		return nil, "", err
	}

	args := xdr.ScVec{wasmHashVal, bankAVal, bankBVal, saladinFactoryVal, txIDVal, identityLookupVal}
	var buf bytes.Buffer
	if _, err := xdr.Marshal(&buf, args); err != nil {
		return nil, "", err
	}

	argsJSONBytes, err := json.Marshal(map[string]any{
		"wasm_hash":       wasmHash.String(),
		"bank_a":          bankA,
		"bank_b":          bankB,
		"saladin_factory": saladinFactory,
		"tx_id":           txID.String(),
		"identity_lookup": identityLookup,
	})
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), string(argsJSONBytes), nil
}

// stellarRepoTermsRegistrationConfig mirrors domainmgr's own generic
// stellarRegistrationConfigWithNotaryLookup (core/go/internal/domainmgr/event_indexer_stellar.go)
// field-for-field - the two sides of a JSON wire boundary between core and this plugin. This shape
// is built by domainmgr for ANY Stellar domain whose factory transaction also publishes an
// IdentityRegistered ("idreg") event alongside its own "reg" registration event
// (core/go/internal/domainmgr/event_indexer.go) - not Noto-specific despite the "notaryLookup"
// field name (that name predates repo-terms; the mechanism itself is domain-agnostic). config is
// always empty for repo-terms (repo-terms-factory's own deploy always passes Bytes::new() as
// register's config - see that factory's own doc comment), so NetworkPassphrase is decoded but
// otherwise unused here.
type stellarRepoTermsRegistrationConfig struct {
	NetworkPassphrase pldtypes.HexBytes `json:"networkPassphrase"`
	NotaryLookup      string            `json:"notaryLookup"`
}

// decodeStellarConfig parses registrationIndexer's own combined {networkPassphrase, notaryLookup}
// JSON (see stellarRepoTermsRegistrationConfig's own doc comment for why the field is still named
// "notaryLookup") and splits the combined identity-lookup string on "|" into exactly the two
// bilateral counterparties' Paladin identity locators - the inverse of deploy_stellar.go's own
// PrepareDeploy, which joins them the same way before calling repo-terms-factory's `deploy`.
func decodeStellarConfig(ctx context.Context, domainConfig []byte) (*RepoTermsParsedConfig, error) {
	var combined stellarRepoTermsRegistrationConfig
	if err := json.Unmarshal(domainConfig, &combined); err != nil {
		return nil, fmt.Errorf("invalid stellar registration config: %w", err)
	}
	parts := strings.Split(combined.NotaryLookup, "|")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("expected identity lookup of the form \"bankALookup|bankBLookup\", got %q", combined.NotaryLookup)
	}
	return &RepoTermsParsedConfig{
		BankALookup: parts[0],
		BankBLookup: parts[1],
	}, nil
}
