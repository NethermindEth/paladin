// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package stellarregistry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/scspec"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// computeEventSelectorWithSpec duplicates core/go/pkg/baseledger/stellar.
// ComputeEventSelectorWithSpec's exact formula. Registry plugins are standalone modules that
// don't depend on core/go (they communicate with it only via the plugintk gRPC protocol), so this
// small pure function is duplicated here rather than shared - it MUST stay byte-for-byte
// identical to the core copy, or selectors this plugin publishes will never match what the
// ingestor computes.
func computeEventSelectorWithSpec(contractSpecName, topic0Symbol string) pldtypes.Bytes32 {
	return sha256.Sum256([]byte("saladin:" + contractSpecName + ":" + topic0Symbol + ":v0"))
}

func parseSelector(hexStr string) (pldtypes.Bytes32, error) {
	b, err := pldtypes.ParseHexBytes(context.Background(), hexStr)
	if err != nil {
		return pldtypes.Bytes32{}, err
	}
	if len(b) != 32 {
		return pldtypes.Bytes32{}, fmt.Errorf("expected a 32-byte selector, got %d bytes", len(b))
	}
	var sig pldtypes.Bytes32
	copy(sig[:], b)
	return sig, nil
}

func hexDecodeName(hexStr string) ([]byte, error) {
	return pldtypes.ParseHexBytes(context.Background(), hexStr)
}

// The identity-registry contract's own event shapes (chapter 13 Phase 4,
// soroban/contracts/identity-registry/src/lib.rs):
//
//	IdentityRegistered { #[topic] identity: BytesN<32>, parent: BytesN<32>, name: Bytes, owner: Address }
//	PropertySet        { #[topic] identity: BytesN<32>, name: Symbol, value: Bytes }
//
// `data_format = "vec"` on both means the non-topic fields encode as a positional ScVal::Vec in
// declaration order - decoded below via a Tuple ScSpecTypeDef. No Udt/spec entries are needed
// (every field is a primitive type), so an empty scspec.Spec suffices - this reuses the exact
// JSON<->ScVal codec chapter 13 Phase 1's XDR_SCVAL addendum built, rather than a new decoder.
const (
	identityRegisteredTopic0 = "identity_registered"
	propertySetTopic0        = "property_set"
)

var (
	bytesN32Type = xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeBytesN, BytesN: &xdr.ScSpecTypeBytesN{N: 32}}
	addressType  = xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeAddress}
	bytesType    = xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeBytes}
	symbolType   = xdr.ScSpecTypeDef{Type: xdr.ScSpecTypeScSpecTypeSymbol}

	// identity_registered data = (parent: BytesN<32>, name: Bytes, owner: Address)
	identityRegisteredDataType = xdr.ScSpecTypeDef{
		Type:  xdr.ScSpecTypeScSpecTypeTuple,
		Tuple: &xdr.ScSpecTypeTuple{ValueTypes: []xdr.ScSpecTypeDef{bytesN32Type, bytesType, addressType}},
	}
	// property_set data = (name: Symbol, value: Bytes)
	propertySetDataType = xdr.ScSpecTypeDef{
		Type:  xdr.ScSpecTypeScSpecTypeTuple,
		Tuple: &xdr.ScSpecTypeTuple{ValueTypes: []xdr.ScSpecTypeDef{symbolType, bytesType}},
	}
)

// noUdtSpec is shared by every decode call in this package: none of the shapes above reference a
// Udt, so an empty Spec (no ScSpecEntry needed for name resolution) is always sufficient.
var noUdtSpec, _ = scspec.ParseSpecXDR(nil)

// eventPayload mirrors the exact JSON shape core/go/internal/ledgerindexer/stellar/
// event_payloads.go's dataJSON() produces for a Stellar event: raw XDR-encoded topics/data,
// hex-encoded. pldtypes.HexBytes tolerates the "0x" prefix on unmarshal.
type eventPayload struct {
	Topics []pldtypes.HexBytes `json:"topics"`
	Data   pldtypes.HexBytes   `json:"data"`
}

func decodeEventPayload(dataJSON string) (*eventPayload, error) {
	var p eventPayload
	if err := json.Unmarshal([]byte(dataJSON), &p); err != nil {
		return nil, fmt.Errorf("invalid event payload JSON: %w", err)
	}
	return &p, nil
}

func decodeScVal(raw []byte) (xdr.ScVal, error) {
	var v xdr.ScVal
	if _, err := xdr.Unmarshal(bytes.NewReader(raw), &v); err != nil {
		return xdr.ScVal{}, fmt.Errorf("invalid ScVal XDR: %w", err)
	}
	return v, nil
}

// decodeIdentityHashTopic decodes topic[1] - the BytesN<32> `identity` field every event in this
// contract carries as its one non-fixed topic (topic[0] is the fixed event-name symbol, already
// consumed by selector matching in HandleRegistryEvents, so it's never decoded here).
func decodeIdentityHashTopic(topics []pldtypes.HexBytes) (string, error) {
	if len(topics) < 2 {
		return "", fmt.Errorf("expected at least 2 topics, got %d", len(topics))
	}
	val, err := decodeScVal(topics[1])
	if err != nil {
		return "", err
	}
	return decodeAsString(val, bytesN32Type)
}

// decodeTupleAsStrings decodes a positional-Vec event data section into its field values, each
// rendered as a JSON string (true for every primitive type this package's event shapes use: hex
// for Bytes/BytesN, strkey for Address, plain text for Symbol - see sdk/go/pkg/scspec/tojson.go).
func decodeTupleAsStrings(data []byte, tupleType xdr.ScSpecTypeDef) ([]string, error) {
	val, err := decodeScVal(data)
	if err != nil {
		return nil, err
	}
	j, err := noUdtSpec.ToJSON(val, tupleType)
	if err != nil {
		return nil, fmt.Errorf("failed to decode event data: %w", err)
	}
	var fields []string
	if err := json.Unmarshal(j, &fields); err != nil {
		return nil, fmt.Errorf("unexpected event data shape: %w", err)
	}
	return fields, nil
}

func decodeAsString(val xdr.ScVal, t xdr.ScSpecTypeDef) (string, error) {
	j, err := noUdtSpec.ToJSON(val, t)
	if err != nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal(j, &s); err != nil {
		return "", fmt.Errorf("unexpected value shape: %w", err)
	}
	return s, nil
}

// isZeroHex reports whether a hex string (with or without 0x prefix) decodes to all-zero bytes -
// used to detect the root identity's own sentinel parent hash (see identity-registry's
// `root_id()`, a fixed [0u8; 32] key), matching EVM IdentityRegistry.sol's equivalent
// `!parsedEvent.ParentIdentityHash.IsZero()` check.
func isZeroHex(hexStr string) bool {
	b, err := pldtypes.ParseHexBytes(context.Background(), hexStr)
	if err != nil {
		return false
	}
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return len(b) > 0
}
