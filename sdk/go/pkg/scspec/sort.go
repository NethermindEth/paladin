// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package scspec

import (
	"bytes"
	"sort"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// sortScMap sorts map entries the same way Soroban itself does when a #[contracttype] struct's
// named fields (or an ordinary Map value) are encoded: primarily by the key's ScValType
// discriminant, then by the key's own value. This is exact for the dominant case of
// Symbol-keyed maps (every named-struct field key, and the common case of an explicit Map
// with uniformly-typed keys) - plain string comparison of the Symbol/String text. For map keys
// of a composite type (Vec/Map/Address/etc, rare in practice), comparison falls back to a
// canonical-XDR byte comparison, which is a reasonable total order but not proven identical to
// Soroban's own recursive Ord in every case - flagged here rather than silently assumed correct.
func sortScMap(m xdr.ScMap) {
	sort.SliceStable(m, func(i, j int) bool {
		return compareScVal(m[i].Key, m[j].Key) < 0
	})
}

func compareScVal(a, b xdr.ScVal) int {
	if a.Type != b.Type {
		if a.Type < b.Type {
			return -1
		}
		return 1
	}
	switch a.Type {
	case xdr.ScValTypeScvBool:
		return compareBool(a.B != nil && *a.B, b.B != nil && *b.B)
	case xdr.ScValTypeScvU32:
		return compareUint64(uint64(derefU32(a.U32)), uint64(derefU32(b.U32)))
	case xdr.ScValTypeScvI32:
		return compareInt64(int64(derefI32(a.I32)), int64(derefI32(b.I32)))
	case xdr.ScValTypeScvU64:
		return compareUint64(uint64(derefU64(a.U64)), uint64(derefU64(b.U64)))
	case xdr.ScValTypeScvI64:
		return compareInt64(int64(derefI64(a.I64)), int64(derefI64(b.I64)))
	case xdr.ScValTypeScvString:
		return compareStr(string(derefStr(a.Str)), string(derefStr(b.Str)))
	case xdr.ScValTypeScvSymbol:
		return compareStr(string(derefSym(a.Sym)), string(derefSym(b.Sym)))
	case xdr.ScValTypeScvBytes:
		return bytes.Compare(derefBytes(a.Bytes), derefBytes(b.Bytes))
	default:
		return bytes.Compare(mustCanonicalXDR(a), mustCanonicalXDR(b))
	}
}

func mustCanonicalXDR(v xdr.ScVal) []byte {
	b, err := v.MarshalBinary()
	if err != nil {
		return nil
	}
	return b
}

func compareBool(a, b bool) int {
	if a == b {
		return 0
	}
	if !a {
		return -1
	}
	return 1
}

func compareUint64(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func compareStr(a, b string) int {
	return bytes.Compare([]byte(a), []byte(b))
}

func derefU32(p *xdr.Uint32) xdr.Uint32 {
	if p == nil {
		return 0
	}
	return *p
}

func derefI32(p *xdr.Int32) xdr.Int32 {
	if p == nil {
		return 0
	}
	return *p
}

func derefU64(p *xdr.Uint64) xdr.Uint64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefI64(p *xdr.Int64) xdr.Int64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefStr(p *xdr.ScString) xdr.ScString {
	if p == nil {
		return ""
	}
	return *p
}

func derefSym(p *xdr.ScSymbol) xdr.ScSymbol {
	if p == nil {
		return ""
	}
	return *p
}

func derefBytes(p *xdr.ScBytes) []byte {
	if p == nil {
		return nil
	}
	return *p
}
