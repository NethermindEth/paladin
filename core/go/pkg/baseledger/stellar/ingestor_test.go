// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package stellar

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/network"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

const testPassphrase = network.TestNetworkPassphrase

// buildTestLedger builds a minimal-but-valid LedgerCloseMeta with a single successful transaction
// from sourceAddr, plus one contract event, mirroring the "barebones" construction pattern the SDK's
// own ingest package tests use (ledger_transaction_reader_test.go's makeTransactions).
func buildTestLedger(t *testing.T, sourceAddr string, contractID xdr.ContractId, successful bool) protocol.LedgerInfo {
	txEnv := xdr.TransactionEnvelope{
		Type: xdr.EnvelopeTypeEnvelopeTypeTx,
		V1: &xdr.TransactionV1Envelope{
			Tx: xdr.Transaction{
				// A populated Ext.SorobanData is what ingest.LedgerTransaction.IsSorobanTx() (and
				// hence GetContractEvents()) actually checks - not the operation list.
				Ext:           xdr.TransactionExt{V: 1, SorobanData: &xdr.SorobanTransactionData{}},
				SourceAccount: xdr.MustMuxedAddress(sourceAddr),
				Operations:    []xdr.Operation{},
				Fee:           100,
				SeqNum:        xdr.SequenceNumber(42),
			},
			Signatures: []xdr.DecoratedSignature{},
		},
	}
	txHash, err := network.HashTransactionInEnvelope(txEnv, testPassphrase)
	require.NoError(t, err)

	resultCode := xdr.TransactionResultCodeTxSuccess
	if !successful {
		resultCode = xdr.TransactionResultCodeTxFailed
	}
	event := xdr.ContractEvent{
		ContractId: &contractID,
		Type:       xdr.ContractEventTypeContract,
		Body: xdr.ContractEventBody{
			V: 0,
			V0: &xdr.ContractEventV0{
				Topics: []xdr.ScVal{
					{Type: xdr.ScValTypeScvSymbol, Sym: symPtr("transfer")},
				},
				Data: xdr.ScVal{Type: xdr.ScValTypeScvBool, B: boolPtr(true)},
			},
		},
	}
	txMeta := xdr.TransactionResultMeta{
		Result: xdr.TransactionResultPair{
			TransactionHash: xdr.Hash(txHash),
			Result: xdr.TransactionResult{
				Result: xdr.TransactionResultResult{Code: resultCode, Results: &[]xdr.OperationResult{}},
			},
		},
		TxApplyProcessing: xdr.TransactionMeta{
			V: 3,
			V3: &xdr.TransactionMetaV3{
				SorobanMeta: &xdr.SorobanTransactionMeta{
					Events:      []xdr.ContractEvent{event},
					ReturnValue: xdr.ScVal{Type: xdr.ScValTypeScvVoid},
				},
			},
		},
	}

	ledgerCloseMeta := xdr.LedgerCloseMeta{
		V: 1,
		V1: &xdr.LedgerCloseMetaV1{
			TxProcessing: []xdr.TransactionResultMeta{txMeta},
			TxSet: xdr.GeneralizedTransactionSet{
				V: 1,
				V1TxSet: &xdr.TransactionSetV1{
					Phases: []xdr.TransactionPhase{{
						V: 0,
						V0Components: &[]xdr.TxSetComponent{{
							TxsMaybeDiscountedFee: &xdr.TxSetComponentTxsMaybeDiscountedFee{
								Txs: []xdr.TransactionEnvelope{txEnv},
							},
						}},
					}},
				},
			},
		},
	}
	metadataXDR, err := xdr.MarshalBase64(ledgerCloseMeta)
	require.NoError(t, err)

	return protocol.LedgerInfo{
		Hash:            "aa00000000000000000000000000000000000000000000000000000000000000"[:64],
		Sequence:        100,
		LedgerCloseTime: 1234567890,
		LedgerMetadata:  metadataXDR,
	}
}

func symPtr(s string) *xdr.ScSymbol {
	sym := xdr.ScSymbol(s)
	return &sym
}

func boolPtr(b bool) *bool {
	return &b
}

// buildTestLedgerV4Classic builds a minimal-but-valid LedgerCloseMeta with a single successful
// classic (non-Soroban) transaction using TransactionMetaV4 - the shape stellar-core produces from
// Protocol 23 onward (CAP-67 "unified events"), and the exact shape a CreateAccountOp transaction
// (channel-account funding) takes on this project's own stellar_quickstart environment. Unlike
// buildTestLedger, Ext has no SorobanData, so ingest.LedgerTransaction.IsSorobanTx() is false -
// this is what previously made tx.GetContractEvents() error "not a soroban transaction".
// opEvents lets callers supply per-operation contract events (or none, matching a bare
// CreateAccountOp) via TransactionMetaV4.Operations[i].Events.
func buildTestLedgerV4Classic(t *testing.T, sourceAddr string, opEvents [][]xdr.ContractEvent) protocol.LedgerInfo {
	txEnv := xdr.TransactionEnvelope{
		Type: xdr.EnvelopeTypeEnvelopeTypeTx,
		V1: &xdr.TransactionV1Envelope{
			Tx: xdr.Transaction{
				SourceAccount: xdr.MustMuxedAddress(sourceAddr),
				Operations:    []xdr.Operation{},
				Fee:           100,
				SeqNum:        xdr.SequenceNumber(42),
			},
			Signatures: []xdr.DecoratedSignature{},
		},
	}
	txHash, err := network.HashTransactionInEnvelope(txEnv, testPassphrase)
	require.NoError(t, err)

	operations := make([]xdr.OperationMetaV2, len(opEvents))
	for i, events := range opEvents {
		operations[i] = xdr.OperationMetaV2{Events: events}
	}

	txMeta := xdr.TransactionResultMeta{
		Result: xdr.TransactionResultPair{
			TransactionHash: xdr.Hash(txHash),
			Result: xdr.TransactionResult{
				Result: xdr.TransactionResultResult{Code: xdr.TransactionResultCodeTxSuccess, Results: &[]xdr.OperationResult{}},
			},
		},
		TxApplyProcessing: xdr.TransactionMeta{
			V: 4,
			V4: &xdr.TransactionMetaV4{
				Operations: operations,
			},
		},
	}

	ledgerCloseMeta := xdr.LedgerCloseMeta{
		V: 1,
		V1: &xdr.LedgerCloseMetaV1{
			TxProcessing: []xdr.TransactionResultMeta{txMeta},
			TxSet: xdr.GeneralizedTransactionSet{
				V: 1,
				V1TxSet: &xdr.TransactionSetV1{
					Phases: []xdr.TransactionPhase{{
						V: 0,
						V0Components: &[]xdr.TxSetComponent{{
							TxsMaybeDiscountedFee: &xdr.TxSetComponentTxsMaybeDiscountedFee{
								Txs: []xdr.TransactionEnvelope{txEnv},
							},
						}},
					}},
				},
			},
		},
	}
	metadataXDR, err := xdr.MarshalBase64(ledgerCloseMeta)
	require.NoError(t, err)

	return protocol.LedgerInfo{
		Hash:            "aa00000000000000000000000000000000000000000000000000000000000000"[:64],
		Sequence:        100,
		LedgerCloseTime: 1234567890,
		LedgerMetadata:  metadataXDR,
	}
}

// TestDecodeLedgerClassicTransactionV4NoEvents is the direct regression test for the live failure:
// a classic (non-Soroban) transaction with TransactionMetaV4 and no operation events at all -
// exactly a bare CreateAccountOp funding transaction. Before the fix, decodeLedger called the
// Soroban-only tx.GetContractEvents(), which errors "not a soroban transaction" for this exact
// shape - and that error, surfacing from poll(), used to permanently kill ledger ingestion for the
// rest of the process.
func TestDecodeLedgerClassicTransactionV4NoEvents(t *testing.T) {
	kp := keypair.MustRandom()
	ledger := buildTestLedgerV4Classic(t, kp.Address(), [][]xdr.ContractEvent{{}})

	unit, err := decodeLedger(context.Background(), testPassphrase, ledger, nil)
	require.NoError(t, err)
	require.Len(t, unit.Txs, 1)
	require.Equal(t, "SUCCESS", unit.Txs[0].Result)
	require.Empty(t, unit.Events)
}

// TestDecodeLedgerClassicTransactionV4WithEvents proves the more complete fix: from
// TransactionMetaV4 onward (CAP-67 unified events), a classic operation can carry real contract
// events too, and decodeLedger must extract them rather than merely tolerating their absence.
func TestDecodeLedgerClassicTransactionV4WithEvents(t *testing.T) {
	kp := keypair.MustRandom()
	var contractID xdr.ContractId
	copy(contractID[:], []byte("contract-id-32-bytes-long!!!!!!"))
	event := xdr.ContractEvent{
		ContractId: &contractID,
		Type:       xdr.ContractEventTypeContract,
		Body: xdr.ContractEventBody{
			V: 0,
			V0: &xdr.ContractEventV0{
				Topics: []xdr.ScVal{
					{Type: xdr.ScValTypeScvSymbol, Sym: symPtr("reg")},
				},
				Data: xdr.ScVal{Type: xdr.ScValTypeScvBool, B: boolPtr(true)},
			},
		},
	}
	ledger := buildTestLedgerV4Classic(t, kp.Address(), [][]xdr.ContractEvent{{event}})

	unit, err := decodeLedger(context.Background(), testPassphrase, ledger, nil)
	require.NoError(t, err)
	require.Len(t, unit.Events, 1)
	ev := unit.Events[0]
	require.Equal(t, int64(0), ev.TxIndex)
	require.Equal(t, int64(0), ev.EventIndex)
	require.Equal(t, ComputeEventSelector("reg"), ev.Selector)
}

func TestDecodeLedgerSuccessfulTransaction(t *testing.T) {
	kp := keypair.MustRandom()
	var contractID xdr.ContractId
	copy(contractID[:], []byte("contract-id-32-bytes-long!!!!!!"))

	ledger := buildTestLedger(t, kp.Address(), contractID, true)
	unit, err := decodeLedger(context.Background(), testPassphrase, ledger, nil)
	require.NoError(t, err)

	require.Equal(t, uint64(100), unit.Sequence)
	require.Equal(t, pldtypes.TimestampFromUnix(1234567890), unit.Timestamp)

	require.Len(t, unit.Txs, 1)
	tx := unit.Txs[0]
	require.Equal(t, "SUCCESS", tx.Result)
	require.Equal(t, kp.Address(), tx.From.String())
	require.Equal(t, uint64(42), tx.Nonce)
	require.Empty(t, tx.RevertData)

	require.Len(t, unit.Events, 1)
	ev := unit.Events[0]
	require.Equal(t, int64(0), ev.TxIndex)
	require.Equal(t, int64(0), ev.EventIndex)
	require.Equal(t, ComputeEventSelector("transfer"), ev.Selector)
	require.Len(t, ev.Topics, 1)
	require.NotEmpty(t, ev.Data)
}

func TestDecodeLedgerFailedTransaction(t *testing.T) {
	kp := keypair.MustRandom()
	var contractID xdr.ContractId
	ledger := buildTestLedger(t, kp.Address(), contractID, false)

	unit, err := decodeLedger(context.Background(), testPassphrase, ledger, nil)
	require.NoError(t, err)
	require.Len(t, unit.Txs, 1)
	require.Equal(t, "FAILED", unit.Txs[0].Result)
	require.NotEmpty(t, unit.Txs[0].RevertData)
}

func TestDecodeLedgerInvalidMetadata(t *testing.T) {
	_, err := decodeLedger(context.Background(), testPassphrase, protocol.LedgerInfo{
		Sequence:       1,
		Hash:           "aa00000000000000000000000000000000000000000000000000000000000000"[:64],
		LedgerMetadata: "not valid base64 xdr",
	}, nil)
	require.ErrorContains(t, err, "invalid ledger metadata")
}

func TestComputeEventSelector(t *testing.T) {
	expected := sha256.Sum256([]byte("saladin:transfer:v0"))
	require.Equal(t, pldtypes.Bytes32(expected), ComputeEventSelector("transfer"))
}

func TestComputeEventSelectorWithSpec(t *testing.T) {
	expected := sha256.Sum256([]byte("saladin:snoto:transfer:v0"))
	require.Equal(t, pldtypes.Bytes32(expected), ComputeEventSelectorWithSpec("snoto", "transfer"))
}

// Two different specs' identically-named "transfer" event must never collide, and neither may
// collide with the unqualified formula a resolver-miss falls back to - the whole point of this fix.
func TestComputeEventSelectorWithSpecNoCollision(t *testing.T) {
	unqualified := ComputeEventSelector("transfer")
	snoto := ComputeEventSelectorWithSpec("snoto", "transfer")
	identityRegistry := ComputeEventSelectorWithSpec("identity-registry", "transfer")
	require.NotEqual(t, unqualified, snoto)
	require.NotEqual(t, unqualified, identityRegistry)
	require.NotEqual(t, snoto, identityRegistry)
}

type fakeSpecResolver struct {
	specs map[pldtypes.ChainAddress]string
}

func (f *fakeSpecResolver) ResolveContractSpecName(_ context.Context, emitter pldtypes.ChainAddress) (string, bool) {
	name, ok := f.specs[emitter]
	return name, ok
}

func TestDecodeLedgerResolverHit(t *testing.T) {
	kp := keypair.MustRandom()
	var contractID xdr.ContractId
	copy(contractID[:], []byte("contract-id-32-bytes-long!!!!!!"))

	ledger := buildTestLedger(t, kp.Address(), contractID, true)
	unqualified, err := decodeLedger(context.Background(), testPassphrase, ledger, nil)
	require.NoError(t, err)
	require.Len(t, unqualified.Events, 1)
	emitter := unqualified.Events[0].Emitter

	resolver := &fakeSpecResolver{specs: map[pldtypes.ChainAddress]string{emitter: "snoto"}}
	resolved, err := decodeLedger(context.Background(), testPassphrase, ledger, resolver)
	require.NoError(t, err)
	require.Len(t, resolved.Events, 1)

	require.Equal(t, ComputeEventSelector("transfer"), unqualified.Events[0].Selector)
	require.Equal(t, ComputeEventSelectorWithSpec("snoto", "transfer"), resolved.Events[0].Selector)
	require.NotEqual(t, unqualified.Events[0].Selector, resolved.Events[0].Selector)
}

func TestDecodeLedgerResolverMissFallsBackToUnqualified(t *testing.T) {
	kp := keypair.MustRandom()
	var contractID xdr.ContractId
	copy(contractID[:], []byte("contract-id-32-bytes-long!!!!!!"))

	ledger := buildTestLedger(t, kp.Address(), contractID, true)
	// A resolver that knows about no emitters at all - every lookup misses.
	resolver := &fakeSpecResolver{specs: map[pldtypes.ChainAddress]string{}}
	unit, err := decodeLedger(context.Background(), testPassphrase, ledger, resolver)
	require.NoError(t, err)
	require.Len(t, unit.Events, 1)
	require.Equal(t, ComputeEventSelector("transfer"), unit.Events[0].Selector)
}

type fakeLedgerRPC struct {
	getLedgers      func(ctx context.Context, req protocol.GetLedgersRequest) (protocol.GetLedgersResponse, error)
	getLatestLedger func(ctx context.Context) (protocol.GetLatestLedgerResponse, error)
}

func (f *fakeLedgerRPC) GetLedgers(ctx context.Context, req protocol.GetLedgersRequest) (protocol.GetLedgersResponse, error) {
	return f.getLedgers(ctx, req)
}

func (f *fakeLedgerRPC) GetLatestLedger(ctx context.Context) (protocol.GetLatestLedgerResponse, error) {
	return f.getLatestLedger(ctx)
}

func TestIngestorTipHeight(t *testing.T) {
	rpc := &fakeLedgerRPC{
		getLatestLedger: func(ctx context.Context) (protocol.GetLatestLedgerResponse, error) {
			return protocol.GetLatestLedgerResponse{Sequence: 555}, nil
		},
	}
	i := NewIngestor(rpc, testPassphrase, time.Millisecond)
	tip, err := i.TipHeight(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 555, tip)
}

func TestIngestorBackfillSource(t *testing.T) {
	i := NewIngestor(&fakeLedgerRPC{}, testPassphrase, time.Millisecond)
	require.Equal(t, baseledger.BackfillArchive, i.BackfillSource())
}

func TestIngestorStreamLedgersFromCheckpoint(t *testing.T) {
	kp := keypair.MustRandom()
	var contractID xdr.ContractId
	ledger101 := buildTestLedger(t, kp.Address(), contractID, true)
	ledger101.Sequence = 101

	requestedStart := make(chan uint32, 1)
	rpc := &fakeLedgerRPC{
		getLedgers: func(ctx context.Context, req protocol.GetLedgersRequest) (protocol.GetLedgersResponse, error) {
			select {
			case requestedStart <- req.StartLedger:
			default:
			}
			return protocol.GetLedgersResponse{Ledgers: []protocol.LedgerInfo{ledger101}}, nil
		},
	}
	i := NewIngestor(rpc, testPassphrase, time.Millisecond)

	ch, err := i.StreamLedgers(context.Background(), baseledger.LedgerCheckpoint{Sequence: 100})
	require.NoError(t, err)

	select {
	case start := <-requestedStart:
		require.EqualValues(t, 101, start)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for getLedgers request")
	}

	select {
	case unit := <-ch:
		require.Equal(t, uint64(101), unit.Sequence)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ledger unit")
	}
}

func TestIngestorStreamLedgersFromTipWhenNoCheckpoint(t *testing.T) {
	rpc := &fakeLedgerRPC{
		getLatestLedger: func(ctx context.Context) (protocol.GetLatestLedgerResponse, error) {
			return protocol.GetLatestLedgerResponse{Sequence: 999}, nil
		},
		getLedgers: func(ctx context.Context, req protocol.GetLedgersRequest) (protocol.GetLedgersResponse, error) {
			require.EqualValues(t, 1000, req.StartLedger)
			return protocol.GetLedgersResponse{}, nil
		},
	}
	i := NewIngestor(rpc, testPassphrase, time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	ch, err := i.StreamLedgers(ctx, baseledger.LedgerCheckpoint{})
	require.NoError(t, err)
	<-ch // closes when ctx is done, since no ledgers are ever returned
}

// TestIngestorPollRetriesAfterDecodeError proves poll() survives a ledger that fails to decode
// instead of permanently closing the channel (the live bug: one classic-op ledger used to kill
// ledger ingestion for the rest of the process). The first getLedgers call returns an unparseable
// ledger; poll must retry from that same starting sequence (not skip past it, not die) until a
// later call returns a good one.
func TestIngestorPollRetriesAfterDecodeError(t *testing.T) {
	kp := keypair.MustRandom()
	var contractID xdr.ContractId
	goodLedger := buildTestLedger(t, kp.Address(), contractID, true)
	goodLedger.Sequence = 101

	badLedger := protocol.LedgerInfo{
		Hash:            "aa00000000000000000000000000000000000000000000000000000000000000"[:64],
		Sequence:        101,
		LedgerCloseTime: 1234567890,
		LedgerMetadata:  "not valid base64 xdr",
	}

	var callCount int
	requestedStarts := make(chan uint32, 10)
	rpc := &fakeLedgerRPC{
		getLedgers: func(ctx context.Context, req protocol.GetLedgersRequest) (protocol.GetLedgersResponse, error) {
			select {
			case requestedStarts <- req.StartLedger:
			default:
			}
			callCount++
			if callCount == 1 {
				return protocol.GetLedgersResponse{Ledgers: []protocol.LedgerInfo{badLedger}}, nil
			}
			return protocol.GetLedgersResponse{Ledgers: []protocol.LedgerInfo{goodLedger}}, nil
		},
	}
	i := NewIngestor(rpc, testPassphrase, time.Millisecond)

	ch, err := i.StreamLedgers(context.Background(), baseledger.LedgerCheckpoint{Sequence: 100})
	require.NoError(t, err)

	select {
	case start := <-requestedStarts:
		require.EqualValues(t, 101, start)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first getLedgers request")
	}
	select {
	case start := <-requestedStarts:
		require.EqualValues(t, 101, start, "expected poll to retry from the same ledger after a decode failure, not skip past it")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for retried getLedgers request")
	}

	select {
	case unit, ok := <-ch:
		require.True(t, ok, "channel closed instead of delivering the ledger unit - poll died on the decode error")
		require.Equal(t, uint64(101), unit.Sequence)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ledger unit after retry")
	}
}
