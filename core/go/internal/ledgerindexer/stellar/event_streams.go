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

// event_streams.go implements blockindexer.EventStreamManager for Stellar: the narrow event-stream
// registration/dispatch surface domainmgr/registrymgr/txmgr actually use (AddEventStream,
// RemoveEventStream, QueryEventStreamDefinitions, StartEventStream, StopEventStream,
// GetEventStreamStatus), backed by the SAME event_streams/event_stream_checkpoints tables the EVM
// engine (pkg/blockindexer/event_streams.go) uses.
//
// This is deliberately much simpler than the EVM engine: Stellar's SCP finality means there is no
// reorg/confirmation-depth reconciliation to do - "ingested" already means "final". So instead of
// the EVM engine's detector/dispatcher split (which exists to smoothly track a moving,
// reorg-capable chain head), each stream here just repeatedly queries indexed_blocks/indexed_events
// (plus this package's stellar_event_payloads companion table - see indexer.go's writeLedger) for
// anything newer than its checkpoint, delivers it, and advances the checkpoint. The same query path
// serves both "catchup" (stream registered after ledgers were already ingested) and "live"
// processing (stream already caught up, woken by a tap from writeLedger after each new ledger) -
// there is no separate code path for the two, unlike the EVM engine.
package stellar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/common/go/pkg/log"
	"github.com/LFDT-Paladin/paladin/core/internal/filters"
	"github.com/LFDT-Paladin/paladin/core/internal/msgs"
	"github.com/LFDT-Paladin/paladin/core/pkg/blockindexer"
	"github.com/LFDT-Paladin/paladin/core/pkg/persistence"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/query"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/retry"
	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

const (
	defaultPollInterval    = 200 * time.Millisecond
	defaultCatchupPageSize = 50
)

// EventStreamEngine is the Stellar counterpart to the EVM blockIndexer's event stream machinery -
// componentmgr constructs it alongside the Indexer, and returns it from EventStreamManager() for a
// type: stellar node (mirroring how BlockIndexer() itself is returned for type: evm).
type EventStreamEngine struct {
	ctx             context.Context
	cancelAll       context.CancelFunc
	persistence     persistence.Persistence
	retry           *retry.Retry
	pollInterval    time.Duration
	catchupPageSize int

	mux     sync.Mutex
	streams map[uuid.UUID]*esStream
}

// NewEventStreamEngine builds an engine ready to accept AddEventStream calls. It has no separate
// Start/Stop lifecycle tied to ledger-ingestion start: streams can be (and, per the domain/registry/
// txmgr init sequencing, routinely are) registered before the ledger indexer itself starts
// polling - they simply find nothing to catch up on until ledgers begin arriving. Stop() tears down
// all stream goroutines on node shutdown.
func NewEventStreamEngine(ctx context.Context, p persistence.Persistence, retry *retry.Retry) *EventStreamEngine {
	ctx = log.WithComponent(ctx, "stellareventstreams")
	ctx, cancel := context.WithCancel(ctx)
	return &EventStreamEngine{
		ctx:             ctx,
		cancelAll:       cancel,
		persistence:     p,
		retry:           retry,
		pollInterval:    defaultPollInterval,
		catchupPageSize: defaultCatchupPageSize,
		streams:         make(map[uuid.UUID]*esStream),
	}
}

// Stop cancels every stream's context and waits for any that had been started to exit.
func (eng *EventStreamEngine) Stop() {
	eng.cancelAll()
	eng.mux.Lock()
	streams := make([]*esStream, 0, len(eng.streams))
	for _, s := range eng.streams {
		streams = append(streams, s)
	}
	eng.mux.Unlock()
	for _, s := range streams {
		if s.runStarted.Load() {
			<-s.doneCh
		}
	}
}

// notifyLedger is called by Indexer.writeLedger after a ledger (and its stellar_event_payloads
// companion rows) have been successfully committed. It just wakes every stream so it re-runs its
// catchup query immediately, rather than waiting for the next poll tick - the query itself is
// exactly the same one used for a stream that is still working through a backlog.
func (eng *EventStreamEngine) notifyLedger() {
	eng.mux.Lock()
	defer eng.mux.Unlock()
	for _, s := range eng.streams {
		select {
		case s.tap <- struct{}{}:
		default:
		}
	}
}

// esStream is the Stellar counterpart to the EVM engine's eventStream - a single registered event
// stream's in-memory state and processing goroutine.
type esStream struct {
	ctx        context.Context
	cancel     context.CancelFunc
	definition *blockindexer.EventStreamDefinition

	useNOTX     bool
	handlerDBTX blockindexer.InternalStreamCallbackDBTX
	handlerNOTX blockindexer.InternalStreamCallbackNOTX

	tap     chan struct{}
	startCh chan struct{}
	doneCh  chan struct{}

	runStarted atomic.Bool // guards launching the goroutine exactly once
	active     atomic.Bool // Started flag - gates actual processing
	checkpoint atomic.Int64
	catchup    atomic.Bool
}

func (s *esStream) ID() uuid.UUID                                   { return s.definition.ID }
func (s *esStream) Definition() *blockindexer.EventStreamDefinition { return s.definition }
func (s *esStream) CheckpointBlock() int64                          { return s.checkpoint.Load() }

// AddEventStream upserts the stream definition into the same event_streams table the EVM engine
// uses, then ensures a processing goroutine exists for it (starting it immediately if Started).
func (eng *EventStreamEngine) AddEventStream(ctx context.Context, dbTX persistence.DBTX, ies *blockindexer.InternalEventStream) (blockindexer.EventStream, error) {
	ctx = log.WithComponent(ctx, "stellareventstreams")
	def := ies.Definition
	if def == nil {
		def = &blockindexer.EventStreamDefinition{}
	}
	if def.Type == "" {
		def.Type = blockindexer.EventStreamTypeInternal.Enum()
	}
	if err := pldtypes.ValidateSafeCharsStartEndAlphaNum(ctx, def.Name, pldtypes.DefaultNameMaxLen, "name"); err != nil {
		return nil, err
	}

	eng.mux.Lock()
	defer eng.mux.Unlock()

	var existing []*blockindexer.EventStreamDefinition
	err := dbTX.DB().
		Table("event_streams").
		Where("type = ?", def.Type).
		Where("name = ?", def.Name).
		WithContext(ctx).
		Find(&existing).
		Error
	if err != nil {
		return nil, err
	}

	if len(existing) > 0 {
		if len(existing[0].Sources) != len(def.Sources) {
			return nil, i18n.NewError(ctx, msgs.MsgBlockIndexerESSourceError)
		}
		def.ID = existing[0].ID
		err = dbTX.DB().
			Table("event_streams").
			Where("id = ?", def.ID).
			WithContext(ctx).
			Updates(&blockindexer.EventStreamDefinition{Config: def.Config}).
			Error
		if err != nil {
			return nil, err
		}
	} else {
		def.ID = uuid.New()
		err = dbTX.DB().
			Table("event_streams").
			WithContext(ctx).
			Create(def).
			Error
		if err != nil {
			return nil, err
		}
	}

	s := eng.streams[def.ID]
	if s == nil {
		sctx, cancel := context.WithCancel(eng.ctx)
		s = &esStream{
			ctx:        sctx,
			cancel:     cancel,
			definition: def,
			tap:        make(chan struct{}, 1),
			startCh:    make(chan struct{}, 1),
			doneCh:     make(chan struct{}),
		}
		s.checkpoint.Store(-1)
		s.catchup.Store(true)
		eng.streams[def.ID] = s
	} else {
		s.definition.Config = def.Config
	}

	if ies.Type == blockindexer.IESTypeEventStreamDBTX {
		s.handlerDBTX = ies.HandlerDBTX
	} else {
		s.useNOTX = true
		s.handlerNOTX = ies.HandlerNOTX
	}

	started := def.Started == nil || *def.Started
	s.active.Store(started)
	if s.runStarted.CompareAndSwap(false, true) {
		go s.run(eng)
	}
	if started {
		select {
		case s.startCh <- struct{}{}:
		default:
		}
	}

	return s, nil
}

func (eng *EventStreamEngine) RemoveEventStream(ctx context.Context, id uuid.UUID) error {
	ctx = log.WithComponent(ctx, "stellareventstreams")
	eng.mux.Lock()
	defer eng.mux.Unlock()

	s := eng.streams[id]
	if s == nil {
		return i18n.NewError(ctx, msgs.MsgBlockIndexerEventStreamNotFound, id)
	}
	err := eng.persistence.NOTX().DB().
		WithContext(ctx).
		Table("event_streams").
		Where("id = ?", id).
		Delete(&blockindexer.EventStreamDefinition{}).
		Error
	if err != nil {
		return err
	}
	s.cancel()
	if s.runStarted.Load() {
		<-s.doneCh
	}
	delete(eng.streams, id)
	return nil
}

func (eng *EventStreamEngine) QueryEventStreamDefinitions(ctx context.Context, dbTX persistence.DBTX, esType pldtypes.Enum[blockindexer.EventStreamType], jq *query.QueryJSON) ([]*blockindexer.EventStreamDefinition, error) {
	ctx = log.WithComponent(ctx, "stellareventstreams")
	if jq == nil || jq.Limit == nil || *jq.Limit == 0 {
		return nil, i18n.NewError(ctx, msgs.MsgBlockIndexerLimitRequired)
	}
	q := dbTX.DB().
		Table("event_streams").
		WithContext(ctx).
		Where("type = ?", esType)
	q = filters.BuildGORM(ctx, jq, q, blockindexer.EventStreamFilters)
	var results []*blockindexer.EventStreamDefinition
	err := q.Find(&results).Error
	return results, err
}

func (eng *EventStreamEngine) getStream(ctx context.Context, id uuid.UUID) (*esStream, error) {
	eng.mux.Lock()
	defer eng.mux.Unlock()
	s := eng.streams[id]
	if s == nil {
		return nil, i18n.NewError(ctx, msgs.MsgBlockIndexerEventStreamNotFound, id)
	}
	return s, nil
}

func (eng *EventStreamEngine) StartEventStream(ctx context.Context, id uuid.UUID) error {
	ctx = log.WithComponent(ctx, "stellareventstreams")
	s, err := eng.getStream(ctx, id)
	if err != nil {
		return err
	}
	err = eng.persistence.NOTX().DB().
		WithContext(ctx).
		Table("event_streams").
		Where("id = ?", id).
		Update("started", true).
		Error
	if err != nil {
		return err
	}
	s.active.Store(true)
	if s.runStarted.CompareAndSwap(false, true) {
		go s.run(eng)
	}
	select {
	case s.startCh <- struct{}{}:
	default:
	}
	return nil
}

func (eng *EventStreamEngine) StopEventStream(ctx context.Context, id uuid.UUID) error {
	ctx = log.WithComponent(ctx, "stellareventstreams")
	s, err := eng.getStream(ctx, id)
	if err != nil {
		return err
	}
	err = eng.persistence.NOTX().DB().
		WithContext(ctx).
		Table("event_streams").
		Where("id = ?", id).
		Update("started", false).
		Error
	if err != nil {
		return err
	}
	s.active.Store(false)
	return nil
}

func (eng *EventStreamEngine) GetEventStreamStatus(ctx context.Context, id uuid.UUID) (*blockindexer.EventStreamStatus, error) {
	ctx = log.WithComponent(ctx, "stellareventstreams")
	s, err := eng.getStream(ctx, id)
	if err != nil {
		return nil, err
	}
	return &blockindexer.EventStreamStatus{
		CheckpointBlock: s.checkpoint.Load(),
		Catchup:         s.catchup.Load(),
	}, nil
}

// run is the per-stream processing loop: wait to be active, load the initial checkpoint, then
// repeatedly process the next chunk of already-ingested ledgers until there's nothing left, at
// which point it waits for either a tap (new ledger ingested) or the poll interval before trying
// again.
func (s *esStream) run(eng *EventStreamEngine) {
	defer close(s.doneCh)

	for !s.active.Load() {
		select {
		case <-s.startCh:
		case <-s.ctx.Done():
			return
		}
	}

	checkpoint, err := eng.loadCheckpoint(s.ctx, s.definition)
	if err != nil {
		log.L(s.ctx).Debugf("exiting before loading checkpoint: %s", err)
		return
	}
	s.checkpoint.Store(checkpoint)

	ticker := time.NewTicker(eng.pollInterval)
	defer ticker.Stop()

	for {
		if !s.active.Load() {
			select {
			case <-s.startCh:
				continue
			case <-s.ctx.Done():
				return
			}
		}

		advanced, err := eng.processNextChunk(s)
		if err != nil {
			log.L(s.ctx).Debugf("exiting during dispatch: %s", err)
			return
		}
		if !advanced {
			select {
			case <-s.tap:
			case <-ticker.C:
			case <-s.startCh:
			case <-s.ctx.Done():
				return
			}
		}
	}
}

// loadCheckpoint reads the persisted checkpoint, or (on first run) resolves the stream's
// fromBlock config into an initial checkpoint - mirroring the EVM engine's readDBCheckpoint.
func (eng *EventStreamEngine) loadCheckpoint(ctx context.Context, def *blockindexer.EventStreamDefinition) (int64, error) {
	var checkpoints []*blockindexer.EventStreamCheckpoint
	err := eng.persistence.DB().
		Table("event_stream_checkpoints").
		Where("stream = ?", def.ID).
		WithContext(ctx).
		Find(&checkpoints).
		Error
	if err != nil {
		return 0, err
	}
	if len(checkpoints) > 0 {
		return checkpoints[0].BlockNumber, nil
	}

	fromLedger, err := parseFromLedger(def.Config.FromBlock)
	if err != nil {
		return 0, err
	}
	if fromLedger == nil {
		// "latest": start from whatever is currently the chain tip - i.e. skip any backlog.
		highest, err := eng.getHighestIndexedLedger(ctx)
		if err != nil {
			return 0, err
		}
		if highest == nil {
			return -1, nil
		}
		return *highest, nil
	}
	return *fromLedger - 1, nil
}

func (eng *EventStreamEngine) getHighestIndexedLedger(ctx context.Context) (*int64, error) {
	var blocks []*pldapi.IndexedBlock
	err := eng.persistence.DB().
		Table("indexed_blocks").
		Order("number DESC").
		Limit(1).
		WithContext(ctx).
		Find(&blocks).
		Error
	if err != nil || len(blocks) == 0 {
		return nil, err
	}
	return &blocks[0].Number, nil
}

// parseFromLedger mirrors blockIndexer.getFromBlockStr's "latest" or numeric JSON convention
// (blockindexer.EventStreamsDefaults.FromBlock, `0` meaning "from the beginning").
func parseFromLedger(raw json.RawMessage) (*int64, error) {
	if len(raw) == 0 {
		raw = blockindexer.EventStreamsDefaults.FromBlock
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var s string
	switch t := v.(type) {
	case string:
		s = t
	case json.Number:
		s = t.String()
	default:
		return nil, fmt.Errorf("invalid fromBlock value %v", raw)
	}
	if strings.EqualFold(s, "latest") {
		return nil, nil
	}
	n, err := strconv.ParseInt(s, 0, 64)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// processNextChunk finds the next page of already-ingested ledgers after the stream's checkpoint,
// matches their events against the stream's sources, dispatches any matches, and advances the
// checkpoint. Returns advanced=true if it found (and processed) at least one ledger, so the caller
// knows to immediately try again rather than waiting.
func (eng *EventStreamEngine) processNextChunk(s *esStream) (bool, error) {
	var blocks []*pldapi.IndexedBlock
	checkpoint := s.checkpoint.Load()
	err := eng.retry.Do(s.ctx, func(attempt int) (bool, error) {
		return true, eng.persistence.DB().
			Table("indexed_blocks").
			Where("number > ?", checkpoint).
			Order("number").
			Limit(eng.catchupPageSize).
			WithContext(s.ctx).
			Find(&blocks).
			Error
	})
	if err != nil {
		return false, err
	}
	if len(blocks) == 0 {
		s.catchup.Store(false)
		return false, nil
	}
	s.catchup.Store(true)

	fromBlock := blocks[0].Number
	toBlock := blocks[len(blocks)-1].Number

	events, err := eng.queryMatchingEvents(s, fromBlock, toBlock)
	if err != nil {
		return false, err
	}

	if len(events) > 0 {
		if err := eng.dispatch(s, events, toBlock); err != nil {
			return false, err
		}
	} else if err := eng.updateCheckpoint(s.ctx, eng.persistence.NOTX(), s.definition.ID, toBlock); err != nil {
		return false, err
	} else {
		s.checkpoint.Store(toBlock)
	}
	return true, nil
}

// queryMatchingEvents fetches candidate events (already narrowed by selector at the SQL level)
// from indexed_events in the given ledger range, joins in the stellar_event_payloads companion row
// for the address/topics/data the shared indexed_events table doesn't carry, and applies the final
// per-source match (selector + optional address) in Go.
func (eng *EventStreamEngine) queryMatchingEvents(s *esStream, fromBlock, toBlock int64) ([]*pldapi.EventWithData, error) {
	var allSelectors []pldtypes.Bytes32
	for _, src := range s.definition.Sources {
		allSelectors = append(allSelectors, src.Selectors...)
	}
	if len(allSelectors) == 0 {
		return nil, nil
	}

	var indexedEvents []*pldapi.IndexedEvent
	err := eng.retry.Do(s.ctx, func(attempt int) (bool, error) {
		return true, eng.persistence.DB().
			Table("indexed_events").
			Where("block_number >= ? AND block_number <= ?", fromBlock, toBlock).
			Where("signature IN (?)", allSelectors).
			Order("block_number").Order("transaction_index").Order("log_index").
			WithContext(s.ctx).
			Find(&indexedEvents).
			Error
	})
	if err != nil || len(indexedEvents) == 0 {
		return nil, err
	}

	var payloads []*stellarEventPayload
	err = eng.retry.Do(s.ctx, func(attempt int) (bool, error) {
		return true, eng.persistence.DB().
			Table(stellarEventPayloadsTable).
			Where("block_number >= ? AND block_number <= ?", fromBlock, toBlock).
			WithContext(s.ctx).
			Find(&payloads).
			Error
	})
	if err != nil {
		return nil, err
	}
	payloadByKey := make(map[eventKey]*stellarEventPayload, len(payloads))
	for _, p := range payloads {
		payloadByKey[eventKey{p.BlockNumber, p.TransactionIndex, p.LogIndex}] = p
	}

	events := make([]*pldapi.EventWithData, 0, len(indexedEvents))
	for _, ie := range indexedEvents {
		payload := payloadByKey[eventKey{ie.BlockNumber, ie.TransactionIndex, ie.LogIndex}]
		if payload == nil {
			// Should not happen - written atomically alongside indexed_events in writeLedger.
			continue
		}
		for _, source := range s.definition.Sources {
			if !eventMatchesSource(payload.Emitter, ie.Signature, source) {
				continue
			}
			emitter := payload.Emitter
			events = append(events, &pldapi.EventWithData{
				IndexedEvent: ie,
				AddressChain: &emitter,
				Data:         payload.dataJSON(),
			})
			break
		}
	}
	return events, nil
}

// eventMatchesSource applies one EventStreamSource's filter to a candidate event. Stellar sources
// are described purely by Selectors (there is no ABI to decode) - a source with no Selectors
// configured contributes nothing, mirroring how an EVM source with an empty ABI never decodes any
// event.
func eventMatchesSource(emitter pldtypes.ChainAddress, selector pldtypes.Bytes32, source blockindexer.EventStreamSource) bool {
	if len(source.Selectors) == 0 {
		return false
	}
	matched := false
	for _, sel := range source.Selectors {
		if sel == selector {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	if source.Address != nil && !source.Address.IsZero() && !source.Address.Equals(&emitter) {
		return false
	}
	return true
}

func (eng *EventStreamEngine) dispatch(s *esStream, events []*pldapi.EventWithData, checkpointAfter int64) error {
	batch := &blockindexer.EventDeliveryBatch{
		StreamID:   s.definition.ID,
		StreamName: s.definition.Name,
		BatchID:    uuid.New(),
		Events:     events,
	}
	return eng.retry.Do(s.ctx, func(attempt int) (bool, error) {
		var err error
		if s.useNOTX {
			err = s.handlerNOTX(s.ctx, batch)
			if err == nil {
				err = eng.updateCheckpoint(s.ctx, eng.persistence.NOTX(), s.definition.ID, checkpointAfter)
			}
		} else {
			err = eng.persistence.Transaction(s.ctx, func(ctx context.Context, dbTX persistence.DBTX) error {
				if err := s.handlerDBTX(ctx, dbTX, batch); err != nil {
					return err
				}
				return eng.updateCheckpoint(ctx, dbTX, s.definition.ID, checkpointAfter)
			})
		}
		if err == nil {
			s.checkpoint.Store(checkpointAfter)
		}
		return true, err
	})
}

func (eng *EventStreamEngine) updateCheckpoint(ctx context.Context, dbTX persistence.DBTX, streamID uuid.UUID, blockNumber int64) error {
	return dbTX.DB().
		WithContext(ctx).
		Table("event_stream_checkpoints").
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "stream"}},
			DoUpdates: clause.AssignmentColumns([]string{"block_number"}),
		}).
		Create(&blockindexer.EventStreamCheckpoint{
			Stream:      streamID,
			BlockNumber: blockNumber,
		}).
		Error
}
