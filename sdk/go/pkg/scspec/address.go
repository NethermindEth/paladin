// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package scspec

import (
	"fmt"

	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// AddressFromStrkey converts a strkey string (account "G...", contract "C...", or muxed account
// "M...") to its xdr.ScAddress form - the reverse of AddressToStrkey. Exported for callers outside
// this package that need to build an Address SCVal without going through the full Spec-driven
// FromJSON path (e.g. domains/noto's Stellar deploy args encoder, chapter 14 step 6, which only
// needs this one conversion for a known, fixed shape).
func AddressFromStrkey(s string) (xdr.ScAddress, error) {
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

// AddressToStrkey converts an xdr.ScAddress (account, contract, or muxed account) to its strkey
// string form. Exported for callers outside this package that need to decode an Address SCVal
// without going through the full Spec-driven ToJSON path (e.g. domainmgr's SaladinFactory.register
// event consumer, chapter 14 step 5, which only needs this one conversion for a known, fixed shape).
func AddressToStrkey(addr xdr.ScAddress) (string, error) {
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
