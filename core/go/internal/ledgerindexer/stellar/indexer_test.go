// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package stellar

import (
	"context"
	"testing"
	"time"

	"github.com/LFDT-Paladin/paladin/config/pkg/pldconf"
	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	"github.com/LFDT-Paladin/paladin/core/pkg/blockindexer"
	"github.com/LFDT-Paladin/paladin/core/pkg/persistence"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/retry"
	"github.com/stretchr/testify/require"
)

type fakeIngestor struct {
	streamLedgers func(ctx context.Context, from baseledger.LedgerCheckpoint) (<-chan *baseledger.LedgerUnit, error)
}

func (f *fakeIngestor) StreamLedgers(ctx context.Context, from baseledger.LedgerCheckpoint) (<-chan *baseledger.LedgerUnit, error) {
	return f.streamLedgers(ctx, from)
}
func (f *fakeIngestor) BackfillSource() baseledger.BackfillCapability { return baseledger.BackfillArchive }
func (f *fakeIngestor) TipHeight(_ context.Context) (uint64, error)   { return 0, nil }

func newTestPersistence(t *testing.T) persistence.Persistence {
	p, err := persistence.NewPersistence(context.Background(), &pldconf.DBConfig{
		Type: "sqlite",
		SQLite: pldconf.SQLiteConfig{
			SQLDBConfig: pldconf.SQLDBConfig{
				DSN:           ":memory:",
				AutoMigrate:   boolPtr(true),
				MigrationsDir: "../../../db/migrations/sqlite",
			},
		},
	})
	require.NoError(t, err)
	t.Cleanup(p.Close)
	return p
}

func boolPtr(b bool) *bool { return &b }

func testLedgerUnit(sequence uint64) *baseledger.LedgerUnit {
	from, _ := pldtypes.NewStellarAccountAddress("GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF")
	contract, _ := pldtypes.NewStellarContractAddress("CAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAABSC4")
	return &baseledger.LedgerUnit{
		Sequence:  sequence,
		Hash:      pldtypes.RandBytes32(),
		Timestamp: pldtypes.TimestampFromUnix(1234567890),
		Txs: []*baseledger.IndexedChainTx{
			{
				TxID:    pldtypes.RandBytes32(),
				From:    from,
				Nonce:   7,
				Result:  "SUCCESS",
				TxIndex: 0,
			},
		},
		Events: []*baseledger.IndexedChainEvent{
			{
				Sequence:   sequence,
				TxIndex:    0,
				EventIndex: 0,
				Emitter:    contract,
				Selector:   pldtypes.RandBytes32(),
			},
		},
	}
}

func TestIndexerNotReadyBeforeAnyLedger(t *testing.T) {
	p := newTestPersistence(t)
	ix := NewIndexer(context.Background(), &fakeIngestor{}, p, retry.NewRetryIndefinite(&pldconf.RetryConfig{}), 10)
	require.Error(t, ix.Ready(context.Background()))
}

func TestIndexerWritesLedgerAndInvokesPreCommitHandler(t *testing.T) {
	p := newTestPersistence(t)

	ch := make(chan *baseledger.LedgerUnit, 1)
	ch <- testLedgerUnit(200)
	ingestor := &fakeIngestor{
		streamLedgers: func(ctx context.Context, from baseledger.LedgerCheckpoint) (<-chan *baseledger.LedgerUnit, error) {
			require.Equal(t, baseledger.LedgerCheckpoint{}, from) // no prior checkpoint
			return ch, nil
		},
	}
	ix := NewIndexer(context.Background(), ingestor, p, retry.NewRetryIndefinite(&pldconf.RetryConfig{}), 10)

	var handlerCalled bool
	var handlerBlocks []*pldapi.IndexedBlock
	var handlerTxs []*blockindexer.IndexedTransactionNotify
	handler := func(ctx context.Context, dbTX persistence.DBTX, blocks []*pldapi.IndexedBlock, transactions []*blockindexer.IndexedTransactionNotify) error {
		handlerCalled = true
		handlerBlocks = blocks
		handlerTxs = transactions
		return nil
	}

	require.NoError(t, ix.Start(handler))
	defer ix.Stop()

	require.Eventually(t, func() bool {
		return ix.Ready(context.Background()) == nil
	}, 2*time.Second, 10*time.Millisecond)

	require.True(t, handlerCalled)
	require.Len(t, handlerBlocks, 1)
	require.EqualValues(t, 200, handlerBlocks[0].Number)
	require.Len(t, handlerTxs, 1)
	require.NotNil(t, handlerTxs[0].FromChain)
	require.Equal(t, "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF", handlerTxs[0].FromChain.String())

	// Verify the rows are actually persisted with the chain-neutral columns populated
	var block pldapi.IndexedBlock
	require.NoError(t, p.DB().Table("indexed_blocks").Where("number = ?", 200).First(&block).Error)

	var tx pldapi.IndexedTransaction
	require.NoError(t, p.DB().Table("indexed_transactions").Where("block_number = ?", 200).First(&tx).Error)
	require.Nil(t, tx.From, "EVM-only column should be left nil for a Stellar transaction")
	require.NotNil(t, tx.FromChain)
	require.EqualValues(t, 7, tx.Nonce)
	require.Equal(t, pldtypes.Enum[pldapi.TransactionResult](pldapi.TXResult_SUCCESS), tx.Result)

	var event pldapi.IndexedEvent
	require.NoError(t, p.DB().Table("indexed_events").Where("block_number = ?", 200).First(&event).Error)
	require.Equal(t, tx.Hash, event.TransactionHash)
}

func TestIndexerLoadCheckpointResumesFromHighestBlock(t *testing.T) {
	p := newTestPersistence(t)
	existingHash := pldtypes.RandBytes32()
	require.NoError(t, p.DB().Table("indexed_blocks").Create(&pldapi.IndexedBlock{
		Number:    150,
		Hash:      existingHash,
		Timestamp: pldtypes.TimestampNow(),
	}).Error)

	var capturedCheckpoint baseledger.LedgerCheckpoint
	ingestor := &fakeIngestor{
		streamLedgers: func(ctx context.Context, from baseledger.LedgerCheckpoint) (<-chan *baseledger.LedgerUnit, error) {
			capturedCheckpoint = from
			ch := make(chan *baseledger.LedgerUnit)
			close(ch)
			return ch, nil
		},
	}
	ix := NewIndexer(context.Background(), ingestor, p, retry.NewRetryIndefinite(&pldconf.RetryConfig{}), 10)
	require.NoError(t, ix.Start())
	defer ix.Stop()

	require.Equal(t, uint64(150), capturedCheckpoint.Sequence)
	require.Equal(t, existingHash, capturedCheckpoint.Hash)
	// A pre-existing checkpoint means we're already "ready" before any new ledger arrives
	require.NoError(t, ix.Ready(context.Background()))
}
