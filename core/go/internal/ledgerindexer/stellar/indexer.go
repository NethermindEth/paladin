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

// Package stellar (ledgerindexer/stellar) is the write/orchestration side of chapter 12 §12.4's
// Stellar ledger ingestor: it consumes baseledger.Ingestor.StreamLedgers, persists into the same
// indexed_blocks/indexed_transactions/indexed_events tables the EVM blockindexer package uses (via
// the chain-neutral from_chain/to_chain/contract_address_chain columns added earlier this chapter,
// leaving the EVM-only fixed-width from/to/contract_address columns NULL), and invokes the same
// blockindexer.PreCommitHandler seam publictxmgr's confirmation-matching already uses - so
// publictxmgr needs zero changes to support Stellar.
//
// Deliberately NOT built here: the consumer-facing EventStream matching/delivery machinery
// (domainmgr/registrymgr's AddEventStream) - that stays blockindexer-specific until chapter 12
// §12.5 (registries/stellar) generalizes it, since only that phase actually needs it.
package stellar

import (
	"context"
	"sync/atomic"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/common/go/pkg/log"
	"github.com/LFDT-Paladin/paladin/core/internal/msgs"
	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	"github.com/LFDT-Paladin/paladin/core/pkg/blockindexer"
	"github.com/LFDT-Paladin/paladin/core/pkg/persistence"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/retry"
)

// Indexer is the Stellar ledger-indexing orchestrator componentmgr constructs and starts in place
// of blockindexer.BlockIndexer for a type: stellar node.
type Indexer struct {
	ctx               context.Context
	cancel            context.CancelFunc
	ingestor          baseledger.Ingestor
	persistence       persistence.Persistence
	retry             *retry.Retry
	insertDBBatchSize int
	preCommitHandlers []blockindexer.PreCommitHandler
	done              chan struct{}
	ready             atomic.Bool
}

// NewIndexer builds a stopped Indexer. Call Start to begin polling and writing.
func NewIndexer(ctx context.Context, ingestor baseledger.Ingestor, p persistence.Persistence, retry *retry.Retry, insertDBBatchSize int) *Indexer {
	ctx = log.WithComponent(ctx, "stellarledgerindexer")
	ctx, cancel := context.WithCancel(ctx)
	return &Indexer{
		ctx:               ctx,
		cancel:            cancel,
		ingestor:          ingestor,
		persistence:       p,
		retry:             retry,
		insertDBBatchSize: insertDBBatchSize,
		done:              make(chan struct{}),
	}
}

// Start begins polling for ledgers and writing them, invoking preCommitHandlers (the same
// blockindexer.PreCommitHandler seam componentmgr's buildInternalEventStreams already collects
// from every manager's ManagerInitResult) inside the same DB transaction as the ledger's own
// indexed_transactions/indexed_events rows.
func (ix *Indexer) Start(preCommitHandlers ...blockindexer.PreCommitHandler) error {
	ix.preCommitHandlers = preCommitHandlers
	checkpoint, err := ix.loadCheckpoint(ix.ctx)
	if err != nil {
		return err
	}
	ch, err := ix.ingestor.StreamLedgers(ix.ctx, checkpoint)
	if err != nil {
		return err
	}
	go ix.consume(ch)
	return nil
}

func (ix *Indexer) Stop() {
	ix.cancel()
	<-ix.done
}

// Ready mirrors blockindexer.BlockIndexer.GetConfirmedBlockHeight's use as a readiness gate
// (components.PreInitComponents.LedgerIndexReady wraps whichever of the two applies): it errors
// until at least one ledger has been successfully persisted.
func (ix *Indexer) Ready(ctx context.Context) error {
	if !ix.ready.Load() {
		return i18n.NewError(ctx, msgs.MsgBlockIndexerNoBlocksIndexed)
	}
	return nil
}

// loadCheckpoint resumes from the highest already-persisted ledger, or returns a zero
// LedgerCheckpoint (meaning "start from the current chain tip") on first boot.
func (ix *Indexer) loadCheckpoint(ctx context.Context) (baseledger.LedgerCheckpoint, error) {
	var highest []*pldapi.IndexedBlock
	err := ix.persistence.DB().
		WithContext(ctx).
		Table("indexed_blocks").
		Order("number DESC").
		Limit(1).
		Find(&highest).
		Error
	if err != nil {
		return baseledger.LedgerCheckpoint{}, err
	}
	if len(highest) == 0 {
		return baseledger.LedgerCheckpoint{}, nil
	}
	block := highest[0]
	ix.ready.Store(true)
	return baseledger.LedgerCheckpoint{Sequence: uint64(block.Number), Hash: block.Hash}, nil //nolint:gosec // ledger sequences are always positive
}

func (ix *Indexer) consume(ch <-chan *baseledger.LedgerUnit) {
	defer close(ix.done)
	for {
		select {
		case <-ix.ctx.Done():
			return
		case unit, open := <-ch:
			if !open {
				// The ingestor's own poll loop has exited (context cancelled, or an unrecoverable
				// decode error it has already logged) - nothing more will ever arrive.
				return
			}
			if err := ix.writeLedger(ix.ctx, unit); err != nil {
				log.L(ix.ctx).Errorf("failed to write ledger %d (indexing stopped): %s", unit.Sequence, err)
				return
			}
			ix.ready.Store(true)
		}
	}
}

func (ix *Indexer) writeLedger(ctx context.Context, unit *baseledger.LedgerUnit) error {
	block, notifyTxs, events := convertLedgerUnit(unit)
	return ix.retry.Do(ctx, func(_ int) (retryable bool, err error) {
		return true, ix.persistence.Transaction(ctx, func(ctx context.Context, dbTX persistence.DBTX) error {
			for _, preCommitHandler := range ix.preCommitHandlers {
				if err := preCommitHandler(ctx, dbTX, []*pldapi.IndexedBlock{block}, notifyTxs); err != nil {
					return err
				}
			}
			if err := dbTX.DB().WithContext(ctx).Table("indexed_blocks").Create(block).Error; err != nil {
				return err
			}
			if len(notifyTxs) > 0 {
				transactions := make([]*pldapi.IndexedTransaction, len(notifyTxs))
				for i, t := range notifyTxs {
					transactions[i] = &t.IndexedTransaction
				}
				if err := dbTX.DB().WithContext(ctx).Table("indexed_transactions").CreateInBatches(transactions, ix.insertDBBatchSize).Error; err != nil {
					return err
				}
			}
			if len(events) > 0 {
				if err := dbTX.DB().WithContext(ctx).Table("indexed_events").Omit("Transaction").Omit("Block").CreateInBatches(events, ix.insertDBBatchSize).Error; err != nil {
					return err
				}
			}
			return nil
		})
	})
}

// convertLedgerUnit maps the chain-neutral LedgerUnit/IndexedChainTx/IndexedChainEvent shapes
// (baseledger/types.go) onto the persisted pldapi types, populating only the chain-neutral
// *Chain address columns - the EVM-only fixed-width From/To/ContractAddress columns are left nil,
// since a Stellar StrKey address cannot fit them (see this chapter's DBPublicTxn migration for the
// same distinction).
func convertLedgerUnit(unit *baseledger.LedgerUnit) (*pldapi.IndexedBlock, []*blockindexer.IndexedTransactionNotify, []*pldapi.IndexedEvent) {
	block := &pldapi.IndexedBlock{
		Number:    int64(unit.Sequence), //nolint:gosec // ledger sequences are always positive
		Hash:      unit.Hash,
		Timestamp: unit.Timestamp,
	}

	txHashByIndex := make(map[int64]pldtypes.Bytes32, len(unit.Txs))
	notifyTxs := make([]*blockindexer.IndexedTransactionNotify, 0, len(unit.Txs))
	for _, tx := range unit.Txs {
		txHashByIndex[tx.TxIndex] = pldtypes.Bytes32(tx.TxID)
		result := pldapi.TXResult_FAILURE.Enum()
		if tx.Result == "SUCCESS" {
			result = pldapi.TXResult_SUCCESS.Enum()
		}
		from := tx.From
		notifyTxs = append(notifyTxs, &blockindexer.IndexedTransactionNotify{
			IndexedTransaction: pldapi.IndexedTransaction{
				Hash:             pldtypes.Bytes32(tx.TxID),
				BlockNumber:      block.Number,
				TransactionIndex: tx.TxIndex,
				FromChain:        &from,
				Nonce:            tx.Nonce,
				Result:           result,
			},
			RevertReason: tx.RevertData,
		})
	}

	events := make([]*pldapi.IndexedEvent, 0, len(unit.Events))
	for _, ev := range unit.Events {
		events = append(events, &pldapi.IndexedEvent{
			BlockNumber:      block.Number,
			TransactionIndex: ev.TxIndex,
			LogIndex:         ev.EventIndex,
			TransactionHash:  txHashByIndex[ev.TxIndex],
			Signature:        ev.Selector,
		})
	}

	return block, notifyTxs, events
}
