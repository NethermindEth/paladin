// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package stellar

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

// testLedgerKey builds a distinct, validly-encodable xdr.LedgerKey (a ContractCode key, the
// simplest union arm requiring only a 32-byte hash) for use as a watched entry in these tests -
// the janitor never inspects the key's semantic content, only round-trips it through
// MarshalBinaryBase64/getLedgerEntries, so the exact key type doesn't matter.
func testLedgerKey(b byte) xdr.LedgerKey {
	return xdr.LedgerKey{
		Type:         xdr.LedgerEntryTypeContractCode,
		ContractCode: &xdr.LedgerKeyContractCode{Hash: xdr.Hash{b}},
	}
}

// testTxHash builds a distinct, validly-parseable 32-byte-hex transaction hash for
// SendTransactionResponse fakes below - Client.Submit rejects anything shorter.
func testTxHash(b byte) string {
	return fmt.Sprintf("%062x%02x", 0, b)
}

// noopSigner returns tx unmodified - the janitor's tests care about which RPC calls happen and
// how many transactions are batched, not about producing a cryptographically valid signature.
func noopSigner(_ context.Context, tx *txnbuild.Transaction) (*txnbuild.Transaction, error) {
	return tx, nil
}

// fakeLedgerEntries builds a getLedgerEntries fake that reports liveUntilLedgerSeq for each
// requested key from the ttls map (keyed by the key's MarshalBinaryBase64 string), at the given
// latestLedger.
func fakeLedgerEntries(t *testing.T, latestLedger uint32, ttls map[byte]uint32) func(ctx context.Context, req protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
	// index keys by their own base64 encoding so the fake can look up the right liveUntilLedgerSeq
	// regardless of request order.
	byKeyStr := make(map[string]uint32, len(ttls))
	for b, ttl := range ttls {
		keyStr, err := testLedgerKey(b).MarshalBinaryBase64()
		require.NoError(t, err)
		byKeyStr[keyStr] = ttl
	}
	return func(ctx context.Context, req protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
		resp := protocol.GetLedgerEntriesResponse{LatestLedger: latestLedger}
		for _, k := range req.Keys {
			ttl, ok := byKeyStr[k]
			require.True(t, ok, "unexpected key in getLedgerEntries request: %s", k)
			liveUntil := ttl
			resp.Entries = append(resp.Entries, protocol.LedgerEntryResult{
				KeyXDR:             k,
				LiveUntilLedgerSeq: &liveUntil,
			})
		}
		return resp, nil
	}
}

func TestTTLJanitorAboveThresholdLeftAlone(t *testing.T) {
	ctx := context.Background()
	var sendCalls, simulateCalls int32

	rpc := &fakeRPC{
		getLedgerEntries: fakeLedgerEntries(t, 100, map[byte]uint32{1: 100_500}), // remaining = 100,400
		simulateTransaction: func(ctx context.Context, req protocol.SimulateTransactionRequest) (protocol.SimulateTransactionResponse, error) {
			atomic.AddInt32(&simulateCalls, 1)
			return protocol.SimulateTransactionResponse{}, nil
		},
		sendTransaction: func(ctx context.Context, req protocol.SendTransactionRequest) (protocol.SendTransactionResponse, error) {
			atomic.AddInt32(&sendCalls, 1)
			return protocol.SendTransactionResponse{Status: "PENDING", Hash: testTxHash(1)}, nil
		},
	}

	j := NewTTLJanitor(rpc, "", TTLJanitorConfig{
		Threshold:     1000,
		ExtendBy:      100000,
		BatchSize:     50,
		SourceAccount: testAccount,
		Sign:          noopSigner,
	})
	require.NoError(t, j.Watch(testLedgerKey(1)))

	j.tick(ctx)

	require.Zero(t, atomic.LoadInt32(&simulateCalls), "an entry above threshold must not be simulated")
	require.Zero(t, atomic.LoadInt32(&sendCalls), "an entry above threshold must not be submitted")
}

func TestTTLJanitorBelowThresholdTriggersExtend(t *testing.T) {
	ctx := context.Background()
	var sendCalls int32
	var lastReq protocol.SendTransactionRequest

	rpc := &fakeRPC{
		getLedgerEntries: fakeLedgerEntries(t, 100, map[byte]uint32{1: 100}), // remaining = 0
		loadAccount:      fakeAccountLoader(41),
		simulateTransaction: func(ctx context.Context, req protocol.SimulateTransactionRequest) (protocol.SimulateTransactionResponse, error) {
			require.NotEmpty(t, req.Transaction)
			return protocol.SimulateTransactionResponse{}, nil
		},
		sendTransaction: func(ctx context.Context, req protocol.SendTransactionRequest) (protocol.SendTransactionResponse, error) {
			atomic.AddInt32(&sendCalls, 1)
			lastReq = req
			return protocol.SendTransactionResponse{Status: "PENDING", Hash: testTxHash(1)}, nil
		},
	}

	j := NewTTLJanitor(rpc, "", TTLJanitorConfig{
		Threshold:     1000,
		ExtendBy:      100000,
		BatchSize:     50,
		SourceAccount: testAccount,
		Sign:          noopSigner,
	})
	require.NoError(t, j.Watch(testLedgerKey(1)))

	j.tick(ctx)

	require.EqualValues(t, 1, atomic.LoadInt32(&sendCalls), "an entry below threshold must trigger exactly one extend_ttl submission")
	require.NotEmpty(t, lastReq.Transaction)
}

func TestTTLJanitorBatchesMultipleEntries(t *testing.T) {
	ctx := context.Background()
	var sendCalls int32

	// 5 entries, all below threshold, batchSize 2 -> ceil(5/2) = 3 transactions, not 5.
	ttls := map[byte]uint32{1: 0, 2: 0, 3: 0, 4: 0, 5: 0}

	rpc := &fakeRPC{
		getLedgerEntries: fakeLedgerEntries(t, 100, ttls),
		loadAccount:      fakeAccountLoader(41),
		simulateTransaction: func(ctx context.Context, req protocol.SimulateTransactionRequest) (protocol.SimulateTransactionResponse, error) {
			return protocol.SimulateTransactionResponse{}, nil
		},
		sendTransaction: func(ctx context.Context, req protocol.SendTransactionRequest) (protocol.SendTransactionResponse, error) {
			n := atomic.AddInt32(&sendCalls, 1)
			return protocol.SendTransactionResponse{Status: "PENDING", Hash: testTxHash(byte(n))}, nil
		},
	}

	j := NewTTLJanitor(rpc, "", TTLJanitorConfig{
		Threshold:     1000,
		ExtendBy:      100000,
		BatchSize:     2,
		SourceAccount: testAccount,
		Sign:          noopSigner,
	})
	for b := range ttls {
		require.NoError(t, j.Watch(testLedgerKey(b)))
	}

	j.tick(ctx)

	require.EqualValues(t, 3, atomic.LoadInt32(&sendCalls), "5 entries with a batch size of 2 must produce 3 transactions, not 5")
}

func TestTTLJanitorUnwatchStopsMonitoring(t *testing.T) {
	ctx := context.Background()
	var getCalls int32

	rpc := &fakeRPC{
		getLedgerEntries: func(ctx context.Context, req protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
			atomic.AddInt32(&getCalls, 1)
			return protocol.GetLedgerEntriesResponse{}, nil
		},
	}

	j := NewTTLJanitor(rpc, "", TTLJanitorConfig{Threshold: 1000, ExtendBy: 100000, BatchSize: 50})
	require.NoError(t, j.Watch(testLedgerKey(1)))
	require.NoError(t, j.Unwatch(testLedgerKey(1)))

	j.tick(ctx)

	require.Zero(t, atomic.LoadInt32(&getCalls), "an empty watch list must not call getLedgerEntries at all")
}

func TestTTLJanitorStartStopNoLeak(t *testing.T) {
	rpc := &fakeRPC{
		getLedgerEntries: func(ctx context.Context, req protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
			return protocol.GetLedgerEntriesResponse{}, nil
		},
	}
	j := NewTTLJanitor(rpc, "", TTLJanitorConfig{PollInterval: time.Millisecond, Threshold: 1000, ExtendBy: 100000, BatchSize: 50})

	done := make(chan struct{})
	go func() {
		j.Start(context.Background())
		time.Sleep(20 * time.Millisecond) // let at least one poll tick fire
		j.Stop()                          // must return promptly - proof the poll goroutine actually exited
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("TTLJanitor.Stop() did not return - poll goroutine leaked")
	}
}

func TestTTLJanitorStopWithoutStartDoesNotPanic(t *testing.T) {
	j := NewTTLJanitor(&fakeRPC{}, "", TTLJanitorConfig{})
	require.NotPanics(t, j.Stop)
}
