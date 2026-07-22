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

package helpers

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger/stellar"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldclient"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/scspec"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

func randContractAddr(t *testing.T) string {
	var raw [32]byte
	_, err := rand.Read(raw[:])
	require.NoError(t, err)
	addr, err := strkey.Encode(strkey.VersionByteContract, raw[:])
	require.NoError(t, err)
	return addr
}

func randAccountAddr(t *testing.T) string {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	addr, err := strkey.Encode(strkey.VersionByteAccountID, pub)
	require.NoError(t, err)
	return addr
}

func TestAtomOperationScValEncoding(t *testing.T) {
	legContract := randContractAddr(t)
	argVal, err := scValSymbol("keepalive-arg")
	require.NoError(t, err)

	op := &SAtomOperation{
		Contract: legContract,
		Function: "keepalive",
		Args:     []xdr.ScVal{argVal},
	}

	scVal, err := atomOperationScVal(op)
	require.NoError(t, err)

	m, ok := scVal.GetMap()
	require.True(t, ok)
	require.NotNil(t, m)
	require.Len(t, *m, 3)

	// #[contracttype] structs encode as a SCMap sorted by field name - confirm "args", "contract",
	// "function" appear in exactly that order, not Rust declaration order (contract, function, args).
	names := make([]string, len(*m))
	for i, entry := range *m {
		sym, ok := entry.Key.GetSym()
		require.True(t, ok)
		names[i] = string(sym)
	}
	require.Equal(t, []string{"args", "contract", "function"}, names)

	contractEntry := (*m)[1]
	addrVal, ok := contractEntry.Val.GetAddress()
	require.True(t, ok)
	strkeyAddr, err := scspec.AddressToStrkey(addrVal)
	require.NoError(t, err)
	require.Equal(t, legContract, strkeyAddr)

	functionEntry := (*m)[2]
	sym, ok := functionEntry.Val.GetSym()
	require.True(t, ok)
	require.Equal(t, "keepalive", string(sym))

	argsEntry := (*m)[0]
	argsVec, ok := argsEntry.Val.GetVec()
	require.True(t, ok)
	require.Len(t, *argsVec, 1)
}

func TestDeploySettlementArgsEncoding(t *testing.T) {
	wasmHash := [32]byte{1, 2, 3}
	txID := [32]byte{7, 7, 7}
	legContract := randContractAddr(t)
	party := randAccountAddr(t)
	saladinFactory := randContractAddr(t)
	config := []byte("config-bytes")

	ops := []*SAtomOperation{{
		Contract: legContract,
		Function: "keepalive",
		Args:     nil,
	}}

	args, err := deploySettlementArgs(wasmHash, ops, []string{party}, saladinFactory, txID, config)
	require.NoError(t, err)
	require.Len(t, args, 6)

	wasmHashBytes, ok := args[0].GetBytes()
	require.True(t, ok)
	require.Equal(t, wasmHash[:], []byte(wasmHashBytes))

	opsVec, ok := args[1].GetVec()
	require.True(t, ok)
	require.Len(t, *opsVec, 1)

	partiesVec, ok := args[2].GetVec()
	require.True(t, ok)
	require.Len(t, *partiesVec, 1)

	_, ok = args[3].GetAddress()
	require.True(t, ok)

	txIDBytes, ok := args[4].GetBytes()
	require.True(t, ok)
	require.Equal(t, txID[:], []byte(txIDBytes))

	configBytes, ok := args[5].GetBytes()
	require.True(t, ok)
	require.Equal(t, config, []byte(configBytes))

	// Round-trip through the exact same marshal + BuildInvokeHostFunctionXDR path
	// stellarFunctionBuilder uses, then unmarshal back into a HostFunction and confirm the
	// contract/function/args all survive untouched.
	argsXDR, err := marshalScVec(args)
	require.NoError(t, err)

	factoryAddr := randContractAddr(t)
	payload, err := stellar.BuildInvokeHostFunctionXDR(factoryAddr, "deploy_settlement", argsXDR)
	require.NoError(t, err)

	var hostFn xdr.HostFunction
	_, err = xdr.Unmarshal(bytes.NewReader(payload), &hostFn)
	require.NoError(t, err)
	require.Equal(t, xdr.HostFunctionTypeHostFunctionTypeInvokeContract, hostFn.Type)
	require.Equal(t, xdr.ScSymbol("deploy_settlement"), hostFn.InvokeContract.FunctionName)
	require.Len(t, hostFn.InvokeContract.Args, 6)
}

func TestStellarFunctionBuilderRawDataPassthrough(t *testing.T) {
	ctx := context.Background()
	pld := pldclient.New()
	contractAddr := randContractAddr(t)

	builder := stellarFunctionBuilder(t, ctx, pld, contractAddr, "execute", nil)
	tx := builder.From("signer1").BuildTX().TX()

	require.Nil(t, tx.ABI)
	require.Equal(t, "execute", tx.Function)
	require.Equal(t, contractAddr, tx.To.String())

	var hexStr string
	require.NoError(t, json.Unmarshal(tx.Data, &hexStr))

	emptyArgsXDR, err := marshalScVec(nil)
	require.NoError(t, err)
	expectedPayload, err := stellar.BuildInvokeHostFunctionXDR(contractAddr, "execute", emptyArgsXDR)
	require.NoError(t, err)
	require.Equal(t, "0x"+hex.EncodeToString(expectedPayload), hexStr)
}

func TestDecodeSAtomRegistrationEvent(t *testing.T) {
	instanceAddr := randContractAddr(t)
	config := []byte("some-config")

	nameVal, err := scValSymbol("reg")
	require.NoError(t, err)
	var nameBuf bytes.Buffer
	_, err = xdr.Marshal(&nameBuf, nameVal)
	require.NoError(t, err)

	txIDVal, err := scValBytes([]byte{9, 9, 9})
	require.NoError(t, err)
	var txIDBuf bytes.Buffer
	_, err = xdr.Marshal(&txIDBuf, txIDVal)
	require.NoError(t, err)

	instanceVal, err := scValAddress(instanceAddr)
	require.NoError(t, err)
	configVal, err := scValBytes(config)
	require.NoError(t, err)
	dataVal, err := scValVec([]xdr.ScVal{instanceVal, configVal})
	require.NoError(t, err)
	var dataBuf bytes.Buffer
	_, err = xdr.Marshal(&dataBuf, dataVal)
	require.NoError(t, err)

	decodedAddr, decodedConfig, err := DecodeSAtomRegistrationEvent(
		[][]byte{nameBuf.Bytes(), txIDBuf.Bytes()},
		dataBuf.Bytes(),
	)
	require.NoError(t, err)
	require.Equal(t, instanceAddr, decodedAddr)
	require.Equal(t, config, decodedConfig)
}
