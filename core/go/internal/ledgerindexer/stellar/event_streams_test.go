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
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/retry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestRetry() *retry.Retry {
	return retry.NewRetryIndefinite(&pldconf.RetryConfig{})
}

// TestEventStreamDeliveryLive registers a stream (with Selectors matching a specific event) BEFORE
// the ledger containing that event is ingested, and asserts it is delivered live - i.e. via
// notifyLedger's tap after writeLedger commits, not via a catchup query finding a backlog.
func TestEventStreamDeliveryLive(t *testing.T) {
	p := newTestPersistence(t)
	ctx := context.Background()

	selector := pldtypes.RandBytes32()

	eng := NewEventStreamEngine(ctx, p, newTestRetry())
	defer eng.Stop()

	delivered := make(chan *blockindexer.EventDeliveryBatch, 1)
	handler := func(_ context.Context, _ persistence.DBTX, batch *blockindexer.EventDeliveryBatch) error {
		delivered <- batch
		return nil
	}

	// Register the stream before any ledger has been ingested.
	stream, err := eng.AddEventStream(ctx, p.NOTX(), &blockindexer.InternalEventStream{
		Definition: &blockindexer.EventStreamDefinition{
			Name: "live_stream",
			Sources: []blockindexer.EventStreamSource{{
				Selectors: []pldtypes.Bytes32{selector},
			}},
		},
		HandlerDBTX: handler,
	})
	require.NoError(t, err)
	require.NotNil(t, stream)

	unit := testLedgerUnit(200)
	unit.Events[0].Selector = selector

	ch := make(chan *baseledger.LedgerUnit, 1)
	ch <- unit
	ingestor := &fakeIngestor{
		streamLedgers: func(_ context.Context, from baseledger.LedgerCheckpoint) (<-chan *baseledger.LedgerUnit, error) {
			require.Equal(t, baseledger.LedgerCheckpoint{}, from)
			return ch, nil
		},
	}
	ix := NewIndexer(ctx, ingestor, p, newTestRetry(), 10)
	ix.SetEventStreamEngine(eng)
	require.NoError(t, ix.Start())
	defer ix.Stop()

	select {
	case batch := <-delivered:
		require.Len(t, batch.Events, 1)
		assert.Equal(t, selector, batch.Events[0].Signature)
		require.NotNil(t, batch.Events[0].AddressChain)
		assert.EqualValues(t, 200, batch.Events[0].BlockNumber)
	case <-time.After(3 * time.Second):
		t.Fatal("event was not delivered live")
	}

	// The checkpoint is only persisted (and stream.checkpoint updated) once the handler-invoking
	// transaction commits - which happens just after the handler sends on the delivered channel
	// above, so poll for it rather than asserting it synchronously with the delivery itself.
	require.Eventually(t, func() bool {
		return stream.CheckpointBlock() == 200
	}, 2*time.Second, 10*time.Millisecond)
}

// TestEventStreamDeliveryCatchup ingests a ledger, waits for it to be fully persisted (including
// the stellar_event_payloads companion row), and only THEN registers a stream with a selector
// matching that ledger's event - asserting it is still delivered, via the catchup query path
// (event_streams.go's processNextChunk/queryMatchingEvents) rather than any live tap.
func TestEventStreamDeliveryCatchup(t *testing.T) {
	p := newTestPersistence(t)
	ctx := context.Background()

	selector := pldtypes.RandBytes32()
	unit := testLedgerUnit(300)
	unit.Events[0].Selector = selector

	ch := make(chan *baseledger.LedgerUnit, 1)
	ch <- unit
	ingestor := &fakeIngestor{
		streamLedgers: func(_ context.Context, _ baseledger.LedgerCheckpoint) (<-chan *baseledger.LedgerUnit, error) {
			return ch, nil
		},
	}

	// No event stream engine wired up yet - this ledger is ingested with nobody watching.
	ix := NewIndexer(ctx, ingestor, p, newTestRetry(), 10)
	require.NoError(t, ix.Start())
	defer ix.Stop()

	require.Eventually(t, func() bool {
		return ix.Ready(ctx) == nil
	}, 2*time.Second, 10*time.Millisecond, "ledger should have been ingested")

	// Only now do we construct the engine and register a stream with a matching selector.
	eng := NewEventStreamEngine(ctx, p, newTestRetry())
	defer eng.Stop()

	delivered := make(chan *blockindexer.EventDeliveryBatch, 1)
	handler := func(_ context.Context, _ persistence.DBTX, batch *blockindexer.EventDeliveryBatch) error {
		delivered <- batch
		return nil
	}
	stream, err := eng.AddEventStream(ctx, p.NOTX(), &blockindexer.InternalEventStream{
		Definition: &blockindexer.EventStreamDefinition{
			Name: "catchup_stream",
			Sources: []blockindexer.EventStreamSource{{
				Selectors: []pldtypes.Bytes32{selector},
			}},
		},
		HandlerDBTX: handler,
	})
	require.NoError(t, err)
	require.NotNil(t, stream)

	select {
	case batch := <-delivered:
		require.Len(t, batch.Events, 1)
		assert.Equal(t, selector, batch.Events[0].Signature)
		require.NotNil(t, batch.Events[0].AddressChain)
		assert.EqualValues(t, 300, batch.Events[0].BlockNumber)
	case <-time.After(3 * time.Second):
		t.Fatal("event was not delivered via catchup")
	}

	// See the comment in TestEventStreamDeliveryLive - the checkpoint update races the delivery
	// signal by design (it happens right after, in the same transaction commit), so poll for it.
	require.Eventually(t, func() bool {
		return stream.CheckpointBlock() == 300
	}, 2*time.Second, 10*time.Millisecond)
}
