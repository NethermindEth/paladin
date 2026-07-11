// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package scspec

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/stellar/go-stellar-sdk/xdr"
)

var jsonNull = []byte("null")

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), jsonNull)
}

// FromJSON converts a JSON value to an xdr.ScVal per the given SEP-48 type definition, following
// stellar-cli/soroban-cli's own soroban-spec-tools JSON conventions (see package doc).
func (s *Spec) FromJSON(value json.RawMessage, t xdr.ScSpecTypeDef) (xdr.ScVal, error) {
	switch t.Type {
	case xdr.ScSpecTypeScSpecTypeBool:
		var v bool
		if err := json.Unmarshal(value, &v); err != nil {
			return xdr.ScVal{}, fmt.Errorf("bool: %w", err)
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvBool, B: &v}, nil

	case xdr.ScSpecTypeScSpecTypeVoid:
		if !isJSONNull(value) {
			return xdr.ScVal{}, fmt.Errorf("void: expected JSON null")
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvVoid}, nil

	case xdr.ScSpecTypeScSpecTypeU32:
		var v xdr.Uint32
		if err := json.Unmarshal(value, &v); err != nil {
			return xdr.ScVal{}, fmt.Errorf("u32: %w", err)
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &v}, nil

	case xdr.ScSpecTypeScSpecTypeI32:
		var v xdr.Int32
		if err := json.Unmarshal(value, &v); err != nil {
			return xdr.ScVal{}, fmt.Errorf("i32: %w", err)
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvI32, I32: &v}, nil

	case xdr.ScSpecTypeScSpecTypeU64:
		var v xdr.Uint64
		if err := json.Unmarshal(value, &v); err != nil {
			return xdr.ScVal{}, fmt.Errorf("u64: %w", err)
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvU64, U64: &v}, nil

	case xdr.ScSpecTypeScSpecTypeI64:
		var v xdr.Int64
		if err := json.Unmarshal(value, &v); err != nil {
			return xdr.ScVal{}, fmt.Errorf("i64: %w", err)
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvI64, I64: &v}, nil

	case xdr.ScSpecTypeScSpecTypeTimepoint:
		var v xdr.Uint64
		if err := json.Unmarshal(value, &v); err != nil {
			return xdr.ScVal{}, fmt.Errorf("timepoint: %w", err)
		}
		tp := xdr.TimePoint(v)
		return xdr.ScVal{Type: xdr.ScValTypeScvTimepoint, Timepoint: &tp}, nil

	case xdr.ScSpecTypeScSpecTypeDuration:
		var v xdr.Uint64
		if err := json.Unmarshal(value, &v); err != nil {
			return xdr.ScVal{}, fmt.Errorf("duration: %w", err)
		}
		d := xdr.Duration(v)
		return xdr.ScVal{Type: xdr.ScValTypeScvDuration, Duration: &d}, nil

	case xdr.ScSpecTypeScSpecTypeU128:
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			return xdr.ScVal{}, fmt.Errorf("u128: expected decimal string: %w", err)
		}
		parts, err := u128ToParts(s)
		if err != nil {
			return xdr.ScVal{}, fmt.Errorf("u128: %w", err)
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvU128, U128: &parts}, nil

	case xdr.ScSpecTypeScSpecTypeI128:
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			return xdr.ScVal{}, fmt.Errorf("i128: expected decimal string: %w", err)
		}
		parts, err := i128ToParts(s)
		if err != nil {
			return xdr.ScVal{}, fmt.Errorf("i128: %w", err)
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvI128, I128: &parts}, nil

	case xdr.ScSpecTypeScSpecTypeU256:
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			return xdr.ScVal{}, fmt.Errorf("u256: expected decimal string: %w", err)
		}
		parts, err := u256ToParts(s)
		if err != nil {
			return xdr.ScVal{}, fmt.Errorf("u256: %w", err)
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvU256, U256: &parts}, nil

	case xdr.ScSpecTypeScSpecTypeI256:
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			return xdr.ScVal{}, fmt.Errorf("i256: expected decimal string: %w", err)
		}
		parts, err := i256ToParts(s)
		if err != nil {
			return xdr.ScVal{}, fmt.Errorf("i256: %w", err)
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvI256, I256: &parts}, nil

	case xdr.ScSpecTypeScSpecTypeBytes:
		b, err := bytesFromJSON(value)
		if err != nil {
			return xdr.ScVal{}, fmt.Errorf("bytes: %w", err)
		}
		bs := xdr.ScBytes(b)
		return xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &bs}, nil

	case xdr.ScSpecTypeScSpecTypeBytesN:
		if t.BytesN == nil {
			return xdr.ScVal{}, fmt.Errorf("bytesN: missing size")
		}
		n := int(t.BytesN.N)
		b, err := bytesNFromJSON(value, n)
		if err != nil {
			return xdr.ScVal{}, fmt.Errorf("bytesN(%d): %w", n, err)
		}
		bs := xdr.ScBytes(b)
		return xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &bs}, nil

	case xdr.ScSpecTypeScSpecTypeString:
		var v string
		if err := json.Unmarshal(value, &v); err != nil {
			return xdr.ScVal{}, fmt.Errorf("string: %w", err)
		}
		sv := xdr.ScString(v)
		return xdr.ScVal{Type: xdr.ScValTypeScvString, Str: &sv}, nil

	case xdr.ScSpecTypeScSpecTypeSymbol:
		var v string
		if err := json.Unmarshal(value, &v); err != nil {
			return xdr.ScVal{}, fmt.Errorf("symbol: %w", err)
		}
		sym := xdr.ScSymbol(v)
		return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}, nil

	case xdr.ScSpecTypeScSpecTypeAddress:
		var v string
		if err := json.Unmarshal(value, &v); err != nil {
			return xdr.ScVal{}, fmt.Errorf("address: %w", err)
		}
		addr, err := addressFromStrkey(v)
		if err != nil {
			return xdr.ScVal{}, fmt.Errorf("address: %w", err)
		}
		return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &addr}, nil

	case xdr.ScSpecTypeScSpecTypeOption:
		if t.Option == nil {
			return xdr.ScVal{}, fmt.Errorf("option: missing value type")
		}
		if isJSONNull(value) {
			return xdr.ScVal{Type: xdr.ScValTypeScvVoid}, nil
		}
		return s.FromJSON(value, t.Option.ValueType)

	case xdr.ScSpecTypeScSpecTypeVec:
		if t.Vec == nil {
			return xdr.ScVal{}, fmt.Errorf("vec: missing element type")
		}
		var items []json.RawMessage
		if err := json.Unmarshal(value, &items); err != nil {
			return xdr.ScVal{}, fmt.Errorf("vec: expected JSON array: %w", err)
		}
		vec, err := s.rawArrayToVec(items, t.Vec.ElementType)
		if err != nil {
			return xdr.ScVal{}, fmt.Errorf("vec: %w", err)
		}
		return vecScVal(vec), nil

	case xdr.ScSpecTypeScSpecTypeTuple:
		if t.Tuple == nil {
			return xdr.ScVal{}, fmt.Errorf("tuple: missing element types")
		}
		var items []json.RawMessage
		if err := json.Unmarshal(value, &items); err != nil {
			return xdr.ScVal{}, fmt.Errorf("tuple: expected JSON array: %w", err)
		}
		if len(items) != len(t.Tuple.ValueTypes) {
			return xdr.ScVal{}, fmt.Errorf("tuple: expected %d elements, got %d", len(t.Tuple.ValueTypes), len(items))
		}
		vec := make(xdr.ScVec, len(items))
		for i, item := range items {
			v, err := s.FromJSON(item, t.Tuple.ValueTypes[i])
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("tuple[%d]: %w", i, err)
			}
			vec[i] = v
		}
		return vecScVal(vec), nil

	case xdr.ScSpecTypeScSpecTypeMap:
		if t.Map == nil {
			return xdr.ScVal{}, fmt.Errorf("map: missing key/value types")
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(value, &obj); err != nil {
			return xdr.ScVal{}, fmt.Errorf("map: expected JSON object: %w", err)
		}
		m, err := s.rawObjectToMap(obj, t.Map.KeyType, t.Map.ValueType)
		if err != nil {
			return xdr.ScVal{}, fmt.Errorf("map: %w", err)
		}
		return mapScVal(m), nil

	case xdr.ScSpecTypeScSpecTypeUdt:
		if t.Udt == nil {
			return xdr.ScVal{}, fmt.Errorf("udt: missing name")
		}
		return s.udtFromJSON(value, t.Udt.Name)

	default:
		return xdr.ScVal{}, fmt.Errorf("unsupported spec type %v", t.Type)
	}
}

func bytesFromJSON(value json.RawMessage) ([]byte, error) {
	var s string
	if err := json.Unmarshal(value, &s); err != nil {
		return nil, fmt.Errorf("expected hex string: %w", err)
	}
	return hex.DecodeString(s)
}

// bytesNFromJSON mirrors soroban-spec-tools: a 32-byte BytesN first tries strkey address
// decoding (G.../C.../M...) before falling back to a plain hex string.
func bytesNFromJSON(value json.RawMessage, n int) ([]byte, error) {
	var s string
	if err := json.Unmarshal(value, &s); err != nil {
		return nil, fmt.Errorf("expected string: %w", err)
	}
	if n == 32 {
		if addr, err := addressFromStrkey(s); err == nil {
			raw, rerr := addressRawBytes(addr)
			if rerr == nil {
				return raw, nil
			}
		}
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("expected hex string: %w", err)
	}
	if len(b) != n {
		return nil, fmt.Errorf("expected %d bytes, got %d", n, len(b))
	}
	return b, nil
}

func addressRawBytes(addr xdr.ScAddress) ([]byte, error) {
	switch addr.Type {
	case xdr.ScAddressTypeScAddressTypeAccount:
		if addr.AccountId == nil || addr.AccountId.Ed25519 == nil {
			return nil, fmt.Errorf("malformed account address")
		}
		b := (*addr.AccountId.Ed25519)[:]
		return append([]byte(nil), b...), nil
	case xdr.ScAddressTypeScAddressTypeContract:
		if addr.ContractId == nil {
			return nil, fmt.Errorf("malformed contract address")
		}
		b := (*addr.ContractId)[:]
		return append([]byte(nil), b...), nil
	default:
		return nil, fmt.Errorf("address type %v has no fixed 32-byte representation", addr.Type)
	}
}

func (s *Spec) rawArrayToVec(items []json.RawMessage, elemType xdr.ScSpecTypeDef) (xdr.ScVec, error) {
	vec := make(xdr.ScVec, len(items))
	for i, item := range items {
		v, err := s.FromJSON(item, elemType)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		vec[i] = v
	}
	return vec, nil
}

func (s *Spec) rawObjectToMap(obj map[string]json.RawMessage, keyType, valType xdr.ScSpecTypeDef) (xdr.ScMap, error) {
	m := make(xdr.ScMap, 0, len(obj))
	for k, raw := range obj {
		keyRaw, err := jsonRawFromMapKey(k, keyType)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", k, err)
		}
		keyVal, err := s.FromJSON(keyRaw, keyType)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", k, err)
		}
		valVal, err := s.FromJSON(raw, valType)
		if err != nil {
			return nil, fmt.Errorf("value for key %q: %w", k, err)
		}
		m = append(m, xdr.ScMapEntry{Key: keyVal, Val: valVal})
	}
	sortScMap(m)
	return m, nil
}

// jsonRawFromMapKey re-wraps a JSON object's (always-string) key into the JSON shape FromJSON
// expects for keyType - a quoted string for string-shaped types, the bare text otherwise (e.g. a
// numeric key "5" becomes the JSON number literal 5).
func jsonRawFromMapKey(key string, keyType xdr.ScSpecTypeDef) (json.RawMessage, error) {
	switch keyType.Type {
	case xdr.ScSpecTypeScSpecTypeString, xdr.ScSpecTypeScSpecTypeSymbol, xdr.ScSpecTypeScSpecTypeAddress,
		xdr.ScSpecTypeScSpecTypeBytes, xdr.ScSpecTypeScSpecTypeBytesN,
		xdr.ScSpecTypeScSpecTypeU128, xdr.ScSpecTypeScSpecTypeI128,
		xdr.ScSpecTypeScSpecTypeU256, xdr.ScSpecTypeScSpecTypeI256:
		b, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		return b, nil
	default:
		return json.RawMessage(key), nil
	}
}

func vecScVal(vec xdr.ScVec) xdr.ScVal {
	vp := &vec
	return xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vp}
}

func mapScVal(m xdr.ScMap) xdr.ScVal {
	mp := &m
	return xdr.ScVal{Type: xdr.ScValTypeScvMap, Map: &mp}
}

// udtFromJSON dispatches by the *kind of spec entry found*, not by JSON shape alone (mirroring
// soroban-spec-tools' parse_udt).
func (s *Spec) udtFromJSON(value json.RawMessage, name string) (xdr.ScVal, error) {
	entry, err := s.lookupUdt(name)
	if err != nil {
		return xdr.ScVal{}, err
	}
	switch entry.Kind {
	case xdr.ScSpecEntryKindScSpecEntryUdtStructV0:
		return s.structFromJSON(value, entry.UdtStructV0)
	case xdr.ScSpecEntryKindScSpecEntryUdtUnionV0:
		return s.unionFromJSON(value, entry.UdtUnionV0)
	case xdr.ScSpecEntryKindScSpecEntryUdtEnumV0:
		return constEnumFromJSON(value, entry.UdtEnumV0)
	default:
		return xdr.ScVal{}, fmt.Errorf("udt %q: unsupported entry kind %v (error enums are not supported)", name, entry.Kind)
	}
}

func isTupleStruct(fields []xdr.ScSpecUdtStructFieldV0) bool {
	for _, f := range fields {
		if f.Name == "0" {
			return true
		}
	}
	return false
}

func (s *Spec) structFromJSON(value json.RawMessage, def *xdr.ScSpecUdtStructV0) (xdr.ScVal, error) {
	if isTupleStruct(def.Fields) || isJSONArray(value) {
		var items []json.RawMessage
		if err := json.Unmarshal(value, &items); err != nil {
			return xdr.ScVal{}, fmt.Errorf("tuple struct %q: expected JSON array: %w", def.Name, err)
		}
		if len(items) != len(def.Fields) {
			return xdr.ScVal{}, fmt.Errorf("tuple struct %q: expected %d fields, got %d", def.Name, len(def.Fields), len(items))
		}
		vec := make(xdr.ScVec, len(items))
		for i, item := range items {
			v, err := s.FromJSON(item, def.Fields[i].Type)
			if err != nil {
				return xdr.ScVal{}, fmt.Errorf("tuple struct %q[%d]: %w", def.Name, i, err)
			}
			vec[i] = v
		}
		return vecScVal(vec), nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(value, &obj); err != nil {
		return xdr.ScVal{}, fmt.Errorf("struct %q: expected JSON object: %w", def.Name, err)
	}
	m := make(xdr.ScMap, 0, len(def.Fields))
	for _, f := range def.Fields {
		raw, ok := obj[f.Name]
		if !ok {
			return xdr.ScVal{}, fmt.Errorf("struct %q: missing field %q", def.Name, f.Name)
		}
		v, err := s.FromJSON(raw, f.Type)
		if err != nil {
			return xdr.ScVal{}, fmt.Errorf("struct %q.%s: %w", def.Name, f.Name, err)
		}
		sym := xdr.ScSymbol(f.Name)
		m = append(m, xdr.ScMapEntry{Key: xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}, Val: v})
	}
	sortScMap(m)
	return mapScVal(m), nil
}

func isJSONArray(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	return len(trimmed) > 0 && trimmed[0] == '['
}

func (s *Spec) unionFromJSON(value json.RawMessage, def *xdr.ScSpecUdtUnionV0) (xdr.ScVal, error) {
	// Void case: a bare JSON string naming the case.
	var caseName string
	if err := json.Unmarshal(value, &caseName); err == nil {
		for _, c := range def.Cases {
			if c.Kind == xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseVoidV0 && c.VoidCase.Name == caseName {
				sym := xdr.ScSymbol(caseName)
				return vecScVal(xdr.ScVec{{Type: xdr.ScValTypeScvSymbol, Sym: &sym}}), nil
			}
		}
		return xdr.ScVal{}, fmt.Errorf("union %q: no void case named %q", def.Name, caseName)
	}
	// Case with data: a single-key JSON object {"CaseName": value-or-array}.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(value, &obj); err != nil {
		return xdr.ScVal{}, fmt.Errorf("union %q: expected a case-name string or single-key object: %w", def.Name, err)
	}
	if len(obj) != 1 {
		return xdr.ScVal{}, fmt.Errorf("union %q: expected exactly one case key, got %d", def.Name, len(obj))
	}
	for k, raw := range obj {
		for _, c := range def.Cases {
			if c.Kind != xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseTupleV0 || c.TupleCase.Name != k {
				continue
			}
			types := c.TupleCase.Type
			var payload []xdr.ScVal
			if len(types) == 1 {
				v, err := s.FromJSON(raw, types[0])
				if err != nil {
					return xdr.ScVal{}, fmt.Errorf("union %q case %q: %w", def.Name, k, err)
				}
				payload = []xdr.ScVal{v}
			} else {
				var items []json.RawMessage
				if err := json.Unmarshal(raw, &items); err != nil {
					return xdr.ScVal{}, fmt.Errorf("union %q case %q: expected JSON array: %w", def.Name, k, err)
				}
				if len(items) != len(types) {
					return xdr.ScVal{}, fmt.Errorf("union %q case %q: expected %d values, got %d", def.Name, k, len(types), len(items))
				}
				payload = make([]xdr.ScVal, len(items))
				for i, item := range items {
					v, err := s.FromJSON(item, types[i])
					if err != nil {
						return xdr.ScVal{}, fmt.Errorf("union %q case %q[%d]: %w", def.Name, k, i, err)
					}
					payload[i] = v
				}
			}
			sym := xdr.ScSymbol(k)
			vec := append(xdr.ScVec{{Type: xdr.ScValTypeScvSymbol, Sym: &sym}}, payload...)
			return vecScVal(vec), nil
		}
		return xdr.ScVal{}, fmt.Errorf("union %q: no tuple case named %q", def.Name, k)
	}
	return xdr.ScVal{}, fmt.Errorf("union %q: empty case object", def.Name)
}

func constEnumFromJSON(value json.RawMessage, def *xdr.ScSpecUdtEnumV0) (xdr.ScVal, error) {
	var n xdr.Uint32
	if err := json.Unmarshal(value, &n); err != nil {
		return xdr.ScVal{}, fmt.Errorf("enum %q: expected JSON number: %w", def.Name, err)
	}
	for _, c := range def.Cases {
		if c.Value == n {
			return xdr.ScVal{Type: xdr.ScValTypeScvU32, U32: &n}, nil
		}
	}
	return xdr.ScVal{}, fmt.Errorf("enum %q: %d is not a declared case value", def.Name, n)
}
