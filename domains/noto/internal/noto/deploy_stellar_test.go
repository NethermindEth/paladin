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
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/LFDT-Paladin/paladin/domains/noto/pkg/types"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/scspec"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/algorithms"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/verifiers"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareDeploy_Stellar(t *testing.T) {
	mockCallbacks := newMockCallbacks()

	snotoFactoryAddress, err := strkey.Encode(strkey.VersionByteContract, pldtypes.RandBytes(32))
	require.NoError(t, err)
	registryAddress, err := strkey.Encode(strkey.VersionByteContract, pldtypes.RandBytes(32))
	require.NoError(t, err)
	wasmHash := pldtypes.RandBytes32()

	networkPassphrase := "Test Stellar Network ; 2026"
	n := &Noto{
		Callbacks:       mockCallbacks,
		chainIO:         newStellarChainIO(networkPassphrase),
		registryAddress: registryAddress,
		config: types.DomainConfig{
			StellarSnotoFactoryAddress: snotoFactoryAddress,
			StellarSnotoWasmHash:       wasmHash.String(),
		},
	}
	ctx := t.Context()

	notaryPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	notaryAddress, err := strkey.Encode(strkey.VersionByteAccountID, notaryPub)
	require.NoError(t, err)

	deployTransaction := &prototk.DeployTransactionSpecification{
		TransactionId: "0x015e1881f2ba769c22d05c841f06949ec6e1bd573f5e1e0328885494212f077d",
		ConstructorParamsJson: `{
			"notary": "notary@node1",
			"notaryMode": "basic"
		}`,
	}

	initDeployRes, err := n.InitDeploy(ctx, &prototk.InitDeployRequest{Transaction: deployTransaction})
	require.NoError(t, err)
	require.Len(t, initDeployRes.RequiredVerifiers, 1)
	assert.Equal(t, verifiers.STELLAR_ADDRESS, initDeployRes.RequiredVerifiers[0].VerifierType)

	prepareDeployRes, err := n.PrepareDeploy(ctx, &prototk.PrepareDeployRequest{
		Transaction: deployTransaction,
		ResolvedVerifiers: []*prototk.ResolvedVerifier{
			{
				Lookup:       "notary@node1",
				Algorithm:    algorithms.EDDSA_ED25519,
				VerifierType: verifiers.STELLAR_ADDRESS,
				Verifier:     notaryAddress,
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, prepareDeployRes.ChainTransaction)
	assert.Equal(t, prototk.PreparedChainTransaction_PUBLIC, prepareDeployRes.ChainTransaction.Type)

	invoke := prepareDeployRes.ChainTransaction.GetSoroban()
	require.NotNil(t, invoke)
	assert.Equal(t, snotoFactoryAddress, invoke.ContractId)
	assert.Equal(t, "deploy", invoke.FunctionName)

	// Decode the XDR args back and confirm they match what was requested - the same round-trip
	// style TestDecodeXDRSCVal already establishes for this codebase's hand-built XDR args.
	// ArgsXdr is a raw XDR-encoded Vec<SCVal> (matching how a Soroban function call's argument
	// list is encoded), not a full ScVal wrapping a Vec - decode as xdr.ScVec directly.
	var args xdr.ScVec
	_, err = xdr.Unmarshal(bytes.NewReader(invoke.ArgsXdr), &args)
	require.NoError(t, err)
	require.Len(t, args, 6)

	assert.Equal(t, xdr.ScValTypeScvBytes, args[0].Type)
	assert.Equal(t, wasmHash[:], []byte(*args[0].Bytes))

	assert.Equal(t, xdr.ScValTypeScvAddress, args[1].Type)
	notaryScAddr, err := scspec.AddressToStrkey(*args[1].Address)
	require.NoError(t, err)
	assert.Equal(t, notaryAddress, notaryScAddr)

	assert.Equal(t, xdr.ScValTypeScvBytes, args[2].Type)
	assert.Equal(t, []byte(networkPassphrase), []byte(*args[2].Bytes))

	assert.Equal(t, xdr.ScValTypeScvAddress, args[3].Type)
	sacScAddr, err := scspec.AddressToStrkey(*args[3].Address)
	require.NoError(t, err)
	// StellarSacAddress wasn't configured, so the notary's own address is used as the documented
	// harmless placeholder (DomainConfig.StellarSacAddress's doc comment).
	assert.Equal(t, notaryAddress, sacScAddr)

	assert.Equal(t, xdr.ScValTypeScvAddress, args[4].Type)
	saladinFactoryScAddr, err := scspec.AddressToStrkey(*args[4].Address)
	require.NoError(t, err)
	// saladin_factory is the domain's own registry (RegistryContractAddress), not
	// StellarSnotoFactoryAddress (the contract actually being invoked, asserted above).
	assert.Equal(t, registryAddress, saladinFactoryScAddr)

	assert.Equal(t, xdr.ScValTypeScvBytes, args[5].Type)
	txID, err := pldtypes.ParseBytes32Ctx(ctx, deployTransaction.TransactionId)
	require.NoError(t, err)
	assert.Equal(t, txID[:], []byte(*args[5].Bytes))
}

func TestPrepareDeploy_Stellar_MissingFactoryConfig(t *testing.T) {
	mockCallbacks := newMockCallbacks()
	n := &Noto{
		Callbacks: mockCallbacks,
		chainIO:   newStellarChainIO("Test Stellar Network ; 2026"),
	}
	ctx := t.Context()

	notaryPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	notaryAddress, err := strkey.Encode(strkey.VersionByteAccountID, notaryPub)
	require.NoError(t, err)

	deployTransaction := &prototk.DeployTransactionSpecification{
		TransactionId:         "0x015e1881f2ba769c22d05c841f06949ec6e1bd573f5e1e0328885494212f077d",
		ConstructorParamsJson: `{"notary": "notary@node1", "notaryMode": "basic"}`,
	}

	_, err = n.PrepareDeploy(ctx, &prototk.PrepareDeployRequest{
		Transaction: deployTransaction,
		ResolvedVerifiers: []*prototk.ResolvedVerifier{
			{Lookup: "notary@node1", Algorithm: algorithms.EDDSA_ED25519, VerifierType: verifiers.STELLAR_ADDRESS, Verifier: notaryAddress},
		},
	})
	assert.ErrorContains(t, err, "stellarSnotoFactoryAddress")
}
