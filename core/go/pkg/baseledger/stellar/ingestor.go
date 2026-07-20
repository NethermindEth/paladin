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

// Package stellar's ingestor.go implements baseledger.Ingestor (chapter 12 §12.4): it polls
// stellar-rpc's getLedgers endpoint and decodes each ledger's LedgerCloseMeta XDR into the
// chain-neutral baseledger.LedgerUnit shape. Unlike EVM's WebSocket block listener, stellar-rpc
// has no push/subscription mode - polling is the only option - and SCP finality means every
// polled ledger is already final: there is no re-org handling or confirmation-depth tracking
// here, unlike blockindexer's EVM-specific blockListener.
package stellar

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"time"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/log"
	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/stellar/go-stellar-sdk/ingest"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// ledgerRPCClient is the minimal subset of *rpcclient.Client the ingestor calls - mirrors how
// Client (client.go) defines its own minimal rpcClient interface rather than depending on the
// concrete SDK type.
type ledgerRPCClient interface {
	GetLedgers(ctx context.Context, req protocol.GetLedgersRequest) (protocol.GetLedgersResponse, error)
	GetLatestLedger(ctx context.Context) (protocol.GetLatestLedgerResponse, error)
}

// SpecResolver resolves the contract-spec name registered for a given emitter address (e.g.
// "snoto", "identity-registry"), so ComputeEventSelectorWithSpec can fold it into the selector and
// disambiguate identically-named events from different domains/specs. Resolve-or-fallback, not
// resolve-or-fail: a nil resolver, or one that returns ok=false for a given emitter (no registered
// spec, or not yet caught up), causes the caller to fall back to the unqualified
// ComputeEventSelector - ingestion never blocks on registry state, and existing rows written under
// the unqualified formula stay valid with no migration/backfill.
type SpecResolver interface {
	ResolveContractSpecName(ctx context.Context, emitter pldtypes.ChainAddress) (specName string, ok bool)
}

// Ingestor implements baseledger.Ingestor by polling getLedgers on a fixed interval (~2s per
// chapter 12 §12.4). It never re-processes a ledger it has already emitted: StreamLedgers resumes
// from the given checkpoint's next sequence, or from the current chain tip if no checkpoint is
// supplied (matching the "start from latest" default the EVM block listener uses on first boot).
type Ingestor struct {
	rpc               ledgerRPCClient
	networkPassphrase string
	pollInterval      time.Duration
	specResolver      SpecResolver
}

// NewIngestor constructs a Stellar baseledger.Ingestor. rpc is typically the same *rpcclient.Client
// stellarclient.NewClient constructs (it satisfies ledgerRPCClient structurally).
func NewIngestor(rpc ledgerRPCClient, networkPassphrase string, pollInterval time.Duration) *Ingestor {
	return &Ingestor{rpc: rpc, networkPassphrase: networkPassphrase, pollInterval: pollInterval}
}

// SetSpecResolver wires a spec-name resolver into an already-constructed Ingestor - mirroring
// componentmgr's own SetEventStreamEngine setter-injection pattern, so NewIngestor's existing call
// sites (and its 3-arg signature) don't need to change. Safe to leave unset: decodeContractEvent
// treats a nil resolver exactly like one that never resolves anything.
func (i *Ingestor) SetSpecResolver(r SpecResolver) {
	i.specResolver = r
}

// BackfillSource reports that deep history (beyond stellar-rpc's 24h-7d retention) would come from
// history archives/Galexie in a future pass - this project has no Horizon dependency anywhere
// (chapter 12's "Implementation status" callout). No backfill source is wired up in this slice;
// this is the capability-advertisement hook only.
func (i *Ingestor) BackfillSource() baseledger.BackfillCapability {
	return baseledger.BackfillArchive
}

func (i *Ingestor) TipHeight(ctx context.Context) (uint64, error) {
	resp, err := i.rpc.GetLatestLedger(ctx)
	if err != nil {
		return 0, err
	}
	return uint64(resp.Sequence), nil
}

func (i *Ingestor) StreamLedgers(ctx context.Context, from baseledger.LedgerCheckpoint) (<-chan *baseledger.LedgerUnit, error) {
	start := from.Sequence + 1
	if from.Sequence == 0 {
		// First boot with no persisted checkpoint: start from the current tip, not genesis -
		// matching the EVM block listener's "latest" default (retention makes historical replay
		// from genesis infeasible anyway; see §12.4's retention/backfill discussion).
		tip, err := i.TipHeight(ctx)
		if err != nil {
			return nil, err
		}
		start = tip + 1
	}
	ch := make(chan *baseledger.LedgerUnit)
	go i.poll(ctx, start, ch)
	return ch, nil
}

func (i *Ingestor) poll(ctx context.Context, start uint64, ch chan<- *baseledger.LedgerUnit) {
	defer close(ch)
	next := start
	ticker := time.NewTicker(i.pollInterval)
	defer ticker.Stop()
tick:
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		resp, err := i.rpc.GetLedgers(ctx, protocol.GetLedgersRequest{StartLedger: uint32(next)}) //nolint:gosec // ledger sequences fit comfortably in uint32
		if err != nil {
			log.L(ctx).Errorf("getLedgers failed (will retry from ledger %d): %s", next, err)
			continue
		}
		for _, l := range resp.Ledgers {
			unit, decodeErr := decodeLedger(ctx, i.networkPassphrase, l, i.specResolver)
			if decodeErr != nil {
				// A single unparseable/unexpected ledger must not permanently stop ingestion for
				// the rest of this process's life - next hasn't advanced past l yet, so retrying
				// the tick loop naturally re-fetches starting from this same ledger.
				log.L(ctx).Errorf("failed to decode ledger %d (will retry from here): %s", l.Sequence, decodeErr)
				continue tick
			}
			select {
			case ch <- unit:
			case <-ctx.Done():
				return
			}
			next = uint64(l.Sequence) + 1
		}
	}
}

// decodeLedger decodes one ledger's LedgerCloseMeta XDR via the SDK's own ingest package
// (ingest.NewLedgerTransactionReaderFromLedgerCloseMeta) rather than hand-parsing the union/nested
// TransactionMeta structures - the SDK's reader already handles the V0/V1/V2 LedgerCloseMeta and
// TransactionMeta version differences correctly.
func decodeLedger(ctx context.Context, networkPassphrase string, l protocol.LedgerInfo, resolver SpecResolver) (*baseledger.LedgerUnit, error) {
	var lcm xdr.LedgerCloseMeta
	if err := xdr.SafeUnmarshalBase64(l.LedgerMetadata, &lcm); err != nil {
		return nil, fmt.Errorf("invalid ledger metadata for ledger %d: %w", l.Sequence, err)
	}
	hash, err := pldtypes.ParseBytes32(l.Hash)
	if err != nil {
		return nil, fmt.Errorf("invalid ledger hash for ledger %d: %w", l.Sequence, err)
	}
	unit := &baseledger.LedgerUnit{
		Sequence:  uint64(l.Sequence),
		Hash:      hash,
		Timestamp: pldtypes.TimestampFromUnix(l.LedgerCloseTime),
	}

	reader, err := ingest.NewLedgerTransactionReaderFromLedgerCloseMeta(networkPassphrase, lcm)
	if err != nil {
		return nil, fmt.Errorf("failed to read ledger %d transactions: %w", l.Sequence, err)
	}
	defer reader.Close()

	var txIndex int64
	for {
		tx, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("failed to read transaction %d of ledger %d: %w", txIndex, l.Sequence, readErr)
		}

		account, err := tx.Account()
		if err != nil {
			return nil, fmt.Errorf("failed to resolve source account for transaction %d of ledger %d: %w", txIndex, l.Sequence, err)
		}
		fromAddr, err := pldtypes.NewStellarAccountAddress(account)
		if err != nil {
			return nil, fmt.Errorf("invalid source account %q for transaction %d of ledger %d: %w", account, txIndex, l.Sequence, err)
		}

		result := "FAILED"
		var revertData []byte
		if tx.Result.Successful() {
			result = "SUCCESS"
		} else if resultBytes, marshalErr := tx.Result.Result.MarshalBinary(); marshalErr == nil {
			revertData = resultBytes
		}

		unit.Txs = append(unit.Txs, &baseledger.IndexedChainTx{
			TxID:       pldtypes.Bytes32(tx.Hash),
			From:       fromAddr,
			Nonce:      uint64(tx.AccountSequence()), //nolint:gosec // sequence numbers are always positive
			Result:     result,
			RevertData: revertData,
			TxIndex:    txIndex,
		})

		// GetTransactionEvents (not the Soroban-only GetContractEvents) is what correctly handles a
		// classic (non-Soroban) transaction - e.g. the CreateAccountOp transactions channel-account
		// funding submits - without erroring. It's also version-aware: TransactionMeta V1/V2 have no
		// events, V3 only carries events for Soroban transactions (matching prior behavior here
		// exactly), and V4+ (Stellar Protocol 23's CAP-67 "unified events") carries real
		// per-operation events for every operation, classic or Soroban.
		txEvents, eventsErr := tx.GetTransactionEvents()
		if eventsErr != nil {
			return nil, fmt.Errorf("failed to read events for transaction %d of ledger %d: %w", txIndex, l.Sequence, eventsErr)
		}
		var eventIndex int64
		for _, opEvents := range txEvents.OperationEvents {
			for _, event := range opEvents {
				indexedEvent, ok, decodeErr := decodeContractEvent(ctx, unit.Sequence, txIndex, eventIndex, event, resolver)
				if decodeErr != nil {
					return nil, fmt.Errorf("failed to decode contract event %d of transaction %d of ledger %d: %w", eventIndex, txIndex, l.Sequence, decodeErr)
				}
				if ok {
					unit.Events = append(unit.Events, indexedEvent)
				}
				eventIndex++
			}
		}

		txIndex++
	}
	return unit, nil
}

// decodeContractEvent converts one Soroban contract event into the chain-neutral
// baseledger.IndexedChainEvent shape. ok is false for events this decoder deliberately skips
// (V0 body only, and topic[0] must be a symbol - the "eventName" convention chapter 12's book
// text assumes) rather than an error, since not every emitted event necessarily follows that
// convention.
func decodeContractEvent(ctx context.Context, ledgerSequence uint64, txIndex, eventIndex int64, event xdr.ContractEvent, resolver SpecResolver) (*baseledger.IndexedChainEvent, bool, error) {
	body, ok := event.Body.GetV0()
	if !ok || len(body.Topics) == 0 {
		return nil, false, nil
	}
	topic0, ok := body.Topics[0].GetSym()
	if !ok {
		return nil, false, nil
	}

	var emitter pldtypes.ChainAddress
	if event.ContractId != nil {
		contractAddr, err := strkey.Encode(strkey.VersionByteContract, event.ContractId[:])
		if err != nil {
			return nil, false, fmt.Errorf("failed to encode contract address: %w", err)
		}
		emitter, err = pldtypes.NewStellarContractAddress(contractAddr)
		if err != nil {
			return nil, false, err
		}
	}

	selector := ComputeEventSelector(string(topic0))
	if resolver != nil && event.ContractId != nil {
		if specName, ok := resolver.ResolveContractSpecName(ctx, emitter); ok {
			selector = ComputeEventSelectorWithSpec(specName, string(topic0))
		}
	}

	topics := make([][]byte, len(body.Topics))
	for i, t := range body.Topics {
		topicBytes, err := t.MarshalBinary()
		if err != nil {
			return nil, false, fmt.Errorf("failed to marshal topic %d: %w", i, err)
		}
		topics[i] = topicBytes
	}
	data, err := body.Data.MarshalBinary()
	if err != nil {
		return nil, false, fmt.Errorf("failed to marshal event data: %w", err)
	}

	return &baseledger.IndexedChainEvent{
		Sequence:   ledgerSequence,
		TxIndex:    txIndex,
		EventIndex: eventIndex,
		Emitter:    emitter,
		Selector:   selector,
		Topics:     topics,
		Data:       data,
	}, true, nil
}

// ComputeEventSelector implements chapter 12 §12.4's event-selector scheme:
// SHA-256("saladin:" + topic0Symbol + ":v0"). Kept as the fallback formula for events whose
// emitting contract has no resolvable spec name (see SpecResolver/ComputeEventSelectorWithSpec,
// chapter 13 Phase 4) - existing rows written under this formula remain valid forever, since a
// resolver miss always falls back to this exact computation.
func ComputeEventSelector(topic0Symbol string) pldtypes.Bytes32 {
	return sha256.Sum256([]byte("saladin:" + topic0Symbol + ":v0"))
}

// ComputeEventSelectorWithSpec extends ComputeEventSelector with a contract-spec-name component
// (chapter 12 §12.4's own formula, deferred at the time to "a separate, still-pending piece" -
// chapter 13 Phase 4 is that piece), so identically-named events from different domains/specs no
// longer collide: SHA-256("saladin:" + contractSpecName + ":" + topic0Symbol + ":v0"). Whatever
// resolves contractSpecName (a registry plugin's `$specName` reserved property, see
// registrymgr/registry.go) must resolve to the exact same string an EventStreamSource.Selectors
// entry is built from, or the two will never match.
func ComputeEventSelectorWithSpec(contractSpecName, topic0Symbol string) pldtypes.Bytes32 {
	return sha256.Sum256([]byte("saladin:" + contractSpecName + ":" + topic0Symbol + ":v0"))
}
