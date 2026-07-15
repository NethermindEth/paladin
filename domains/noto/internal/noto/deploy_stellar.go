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
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/domains/noto/internal/msgs"
	"github.com/LFDT-Paladin/paladin/domains/noto/pkg/types"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/google/uuid"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// stellarPrepareDeploy builds a real PreparedChainTransaction.soroban (SorobanInvoke) targeting
// the domain's configured SNotoFactory instance's `deploy` function (soroban/contracts/
// snoto-factory, chapter 14 step 6) - the Stellar sibling to PrepareDeploy's EVM path, mirroring
// how mint/transfer/lock/unlock each dispatch to their own stellarBaseLedgerInvoke* method rather
// than branching inside the EVM-ABI-shaped code. No auth_entries_xdr/read_footprint_hints, for the
// same reason mint's stellarBaseLedgerInvoke leaves them empty: that's Paladin's core signing/
// submission pipeline's job, not this domain plugin's.
func (n *Noto) stellarPrepareDeploy(ctx context.Context, req *prototk.PrepareDeployRequest, params *types.ConstructorParams, notary *identityPair) (*prototk.PrepareDeployResponse, error) {
	if n.config.StellarSnotoFactoryAddress == "" {
		return nil, i18n.NewError(ctx, msgs.MsgParameterRequired, "stellarSnotoFactoryAddress")
	}
	if n.config.StellarSnotoWasmHash == "" {
		return nil, i18n.NewError(ctx, msgs.MsgParameterRequired, "stellarSnotoWasmHash")
	}
	if n.registryAddress == "" {
		return nil, i18n.NewError(ctx, msgs.MsgParameterRequired, "registryContractAddress")
	}

	txID, err := pldtypes.ParseBytes32Ctx(ctx, req.Transaction.TransactionId)
	if err != nil {
		return nil, err
	}
	wasmHash, err := pldtypes.ParseBytes32Ctx(ctx, n.config.StellarSnotoWasmHash)
	if err != nil {
		return nil, err
	}

	sacAddress := n.config.StellarSacAddress
	if sacAddress == "" {
		// See DomainConfig.StellarSacAddress's doc comment: a harmless inert placeholder until
		// native-asset (shield/unshield) config wiring exists for Stellar.
		sacAddress = notary.chainAddress.String()
	}

	config := []byte(n.getChainIO().NetworkPassphrase())

	// saladin_factory is the domain's own registry (RegistryContractAddress, ConfigureDomain) -
	// the per-domain SaladinFactory instance domainmgr's step 5 event-stream trusts - NOT
	// StellarSnotoFactoryAddress (that's the contract being invoked below, a different instance).
	argsXDR, argsJSON, err := encodeSNotoFactoryDeployArgs(wasmHash, notary.chainAddress.String(), config, sacAddress, n.registryAddress, txID)
	if err != nil {
		return nil, err
	}

	signer := n.fixedSigningIdentity
	if signer == "" {
		// Use a random key to deploy if no default signing identity is set
		signer = fmt.Sprintf("%s.deploy.%s", n.name, uuid.New())
	}

	return &prototk.PrepareDeployResponse{
		ChainTransaction: &prototk.PreparedChainTransaction{
			Type: prototk.PreparedChainTransaction_PUBLIC,
			Payload: &prototk.PreparedChainTransaction_Soroban{
				Soroban: &prototk.SorobanInvoke{
					ContractId:   n.config.StellarSnotoFactoryAddress,
					FunctionName: "deploy",
					ArgsXdr:      argsXDR,
					ArgsJson:     argsJSON,
				},
			},
		},
		Signer: &signer,
	}, nil
}

// encodeSNotoFactoryDeployArgs builds the real Soroban call args for SNotoFactory's
// `deploy(wasm_hash, notary, config, sac, saladin_factory, tx_id)` (soroban/contracts/
// snoto-factory/src/lib.rs) - argument order must match the Rust signature exactly.
func encodeSNotoFactoryDeployArgs(wasmHash pldtypes.Bytes32, notary string, config []byte, sac, saladinFactory string, txID pldtypes.Bytes32) (argsXDR []byte, argsJSON string, err error) {
	wasmHashVal, err := scValBytes(wasmHash[:])
	if err != nil {
		return nil, "", err
	}
	notaryVal, err := scValAddress(notary)
	if err != nil {
		return nil, "", err
	}
	configVal, err := scValBytes(config)
	if err != nil {
		return nil, "", err
	}
	sacVal, err := scValAddress(sac)
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

	args := xdr.ScVec{wasmHashVal, notaryVal, configVal, sacVal, saladinFactoryVal, txIDVal}
	var buf bytes.Buffer
	if _, err := xdr.Marshal(&buf, args); err != nil {
		return nil, "", err
	}

	argsJSONBytes, err := json.Marshal(map[string]any{
		"wasm_hash":       wasmHash.String(),
		"notary":          notary,
		"config":          pldtypes.HexBytes(config).String(),
		"sac":             sac,
		"saladin_factory": saladinFactory,
		"tx_id":           txID.String(),
	})
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), string(argsJSONBytes), nil
}
