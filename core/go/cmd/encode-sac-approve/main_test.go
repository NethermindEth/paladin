// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"

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

func TestRunEncodesApproveCall(t *testing.T) {
	sacAddress := randContractAddr(t)
	fromAddr := randAccountAddr(t)
	spenderAddr := randContractAddr(t)

	hexPayload, err := run(sacAddress, fromAddr, spenderAddr, 500, 123456)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(hexPayload, "0x"))

	payload, err := hex.DecodeString(strings.TrimPrefix(hexPayload, "0x"))
	require.NoError(t, err)

	var hostFn xdr.HostFunction
	_, err = xdr.Unmarshal(bytes.NewReader(payload), &hostFn)
	require.NoError(t, err)
	require.Equal(t, xdr.HostFunctionTypeHostFunctionTypeInvokeContract, hostFn.Type)
	require.Equal(t, xdr.ScSymbol("approve"), hostFn.InvokeContract.FunctionName)
	require.Len(t, hostFn.InvokeContract.Args, 4)

	fromVal, ok := hostFn.InvokeContract.Args[0].GetAddress()
	require.True(t, ok)
	fromStrkey, err := strkey.Encode(strkey.VersionByteAccountID, fromVal.MustAccountId().Ed25519[:])
	require.NoError(t, err)
	require.Equal(t, fromAddr, fromStrkey)

	amountVal, ok := hostFn.InvokeContract.Args[2].GetI128()
	require.True(t, ok)
	require.EqualValues(t, 500, amountVal.Lo)
	require.EqualValues(t, 0, amountVal.Hi)

	expirationVal, ok := hostFn.InvokeContract.Args[3].GetU32()
	require.True(t, ok)
	require.EqualValues(t, 123456, expirationVal)
}

func TestRunRejectsInvalidAddress(t *testing.T) {
	_, err := run(randContractAddr(t), "not-a-valid-address", randContractAddr(t), 1, 1)
	require.Error(t, err)
}
