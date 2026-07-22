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

func randAccountAddr(t *testing.T) string {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	addr, err := strkey.Encode(strkey.VersionByteAccountID, pub)
	require.NoError(t, err)
	return addr
}

func TestRunEncodesChangeTrustOp(t *testing.T) {
	holder := randAccountAddr(t)
	issuer := randAccountAddr(t)

	hexPayload, err := run(holder, "TUSD", issuer)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(hexPayload, "0x"))

	payload, err := hex.DecodeString(strings.TrimPrefix(hexPayload, "0x"))
	require.NoError(t, err)

	var ops []xdr.Operation
	_, err = xdr.Unmarshal(bytes.NewReader(payload), &ops)
	require.NoError(t, err)
	require.Len(t, ops, 1)
	require.Equal(t, xdr.OperationTypeChangeTrust, ops[0].Body.Type)

	holderAccountID, err := xdr.AddressToAccountId(holder)
	require.NoError(t, err)
	require.NotNil(t, ops[0].SourceAccount)
	require.Equal(t, holderAccountID.Address(), ops[0].SourceAccount.ToAccountId().Address())

	changeTrust := ops[0].Body.ChangeTrustOp
	require.NotNil(t, changeTrust)
	require.Equal(t, xdr.AssetTypeAssetTypeCreditAlphanum4, changeTrust.Line.Type)
}
