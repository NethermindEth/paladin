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

// event_payloads.go is the data-availability fix companion to the shared indexed_events table:
// indexed_events (pkg/blockindexer) only carries the block/tx/log position and the event
// signature/selector - not the emitter/topics/data a decoded chain event needs. Rather than widen
// the shared, doc-generated pldapi.IndexedEvent/indexed_events table (public API surface used by
// both chains), this Stellar-only table carries the extra fields, keyed by the same
// (block_number, transaction_index, log_index) triple, written once at ingest time in the same DB
// transaction as indexed_events itself (see indexer.go's writeLedger). It is read only by this
// package's event-stream engine catchup path (event_streams.go's queryMatchingEvents).
package stellar

import (
	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
)

const stellarEventPayloadsTable = "stellar_event_payloads"

type eventKey struct {
	blockNumber      int64
	transactionIndex int64
	logIndex         int64
}

type stellarEventPayload struct {
	BlockNumber      int64                 `gorm:"column:block_number;primaryKey"`
	TransactionIndex int64                 `gorm:"column:transaction_index;primaryKey"`
	LogIndex         int64                 `gorm:"column:log_index;primaryKey"`
	Emitter          pldtypes.ChainAddress `gorm:"column:emitter"`
	Topics           [][]byte              `gorm:"column:topics;serializer:json"`
	Data             pldtypes.HexBytes     `gorm:"column:data"`
}

// dataJSON renders the payload's raw topics/data as the JSON blob delivered in
// pldapi.EventWithData.Data. There is no ABI (or Stellar contract-spec resolution -
// registries/stellar, chapter 12 §12.5, is explicitly out of scope here) to decode this into typed
// fields, so consumers get the raw hex-encoded topics/data - the same shape the underlying
// baseledger.IndexedChainEvent carries.
func (p *stellarEventPayload) dataJSON() pldtypes.RawJSON {
	topics := make([]string, len(p.Topics))
	for i, t := range p.Topics {
		topics[i] = pldtypes.HexBytes(t).String()
	}
	return pldtypes.JSONString(struct {
		Topics []string `json:"topics,omitempty"`
		Data   string   `json:"data,omitempty"`
	}{
		Topics: topics,
		Data:   pldtypes.HexBytes(p.Data).String(),
	})
}

// buildEventPayloads converts one ledger's decoded chain events into the companion rows written
// alongside indexed_events in the same DB transaction.
func buildEventPayloads(blockNumber int64, events []*baseledger.IndexedChainEvent) []*stellarEventPayload {
	if len(events) == 0 {
		return nil
	}
	payloads := make([]*stellarEventPayload, len(events))
	for i, ev := range events {
		payloads[i] = &stellarEventPayload{
			BlockNumber:      blockNumber,
			TransactionIndex: ev.TxIndex,
			LogIndex:         ev.EventIndex,
			Emitter:          ev.Emitter,
			Topics:           ev.Topics,
			Data:             pldtypes.HexBytes(ev.Data),
		}
	}
	return payloads
}
