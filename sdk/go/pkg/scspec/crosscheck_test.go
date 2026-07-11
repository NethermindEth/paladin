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

// TestCrossCheckAgainstSorobanSpecTools locks in wire-compatibility with the real Rust library
// stellar-cli/soroban-cli use for JSON<->ScVal conversion (soroban-spec-tools). The expected
// base64 XDR values were produced independently, by a standalone Rust program (kept outside this
// repo, in the session scratchpad, not committed) that depends on the real soroban-spec-tools
// v27.0.0 crate and calls its own Spec::from_json for the exact same JSON+spec inputs used here -
// not by hand-deriving these bytes from this package's own code. If this test ever fails after a
// change to fromjson.go/tojson.go/sort.go, that means this package's wire format has drifted from
// stellar-cli's own, which is the one thing this codec exists to avoid.
func TestCrossCheckAgainstSorobanSpecTools(t *testing.T) {
	cases := []struct {
		name      string
		spec      *Spec
		typeDef   xdr.ScSpecTypeDef
		jsonValue string
		expectB64 string
	}{
		{
			name:      "u32_42",
			spec:      &Spec{udts: map[string]xdr.ScSpecEntry{}},
			typeDef:   xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeU32},
			jsonValue: `42`,
			expectB64: "AAAAAwAAACo=",
		},
		{
			name: "named_struct",
			spec: &Spec{udts: map[string]xdr.ScSpecEntry{"Transfer": {
				Kind: xdr.ScSpecEntryKindScSpecEntryUdtStructV0,
				UdtStructV0: &xdr.ScSpecUdtStructV0{
					Name: "Transfer",
					Fields: []xdr.ScSpecUdtStructFieldV0{
						{Name: "amount", Type: td(xdr.ScSpecTypeScSpecTypeU64)},
						{Name: "count", Type: td(xdr.ScSpecTypeScSpecTypeU32)},
					},
				},
			}}},
			typeDef:   xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeUdt, Udt: &xdr.ScSpecTypeUdt{Name: "Transfer"}},
			jsonValue: `{"amount":100,"count":3}`,
			expectB64: "AAAAEQAAAAEAAAACAAAADwAAAAZhbW91bnQAAAAAAAUAAAAAAAAAZAAAAA8AAAAFY291bnQAAAAAAAADAAAAAw==",
		},
		{
			name: "tuple_struct",
			spec: &Spec{udts: map[string]xdr.ScSpecEntry{"TransferPayload": {
				Kind: xdr.ScSpecEntryKindScSpecEntryUdtStructV0,
				UdtStructV0: &xdr.ScSpecUdtStructV0{
					Name: "TransferPayload",
					Fields: []xdr.ScSpecUdtStructFieldV0{
						{Name: "0", Type: td(xdr.ScSpecTypeScSpecTypeU64)},
						{Name: "1", Type: td(xdr.ScSpecTypeScSpecTypeU32)},
					},
				},
			}}},
			typeDef:   xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeUdt, Udt: &xdr.ScSpecTypeUdt{Name: "TransferPayload"}},
			jsonValue: `[100,3]`,
			expectB64: "AAAAEAAAAAEAAAACAAAABQAAAAAAAABkAAAAAwAAAAM=",
		},
		{
			name:      "union_void",
			spec:      &Spec{udts: map[string]xdr.ScSpecEntry{"Status": statusUnionEntry()}},
			typeDef:   xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeUdt, Udt: &xdr.ScSpecTypeUdt{Name: "Status"}},
			jsonValue: `"Pending"`,
			expectB64: "AAAAEAAAAAEAAAABAAAADwAAAAdQZW5kaW5nAA==",
		},
		{
			name:      "union_single",
			spec:      &Spec{udts: map[string]xdr.ScSpecEntry{"Status": statusUnionEntry()}},
			typeDef:   xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeUdt, Udt: &xdr.ScSpecTypeUdt{Name: "Status"}},
			jsonValue: `{"Failed":"timeout"}`,
			expectB64: "AAAAEAAAAAEAAAACAAAADwAAAAZGYWlsZWQAAAAAAA4AAAAHdGltZW91dAA=",
		},
		{
			name:      "union_multi",
			spec:      &Spec{udts: map[string]xdr.ScSpecEntry{"Status": statusUnionEntry()}},
			typeDef:   xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeUdt, Udt: &xdr.ScSpecTypeUdt{Name: "Status"}},
			jsonValue: `{"Range":[1,10]}`,
			expectB64: "AAAAEAAAAAEAAAADAAAADwAAAAVSYW5nZQAAAAAAAAMAAAABAAAAAwAAAAo=",
		},
		{
			name: "const_enum",
			spec: &Spec{udts: map[string]xdr.ScSpecEntry{"Color": {
				Kind: xdr.ScSpecEntryKindScSpecEntryUdtEnumV0,
				UdtEnumV0: &xdr.ScSpecUdtEnumV0{
					Name: "Color",
					Cases: []xdr.ScSpecUdtEnumCaseV0{
						{Name: "Red", Value: 0},
						{Name: "Green", Value: 1},
					},
				},
			}}},
			typeDef:   xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeUdt, Udt: &xdr.ScSpecTypeUdt{Name: "Color"}},
			jsonValue: `1`,
			expectB64: "AAAAAwAAAAE=",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			val, err := c.spec.FromJSON(json.RawMessage(c.jsonValue), c.typeDef)
			require.NoError(t, err)
			b64, err := xdr.MarshalBase64(val)
			require.NoError(t, err)
			require.Equal(t, c.expectB64, b64)
		})
	}
}

func statusUnionEntry() xdr.ScSpecEntry {
	return xdr.ScSpecEntry{
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
}
