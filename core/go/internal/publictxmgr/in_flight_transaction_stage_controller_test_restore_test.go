// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package publictxmgr

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/LFDT-Paladin/paladin/config/pkg/confutil"
	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRestoreChainSubmitter is a hand-rolled ChainSubmitter used only by the restore-preamble
// tests below - it's chain-neutral orchestrator code being tested here (TriggerRestoreTx,
// restoreTX, processRestoreStageOutput), not Stellar-specific build/sign logic (already covered
// by stellar_chain_submitter_test.go), so a minimal fake is enough.
type fakeRestoreChainSubmitter struct {
	ChainSubmitter
	prepareRestore func(ctx context.Context, ptx *DBPublicTxn, soroban *baseledger.SorobanResources) (*PreparedSubmission, error)
	submit         func(ctx context.Context, ps *PreparedSubmission) (*SubmitResult, error)
}

func (f *fakeRestoreChainSubmitter) PrepareRestore(ctx context.Context, ptx *DBPublicTxn, soroban *baseledger.SorobanResources) (*PreparedSubmission, error) {
	return f.prepareRestore(ctx, ptx, soroban)
}

func (f *fakeRestoreChainSubmitter) Submit(ctx context.Context, ps *PreparedSubmission) (*SubmitResult, error) {
	return f.submit(ctx, ps)
}

// fakeRestoreBaseLedger is a hand-rolled baseledger.Client used only by the restoreTX polling
// tests below.
type fakeRestoreBaseLedger struct {
	baseledger.Client
	getTransactionResult func(ctx context.Context, id baseledger.TxID) (*baseledger.TxResult, error)
}

func (f *fakeRestoreBaseLedger) GetTransactionResult(ctx context.Context, id baseledger.TxID) (*baseledger.TxResult, error) {
	return f.getTransactionResult(ctx, id)
}

func TestRestoreTXSuccess(t *testing.T) {
	ctx, o, _, done := newTestOrchestrator(t)
	defer done()
	it, _ := newInflightTransaction(o, 1)

	txHash := pldtypes.MustParseBytes32(testTxHash)
	o.chainSubmitter = &fakeRestoreChainSubmitter{
		prepareRestore: func(ctx context.Context, ptx *DBPublicTxn, soroban *baseledger.SorobanResources) (*PreparedSubmission, error) {
			return &PreparedSubmission{PublicTxnID: ptx.PublicTxnID, RawTransaction: []byte{0x01}, TransactionHash: &txHash}, nil
		},
		submit: func(ctx context.Context, ps *PreparedSubmission) (*SubmitResult, error) {
			return &SubmitResult{TxHash: ps.TransactionHash, Outcome: SubmissionOutcomeSubmittedNew}, nil
		},
	}
	getCalls := 0
	o.baseLedger = &fakeRestoreBaseLedger{
		getTransactionResult: func(ctx context.Context, id baseledger.TxID) (*baseledger.TxResult, error) {
			getCalls++
			if getCalls < 2 {
				return nil, fmt.Errorf("not found yet")
			}
			return &baseledger.TxResult{ID: id, Success: true}, nil
		},
	}

	result, err := it.restoreTX(ctx, &DBPublicTxn{PublicTxnID: 1, Nonce: confutil.P(uint64(7))}, &baseledger.SorobanResources{}, func(context.Context) bool { return false })
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, txHash, *result)
	assert.Equal(t, 2, getCalls)
}

func TestRestoreTXPrepareFails(t *testing.T) {
	ctx, o, _, done := newTestOrchestrator(t)
	defer done()
	it, _ := newInflightTransaction(o, 1)

	o.chainSubmitter = &fakeRestoreChainSubmitter{
		prepareRestore: func(ctx context.Context, ptx *DBPublicTxn, soroban *baseledger.SorobanResources) (*PreparedSubmission, error) {
			return nil, fmt.Errorf("no restore preamble available")
		},
	}

	_, err := it.restoreTX(ctx, &DBPublicTxn{PublicTxnID: 1}, &baseledger.SorobanResources{}, func(context.Context) bool { return false })
	require.ErrorContains(t, err, "no restore preamble available")
}

func TestRestoreTXConfirmationFails(t *testing.T) {
	ctx, o, _, done := newTestOrchestrator(t)
	defer done()
	it, _ := newInflightTransaction(o, 1)

	txHash := pldtypes.MustParseBytes32(testTxHash)
	o.chainSubmitter = &fakeRestoreChainSubmitter{
		prepareRestore: func(ctx context.Context, ptx *DBPublicTxn, soroban *baseledger.SorobanResources) (*PreparedSubmission, error) {
			return &PreparedSubmission{PublicTxnID: ptx.PublicTxnID, RawTransaction: []byte{0x01}, TransactionHash: &txHash}, nil
		},
		submit: func(ctx context.Context, ps *PreparedSubmission) (*SubmitResult, error) {
			return &SubmitResult{TxHash: ps.TransactionHash, Outcome: SubmissionOutcomeSubmittedNew}, nil
		},
	}
	o.baseLedger = &fakeRestoreBaseLedger{
		getTransactionResult: func(ctx context.Context, id baseledger.TxID) (*baseledger.TxResult, error) {
			return &baseledger.TxResult{ID: id, Success: false}, nil
		},
	}

	result, err := it.restoreTX(ctx, &DBPublicTxn{PublicTxnID: 1, Nonce: confutil.P(uint64(7))}, &baseledger.SorobanResources{}, func(context.Context) bool { return false })
	require.Error(t, err)
	require.NotNil(t, result)
	assert.Equal(t, txHash, *result)
}

func TestProduceLatestInFlightStageContextSigningRequiresRestore(t *testing.T) {
	ctx, o, m, done := newTestOrchestrator(t)
	defer done()
	it, mTS := newInflightTransaction(o, 1)
	it.testOnlyNoActionMode = true
	mTS.statusUpdater = &mockStatusUpdater{
		updateSubStatus: func(ctx context.Context, imtx InMemoryTxStateReadOnly, subStatus BaseTxSubStatus, action BaseTxAction, info, err pldtypes.RawJSON, actionOccurred *pldtypes.Timestamp) error {
			return nil
		},
		updateRestoreState: func(ctx context.Context, pubTxnID uint64, requiresRestore *bool, restoreTxHash *pldtypes.Bytes32, nonce *uint64) error {
			return nil
		},
	}

	for range 5 {
		m.db.ExpectQuery("SELECT.*public_txn_bindings").WillReturnRows(sqlmock.NewRows([]string{"transaction"}).AddRow(uuid.New().String()))
	}

	mTS.ApplyInMemoryUpdates(ctx, &BaseTXUpdates{
		NewValues: BaseTXUpdateNewValues{
			GasPricing: &pldapi.PublicTxGasPricing{
				MaxFeePerGas:         pldtypes.Uint64ToUint256(10),
				MaxPriorityFeePerGas: pldtypes.Uint64ToUint256(1),
			},
		},
	})

	// trigger signing
	tOut := it.ProduceLatestInFlightStageContext(ctx, &OrchestratorContext{})
	assert.NotEmpty(t, *tOut)
	rsc := it.stateManager.GetCurrentGeneration(ctx).GetRunningStageContext(ctx)
	assert.Equal(t, InFlightTxStageSigning, rsc.Stage)
	currentGeneration := it.stateManager.GetCurrentGeneration(ctx).(*inFlightTransactionStateGeneration)

	restoreSoroban := &baseledger.SorobanResources{RestorePreambleTransactionDataXDR: []byte{0x01}}
	currentGeneration.bufferedStageOutputs = make([]*StageOutput, 0)
	it.stateManager.GetCurrentGeneration(ctx).AddSignOutput(ctx, nil, nil, true, restoreSoroban, nil)
	_ = it.ProduceLatestInFlightStageContext(ctx, &OrchestratorContext{})
	rsc = it.stateManager.GetCurrentGeneration(ctx).GetRunningStageContext(ctx)
	assert.Equal(t, InFlightTxStageSigning, rsc.Stage)
	require.NotNil(t, rsc.StageOutputsToBePersisted)
	require.NotNil(t, rsc.StageOutputsToBePersisted.TxUpdates)
	require.NotNil(t, rsc.StageOutputsToBePersisted.TxUpdates.NewValues.RequiresRestore)
	assert.True(t, *rsc.StageOutputsToBePersisted.TxUpdates.NewValues.RequiresRestore)

	// persist completes successfully - should move to the restoring stage, carrying RestoreSoroban
	currentGeneration.bufferedStageOutputs = make([]*StageOutput, 0)
	it.stateManager.GetCurrentGeneration(ctx).AddPersistenceOutput(ctx, InFlightTxStageSigning, time.Now(), nil)
	_ = it.ProduceLatestInFlightStageContext(ctx, &OrchestratorContext{})
	rsc = it.stateManager.GetCurrentGeneration(ctx).GetRunningStageContext(ctx)
	assert.Equal(t, InFlightTxStageRestoring, rsc.Stage)
	require.NotNil(t, currentGeneration.TransientPreviousStageOutputs)
	assert.Equal(t, restoreSoroban, currentGeneration.TransientPreviousStageOutputs.RestoreSoroban)
}

func TestProduceLatestInFlightStageContextRestoringSuccess(t *testing.T) {
	ctx, o, m, done := newTestOrchestrator(t)
	defer done()
	it, mTS := newInflightTransaction(o, 7)
	it.testOnlyNoActionMode = true
	mTS.statusUpdater = &mockStatusUpdater{
		updateSubStatus: func(ctx context.Context, imtx InMemoryTxStateReadOnly, subStatus BaseTxSubStatus, action BaseTxAction, info, err pldtypes.RawJSON, actionOccurred *pldtypes.Timestamp) error {
			return nil
		},
	}

	// force the current stage to Restoring directly, mirroring how the signing test drives Signing
	currentGeneration := it.stateManager.GetCurrentGeneration(ctx).(*inFlightTransactionStateGeneration)
	currentGeneration.StartNewStageContext(ctx, InFlightTxStageRestoring, BaseTxSubStatusReceived)
	rsc := it.stateManager.GetCurrentGeneration(ctx).GetRunningStageContext(ctx)
	assert.Equal(t, InFlightTxStageRestoring, rsc.Stage)

	restoreTxHash := pldtypes.MustParseBytes32(testTxHash)
	currentGeneration.bufferedStageOutputs = make([]*StageOutput, 0)
	it.stateManager.GetCurrentGeneration(ctx).AddRestoreOutput(ctx, &restoreTxHash, nil)
	_ = it.ProduceLatestInFlightStageContext(ctx, &OrchestratorContext{})
	rsc = it.stateManager.GetCurrentGeneration(ctx).GetRunningStageContext(ctx)
	assert.Equal(t, InFlightTxStageRestoring, rsc.Stage)
	require.NotNil(t, rsc.StageOutputsToBePersisted)
	require.NotNil(t, rsc.StageOutputsToBePersisted.TxUpdates)
	assert.Equal(t, &restoreTxHash, rsc.StageOutputsToBePersisted.TxUpdates.NewValues.RestoreTxHash)
	require.NotNil(t, rsc.StageOutputsToBePersisted.TxUpdates.NewValues.Nonce)
	assert.Equal(t, uint64(8), *rsc.StageOutputsToBePersisted.TxUpdates.NewValues.Nonce)
	require.NotNil(t, rsc.StageOutputsToBePersisted.TxUpdates.NewValues.RequiresRestore)
	assert.False(t, *rsc.StageOutputsToBePersisted.TxUpdates.NewValues.RequiresRestore)

	// testOnlyNoActionMode means TriggerPersistTxState's async write never runs on its own -
	// invoke PersistTxState directly to exercise the real DB write path (pubTxManager.UpdateRestoreState).
	m.db.ExpectExec(`UPDATE "public_txns" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	_, _, err := currentGeneration.PersistTxState(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(8), it.stateManager.GetNonce())

	// persist completes successfully - clears the running stage context (next poll re-enters signing)
	currentGeneration.bufferedStageOutputs = make([]*StageOutput, 0)
	it.stateManager.GetCurrentGeneration(ctx).AddPersistenceOutput(ctx, InFlightTxStageRestoring, time.Now(), nil)
	_ = it.ProduceLatestInFlightStageContext(ctx, &OrchestratorContext{})
}

func TestProduceLatestInFlightStageContextRestoringError(t *testing.T) {
	ctx, o, _, done := newTestOrchestrator(t)
	defer done()
	it, mTS := newInflightTransaction(o, 7)
	it.testOnlyNoActionMode = true
	mTS.statusUpdater = &mockStatusUpdater{
		updateSubStatus: func(ctx context.Context, imtx InMemoryTxStateReadOnly, subStatus BaseTxSubStatus, action BaseTxAction, info, err pldtypes.RawJSON, actionOccurred *pldtypes.Timestamp) error {
			return nil
		},
	}

	currentGeneration := it.stateManager.GetCurrentGeneration(ctx).(*inFlightTransactionStateGeneration)
	currentGeneration.StartNewStageContext(ctx, InFlightTxStageRestoring, BaseTxSubStatusReceived)
	rsc := it.stateManager.GetCurrentGeneration(ctx).GetRunningStageContext(ctx)

	currentGeneration.bufferedStageOutputs = make([]*StageOutput, 0)
	it.stateManager.GetCurrentGeneration(ctx).AddRestoreOutput(ctx, nil, fmt.Errorf("restore submission failed"))
	_ = it.ProduceLatestInFlightStageContext(ctx, &OrchestratorContext{})
	assert.Equal(t, InFlightTxStageRestoring, rsc.Stage)
	require.NotNil(t, rsc.StageOutputsToBePersisted)

	// persist of the error completes - marks the stage errored, waiting for the stage retry timeout
	currentGeneration.bufferedStageOutputs = make([]*StageOutput, 0)
	it.stateManager.GetCurrentGeneration(ctx).AddPersistenceOutput(ctx, InFlightTxStageRestoring, time.Now(), nil)
	_ = it.ProduceLatestInFlightStageContext(ctx, &OrchestratorContext{})
	assert.True(t, rsc.StageErrored)
}
