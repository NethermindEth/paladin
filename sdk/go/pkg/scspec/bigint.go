// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package scspec

import (
	"fmt"
	"math/big"

	"github.com/stellar/go-stellar-sdk/xdr"
)

var (
	mask64  = new(big.Int).SetUint64(^uint64(0))
	pow64   = new(big.Int).Lsh(big.NewInt(1), 64)
	pow128  = new(big.Int).Lsh(big.NewInt(1), 128)
	pow256  = new(big.Int).Lsh(big.NewInt(1), 256)
	bigZero = big.NewInt(0)
)

func words64(u *big.Int, n int) []uint64 {
	out := make([]uint64, n)
	rest := new(big.Int).Set(u)
	for i := n - 1; i >= 0; i-- {
		w := new(big.Int).And(rest, mask64)
		out[i] = w.Uint64()
		rest.Rsh(rest, 64)
	}
	return out
}

func fromWords64(words ...uint64) *big.Int {
	out := new(big.Int)
	for _, w := range words {
		out.Lsh(out, 64)
		out.Or(out, new(big.Int).SetUint64(w))
	}
	return out
}

func toUnsignedParts(s string, bits int) (*big.Int, error) {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("invalid unsigned integer string %q", s)
	}
	if v.Sign() < 0 {
		return nil, fmt.Errorf("value %q is negative, expected unsigned", s)
	}
	limit := new(big.Int).Lsh(big.NewInt(1), uint(bits))
	if v.Cmp(limit) >= 0 {
		return nil, fmt.Errorf("value %q out of range for u%d", s, bits)
	}
	return v, nil
}

func toSignedParts(s string, bits int) (*big.Int, error) {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("invalid signed integer string %q", s)
	}
	half := new(big.Int).Lsh(big.NewInt(1), uint(bits-1))
	minV := new(big.Int).Neg(half)
	maxV := new(big.Int).Sub(half, big.NewInt(1))
	if v.Cmp(minV) < 0 || v.Cmp(maxV) > 0 {
		return nil, fmt.Errorf("value %q out of range for i%d", s, bits)
	}
	if v.Sign() < 0 {
		mod := new(big.Int).Lsh(big.NewInt(1), uint(bits))
		v = new(big.Int).Add(mod, v)
	}
	return v, nil
}

// signedFromPattern interprets a bits-wide unsigned two's-complement bit pattern as a signed value.
func signedFromPattern(pattern *big.Int, bits int) *big.Int {
	out := new(big.Int).Set(pattern)
	if out.Bit(bits-1) == 1 {
		mod := new(big.Int).Lsh(big.NewInt(1), uint(bits))
		out.Sub(out, mod)
	}
	return out
}

func u128ToParts(s string) (xdr.UInt128Parts, error) {
	v, err := toUnsignedParts(s, 128)
	if err != nil {
		return xdr.UInt128Parts{}, err
	}
	w := words64(v, 2)
	return xdr.UInt128Parts{Hi: xdr.Uint64(w[0]), Lo: xdr.Uint64(w[1])}, nil
}

func partsToU128(p xdr.UInt128Parts) string {
	return fromWords64(uint64(p.Hi), uint64(p.Lo)).String()
}

func i128ToParts(s string) (xdr.Int128Parts, error) {
	v, err := toSignedParts(s, 128)
	if err != nil {
		return xdr.Int128Parts{}, err
	}
	w := words64(v, 2)
	return xdr.Int128Parts{Hi: xdr.Int64(int64(w[0])), Lo: xdr.Uint64(w[1])}, nil
}

func partsToI128(p xdr.Int128Parts) string {
	pattern := fromWords64(uint64(p.Hi), uint64(p.Lo))
	return signedFromPattern(pattern, 128).String()
}

func u256ToParts(s string) (xdr.UInt256Parts, error) {
	v, err := toUnsignedParts(s, 256)
	if err != nil {
		return xdr.UInt256Parts{}, err
	}
	w := words64(v, 4)
	return xdr.UInt256Parts{HiHi: xdr.Uint64(w[0]), HiLo: xdr.Uint64(w[1]), LoHi: xdr.Uint64(w[2]), LoLo: xdr.Uint64(w[3])}, nil
}

func partsToU256(p xdr.UInt256Parts) string {
	return fromWords64(uint64(p.HiHi), uint64(p.HiLo), uint64(p.LoHi), uint64(p.LoLo)).String()
}

func i256ToParts(s string) (xdr.Int256Parts, error) {
	v, err := toSignedParts(s, 256)
	if err != nil {
		return xdr.Int256Parts{}, err
	}
	w := words64(v, 4)
	return xdr.Int256Parts{HiHi: xdr.Int64(int64(w[0])), HiLo: xdr.Uint64(w[1]), LoHi: xdr.Uint64(w[2]), LoLo: xdr.Uint64(w[3])}, nil
}

func partsToI256(p xdr.Int256Parts) string {
	pattern := fromWords64(uint64(p.HiHi), uint64(p.HiLo), uint64(p.LoHi), uint64(p.LoLo))
	return signedFromPattern(pattern, 256).String()
}
