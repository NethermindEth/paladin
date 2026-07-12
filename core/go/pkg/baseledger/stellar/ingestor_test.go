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
