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
	"crypto/ed25519"
	"crypto/rand"
	"testing"

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

	factoryAddress, err := strkey.Encode(strkey.VersionByteContract, pldtypes.RandBytes(32))
	require.NoError(t, err)
	registryAddress, err := strkey.Encode(strkey.VersionByteContract, pldtypes.RandBytes(32))
	require.NoError(t, err)
	wasmHash := pldtypes.RandBytes32()

	r := &RepoTerms{
		Callbacks:       mockCallbacks,
		registryAddress: registryAddress,
		config: DomainConfig{
			StellarRepoTermsFactoryAddress: factoryAddress,
			StellarRepoTermsWasmHash:       wasmHash.String(),
		},
	}
	ctx := t.Context()

	bankAPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	bankAAddress, err := strkey.Encode(strkey.VersionByteAccountID, bankAPub)
	require.NoError(t, err)
	bankBPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	bankBAddress, err := strkey.Encode(strkey.VersionByteAccountID, bankBPub)
	require.NoError(t, err)

	deployTransaction := &prototk.DeployTransactionSpecification{
		TransactionId:         "0x015e1881f2ba769c22d05c841f06949ec6e1bd573f5e1e0328885494212f077d",
		ConstructorParamsJson: `{"bankA": "bankA@node2", "bankB": "bankB@node3"}`,
	}

	initDeployRes, err := r.InitDeploy(ctx, &prototk.InitDeployRequest{Transaction: deployTransaction})
	require.NoError(t, err)
	require.Len(t, initDeployRes.RequiredVerifiers, 2)
	for _, v := range initDeployRes.RequiredVerifiers {
		assert.Equal(t, verifiers.STELLAR_ADDRESS, v.VerifierType)
	}

	prepareDeployRes, err := r.PrepareDeploy(ctx, &prototk.PrepareDeployRequest{
		Transaction: deployTransaction,
		ResolvedVerifiers: []*prototk.ResolvedVerifier{
			{Lookup: "bankA@node2", Algorithm: algorithms.EDDSA_ED25519, VerifierType: verifiers.STELLAR_ADDRESS, Verifier: bankAAddress},
			{Lookup: "bankB@node3", Algorithm: algorithms.EDDSA_ED25519, VerifierType: verifiers.STELLAR_ADDRESS, Verifier: bankBAddress},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, prepareDeployRes.ChainTransaction)
	assert.Equal(t, prototk.PreparedChainTransaction_PUBLIC, prepareDeployRes.ChainTransaction.Type)

	invoke := prepareDeployRes.ChainTransaction.GetSoroban()
	require.NotNil(t, invoke)
	assert.Equal(t, factoryAddress, invoke.ContractId)
	assert.Equal(t, "deploy", invoke.FunctionName)

	var args xdr.ScVec
	_, err = xdr.Unmarshal(bytes.NewReader(invoke.ArgsXdr), &args)
	require.NoError(t, err)
	require.Len(t, args, 6)

	assert.Equal(t, xdr.ScValTypeScvBytes, args[0].Type)
	assert.Equal(t, wasmHash[:], []byte(*args[0].Bytes))

	assert.Equal(t, xdr.ScValTypeScvAddress, args[1].Type)
	bankAScAddr, err := scspec.AddressToStrkey(*args[1].Address)
	require.NoError(t, err)
	assert.Equal(t, bankAAddress, bankAScAddr)

	assert.Equal(t, xdr.ScValTypeScvAddress, args[2].Type)
	bankBScAddr, err := scspec.AddressToStrkey(*args[2].Address)
	require.NoError(t, err)
	assert.Equal(t, bankBAddress, bankBScAddr)

	assert.Equal(t, xdr.ScValTypeScvAddress, args[3].Type)
	saladinFactoryScAddr, err := scspec.AddressToStrkey(*args[3].Address)
	require.NoError(t, err)
	assert.Equal(t, registryAddress, saladinFactoryScAddr)

	assert.Equal(t, xdr.ScValTypeScvBytes, args[4].Type)
	txID, err := pldtypes.ParseBytes32Ctx(ctx, deployTransaction.TransactionId)
	require.NoError(t, err)
	assert.Equal(t, txID[:], []byte(*args[4].Bytes))

	assert.Equal(t, xdr.ScValTypeScvString, args[5].Type)
	assert.Equal(t, "bankA@node2|bankB@node3", string(*args[5].Str))
}

func TestPrepareDeploy_Stellar_MissingFactoryConfig(t *testing.T) {
	r := &RepoTerms{Callbacks: newMockCallbacks()}
	ctx := t.Context()

	deployTransaction := &prototk.DeployTransactionSpecification{
		TransactionId:         "0x015e1881f2ba769c22d05c841f06949ec6e1bd573f5e1e0328885494212f077d",
		ConstructorParamsJson: `{"bankA": "bankA@node2", "bankB": "bankB@node3"}`,
	}

	_, err := r.PrepareDeploy(ctx, &prototk.PrepareDeployRequest{Transaction: deployTransaction})
	assert.ErrorContains(t, err, "stellarRepoTermsFactoryAddress")
}

func TestInitDeploy_RequiresBothBanks(t *testing.T) {
	r := &RepoTerms{Callbacks: newMockCallbacks()}
	ctx := t.Context()

	_, err := r.InitDeploy(ctx, &prototk.InitDeployRequest{
		Transaction: &prototk.DeployTransactionSpecification{ConstructorParamsJson: `{"bankA": "bankA@node2"}`},
	})
	assert.ErrorContains(t, err, "bankB")

	_, err = r.InitDeploy(ctx, &prototk.InitDeployRequest{
		Transaction: &prototk.DeployTransactionSpecification{ConstructorParamsJson: `{"bankB": "bankB@node3"}`},
	})
	assert.ErrorContains(t, err, "bankA")
}
