// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package pldtypes

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChainAddressEVMJSONAndStorageCompatibility(t *testing.T) {
	ethAddr := MustEthAddress("0x1d0cd5b99d2e2a380e52b4000377dd507c6df754")
	chainAddr := ethAddr.ChainAddress()

	require.Equal(t, ChainAddressKindEVM, chainAddr.Kind())
	require.Equal(t, ethAddr.String(), chainAddr.String())
	require.Equal(t, ethAddr.HexString(), chainAddr.StorageString())

	ethJSON, err := json.Marshal(ethAddr)
	require.NoError(t, err)
	chainJSON, err := json.Marshal(chainAddr)
	require.NoError(t, err)
	require.JSONEq(t, string(ethJSON), string(chainJSON))

	dbValue, err := chainAddr.Value()
	require.NoError(t, err)
	require.Equal(t, ethAddr.HexString(), dbValue)
}

func TestChainAddressParseEVMStorageValue(t *testing.T) {
	addr, err := ParseChainAddress("1d0cD5b99d2E2a380e52b4000377Dd507c6df754")
	require.NoError(t, err)
	require.Equal(t, ChainAddressKindEVM, addr.Kind())
	require.Equal(t, "0x1d0cd5b99d2e2a380e52b4000377dd507c6df754", addr.String())

	ethAddr, err := addr.EthAddress()
	require.NoError(t, err)
	require.Equal(t, "1d0cd5b99d2e2a380e52b4000377dd507c6df754", ethAddr.HexString())
}

func TestChainAddressParseStellarPrefixes(t *testing.T) {
	account, err := ParseChainAddress("GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF")
	require.NoError(t, err)
	require.Equal(t, ChainAddressKindStellarAccount, account.Kind())
	require.Equal(t, "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF", account.String())

	contract, err := ParseChainAddress("CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4")
	require.NoError(t, err)
	require.Equal(t, ChainAddressKindStellarContract, contract.Kind())
	require.Equal(t, "CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4", contract.String())
}

func TestChainAddressRejectsCorruptedStellarStrKeys(t *testing.T) {
	// Same payload as the valid account StrKey above, but with the trailing checksum character
	// altered - must be rejected by real StrKey validation (CRC16-XMODEM), not just a prefix check.
	_, err := ParseChainAddress("GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHG")
	require.ErrorContains(t, err, "invalid stellar account address")

	// Truncated - too short to be a valid StrKey at all.
	_, err = NewStellarAccountAddress("GAAAA")
	require.ErrorContains(t, err, "invalid stellar account address")

	// A validly-checksummed *account* StrKey must not be accepted as a contract address (wrong
	// version byte), and vice versa.
	_, err = NewStellarContractAddress("GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF")
	require.ErrorContains(t, err, "invalid stellar contract address")

	_, err = NewStellarAccountAddress("CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4")
	require.ErrorContains(t, err, "invalid stellar account address")
}
