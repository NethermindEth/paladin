// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package scspec

import (
	"encoding/json"
	"testing"

	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

func td(t xdr.ScSpecType) xdr.ScSpecTypeDef { return xdr.ScSpecTypeDef{Type: t} }

func roundTrip(t *testing.T, s *Spec, def xdr.ScSpecTypeDef, jsonIn string) xdr.ScVal {
	t.Helper()
	val, err := s.FromJSON(json.RawMessage(jsonIn), def)
	require.NoError(t, err)
	out, err := s.ToJSON(val, def)
	require.NoError(t, err)
	require.JSONEq(t, jsonIn, string(out))
	return val
}

func TestPrimitives(t *testing.T) {
	s := &Spec{udts: map[string]xdr.ScSpecEntry{}}

	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeBool), `true`)
	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeBool), `false`)
	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeVoid), `null`)
	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeU32), `42`)
	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeI32), `-42`)
	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeU64), `18446744073709551615`)
	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeI64), `-9223372036854775808`)
	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeTimepoint), `12345`)
	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeDuration), `67890`)
	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeString), `"hello world"`)
	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeSymbol), `"transfer"`)
	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeBytes), `"deadbeef"`)
}

func TestBigIntegers(t *testing.T) {
	s := &Spec{udts: map[string]xdr.ScSpecEntry{}}

	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeU128), `"340282366920938463463374607431768211455"`) // max u128
	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeU128), `"0"`)
	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeI128), `"-170141183460469231731687303715884105728"`) // min i128
	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeI128), `"170141183460469231731687303715884105727"`)  // max i128
	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeI128), `"-1"`)
	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeU256), `"115792089237316195423570985008687907853269984665640564039457584007913129639935"`)
	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeI256), `"-1"`)
	roundTrip(t, s, td(xdr.ScSpecTypeScSpecTypeI256), `"57896044618658097711785492504343953926634992332820282019728792003956564819967"`)

	// Out of range must error, not silently wrap.
	_, err := s.FromJSON(json.RawMessage(`"340282366920938463463374607431768211456"`), td(xdr.ScSpecTypeScSpecTypeU128))
	require.Error(t, err)
	_, err = s.FromJSON(json.RawMessage(`"-1"`), td(xdr.ScSpecTypeScSpecTypeU128))
	require.Error(t, err)
}

func TestBytesN(t *testing.T) {
	s := &Spec{udts: map[string]xdr.ScSpecEntry{}}
	bytesN32 := xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeBytesN, BytesN: &xdr.ScSpecTypeBytesN{N: 32}}
	bytesN4 := xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeBytesN, BytesN: &xdr.ScSpecTypeBytesN{N: 4}}

	// Plain hex for a non-32 length.
	val, err := s.FromJSON(json.RawMessage(`"deadbeef"`), bytesN4)
	require.NoError(t, err)
	require.Equal(t, []byte{0xde, 0xad, 0xbe, 0xef}, []byte(*val.Bytes))

	// 32-byte hex still works when it's not a valid strkey.
	hex32 := `"` + repeatHex("ab", 32) + `"`
	roundTrip(t, s, bytesN32, hex32)

	// 32-byte strkey contract address is accepted and decoded to its raw 32 bytes.
	contractAddr := "CBBEGRCFIZDUQSKKJNGE2TSPKBIVEU2UKVLFOWCZLJNVYXK6L5QGDRBB"
	val, err = s.FromJSON(json.RawMessage(`"`+contractAddr+`"`), bytesN32)
	require.NoError(t, err)
	require.Len(t, []byte(*val.Bytes), 32)
}

func repeatHex(pair string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += pair
	}
	return out
}

func TestAddress(t *testing.T) {
	s := &Spec{udts: map[string]xdr.ScSpecEntry{}}
	addrType := td(xdr.ScSpecTypeScSpecTypeAddress)

	roundTrip(t, s, addrType, `"CBBEGRCFIZDUQSKKJNGE2TSPKBIVEU2UKVLFOWCZLJNVYXK6L5QGDRBB"`)
	roundTrip(t, s, addrType, `"GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"`)
}

func TestOption(t *testing.T) {
	s := &Spec{udts: map[string]xdr.ScSpecEntry{}}
	opt := xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeOption, Option: &xdr.ScSpecTypeOption{ValueType: td(xdr.ScSpecTypeScSpecTypeU32)}}

	roundTrip(t, s, opt, `null`)
	roundTrip(t, s, opt, `7`)
}

func TestVecAndTuple(t *testing.T) {
	s := &Spec{udts: map[string]xdr.ScSpecEntry{}}
	vec := xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeVec, Vec: &xdr.ScSpecTypeVec{ElementType: td(xdr.ScSpecTypeScSpecTypeU32)}}
	roundTrip(t, s, vec, `[]`)
	roundTrip(t, s, vec, `[1,2,3]`)

	tuple := xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeTuple, Tuple: &xdr.ScSpecTypeTuple{
		ValueTypes: []xdr.ScSpecTypeDef{td(xdr.ScSpecTypeScSpecTypeU32), td(xdr.ScSpecTypeScSpecTypeString)},
	}}
	roundTrip(t, s, tuple, `[5,"five"]`)
}

func TestMap(t *testing.T) {
	s := &Spec{udts: map[string]xdr.ScSpecEntry{}}
	m := xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeMap, Map: &xdr.ScSpecTypeMap{
		KeyType:   td(xdr.ScSpecTypeScSpecTypeSymbol),
		ValueType: td(xdr.ScSpecTypeScSpecTypeU32),
	}}
	roundTrip(t, s, m, `{"a":1,"b":2}`)

	// Encoding must sort entries by key.
	val, err := s.FromJSON(json.RawMessage(`{"z":1,"a":2,"m":3}`), m)
	require.NoError(t, err)
	scMap, err := scValMap(val)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "m", "z"}, []string{string(*scMap[0].Key.Sym), string(*scMap[1].Key.Sym), string(*scMap[2].Key.Sym)})
}

func TestNamedStruct(t *testing.T) {
	entry := xdr.ScSpecEntry{
		Kind: xdr.ScSpecEntryKindScSpecEntryUdtStructV0,
		UdtStructV0: &xdr.ScSpecUdtStructV0{
			Name: "Transfer",
			Fields: []xdr.ScSpecUdtStructFieldV0{
				{Name: "amount", Type: td(xdr.ScSpecTypeScSpecTypeU64)},
				{Name: "to", Type: td(xdr.ScSpecTypeScSpecTypeAddress)},
			},
		},
	}
	s := &Spec{udts: map[string]xdr.ScSpecEntry{"Transfer": entry}}
	udt := xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeUdt, Udt: &xdr.ScSpecTypeUdt{Name: "Transfer"}}

	roundTrip(t, s, udt, `{"amount":100,"to":"CBBEGRCFIZDUQSKKJNGE2TSPKBIVEU2UKVLFOWCZLJNVYXK6L5QGDRBB"}`)

	// Encodes as a sorted Map (by field name "amount" < "to"), not a Vec.
	val, err := s.FromJSON(json.RawMessage(`{"amount":100,"to":"CBBEGRCFIZDUQSKKJNGE2TSPKBIVEU2UKVLFOWCZLJNVYXK6L5QGDRBB"}`), udt)
	require.NoError(t, err)
	require.Equal(t, xdr.ScValTypeScvMap, val.Type)
}

func TestTupleStruct(t *testing.T) {
	entry := xdr.ScSpecEntry{
		Kind: xdr.ScSpecEntryKindScSpecEntryUdtStructV0,
		UdtStructV0: &xdr.ScSpecUdtStructV0{
			Name: "TransferPayload",
			Fields: []xdr.ScSpecUdtStructFieldV0{
				{Name: "0", Type: td(xdr.ScSpecTypeScSpecTypeU64)},
				{Name: "1", Type: td(xdr.ScSpecTypeScSpecTypeAddress)},
			},
		},
	}
	s := &Spec{udts: map[string]xdr.ScSpecEntry{"TransferPayload": entry}}
	udt := xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeUdt, Udt: &xdr.ScSpecTypeUdt{Name: "TransferPayload"}}

	roundTrip(t, s, udt, `[100,"CBBEGRCFIZDUQSKKJNGE2TSPKBIVEU2UKVLFOWCZLJNVYXK6L5QGDRBB"]`)

	// Encodes as a Vec (positional), not a Map.
	val, err := s.FromJSON(json.RawMessage(`[100,"CBBEGRCFIZDUQSKKJNGE2TSPKBIVEU2UKVLFOWCZLJNVYXK6L5QGDRBB"]`), udt)
	require.NoError(t, err)
	require.Equal(t, xdr.ScValTypeScvVec, val.Type)
}

func TestUnion(t *testing.T) {
	entry := xdr.ScSpecEntry{
		Kind: xdr.ScSpecEntryKindScSpecEntryUdtUnionV0,
		UdtUnionV0: &xdr.ScSpecUdtUnionV0{
			Name: "Status",
			Cases: []xdr.ScSpecUdtUnionCaseV0{
				{Kind: xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseVoidV0, VoidCase: &xdr.ScSpecUdtUnionCaseVoidV0{Name: "Pending"}},
				{Kind: xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseTupleV0, TupleCase: &xdr.ScSpecUdtUnionCaseTupleV0{
					Name: "Failed", Type: []xdr.ScSpecTypeDef{td(xdr.ScSpecTypeScSpecTypeString)},
				}},
				{Kind: xdr.ScSpecUdtUnionCaseV0KindScSpecUdtUnionCaseTupleV0, TupleCase: &xdr.ScSpecUdtUnionCaseTupleV0{
					Name: "Range", Type: []xdr.ScSpecTypeDef{td(xdr.ScSpecTypeScSpecTypeU32), td(xdr.ScSpecTypeScSpecTypeU32)},
				}},
			},
		},
	}
	s := &Spec{udts: map[string]xdr.ScSpecEntry{"Status": entry}}
	udt := xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeUdt, Udt: &xdr.ScSpecTypeUdt{Name: "Status"}}

	roundTrip(t, s, udt, `"Pending"`)
	roundTrip(t, s, udt, `{"Failed":"timeout"}`)
	roundTrip(t, s, udt, `{"Range":[1,10]}`)
}

func TestConstEnum(t *testing.T) {
	entry := xdr.ScSpecEntry{
		Kind: xdr.ScSpecEntryKindScSpecEntryUdtEnumV0,
		UdtEnumV0: &xdr.ScSpecUdtEnumV0{
			Name: "Color",
			Cases: []xdr.ScSpecUdtEnumCaseV0{
				{Name: "Red", Value: 0},
				{Name: "Green", Value: 1},
			},
		},
	}
	s := &Spec{udts: map[string]xdr.ScSpecEntry{"Color": entry}}
	udt := xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeUdt, Udt: &xdr.ScSpecTypeUdt{Name: "Color"}}

	roundTrip(t, s, udt, `0`)
	roundTrip(t, s, udt, `1`)
	_, err := s.FromJSON(json.RawMessage(`2`), udt)
	require.Error(t, err)
}

func TestParseSpecXDREmpty(t *testing.T) {
	s, err := ParseSpecXDR(nil)
	require.NoError(t, err)
	require.NotNil(t, s)
	_, err = s.lookupUdt("Anything")
	require.Error(t, err)
}
