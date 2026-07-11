// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package scspec

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

func addressFromStrkey(s string) (xdr.ScAddress, error) {
	version, raw, err := strkey.DecodeAny(s)
	if err != nil {
		return xdr.ScAddress{}, fmt.Errorf("invalid strkey address %q: %w", s, err)
	}
	switch version {
	case strkey.VersionByteAccountID:
		if len(raw) != 32 {
			return xdr.ScAddress{}, fmt.Errorf("invalid account strkey %q: expected 32 bytes", s)
		}
		var key xdr.Uint256
		copy(key[:], raw)
		return xdr.ScAddress{
			Type:      xdr.ScAddressTypeScAddressTypeAccount,
			AccountId: &xdr.AccountId{Type: xdr.PublicKeyTypePublicKeyTypeEd25519, Ed25519: &key},
		}, nil
	case strkey.VersionByteContract:
		if len(raw) != 32 {
			return xdr.ScAddress{}, fmt.Errorf("invalid contract strkey %q: expected 32 bytes", s)
		}
		var cid xdr.ContractId
		copy(cid[:], raw)
		return xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &cid}, nil
	case strkey.VersionByteMuxedAccount:
		m, err := strkey.DecodeMuxedAccount(s)
		if err != nil {
			return xdr.ScAddress{}, fmt.Errorf("invalid muxed account strkey %q: %w", s, err)
		}
		ed := m.Ed25519()
		var key xdr.Uint256
		copy(key[:], ed[:])
		return xdr.ScAddress{
			Type:         xdr.ScAddressTypeScAddressTypeMuxedAccount,
			MuxedAccount: &xdr.MuxedEd25519Account{Id: xdr.Uint64(m.ID()), Ed25519: key},
		}, nil
	default:
		return xdr.ScAddress{}, fmt.Errorf("unsupported strkey address version for %q", s)
	}
}

func addressToStrkey(addr xdr.ScAddress) (string, error) {
	switch addr.Type {
	case xdr.ScAddressTypeScAddressTypeAccount:
		if addr.AccountId == nil || addr.AccountId.Ed25519 == nil {
			return "", fmt.Errorf("malformed account address")
		}
		return strkey.Encode(strkey.VersionByteAccountID, (*addr.AccountId.Ed25519)[:])
	case xdr.ScAddressTypeScAddressTypeContract:
		if addr.ContractId == nil {
			return "", fmt.Errorf("malformed contract address")
		}
		return strkey.Encode(strkey.VersionByteContract, (*addr.ContractId)[:])
	case xdr.ScAddressTypeScAddressTypeMuxedAccount:
		if addr.MuxedAccount == nil {
			return "", fmt.Errorf("malformed muxed account address")
		}
		m := &strkey.MuxedAccount{}
		m.SetID(uint64(addr.MuxedAccount.Id))
		if err := m.SetAccountID(mustEncodeAccountID(addr.MuxedAccount.Ed25519)); err != nil {
			return "", fmt.Errorf("invalid muxed account: %w", err)
		}
		return m.Address()
	default:
		return "", fmt.Errorf("unsupported address type %v", addr.Type)
	}
}

func mustEncodeAccountID(key xdr.Uint256) string {
	s, _ := strkey.Encode(strkey.VersionByteAccountID, key[:])
	return s
}
