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

// Ingestor implements baseledger.Ingestor by polling getLedgers on a fixed interval (~2s per
// chapter 12 §12.4). It never re-processes a ledger it has already emitted: StreamLedgers resumes
// from the given checkpoint's next sequence, or from the current chain tip if no checkpoint is
// supplied (matching the "start from latest" default the EVM block listener uses on first boot).
type Ingestor struct {
	rpc               ledgerRPCClient
	networkPassphrase string
	pollInterval      time.Duration
}

// NewIngestor constructs a Stellar baseledger.Ingestor. rpc is typically the same *rpcclient.Client
// stellarclient.NewClient constructs (it satisfies ledgerRPCClient structurally).
func NewIngestor(rpc ledgerRPCClient, networkPassphrase string, pollInterval time.Duration) *Ingestor {
	return &Ingestor{rpc: rpc, networkPassphrase: networkPassphrase, pollInterval: pollInterval}
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
			unit, decodeErr := decodeLedger(i.networkPassphrase, l)
			if decodeErr != nil {
				log.L(ctx).Errorf("failed to decode ledger %d: %s", l.Sequence, decodeErr)
				return
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
func decodeLedger(networkPassphrase string, l protocol.LedgerInfo) (*baseledger.LedgerUnit, error) {
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

		events, eventsErr := tx.GetContractEvents()
		if eventsErr != nil {
			return nil, fmt.Errorf("failed to read contract events for transaction %d of ledger %d: %w", txIndex, l.Sequence, eventsErr)
		}
		for eventIndex, event := range events {
			indexedEvent, ok, decodeErr := decodeContractEvent(unit.Sequence, txIndex, int64(eventIndex), event)
			if decodeErr != nil {
				return nil, fmt.Errorf("failed to decode contract event %d of transaction %d of ledger %d: %w", eventIndex, txIndex, l.Sequence, decodeErr)
			}
			if ok {
				unit.Events = append(unit.Events, indexedEvent)
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
func decodeContractEvent(ledgerSequence uint64, txIndex, eventIndex int64, event xdr.ContractEvent) (*baseledger.IndexedChainEvent, bool, error) {
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
		Selector:   ComputeEventSelector(string(topic0)),
		Topics:     topics,
		Data:       data,
	}, true, nil
}

// ComputeEventSelector implements chapter 12 §12.4's event-selector scheme:
// SHA-256("saladin:" + topic0Symbol + ":v0"). The book's own formula also folds in a
// "contract_spec_name" component (so semantically-identical events from different domains/specs
// don't collide) - that requires resolving the emitting contract's registered spec, which needs
// the registries/stellar plugin (chapter 12 §12.5, a separate, still-pending piece) to exist
// first. Exported so §12.5's future event-stream consumer registration can compute the exact same
// selector this ingestor writes.
func ComputeEventSelector(topic0Symbol string) pldtypes.Bytes32 {
	return sha256.Sum256([]byte("saladin:" + topic0Symbol + ":v0"))
}
