// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package stellar

import (
	"bytes"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncodeDecodeClassicOperationsRoundTrip(t *testing.T) {
	trustor := keypair.MustRandom().Address()
	issuer := keypair.MustRandom().Address()
	destination := keypair.MustRandom().Address()
	asset := txnbuild.CreditAsset{Code: "USDX", Issuer: issuer}

	ops := []txnbuild.Operation{
		&txnbuild.CreateAccount{Destination: destination, Amount: "10"},
		&txnbuild.Payment{Destination: destination, Amount: "5", Asset: txnbuild.NativeAsset{}},
		&txnbuild.ChangeTrust{Line: asset.MustToChangeTrustAsset(), Limit: "1000"},
		&txnbuild.SetTrustLineFlags{
			Trustor:  trustor,
			Asset:    asset,
			SetFlags: []txnbuild.TrustLineFlag{txnbuild.TrustLineAuthorized},
		},
	}

	payload, err := EncodeClassicOperations(ops)
	require.NoError(t, err)
	require.NotEmpty(t, payload)

	decoded, err := DecodeClassicOperations(payload)
	require.NoError(t, err)
	require.Len(t, decoded, 4)

	createAccount, ok := decoded[0].(*txnbuild.CreateAccount)
	require.True(t, ok)
	assert.Equal(t, destination, createAccount.Destination)
	assert.Equal(t, "10.0000000", createAccount.Amount)

	payment, ok := decoded[1].(*txnbuild.Payment)
	require.True(t, ok)
	assert.Equal(t, destination, payment.Destination)
	assert.True(t, payment.Asset.IsNative())

	changeTrust, ok := decoded[2].(*txnbuild.ChangeTrust)
	require.True(t, ok)
	assert.Equal(t, "USDX", changeTrust.Line.GetCode())
	assert.Equal(t, issuer, changeTrust.Line.GetIssuer())

	setFlags, ok := decoded[3].(*txnbuild.SetTrustLineFlags)
	require.True(t, ok)
	assert.Equal(t, trustor, setFlags.Trustor)
	require.Len(t, setFlags.SetFlags, 1)
	assert.Equal(t, txnbuild.TrustLineAuthorized, setFlags.SetFlags[0])
}

func TestDecodeClassicOperationsRejectsUnsupportedType(t *testing.T) {
	destination := keypair.MustRandom().Address()
	// AccountMerge is a real classic operation, but deliberately not in the supported list
	// (chapter 12 §12.3's scope-creep warning).
	payload, err := EncodeClassicOperations([]txnbuild.Operation{&txnbuild.AccountMerge{Destination: destination}})
	require.NoError(t, err)

	_, err = DecodeClassicOperations(payload)
	require.ErrorContains(t, err, "unsupported classic operation type")
}

func TestDecodeClassicOperationsRejectsEmptyPayload(t *testing.T) {
	// nil/empty bytes fail to decode even a length prefix - that's covered by
	// TestDecodeClassicOperationsRejectsInvalidXDR. This test covers a validly-encoded, but empty,
	// operations array.
	var buf bytes.Buffer
	_, err := xdr.Marshal(&buf, []xdr.Operation{})
	require.NoError(t, err)

	_, err = DecodeClassicOperations(buf.Bytes())
	require.ErrorContains(t, err, "at least one classic operation is required")
}

func TestDecodeClassicOperationsRejectsInvalidXDR(t *testing.T) {
	_, err := DecodeClassicOperations([]byte("not valid xdr"))
	require.ErrorContains(t, err, "invalid classic operations payload")
}

func TestEncodeClassicOperationsRejectsEmpty(t *testing.T) {
	_, err := EncodeClassicOperations(nil)
	require.ErrorContains(t, err, "at least one classic operation is required")
}

func TestBuildChangeTrustPayload(t *testing.T) {
	issuer := keypair.MustRandom().Address()
	asset := txnbuild.CreditAsset{Code: "REG", Issuer: issuer}

	payload, err := BuildChangeTrustPayload(asset, "500")
	require.NoError(t, err)

	decoded, err := DecodeClassicOperations(payload)
	require.NoError(t, err)
	require.Len(t, decoded, 1)
	changeTrust, ok := decoded[0].(*txnbuild.ChangeTrust)
	require.True(t, ok)
	assert.Equal(t, "REG", changeTrust.Line.GetCode())
	assert.Equal(t, "500.0000000", changeTrust.Limit)
}

func TestBuildSetTrustLineFlagsPayload(t *testing.T) {
	issuer := keypair.MustRandom().Address()
	trustor := keypair.MustRandom().Address()
	asset := txnbuild.CreditAsset{Code: "REG", Issuer: issuer}

	payload, err := BuildSetTrustLineFlagsPayload(trustor, asset, []txnbuild.TrustLineFlag{txnbuild.TrustLineAuthorized}, nil)
	require.NoError(t, err)

	decoded, err := DecodeClassicOperations(payload)
	require.NoError(t, err)
	require.Len(t, decoded, 1)
	setFlags, ok := decoded[0].(*txnbuild.SetTrustLineFlags)
	require.True(t, ok)
	assert.Equal(t, trustor, setFlags.Trustor)
}
