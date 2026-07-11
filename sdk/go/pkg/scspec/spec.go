// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

// Package scspec implements a JSON<->xdr.ScVal codec for Soroban contract values, driven by
// SEP-48 contract-spec type definitions (xdr.ScSpecTypeDef/xdr.ScSpecEntry). SEP-48 itself
// mandates no JSON value convention, so the conventions implemented here deliberately mirror
// stellar-cli/soroban-cli's own soroban-spec-tools crate byte-for-byte, since that is the de
// facto convention any JSON produced or consumed here needs to stay wire-compatible with.
//
// Deliberately out of scope for now: xdr.ScSpecEntryUdtErrorEnumV0 and the Result spec type
// (neither is cleanly invertible to/from JSON), and reading a contract's embedded Wasm
// "contractspecv0" custom section directly (callers must supply spec/type XDR themselves).
package scspec

import (
	"bytes"
	"fmt"

	"github.com/stellar/go-stellar-sdk/xdr"
)

// Spec holds the UDT entries (struct/union/enum definitions) needed to resolve xdr.ScSpecTypeUdt
// references by name. A Spec with no entries is valid and sufficient for payloads that only use
// primitive/vec/map/tuple/option types with no UDT references.
type Spec struct {
	udts map[string]xdr.ScSpecEntry
}

// ParseSpecXDR parses zero or more back-to-back XDR-encoded xdr.ScSpecEntry values (the framing
// used for a contract's embedded SEP-48 spec) into a Spec. A nil/empty input yields an empty,
// valid Spec.
func ParseSpecXDR(specXDR []byte) (*Spec, error) {
	s := &Spec{udts: map[string]xdr.ScSpecEntry{}}
	r := bytes.NewReader(specXDR)
	for r.Len() > 0 {
		var entry xdr.ScSpecEntry
		if _, err := xdr.Unmarshal(r, &entry); err != nil {
			return nil, fmt.Errorf("invalid spec entry XDR: %w", err)
		}
		name, ok := udtEntryName(entry)
		if ok {
			s.udts[name] = entry
		}
	}
	return s, nil
}

func udtEntryName(e xdr.ScSpecEntry) (string, bool) {
	switch e.Kind {
	case xdr.ScSpecEntryKindScSpecEntryUdtStructV0:
		return e.UdtStructV0.Name, true
	case xdr.ScSpecEntryKindScSpecEntryUdtUnionV0:
		return e.UdtUnionV0.Name, true
	case xdr.ScSpecEntryKindScSpecEntryUdtEnumV0:
		return e.UdtEnumV0.Name, true
	default:
		// FunctionV0/EventV0/UdtErrorEnumV0 are not name-addressable Udt targets for this codec.
		return "", false
	}
}

func (s *Spec) lookupUdt(name string) (xdr.ScSpecEntry, error) {
	e, ok := s.udts[name]
	if !ok {
		return xdr.ScSpecEntry{}, fmt.Errorf("spec has no UDT entry named %q", name)
	}
	return e, nil
}
