/*
 * Copyright © 2026 Kaleido, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with
 * the License. You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on
 * an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the
 * specific language governing permissions and limitations under the License.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package repoterms

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// stellarEventsJSON declares repo-terms's own on-chain event name
// (soroban/contracts/repo-terms/src/lib.rs's #[contractevent(topics = ["set_terms"], ...)]) as this
// domain's AbiEventsJson - domainmgr's event-stream registration
// (core/go/internal/domainmgr/domain.go) computes a Stellar selector as
// SHA-256("saladin:"+event.Name+":v0") for every event in whatever AbiEventsJson a domain returns,
// so the name here must literally equal the on-chain topic0 symbol. Mirrors
// domains/noto/internal/noto/noto.go's own stellarEventsJSON convention. Field types are filled in
// with repo-terms's real positional "vec" data shape for documentation only - Stellar events are
// dispatched by raw selector (events.go's HandleEventBatch), not by decoding these types.
const stellarEventsJSON = `[
  {
    "type": "event",
    "name": "set_terms",
    "anonymous": false,
    "inputs": [
      {"name": "tx_id", "type": "bytes32", "indexed": true},
      {"name": "terms_state_id", "type": "bytes32", "indexed": false}
    ]
  }
]`

// stellarEventSelector mirrors domains/noto/internal/noto/events_stellar.go's own identical
// function: SHA-256("saladin:" + topic0Symbol + ":v0"). Duplicated here rather than imported - this
// Go module (domains/repo-terms) is a standalone domain-plugin binary, the same reasoning
// domains/noto's own doc comment on this function gives for not sharing it via a common module.
func stellarEventSelector(topic0Symbol string) pldtypes.Bytes32 {
	return sha256.Sum256([]byte("saladin:" + topic0Symbol + ":v0"))
}

// stellarSetTermsSelector is what HandleEventBatch (events.go) matches a delivered event's raw
// ev.Signature against - the Stellar counterpart to an EVM domain's SoliditySignature lookup, since
// Stellar events carry no SoliditySignature at all.
var stellarSetTermsSelector = stellarEventSelector("set_terms").String()

// stellarEventPayload mirrors the JSON shape delivered in ev.DataJson for a Stellar event
// (core/go/internal/ledgerindexer/stellar/event_payloads.go's stellarEventPayload.dataJSON()) -
// copied from domains/noto/internal/noto/events_stellar.go's own identical type, for the same
// cross-module reason.
type stellarEventPayload struct {
	Topics []string `json:"topics"`
	Data   string   `json:"data"`
}

func decodeStellarEventPayload(ev *prototk.OnChainEvent) (*stellarEventPayload, error) {
	var payload stellarEventPayload
	if err := json.Unmarshal([]byte(ev.DataJson), &payload); err != nil {
		return nil, fmt.Errorf("invalid event payload: %w", err)
	}
	return &payload, nil
}

// decodeStellarScVal hex-decodes and XDR-unmarshals a single topic or data field (each is the full
// XDR encoding of one ScVal) - copied from domains/noto/internal/noto/events_stellar.go's own
// identical helper.
func decodeStellarScVal(ctx context.Context, hexVal string) (xdr.ScVal, error) {
	raw, err := pldtypes.ParseHexBytes(ctx, hexVal)
	if err != nil {
		return xdr.ScVal{}, err
	}
	var val xdr.ScVal
	if _, err := xdr.Unmarshal(bytes.NewReader(raw), &val); err != nil {
		return xdr.ScVal{}, err
	}
	return val, nil
}

// scValToBytes32 expects a 32-byte BytesN ScVal (repo-terms's tx_id/terms_state_id shape).
func scValToBytes32(val xdr.ScVal) (pldtypes.Bytes32, error) {
	if val.Type != xdr.ScValTypeScvBytes || val.Bytes == nil || len(*val.Bytes) != 32 {
		return pldtypes.Bytes32{}, fmt.Errorf("expected a 32-byte BytesN value")
	}
	var b32 pldtypes.Bytes32
	copy(b32[:], *val.Bytes)
	return b32, nil
}

// decodeStellarEventDataVec XDR-decodes ev.DataJson's "data" field (the full data_format="vec"
// payload, everything but the topics) and checks it has exactly the expected number of positional
// elements for the event kind being decoded.
func decodeStellarEventDataVec(ctx context.Context, hexData string, expectedLen int) ([]xdr.ScVal, error) {
	val, err := decodeStellarScVal(ctx, hexData)
	if err != nil {
		return nil, fmt.Errorf("invalid event data XDR: %w", err)
	}
	if val.Type != xdr.ScValTypeScvVec || val.Vec == nil || *val.Vec == nil {
		return nil, fmt.Errorf("event data: expected a Vec")
	}
	vec := **val.Vec
	if len(vec) != expectedLen {
		return nil, fmt.Errorf("event data: expected %d elements, got %d", expectedLen, len(vec))
	}
	return vec, nil
}

// SetTermsEvent is the decoded shape of repo-terms's on-chain `set_terms` event (topics =
// ["set_terms", tx_id], data = vec![terms_state_id] - soroban/contracts/repo-terms/src/lib.rs's
// `SetTerms` struct, data_format="vec" wrapping its one non-topic field). Mirrors
// domains/noto/internal/noto/events_stellar.go's own decodeStellarLockEvent's shape/style.
type SetTermsEvent struct {
	TxId         pldtypes.Bytes32
	TermsStateId pldtypes.Bytes32
}

// decodeStellarSetTermsEvent decodes a delivered `set_terms` event into SetTermsEvent.
func decodeStellarSetTermsEvent(ctx context.Context, ev *prototk.OnChainEvent) (*SetTermsEvent, error) {
	payload, err := decodeStellarEventPayload(ev)
	if err != nil {
		return nil, err
	}
	if len(payload.Topics) < 2 {
		return nil, fmt.Errorf("expected at least 2 topics (symbol, tx_id), got %d", len(payload.Topics))
	}
	txIDVal, err := decodeStellarScVal(ctx, payload.Topics[1])
	if err != nil {
		return nil, fmt.Errorf("tx_id topic: %w", err)
	}
	txID, err := scValToBytes32(txIDVal)
	if err != nil {
		return nil, fmt.Errorf("tx_id topic: %w", err)
	}

	vec, err := decodeStellarEventDataVec(ctx, payload.Data, 1)
	if err != nil {
		return nil, err
	}
	termsStateID, err := scValToBytes32(vec[0])
	if err != nil {
		return nil, fmt.Errorf("event data[0] (terms_state_id): %w", err)
	}

	return &SetTermsEvent{TxId: txID, TermsStateId: termsStateID}, nil
}
