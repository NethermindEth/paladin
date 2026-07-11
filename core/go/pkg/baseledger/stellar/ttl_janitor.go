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

// ttl_janitor.go implements chapter 12 §12.6's ttlJanitor: a background task that keeps a
// caller-registered set of Soroban contract storage ledger entries from being archived by
// periodically checking each entry's liveUntilLedgerSeq (via getLedgerEntries) and submitting
// batched ExtendFootprintTtl operations for any entry that has fallen below a configured
// ledger-count threshold. It deliberately has no notion of *which* entries matter - chapter 12's
// own scope-creep warning (risk R22, echoed in classic_ops.go) applies equally here: no domain
// contract exists yet to auto-discover keys from, so callers (a future domain plugin) register the
// keys they own via Watch/Unwatch. Until something calls those, the janitor simply polls an empty
// set and does nothing.
package stellar

import (
	"context"
	"sync"
	"time"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/log"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// TxSigner signs tx on behalf of the janitor's configured source account, returning the same
// transaction with a decorated signature attached (the shape
// stellarChainSubmitter.signAndSerializeStellarTx builds internally via
// tx.AddSignatureDecorated) - exposed as a caller-supplied hook rather than a direct KeyManager
// dependency so this package doesn't need to import internal/keymanager (componentmgr, which does
// have both, constructs the closure - see initStellarTTLJanitor).
type TxSigner func(ctx context.Context, tx *txnbuild.Transaction) (*txnbuild.Transaction, error)

// TTLJanitorConfig carries the resolved (already-defaulted) tunables for NewTTLJanitor - the
// pldconf.TTLJanitorConfig counterpart with string/pointer fields resolved to concrete values,
// mirroring how NewIngestor takes a plain time.Duration rather than the raw *string config field.
type TTLJanitorConfig struct {
	// PollInterval is how often the watch list is re-checked.
	PollInterval time.Duration
	// Threshold is the number of ledgers remaining before expiry (liveUntilLedgerSeq - current
	// ledger) below which an entry is queued for a TTL extension.
	Threshold uint32
	// ExtendBy is the ExtendFootprintTtlOp.ExtendTo value submitted for any entry below
	// Threshold - the number of ledgers, counted from the current ledger, the entry's TTL should
	// be extended to cover (Soroban's "extend_to" semantics: an absolute ledger-count-from-now,
	// not a delta added to the entry's current liveUntilLedgerSeq).
	ExtendBy uint32
	// BatchSize is the maximum number of ledger keys combined into a single ExtendFootprintTtl
	// operation's footprint (and therefore a single transaction) per extend attempt.
	BatchSize int
	// SourceAccount is the Stellar G... address the extend_ttl transactions are built and signed
	// from - resolved once by the caller (componentmgr) from the configured signer identity.
	SourceAccount string
	Sign          TxSigner
}

// TTLJanitor is the chapter 12 §12.6 background task. Construct with NewTTLJanitor, register
// entries to watch via Watch (Unwatch to stop), then Start(ctx) to begin polling; Stop() cancels
// and waits for the poll loop to exit - the same context-cancellation-plus-wait idiom used
// elsewhere for background services in this codebase (e.g. ledgerindexer/stellar.Indexer.Stop).
type TTLJanitor struct {
	rpc    rpcClient
	client *Client
	cfg    TTLJanitorConfig

	mu      sync.Mutex
	watched map[string]xdr.LedgerKey // keyed by MarshalBinaryBase64() for de-duplication

	cancel  context.CancelFunc
	stopped chan struct{}
}

// NewTTLJanitor constructs a TTLJanitor. rpc is typically the same *rpcclient.Client
// stellarclient.NewClient constructs (it satisfies rpcClient structurally, as elsewhere in this
// package). networkPassphrase is used only to sign transactions built here (via cfg.Sign's own
// closure, not by this constructor) - it is accepted so a *Client can be built internally to reuse
// Client.Submit's SubmissionRejectedError classification rather than duplicating it.
func NewTTLJanitor(rpc rpcClient, networkPassphrase string, cfg TTLJanitorConfig) *TTLJanitor {
	return &TTLJanitor{
		rpc:     rpc,
		client:  WrapClient(rpc, networkPassphrase, nil),
		cfg:     cfg,
		watched: make(map[string]xdr.LedgerKey),
	}
}

// Watch registers a ledger key (a contract storage entry, typically a LedgerKeyContractData) for
// TTL monitoring. Safe to call concurrently, including while Start's poll loop is running - the
// mechanism a future domain plugin will use to hand the janitor its own storage keys, even though
// nothing in this slice calls it yet (chapter 12 §12.6's "Implementation status": no real domain
// exists to own keys yet).
func (j *TTLJanitor) Watch(key xdr.LedgerKey) error {
	keyStr, err := key.MarshalBinaryBase64()
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.watched[keyStr] = key
	return nil
}

// Unwatch removes a previously registered key. A no-op if key was never registered (or was already
// removed).
func (j *TTLJanitor) Unwatch(key xdr.LedgerKey) error {
	keyStr, err := key.MarshalBinaryBase64()
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.watched, keyStr)
	return nil
}

// watchedKeys returns a point-in-time snapshot of the watch list - taken once per poll tick so a
// concurrent Watch/Unwatch call never races with the in-flight getLedgerEntries/extend pass.
func (j *TTLJanitor) watchedKeys() []xdr.LedgerKey {
	j.mu.Lock()
	defer j.mu.Unlock()
	keys := make([]xdr.LedgerKey, 0, len(j.watched))
	for _, k := range j.watched {
		keys = append(keys, k)
	}
	return keys
}

// Start begins the poll loop as a background goroutine and returns immediately - matching
// Ingestor.StreamLedgers/poll's own "returns immediately, runs in the background" shape. Calling
// Start twice without an intervening Stop is not supported (mirrors every other background task in
// this codebase: componentmgr never does this).
func (j *TTLJanitor) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	j.cancel = cancel
	j.stopped = make(chan struct{})
	go j.run(ctx)
}

// Stop cancels the poll loop's context and waits for it to exit - so a caller's own shutdown
// sequence (componentmgr.Stop) never races a poll tick still in flight against a closed
// connection.
func (j *TTLJanitor) Stop() {
	if j.cancel != nil {
		j.cancel()
	}
	if j.stopped != nil {
		<-j.stopped
	}
}

func (j *TTLJanitor) run(ctx context.Context) {
	defer close(j.stopped)
	ticker := time.NewTicker(j.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		j.tick(ctx)
	}
}

// tick performs one poll pass: fetch current TTLs for every watched key, then extend (in batches
// of at most cfg.BatchSize) whichever are below cfg.Threshold. Errors are logged and left for the
// next tick to retry - extend_ttl is idempotent (re-extending an entry that's already safely above
// threshold is a correctness no-op, just a wasted fee), so there is no special backoff beyond the
// regular poll interval, mirroring Ingestor.poll's own "log and retry next tick" idiom.
func (j *TTLJanitor) tick(ctx context.Context) {
	keys := j.watchedKeys()
	if len(keys) == 0 {
		return
	}

	keyStrs := make([]string, 0, len(keys))
	byKeyStr := make(map[string]xdr.LedgerKey, len(keys))
	for _, k := range keys {
		keyStr, err := k.MarshalBinaryBase64()
		if err != nil {
			log.L(ctx).Errorf("ttlJanitor: failed to encode watched ledger key (skipping): %s", err)
			continue
		}
		keyStrs = append(keyStrs, keyStr)
		byKeyStr[keyStr] = k
	}
	if len(keyStrs) == 0 {
		return
	}

	resp, err := j.rpc.GetLedgerEntries(ctx, protocol.GetLedgerEntriesRequest{Keys: keyStrs})
	if err != nil {
		log.L(ctx).Errorf("ttlJanitor: getLedgerEntries failed (will retry next tick): %s", err)
		return
	}

	var toExtend []xdr.LedgerKey
	for _, entry := range resp.Entries {
		if entry.LiveUntilLedgerSeq == nil {
			// No associated TTL ledger entry - not a temporary/persistent Soroban entry (or it's
			// already been evicted entirely, in which case extend_ttl cannot help; restoring it is
			// a different operation, out of scope here per chapter 12 §12.6).
			continue
		}
		remaining := int64(*entry.LiveUntilLedgerSeq) - int64(resp.LatestLedger)
		if remaining >= int64(j.cfg.Threshold) {
			continue
		}
		key, ok := byKeyStr[entry.KeyXDR]
		if !ok {
			log.L(ctx).Warnf("ttlJanitor: getLedgerEntries returned an entry for an unrecognized key %q (skipping)", entry.KeyXDR)
			continue
		}
		toExtend = append(toExtend, key)
	}

	batchSize := j.cfg.BatchSize
	if batchSize <= 0 {
		batchSize = len(toExtend)
	}
	for len(toExtend) > 0 {
		n := batchSize
		if n > len(toExtend) {
			n = len(toExtend)
		}
		batch := toExtend[:n]
		toExtend = toExtend[n:]
		if err := j.extendBatch(ctx, batch); err != nil {
			log.L(ctx).Errorf("ttlJanitor: extend_ttl failed for a batch of %d entries (will retry next tick): %s", len(batch), err)
		}
	}
}

// extendBatch builds, simulates, signs, and submits a single transaction containing one
// ExtendFootprintTtl operation whose footprint covers every key in batch - this is the "batching"
// chapter 12 §12.6 calls for: N entries below threshold become one transaction (bounded by
// cfg.BatchSize), not N.
//
// Simulation happens twice, mirroring how Client.buildTransaction/EstimateResources and
// PrepareSubmission split "build a probe, simulate it, then rebuild with the simulation's own
// SorobanTransactionData" for InvokeHostFunction - unlike that path, ExtendFootprintTtl/
// RestoreFootprint have no invocation for stellar-rpc to introspect a footprint from, so the probe
// must already carry the footprint (the entries themselves) for simulateTransaction to compute the
// correct resources/fee against.
func (j *TTLJanitor) extendBatch(ctx context.Context, batch []xdr.LedgerKey) error {
	footprint := xdr.LedgerFootprint{ReadOnly: batch}

	account, err := j.rpc.LoadAccount(ctx, j.cfg.SourceAccount)
	if err != nil {
		return err
	}
	seq, err := account.GetSequenceNumber()
	if err != nil {
		return err
	}

	buildTx := func(op *txnbuild.ExtendFootprintTtl) (*txnbuild.Transaction, error) {
		src := txnbuild.NewSimpleAccount(j.cfg.SourceAccount, seq)
		return txnbuild.NewTransaction(txnbuild.TransactionParams{
			SourceAccount:        &src,
			IncrementSequenceNum: true,
			Operations:           []txnbuild.Operation{op},
			BaseFee:              txnbuild.MinBaseFee,
			Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
		})
	}

	probeTx, err := buildTx(&txnbuild.ExtendFootprintTtl{
		ExtendTo:      j.cfg.ExtendBy,
		SourceAccount: j.cfg.SourceAccount,
		Ext: xdr.TransactionExt{
			V:           1,
			SorobanData: &xdr.SorobanTransactionData{Resources: xdr.SorobanResources{Footprint: footprint}},
		},
	})
	if err != nil {
		return err
	}
	probeTxBase64, err := probeTx.Base64()
	if err != nil {
		return err
	}

	simResp, err := j.rpc.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{Transaction: probeTxBase64})
	if err != nil {
		return err
	}
	if simResp.Error != "" {
		return &SubmissionRejectedError{Status: "SIMULATION_ERROR", ErrorResultXDR: simResp.Error}
	}

	sorobanData := xdr.SorobanTransactionData{Resources: xdr.SorobanResources{Footprint: footprint}}
	if simResp.TransactionDataXDR != "" {
		if unmarshalErr := xdr.SafeUnmarshalBase64(simResp.TransactionDataXDR, &sorobanData); unmarshalErr != nil {
			return unmarshalErr
		}
	}

	finalTx, err := buildTx(&txnbuild.ExtendFootprintTtl{
		ExtendTo:      j.cfg.ExtendBy,
		SourceAccount: j.cfg.SourceAccount,
		Ext:           xdr.TransactionExt{V: 1, SorobanData: &sorobanData},
	})
	if err != nil {
		return err
	}

	if j.cfg.Sign == nil {
		return &SubmissionRejectedError{Status: "NO_SIGNER_CONFIGURED"}
	}
	finalTx, err = j.cfg.Sign(ctx, finalTx)
	if err != nil {
		return err
	}
	rawTransaction, err := finalTx.MarshalBinary()
	if err != nil {
		return err
	}

	txID, submitErr := j.client.Submit(ctx, rawTransaction)
	if submitErr != nil {
		return submitErr
	}
	log.L(ctx).Infof("ttlJanitor: submitted extend_ttl transaction %s for %d entries (extend_to=%d)", txID, len(batch), j.cfg.ExtendBy)
	return nil
}
