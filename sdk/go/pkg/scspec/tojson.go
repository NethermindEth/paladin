// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package scspec

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// ToJSON converts an xdr.ScVal to a JSON value per the given SEP-48 type definition - the exact
// reverse of FromJSON, so a value round-trips through FromJSON(ToJSON(v)) == v.
func (s *Spec) ToJSON(val xdr.ScVal, t xdr.ScSpecTypeDef) (json.RawMessage, error) {
	switch t.Type {
	case xdr.ScSpecTypeScSpecTypeBool:
		if val.B == nil {
			return nil, fmt.Errorf("bool: missing value")
		}
		return json.Marshal(*val.B)

	case xdr.ScSpecTypeScSpecTypeVoid:
		return json.RawMessage("null"), nil

	case xdr.ScSpecTypeScSpecTypeU32:
		if val.U32 == nil {
			return nil, fmt.Errorf("u32: missing value")
		}
		return json.Marshal(*val.U32)

	case xdr.ScSpecTypeScSpecTypeI32:
		if val.I32 == nil {
			return nil, fmt.Errorf("i32: missing value")
		}
		return json.Marshal(*val.I32)

	case xdr.ScSpecTypeScSpecTypeU64:
		if val.U64 == nil {
			return nil, fmt.Errorf("u64: missing value")
		}
		return json.Marshal(*val.U64)

	case xdr.ScSpecTypeScSpecTypeI64:
		if val.I64 == nil {
			return nil, fmt.Errorf("i64: missing value")
		}
		return json.Marshal(*val.I64)

	case xdr.ScSpecTypeScSpecTypeTimepoint:
		if val.Timepoint == nil {
			return nil, fmt.Errorf("timepoint: missing value")
		}
		return json.Marshal(uint64(*val.Timepoint))

	case xdr.ScSpecTypeScSpecTypeDuration:
		if val.Duration == nil {
			return nil, fmt.Errorf("duration: missing value")
		}
		return json.Marshal(uint64(*val.Duration))

	case xdr.ScSpecTypeScSpecTypeU128:
		if val.U128 == nil {
			return nil, fmt.Errorf("u128: missing value")
		}
		return json.Marshal(partsToU128(*val.U128))

	case xdr.ScSpecTypeScSpecTypeI128:
		if val.I128 == nil {
			return nil, fmt.Errorf("i128: missing value")
		}
		return json.Marshal(partsToI128(*val.I128))

	case xdr.ScSpecTypeScSpecTypeU256:
		if val.U256 == nil {
			return nil, fmt.Errorf("u256: missing value")
		}
		return json.Marshal(partsToU256(*val.U256))

	case xdr.ScSpecTypeScSpecTypeI256:
		if val.I256 == nil {
			return nil, fmt.Errorf("i256: missing value")
		}
		return json.Marshal(partsToI256(*val.I256))

	case xdr.ScSpecTypeScSpecTypeBytes:
		if val.Bytes == nil {
			return nil, fmt.Errorf("bytes: missing value")
		}
		return json.Marshal(hex.EncodeToString(*val.Bytes))

	case xdr.ScSpecTypeScSpecTypeBytesN:
		if val.Bytes == nil {
			return nil, fmt.Errorf("bytesN: missing value")
		}
		return json.Marshal(hex.EncodeToString(*val.Bytes))

	case xdr.ScSpecTypeScSpecTypeString:
		if val.Str == nil {
			return nil, fmt.Errorf("string: missing value")
		}
		return json.Marshal(string(*val.Str))

	case xdr.ScSpecTypeScSpecTypeSymbol:
		if val.Sym == nil {
			return nil, fmt.Errorf("symbol: missing value")
		}
		return json.Marshal(string(*val.Sym))

	case xdr.ScSpecTypeScSpecTypeAddress:
		if val.Address == nil {
			return nil, fmt.Errorf("address: missing value")
		}
		addr, err := AddressToStrkey(*val.Address)
		if err != nil {
			return nil, fmt.Errorf("address: %w", err)
		}
		return json.Marshal(addr)

	case xdr.ScSpecTypeScSpecTypeOption:
		if t.Option == nil {
			return nil, fmt.Errorf("option: missing value type")
		}
		if val.Type == xdr.ScValTypeScvVoid {
			return json.RawMessage("null"), nil
		}
		return s.ToJSON(val, t.Option.ValueType)

	case xdr.ScSpecTypeScSpecTypeVec:
		if t.Vec == nil {
			return nil, fmt.Errorf("vec: missing element type")
		}
		vec, err := scValVec(val)
		if err != nil {
			return nil, fmt.Errorf("vec: %w", err)
		}
		return s.vecToJSONArray(vec, t.Vec.ElementType)

	case xdr.ScSpecTypeScSpecTypeTuple:
		if t.Tuple == nil {
			return nil, fmt.Errorf("tuple: missing element types")
		}
		vec, err := scValVec(val)
		if err != nil {
			return nil, fmt.Errorf("tuple: %w", err)
		}
		if len(vec) != len(t.Tuple.ValueTypes) {
			return nil, fmt.Errorf("tuple: expected %d elements, got %d", len(t.Tuple.ValueTypes), len(vec))
		}
		items := make([]json.RawMessage, len(vec))
		for i, v := range vec {
			raw, err := s.ToJSON(v, t.Tuple.ValueTypes[i])
			if err != nil {
				return nil, fmt.Errorf("tuple[%d]: %w", i, err)
			}
			items[i] = raw
		}
		return json.Marshal(items)

	case xdr.ScSpecTypeScSpecTypeMap:
		if t.Map == nil {
			return nil, fmt.Errorf("map: missing key/value types")
		}
		m, err := scValMap(val)
		if err != nil {
			return nil, fmt.Errorf("map: %w", err)
		}
		return s.mapToJSONObject(m, t.Map.KeyType, t.Map.ValueType)

	case xdr.ScSpecTypeScSpecTypeUdt:
		if t.Udt == nil {
			return nil, fmt.Errorf("udt: missing name")
		}
		return s.udtToJSON(val, t.Udt.Name)

	default:
		return nil, fmt.Errorf("unsupported spec type %v", t.Type)
	}
}

func scValVec(val xdr.ScVal) (xdr.ScVec, error) {
	if val.Vec == nil || *val.Vec == nil {
		return nil, fmt.Errorf("expected ScVal Vec")
	}
	return **val.Vec, nil
}

func scValMap(val xdr.ScVal) (xdr.ScMap, error) {
	if val.Map == nil || *val.Map == nil {
		return nil, fmt.Errorf("expected ScVal Map")
	}
	return **val.Map, nil
}

func (s *Spec) vecToJSONArray(vec xdr.ScVec, elemType xdr.ScSpecTypeDef) (json.RawMessage, error) {
	items := make([]json.RawMessage, len(vec))
	for i, v := range vec {
		raw, err := s.ToJSON(v, elemType)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		items[i] = raw
	}
	return json.Marshal(items)
}

func (s *Spec) mapToJSONObject(m xdr.ScMap, keyType, valType xdr.ScSpecTypeDef) (json.RawMessage, error) {
	obj := make(map[string]json.RawMessage, len(m))
	for _, entry := range m {
		key, err := mapKeyToString(entry.Key, keyType)
		if err != nil {
			return nil, err
		}
		raw, err := s.ToJSON(entry.Val, valType)
		if err != nil {
			return nil, fmt.Errorf("value for key %q: %w", key, err)
		}
		obj[key] = raw
	}
	return json.Marshal(obj)
}

func mapKeyToString(key xdr.ScVal, keyType xdr.ScSpecTypeDef) (string, error) {
	switch keyType.Type {
	case xdr.ScSpecTypeScSpecTypeSymbol:
		if key.Sym == nil {
			return "", fmt.Errorf("map key: missing symbol")
		}
		return string(*key.Sym), nil
	case xdr.ScSpecTypeScSpecTypeString:
		if key.Str == nil {
			return "", fmt.Errorf("map key: missing string")
		}
		return string(*key.Str), nil
	case xdr.ScSpecTypeScSpecTypeU32:
		if key.U32 == nil {
			return "", fmt.Errorf("map key: missing u32")
		}
		return fmt.Sprintf("%d", *key.U32), nil
	case xdr.ScSpecTypeScSpecTypeI32:
		if key.I32 == nil {
			return "", fmt.Errorf("map key: missing i32")
		}
		return fmt.Sprintf("%d", *key.I32), nil
	case xdr.ScSpecTypeScSpecTypeU64:
		if key.U64 == nil {
			return "", fmt.Errorf("map key: missing u64")
		}
		return fmt.Sprintf("%d", *key.U64), nil
	case xdr.ScSpecTypeScSpecTypeI64:
		if key.I64 == nil {
			return "", fmt.Errorf("map key: missing i64")
		}
		return fmt.Sprintf("%d", *key.I64), nil
	default:
		return "", fmt.Errorf("unsupported map key type %v for JSON object rendering", keyType.Type)
	}
}

func (s *Spec) udtToJSON(val xdr.ScVal, name string) (json.RawMessage, error) {
	entry, err := s.lookupUdt(name)
	if err != nil {
		return nil, err
	}
	switch entry.Kind {
	case xdr.ScSpecEntryKindScSpecEntryUdtStructV0:
		return s.structToJSON(val, entry.UdtStructV0)
	case xdr.ScSpecEntryKindScSpecEntryUdtUnionV0:
		return s.unionToJSON(val, entry.UdtUnionV0)
	case xdr.ScSpecEntryKindScSpecEntryUdtEnumV0:
		return constEnumToJSON(val, entry.UdtEnumV0)
	default:
		return nil, fmt.Errorf("udt %q: unsupported entry kind %v (error enums are not supported)", name, entry.Kind)
	}
}

func (s *Spec) structToJSON(val xdr.ScVal, def *xdr.ScSpecUdtStructV0) (json.RawMessage, error) {
	if isTupleStruct(def.Fields) {
		vec, err := scValVec(val)
		if err != nil {
			return nil, fmt.Errorf("tuple struct %q: %w", def.Name, err)
		}
		if len(vec) != len(def.Fields) {
			return nil, fmt.Errorf("tuple struct %q: expected %d fields, got %d", def.Name, len(def.Fields), len(vec))
		}
		items := make([]json.RawMessage, len(vec))
		for i, v := range vec {
			raw, err := s.ToJSON(v, def.Fields[i].Type)
			if err != nil {
				return nil, fmt.Errorf("tuple struct %q[%d]: %w", def.Name, i, err)
			}
			items[i] = raw
		}
		return json.Marshal(items)
	}
	m, err := scValMap(val)
	if err != nil {
		return nil, fmt.Errorf("struct %q: %w", def.Name, err)
	}
	byName := make(map[string]xdr.ScVal, len(m))
	for _, entry := range m {
		if entry.Key.Sym == nil {
			return nil, fmt.Errorf("struct %q: map key is not a symbol", def.Name)
		}
		byName[string(*entry.Key.Sym)] = entry.Val
	}
	obj := make(map[string]json.RawMessage, len(def.Fields))
	for _, f := range def.Fields {
		v, ok := byName[f.Name]
		if !ok {
			return nil, fmt.Errorf("struct %q: missing field %q", def.Name, f.Name)
		}
		raw, err := s.ToJSON(v, f.Type)
		if err != nil {
			return nil, fmt.Errorf("struct %q.%s: %w", def.Name, f.Name, err)
		}
		obj[f.Name] = raw
	}
	return json.Marshal(obj)
}

func (s *Spec) unionToJSON(val xdr.ScVal, def *xdr.ScSpecUdtUnionV0) (json.RawMessage, error) {
	vec, err := scValVec(val)
	if err != nil {
		return nil, fmt.Errorf("union %q: %w", def.Name, err)
	}
	if len(vec) == 0 || vec[0].Sym == nil {
		return nil, fmt.Errorf("union %q: expected [Symbol(caseName), ...]", def.Name)
	}
	caseName := string(*vec[0].Sym)
	for _, c := range def.Cases {
		switch c.Kind {
		case xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseVoidV0:
			if c.VoidCase.Name == caseName {
				return json.Marshal(caseName)
			}
		case xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseTupleV0:
			if c.TupleCase.Name != caseName {
				continue
			}
			types := c.TupleCase.Type
			payload := vec[1:]
			if len(payload) != len(types) {
				return nil, fmt.Errorf("union %q case %q: expected %d values, got %d", def.Name, caseName, len(types), len(payload))
			}
			var valueRaw json.RawMessage
			if len(types) == 1 {
				raw, err := s.ToJSON(payload[0], types[0])
				if err != nil {
					return nil, fmt.Errorf("union %q case %q: %w", def.Name, caseName, err)
				}
				valueRaw = raw
			} else {
				items := make([]json.RawMessage, len(payload))
				for i, v := range payload {
					raw, err := s.ToJSON(v, types[i])
					if err != nil {
						return nil, fmt.Errorf("union %q case %q[%d]: %w", def.Name, caseName, i, err)
					}
					items[i] = raw
				}
				b, err := json.Marshal(items)
				if err != nil {
					return nil, err
				}
				valueRaw = b
			}
			return json.Marshal(map[string]json.RawMessage{caseName: valueRaw})
		}
	}
	return nil, fmt.Errorf("union %q: no case named %q", def.Name, caseName)
}

func constEnumToJSON(val xdr.ScVal, def *xdr.ScSpecUdtEnumV0) (json.RawMessage, error) {
	if val.U32 == nil {
		return nil, fmt.Errorf("enum %q: missing value", def.Name)
	}
	for _, c := range def.Cases {
		if c.Value == *val.U32 {
			return json.Marshal(uint32(*val.U32))
		}
	}
	return nil, fmt.Errorf("enum %q: %d is not a declared case value", def.Name, *val.U32)
}
