// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package publictxmgr

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/LFDT-Paladin/paladin/config/pkg/confutil"
	"github.com/LFDT-Paladin/paladin/config/pkg/pldconf"
	"github.com/LFDT-Paladin/paladin/core/mocks/componentsmocks"
	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	baseledgerstellar "github.com/LFDT-Paladin/paladin/core/pkg/baseledger/stellar"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/retry"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/algorithms"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/signpayloads"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/verifiers"
	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const testStellarAccount = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"

func validStellarInvokeContractPayload(t *testing.T) []byte {
	hostFunction := xdr.HostFunction{
		Type: xdr.HostFunctionTypeHostFunctionTypeInvokeContract,
		InvokeContract: &xdr.InvokeContractArgs{
			ContractAddress: xdr.ScAddress{
				Type:       xdr.ScAddressTypeScAddressTypeContract,
				ContractId: &xdr.ContractId{},
			},
			FunctionName: xdr.ScSymbol("test"),
		},
	}
	payload, err := hostFunction.MarshalBinary()
	require.NoError(t, err)
	return payload
}

func newTestStellarSubmitter(t *testing.T) (context.Context, *stellarChainSubmitter, *mocksAndTestControl, func()) {
	ctx, ptm, m, done := newTestPublicTxManager(t, false, func(mocks *mocksAndTestControl, conf *pldconf.PublicTxManagerConfig) {
		mocks.disableManagerStart = true
	})
	ptm.baseLedger = &mockStellarBaseLedger{}
	ptm.chainSubmitter = newStellarChainSubmitter(ptm, &pldconf.ChannelAccountsConfig{})
	return ctx, ptm.chainSubmitter.(*stellarChainSubmitter), m, done
}

type mockStellarBaseLedger struct {
	getAccountInfo       func(ctx context.Context, addr pldtypes.ChainAddress) (*baseledger.AccountInfo, error)
	submit               func(ctx context.Context, raw baseledger.SignedChainTx) (baseledger.TxID, error)
	estimateResources    func(ctx context.Context, tx *baseledger.UnsignedChainTx) (*baseledger.ResourceEstimate, error)
	getTransactionResult func(ctx context.Context, id baseledger.TxID) (*baseledger.TxResult, error)
}

func (m *mockStellarBaseLedger) Close() {}
func (m *mockStellarBaseLedger) ChainInfo() baseledger.ChainInfo {
	return baseledger.ChainInfo{Kind: baseledger.ChainKindStellar, NetworkID: "Test SDF Network ; September 2015"}
}
func (m *mockStellarBaseLedger) Call(ctx context.Context, req *baseledger.CallRequest) (*baseledger.CallResult, error) {
	return nil, fmt.Errorf("unused")
}
func (m *mockStellarBaseLedger) GetAccountInfo(ctx context.Context, addr pldtypes.ChainAddress) (*baseledger.AccountInfo, error) {
	return m.getAccountInfo(ctx, addr)
}
func (m *mockStellarBaseLedger) EstimateResources(ctx context.Context, tx *baseledger.UnsignedChainTx) (*baseledger.ResourceEstimate, error) {
	if m.estimateResources != nil {
		return m.estimateResources(ctx, tx)
	}
	return nil, fmt.Errorf("unused")
}
func (m *mockStellarBaseLedger) BuildTransaction(ctx context.Context, tx *baseledger.UnsignedChainTx, est *baseledger.ResourceEstimate) (baseledger.SignablePayload, error) {
	return baseledger.SignablePayload{}, fmt.Errorf("unused")
}
func (m *mockStellarBaseLedger) Submit(ctx context.Context, raw baseledger.SignedChainTx) (baseledger.TxID, error) {
	return m.submit(ctx, raw)
}
func (m *mockStellarBaseLedger) GetTransactionResult(ctx context.Context, id baseledger.TxID) (*baseledger.TxResult, error) {
	if m.getTransactionResult != nil {
		return m.getTransactionResult(ctx, id)
	}
	return nil, fmt.Errorf("unused")
}

func TestStellarAssignOrderingKeys(t *testing.T) {
	ctx, submitter, m, done := newTestStellarSubmitter(t)
	defer done()
	submitter.channelAccounts = &pldconf.ChannelAccountsConfig{PoolSize: confutil.P(2)}

	addr := *pldtypes.MustParseChainAddress(testStellarAccount)
	identity := &pldapi.KeyMappingAndVerifier{
		KeyMappingWithPath: &pldapi.KeyMappingWithPath{KeyMapping: &pldapi.KeyMapping{Identifier: "stellar.key"}},
		Verifier:           &pldapi.KeyVerifier{Verifier: testStellarAccount},
	}
	channel0 := keypair.MustRandom().Address()
	channel1 := keypair.MustRandom().Address()
	channelKeys := []*pldapi.KeyMappingAndVerifier{
		{KeyMappingWithPath: &pldapi.KeyMappingWithPath{KeyMapping: &pldapi.KeyMapping{Identifier: "stellar.key.channel.0"}}, Verifier: &pldapi.KeyVerifier{Verifier: channel0}},
		{KeyMappingWithPath: &pldapi.KeyMappingWithPath{KeyMapping: &pldapi.KeyMapping{Identifier: "stellar.key.channel.1"}}, Verifier: &pldapi.KeyVerifier{Verifier: channel1}},
	}

	mockKeyManager := m.keyManager.(*componentsmocks.KeyManager)
	mockKeyManager.On("ReverseKeyLookup", mock.Anything, mock.Anything, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, testStellarAccount).Return(identity, nil).Once()
	mockKeyManager.On("ResolveBatchNewDatabaseTX", mock.Anything, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, []string{"stellar.key.channel.0", "stellar.key.channel.1"}).Return(channelKeys, nil).Once()

	ledger := submitter.ptm.baseLedger.(*mockStellarBaseLedger)
	ledger.getAccountInfo = func(ctx context.Context, got pldtypes.ChainAddress) (*baseledger.AccountInfo, error) {
		nonce := pldtypes.HexUint64(42)
		return &baseledger.AccountInfo{Address: got, OrderingKey: &nonce}, nil
	}

	keys, err := submitter.AssignOrderingKeys(ctx, addr)
	require.NoError(t, err)
	require.Len(t, keys, 2)
	require.NotNil(t, keys[0].ChannelAccount)
	require.NotNil(t, keys[1].ChannelAccount)
	assert.Equal(t, channel0, keys[0].ChannelAccount.String())
	assert.Equal(t, channel1, keys[1].ChannelAccount.String())
	assert.Equal(t, uint64(42), keys[0].OrderingKey)
	assert.Equal(t, uint64(42), keys[1].OrderingKey)
}

func TestStellarAssignOrderingKeysBootstrapsMissingAccount(t *testing.T) {
	ctx, submitter, m, done := newTestStellarSubmitter(t)
	defer done()
	funderIdentifier := "stellar.funder"
	submitter.channelAccounts = &pldconf.ChannelAccountsConfig{PoolSize: confutil.P(1), Funder: &funderIdentifier}

	addr := *pldtypes.MustParseChainAddress(testStellarAccount)
	identity := &pldapi.KeyMappingAndVerifier{
		KeyMappingWithPath: &pldapi.KeyMappingWithPath{KeyMapping: &pldapi.KeyMapping{Identifier: "stellar.key"}},
		Verifier:           &pldapi.KeyVerifier{Verifier: testStellarAccount},
	}
	channelAddr := keypair.MustRandom().Address()
	channelKeys := []*pldapi.KeyMappingAndVerifier{
		{KeyMappingWithPath: &pldapi.KeyMappingWithPath{KeyMapping: &pldapi.KeyMapping{Identifier: "stellar.key.channel.0"}}, Verifier: &pldapi.KeyVerifier{Verifier: channelAddr}},
	}
	funderAddr := keypair.MustRandom().Address()
	funderKey := &pldapi.KeyMappingAndVerifier{
		KeyMappingWithPath: &pldapi.KeyMappingWithPath{KeyMapping: &pldapi.KeyMapping{Identifier: funderIdentifier, KeyHandle: "m/44'/148'/1'"}},
		Verifier:           &pldapi.KeyVerifier{Verifier: funderAddr},
	}

	mockKeyManager := m.keyManager.(*componentsmocks.KeyManager)
	mockKeyManager.On("ReverseKeyLookup", mock.Anything, mock.Anything, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, testStellarAccount).Return(identity, nil).Once()
	mockKeyManager.On("ResolveBatchNewDatabaseTX", mock.Anything, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, []string{"stellar.key.channel.0"}).Return(channelKeys, nil).Once()
	mockKeyManager.On("ResolveKeyNewDatabaseTX", mock.Anything, funderIdentifier, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS).Return(funderKey, nil).Once()
	mockKeyManager.On("ReverseKeyLookup", mock.Anything, mock.Anything, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, funderAddr).Return(funderKey, nil).Once()
	mockKeyManager.On("Sign", mock.Anything, funderKey, signpayloads.OPAQUE_TO_EDDSA, mock.Anything).Return([]byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), nil).Once()

	ledger := submitter.ptm.baseLedger.(*mockStellarBaseLedger)
	getAccountInfoCalls := 0
	ledger.getAccountInfo = func(ctx context.Context, got pldtypes.ChainAddress) (*baseledger.AccountInfo, error) {
		if got.String() == channelAddr {
			getAccountInfoCalls++
			// Calls 1 and 2 are ensureAccountFunded's own entry check and its per-attempt recheck
			// inside the rebuild loop, both before the account has been created; call 3 is
			// AssignOrderingKeys' own post-funding lookup, after ensureAccountFunded has returned.
			if getAccountInfoCalls <= 2 {
				return nil, fmt.Errorf("account not found")
			}
			nonce := pldtypes.HexUint64(42)
			return &baseledger.AccountInfo{Address: got, OrderingKey: &nonce}, nil
		}
		// the funder
		nonce := pldtypes.HexUint64(7)
		return &baseledger.AccountInfo{Address: got, OrderingKey: &nonce}, nil
	}
	submittedHash := pldtypes.MustParseBytes32("0x0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	ledger.submit = func(ctx context.Context, raw baseledger.SignedChainTx) (baseledger.TxID, error) {
		return submittedHash, nil
	}
	ledger.getTransactionResult = func(ctx context.Context, id baseledger.TxID) (*baseledger.TxResult, error) {
		return &baseledger.TxResult{ID: id, Success: true}, nil
	}

	keys, err := submitter.AssignOrderingKeys(ctx, addr)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, channelAddr, keys[0].ChannelAccount.String())
	assert.Equal(t, uint64(42), keys[0].OrderingKey)
}

func TestStellarAssignOrderingKeysNoFunderConfigured(t *testing.T) {
	ctx, submitter, m, done := newTestStellarSubmitter(t)
	defer done()
	submitter.channelAccounts = &pldconf.ChannelAccountsConfig{PoolSize: confutil.P(1)}

	addr := *pldtypes.MustParseChainAddress(testStellarAccount)
	identity := &pldapi.KeyMappingAndVerifier{
		KeyMappingWithPath: &pldapi.KeyMappingWithPath{KeyMapping: &pldapi.KeyMapping{Identifier: "stellar.key"}},
		Verifier:           &pldapi.KeyVerifier{Verifier: testStellarAccount},
	}
	channelAddr := keypair.MustRandom().Address()
	channelKeys := []*pldapi.KeyMappingAndVerifier{
		{KeyMappingWithPath: &pldapi.KeyMappingWithPath{KeyMapping: &pldapi.KeyMapping{Identifier: "stellar.key.channel.0"}}, Verifier: &pldapi.KeyVerifier{Verifier: channelAddr}},
	}

	mockKeyManager := m.keyManager.(*componentsmocks.KeyManager)
	mockKeyManager.On("ReverseKeyLookup", mock.Anything, mock.Anything, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, testStellarAccount).Return(identity, nil).Once()
	mockKeyManager.On("ResolveBatchNewDatabaseTX", mock.Anything, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, []string{"stellar.key.channel.0"}).Return(channelKeys, nil).Once()

	ledger := submitter.ptm.baseLedger.(*mockStellarBaseLedger)
	ledger.getAccountInfo = func(ctx context.Context, got pldtypes.ChainAddress) (*baseledger.AccountInfo, error) {
		return nil, fmt.Errorf("account not found")
	}

	_, err := submitter.AssignOrderingKeys(ctx, addr)
	require.ErrorContains(t, err, "channel account funder")
}

func TestStellarEnsureFromAccountFundedBootstrapsMissingAccount(t *testing.T) {
	ctx, submitter, m, done := newTestStellarSubmitter(t)
	defer done()
	funderIdentifier := "stellar.funder"
	submitter.channelAccounts = &pldconf.ChannelAccountsConfig{PoolSize: confutil.P(1), Funder: &funderIdentifier}

	from := *pldtypes.MustParseChainAddress(testStellarAccount)
	funderAddr := keypair.MustRandom().Address()
	funderKey := &pldapi.KeyMappingAndVerifier{
		KeyMappingWithPath: &pldapi.KeyMappingWithPath{KeyMapping: &pldapi.KeyMapping{Identifier: funderIdentifier, KeyHandle: "m/44'/148'/1'"}},
		Verifier:           &pldapi.KeyVerifier{Verifier: funderAddr},
	}

	mockKeyManager := m.keyManager.(*componentsmocks.KeyManager)
	mockKeyManager.On("ResolveKeyNewDatabaseTX", mock.Anything, funderIdentifier, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS).Return(funderKey, nil).Once()
	mockKeyManager.On("ReverseKeyLookup", mock.Anything, mock.Anything, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, funderAddr).Return(funderKey, nil).Once()
	mockKeyManager.On("Sign", mock.Anything, funderKey, signpayloads.OPAQUE_TO_EDDSA, mock.Anything).Return([]byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), nil).Once()

	ledger := submitter.ptm.baseLedger.(*mockStellarBaseLedger)
	getAccountInfoCalls := 0
	ledger.getAccountInfo = func(ctx context.Context, got pldtypes.ChainAddress) (*baseledger.AccountInfo, error) {
		if got.String() == from.String() {
			getAccountInfoCalls++
			return nil, fmt.Errorf("account not found")
		}
		// the funder
		nonce := pldtypes.HexUint64(7)
		return &baseledger.AccountInfo{Address: got, OrderingKey: &nonce}, nil
	}
	submittedHash := pldtypes.MustParseBytes32("0x0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	ledger.submit = func(ctx context.Context, raw baseledger.SignedChainTx) (baseledger.TxID, error) {
		return submittedHash, nil
	}
	ledger.getTransactionResult = func(ctx context.Context, id baseledger.TxID) (*baseledger.TxResult, error) {
		return &baseledger.TxResult{ID: id, Success: true}, nil
	}

	err := submitter.EnsureFromAccountFunded(ctx, from)
	require.NoError(t, err)
	// The entry check plus the rebuild loop's own per-attempt recheck, both before the account
	// exists.
	assert.Equal(t, 2, getAccountInfoCalls)
}

// stellarBadSeqErrorResultXDR builds a base64 ErrorResultXDR for a txBAD_SEQ rejection - what a
// real Stellar node returns when a submitted sequence number was already consumed, which is
// exactly what two concurrent ensureAccountFunded calls against the same funder identity produce
// (see AssignOrderingKeys' own doc comment: a deploy needs two separate 8-account channel-account
// pools bootstrapped simultaneously - the notary's own identity, plus a fresh per-deploy nonce
// identity - both funded from the same configured funder, with no in-process serialization).
func stellarBadSeqErrorResultXDR(t *testing.T) string {
	t.Helper()
	var badSeqResult xdr.TransactionResult
	badSeqResult.Result.Code = xdr.TransactionResultCodeTxBadSeq
	var buf bytes.Buffer
	_, err := xdr.Marshal(&buf, badSeqResult)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// TestStellarEnsureAccountFundedRetriesAndRecoversFromBadSeq proves the fix for a live failure
// observed against a genuinely cold-started (fresh) Stellar network: stellarChainSubmitter.Submit
// already translates a txBAD_SEQ rejection into SubmitResult{Outcome: SubmissionOutcomeNonceTooLow,
// TxHash: <the original intended hash>}, err == nil - previously, ensureAccountFunded never
// inspected result.Outcome, so a rejected submission was indistinguishable from an accepted one
// and fell straight through to polling GetTransactionResult for a hash that was never actually
// accepted on-chain (reproduced by the sibling
// TestStellarEnsureAccountFundedRetriesThenFailsOnPersistentBadSeq test below).
//
// Here the first submission attempt is rejected (funder sequence stale, as if a concurrent
// bootstrap already consumed it), and the second attempt observes the funder's now-advanced
// sequence number (as if that concurrent submission has since confirmed) and succeeds - the exact
// recovery a sequence-number race calls for.
func TestStellarEnsureAccountFundedRetriesAndRecoversFromBadSeq(t *testing.T) {
	ctx, submitter, m, done := newTestStellarSubmitter(t)
	defer done()
	funderIdentifier := "stellar.funder"
	submitter.channelAccounts = &pldconf.ChannelAccountsConfig{PoolSize: confutil.P(1), Funder: &funderIdentifier}
	// Fast retry bounds so this test doesn't wait out the real production timing (2-5s per
	// attempt, up to 30 attempts).
	submitter.fundingConfirmationRetry = retry.NewRetryLimited(&pldconf.RetryConfigWithMax{
		RetryConfig: pldconf.RetryConfig{InitialDelay: confutil.P("1ms"), MaxDelay: confutil.P("1ms"), Factor: confutil.P(1.0)},
		MaxAttempts: confutil.P(3),
	})

	from := *pldtypes.MustParseChainAddress(testStellarAccount)
	funderAddr := keypair.MustRandom().Address()
	funderKey := &pldapi.KeyMappingAndVerifier{
		KeyMappingWithPath: &pldapi.KeyMappingWithPath{KeyMapping: &pldapi.KeyMapping{Identifier: funderIdentifier, KeyHandle: "m/44'/148'/1'"}},
		Verifier:           &pldapi.KeyVerifier{Verifier: funderAddr},
	}

	mockKeyManager := m.keyManager.(*componentsmocks.KeyManager)
	mockKeyManager.On("ResolveKeyNewDatabaseTX", mock.Anything, funderIdentifier, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS).Return(funderKey, nil).Once()
	mockKeyManager.On("ReverseKeyLookup", mock.Anything, mock.Anything, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, funderAddr).Return(funderKey, nil)
	mockKeyManager.On("Sign", mock.Anything, funderKey, signpayloads.OPAQUE_TO_EDDSA, mock.Anything).Return([]byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), nil)

	getAccountInfoCallsForFunder := 0
	ledger := submitter.ptm.baseLedger.(*mockStellarBaseLedger)
	ledger.getAccountInfo = func(ctx context.Context, got pldtypes.ChainAddress) (*baseledger.AccountInfo, error) {
		if got.String() == from.String() {
			return nil, fmt.Errorf("account not found")
		}
		getAccountInfoCallsForFunder++
		// First read: the sequence a concurrent submission is ABOUT to consume. Second read
		// onward: that concurrent submission has since confirmed, advancing the sequence -
		// exactly what re-fetching before retrying should observe.
		nonce := pldtypes.HexUint64(7)
		if getAccountInfoCallsForFunder > 1 {
			nonce = 8
		}
		return &baseledger.AccountInfo{Address: got, OrderingKey: &nonce}, nil
	}

	errorResultXDR := stellarBadSeqErrorResultXDR(t)
	submitCalls := 0
	ledger.submit = func(ctx context.Context, raw baseledger.SignedChainTx) (baseledger.TxID, error) {
		submitCalls++
		if submitCalls == 1 {
			return baseledger.TxID{}, &baseledgerstellar.SubmissionRejectedError{Status: "ERROR", ErrorResultXDR: errorResultXDR}
		}
		var hash baseledger.TxID
		hash[0] = byte(submitCalls)
		return hash, nil
	}
	ledger.getTransactionResult = func(ctx context.Context, id baseledger.TxID) (*baseledger.TxResult, error) {
		return &baseledger.TxResult{ID: id, Success: true}, nil
	}

	err := submitter.EnsureFromAccountFunded(ctx, from)

	require.NoError(t, err, "expected the second attempt (against the funder's now-advanced sequence) to succeed")
	assert.Equal(t, 2, submitCalls, "expected exactly one retry after the bad-sequence rejection")
}

// TestStellarEnsureAccountFundedRetriesThenFailsOnPersistentBadSeq confirms the retry is bounded
// (matching fundingConfirmationRetry's own MaxAttempts) rather than looping forever, if every
// submission attempt keeps losing the sequence-number race.
func TestStellarEnsureAccountFundedRetriesThenFailsOnPersistentBadSeq(t *testing.T) {
	ctx, submitter, m, done := newTestStellarSubmitter(t)
	defer done()
	funderIdentifier := "stellar.funder"
	submitter.channelAccounts = &pldconf.ChannelAccountsConfig{PoolSize: confutil.P(1), Funder: &funderIdentifier}
	// fundingRebuildRetry (not fundingConfirmationRetry) is what bounds submission-rejection
	// retries: confirmation polling is never reached in this test, since every submission attempt
	// is itself rejected.
	submitter.fundingRebuildRetry = retry.NewRetryLimited(&pldconf.RetryConfigWithMax{
		RetryConfig: pldconf.RetryConfig{InitialDelay: confutil.P("1ms"), MaxDelay: confutil.P("1ms"), Factor: confutil.P(1.0)},
		MaxAttempts: confutil.P(2),
	})

	from := *pldtypes.MustParseChainAddress(testStellarAccount)
	funderAddr := keypair.MustRandom().Address()
	funderKey := &pldapi.KeyMappingAndVerifier{
		KeyMappingWithPath: &pldapi.KeyMappingWithPath{KeyMapping: &pldapi.KeyMapping{Identifier: funderIdentifier, KeyHandle: "m/44'/148'/1'"}},
		Verifier:           &pldapi.KeyVerifier{Verifier: funderAddr},
	}

	mockKeyManager := m.keyManager.(*componentsmocks.KeyManager)
	mockKeyManager.On("ResolveKeyNewDatabaseTX", mock.Anything, funderIdentifier, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS).Return(funderKey, nil).Once()
	mockKeyManager.On("ReverseKeyLookup", mock.Anything, mock.Anything, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, funderAddr).Return(funderKey, nil)
	mockKeyManager.On("Sign", mock.Anything, funderKey, signpayloads.OPAQUE_TO_EDDSA, mock.Anything).Return([]byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), nil)

	ledger := submitter.ptm.baseLedger.(*mockStellarBaseLedger)
	ledger.getAccountInfo = func(ctx context.Context, got pldtypes.ChainAddress) (*baseledger.AccountInfo, error) {
		if got.String() == from.String() {
			return nil, fmt.Errorf("account not found")
		}
		nonce := pldtypes.HexUint64(7)
		return &baseledger.AccountInfo{Address: got, OrderingKey: &nonce}, nil
	}

	errorResultXDR := stellarBadSeqErrorResultXDR(t)
	submitCalls := 0
	ledger.submit = func(ctx context.Context, raw baseledger.SignedChainTx) (baseledger.TxID, error) {
		submitCalls++
		return baseledger.TxID{}, &baseledgerstellar.SubmissionRejectedError{Status: "ERROR", ErrorResultXDR: errorResultXDR}
	}
	getTransactionResultCalls := 0
	ledger.getTransactionResult = func(ctx context.Context, id baseledger.TxID) (*baseledger.TxResult, error) {
		getTransactionResultCalls++
		return nil, fmt.Errorf("transaction %s not found (or outside the RPC retention window)", id)
	}

	err := submitter.EnsureFromAccountFunded(ctx, from)

	require.Error(t, err, "ensureAccountFunded should surface a failure if the sequence race never resolves")
	assert.Equal(t, 2, submitCalls, "expected the bounded retry count (MaxAttempts=2), not an unbounded loop")
	assert.Equal(t, 0, getTransactionResultCalls, "expected it to never reach confirmation polling, since no submission was ever accepted")
}

// TestStellarEnsureAccountFundedRebuildsWhenConfirmationNeverArrives proves the second, subtler
// half of the same live failure: stellar-rpc only synchronously rejects a submission whose
// sequence is ALREADY stale against its current ledger view. Two concurrent submissions built
// before either has landed in a ledger can both look valid and both get accepted (Submit returns
// no error, Outcome != SubmissionOutcomeNonceTooLow) - the eventual loser is then silently dropped
// from the mempool once the winner's inclusion advances the funder's sequence, and never surfaces
// as a rejection, only as a confirmation that never arrives. Confirmed against a real cold-started
// network: the first fix (retrying on an outright NonceTooLow rejection) was not sufficient on its
// own - a live deploy still failed with "transaction ... not found (or outside the RPC retention
// window)" after polling the same never-confirming hash for fundingConfirmationRetry's entire
// budget. The fix is for a confirmation timeout to also trigger a rebuild (fresh funder sequence,
// fresh submission) rather than only an outright rejection.
func TestStellarEnsureAccountFundedRebuildsWhenConfirmationNeverArrives(t *testing.T) {
	ctx, submitter, m, done := newTestStellarSubmitter(t)
	defer done()
	funderIdentifier := "stellar.funder"
	submitter.channelAccounts = &pldconf.ChannelAccountsConfig{PoolSize: confutil.P(1), Funder: &funderIdentifier}
	// Fast bounds so the test doesn't wait out real production timing.
	submitter.fundingConfirmationRetry = retry.NewRetryLimited(&pldconf.RetryConfigWithMax{
		RetryConfig: pldconf.RetryConfig{InitialDelay: confutil.P("1ms"), MaxDelay: confutil.P("1ms"), Factor: confutil.P(1.0)},
		MaxAttempts: confutil.P(2),
	})
	submitter.fundingRebuildRetry = retry.NewRetryLimited(&pldconf.RetryConfigWithMax{
		RetryConfig: pldconf.RetryConfig{InitialDelay: confutil.P("1ms"), MaxDelay: confutil.P("1ms"), Factor: confutil.P(1.0)},
		MaxAttempts: confutil.P(3),
	})

	from := *pldtypes.MustParseChainAddress(testStellarAccount)
	funderAddr := keypair.MustRandom().Address()
	funderKey := &pldapi.KeyMappingAndVerifier{
		KeyMappingWithPath: &pldapi.KeyMappingWithPath{KeyMapping: &pldapi.KeyMapping{Identifier: funderIdentifier, KeyHandle: "m/44'/148'/1'"}},
		Verifier:           &pldapi.KeyVerifier{Verifier: funderAddr},
	}

	mockKeyManager := m.keyManager.(*componentsmocks.KeyManager)
	mockKeyManager.On("ResolveKeyNewDatabaseTX", mock.Anything, funderIdentifier, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS).Return(funderKey, nil).Once()
	mockKeyManager.On("ReverseKeyLookup", mock.Anything, mock.Anything, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, funderAddr).Return(funderKey, nil)
	mockKeyManager.On("Sign", mock.Anything, funderKey, signpayloads.OPAQUE_TO_EDDSA, mock.Anything).Return([]byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), nil)

	fundedOnChain := false
	ledger := submitter.ptm.baseLedger.(*mockStellarBaseLedger)
	getFunderInfoCalls := 0
	ledger.getAccountInfo = func(ctx context.Context, got pldtypes.ChainAddress) (*baseledger.AccountInfo, error) {
		if got.String() == from.String() {
			if fundedOnChain {
				nonce := pldtypes.HexUint64(1)
				return &baseledger.AccountInfo{Address: got, OrderingKey: &nonce}, nil
			}
			return nil, fmt.Errorf("account not found")
		}
		getFunderInfoCalls++
		// Each rebuild re-reads a fresh (higher) sequence, as if a racing concurrent submission
		// keeps confirming ahead of ours.
		nonce := pldtypes.HexUint64(6 + uint64(getFunderInfoCalls))
		return &baseledger.AccountInfo{Address: got, OrderingKey: &nonce}, nil
	}

	submitCalls := 0
	confirmedHashes := map[baseledger.TxID]bool{}
	ledger.submit = func(ctx context.Context, raw baseledger.SignedChainTx) (baseledger.TxID, error) {
		submitCalls++
		var hash baseledger.TxID
		hash[0] = byte(submitCalls)
		if submitCalls == 3 {
			// The third rebuild finally wins the race and actually lands.
			confirmedHashes[hash] = true
			fundedOnChain = true
		}
		return hash, nil
	}
	ledger.getTransactionResult = func(ctx context.Context, id baseledger.TxID) (*baseledger.TxResult, error) {
		if confirmedHashes[id] {
			return &baseledger.TxResult{ID: id, Success: true}, nil
		}
		// The first two submissions were silently lost to the race: never rejected outright, but
		// never confirming either.
		return nil, fmt.Errorf("transaction %s not found (or outside the RPC retention window)", id)
	}

	err := submitter.EnsureFromAccountFunded(ctx, from)

	require.NoError(t, err, "expected the third rebuild (which actually lands) to succeed")
	assert.Equal(t, 3, submitCalls, "expected exactly two rebuilds after the first two submissions were silently lost")
}

func TestStellarEnsureFromAccountFundedNoopIfAlreadyFunded(t *testing.T) {
	ctx, submitter, _, done := newTestStellarSubmitter(t)
	defer done()

	from := *pldtypes.MustParseChainAddress(testStellarAccount)
	ledger := submitter.ptm.baseLedger.(*mockStellarBaseLedger)
	nonce := pldtypes.HexUint64(1)
	ledger.getAccountInfo = func(ctx context.Context, got pldtypes.ChainAddress) (*baseledger.AccountInfo, error) {
		return &baseledger.AccountInfo{Address: got, OrderingKey: &nonce}, nil
	}

	err := submitter.EnsureFromAccountFunded(ctx, from)
	require.NoError(t, err)
}

func TestStellarEnsureFromAccountFundedNoFunderConfigured(t *testing.T) {
	ctx, submitter, _, done := newTestStellarSubmitter(t)
	defer done()
	submitter.channelAccounts = &pldconf.ChannelAccountsConfig{PoolSize: confutil.P(1)}

	from := *pldtypes.MustParseChainAddress(testStellarAccount)
	ledger := submitter.ptm.baseLedger.(*mockStellarBaseLedger)
	ledger.getAccountInfo = func(ctx context.Context, got pldtypes.ChainAddress) (*baseledger.AccountInfo, error) {
		return nil, fmt.Errorf("account not found")
	}

	err := submitter.EnsureFromAccountFunded(ctx, from)
	require.ErrorContains(t, err, "channel account funder")
}

func TestStellarPrepareSubmission(t *testing.T) {
	ctx, submitter, m, done := newTestStellarSubmitter(t)
	defer done()
	ledger := submitter.ptm.baseLedger.(*mockStellarBaseLedger)
	ledger.estimateResources = func(ctx context.Context, tx *baseledger.UnsignedChainTx) (*baseledger.ResourceEstimate, error) {
		return &baseledger.ResourceEstimate{Soroban: &baseledger.SorobanResources{}}, nil
	}
	addr := *pldtypes.MustParseChainAddress(testStellarAccount)
	ptx := &DBPublicTxn{PublicTxnID: 1, From: addr, Nonce: confutil.P(uint64(7)), Data: validStellarInvokeContractPayload(t)}
	keyMapping := &pldapi.KeyMappingAndVerifier{
		KeyMappingWithPath: &pldapi.KeyMappingWithPath{KeyMapping: &pldapi.KeyMapping{Identifier: "stellar.key", KeyHandle: "m/44'/148'/0'"}},
		Verifier:           &pldapi.KeyVerifier{Verifier: testStellarAccount},
	}
	mockKeyManager := m.keyManager.(*componentsmocks.KeyManager)
	mockKeyManager.On("ReverseKeyLookup", mock.Anything, mock.Anything, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, testStellarAccount).Return(keyMapping, nil).Once()
	mockKeyManager.On("Sign", mock.Anything, keyMapping, signpayloads.OPAQUE_TO_EDDSA, mock.Anything).Return([]byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), nil).Once()

	prepared, err := submitter.PrepareSubmission(ctx, ptx, &baseledger.ResourceEstimate{})
	require.NoError(t, err)
	require.NotNil(t, prepared)
	assert.Equal(t, uint64(1), prepared.PublicTxnID)
	require.NotNil(t, prepared.TransactionHash)
	assert.NotEmpty(t, prepared.RawTransaction)

	fromAddr, err := keypair.ParseAddress(testStellarAccount)
	require.NoError(t, err)
	var envelope xdr.TransactionEnvelope
	require.NoError(t, envelope.UnmarshalBinary(prepared.RawTransaction))
	signatures := envelope.Signatures()
	require.Len(t, signatures, 1)
	assert.Equal(t, xdr.SignatureHint(fromAddr.Hint()), signatures[0].Hint)
}

func TestStellarPrepareSubmissionWithChannelAccount(t *testing.T) {
	ctx, submitter, m, done := newTestStellarSubmitter(t)
	defer done()
	ledger := submitter.ptm.baseLedger.(*mockStellarBaseLedger)
	ledger.estimateResources = func(ctx context.Context, tx *baseledger.UnsignedChainTx) (*baseledger.ResourceEstimate, error) {
		return &baseledger.ResourceEstimate{Soroban: &baseledger.SorobanResources{}}, nil
	}
	businessIdentity := *pldtypes.MustParseChainAddress(testStellarAccount)
	channelAddr := keypair.MustRandom().Address()
	channelAccount := *pldtypes.MustParseChainAddress(channelAddr)
	ptx := &DBPublicTxn{
		PublicTxnID: 1, From: businessIdentity, ChannelAccount: &channelAccount,
		Nonce: confutil.P(uint64(7)), Data: validStellarInvokeContractPayload(t),
	}
	channelKeyMapping := &pldapi.KeyMappingAndVerifier{
		KeyMappingWithPath: &pldapi.KeyMappingWithPath{KeyMapping: &pldapi.KeyMapping{Identifier: "stellar.key.channel.0", KeyHandle: "m/44'/148'/1'"}},
		Verifier:           &pldapi.KeyVerifier{Verifier: channelAddr},
	}
	businessKeyMapping := &pldapi.KeyMappingAndVerifier{
		KeyMappingWithPath: &pldapi.KeyMappingWithPath{KeyMapping: &pldapi.KeyMapping{Identifier: "stellar.key.business", KeyHandle: "m/44'/148'/0'"}},
		Verifier:           &pldapi.KeyVerifier{Verifier: testStellarAccount},
	}
	mockKeyManager := m.keyManager.(*componentsmocks.KeyManager)
	// Signing must resolve BOTH the channel account's key (the transaction's envelope source -
	// sequence + fee payer) AND the business identity's key (the InvokeHostFunction operation's
	// own, distinct source account) - stellar-core rejects the operation with opBadAuth if the
	// business identity's signature is missing.
	mockKeyManager.On("ReverseKeyLookup", mock.Anything, mock.Anything, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, channelAddr).Return(channelKeyMapping, nil).Once()
	mockKeyManager.On("Sign", mock.Anything, channelKeyMapping, signpayloads.OPAQUE_TO_EDDSA, mock.Anything).Return([]byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), nil).Once()
	mockKeyManager.On("ReverseKeyLookup", mock.Anything, mock.Anything, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, testStellarAccount).Return(businessKeyMapping, nil).Once()
	mockKeyManager.On("Sign", mock.Anything, businessKeyMapping, signpayloads.OPAQUE_TO_EDDSA, mock.Anything).Return([]byte("fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"), nil).Once()

	prepared, err := submitter.PrepareSubmission(ctx, ptx, &baseledger.ResourceEstimate{})
	require.NoError(t, err)
	require.NotNil(t, prepared)
	assert.NotEmpty(t, prepared.RawTransaction)

	var envelope xdr.TransactionEnvelope
	require.NoError(t, envelope.UnmarshalBinary(prepared.RawTransaction))
	// the envelope's own source account is the channel account...
	envelopeSourceAddr, err := xdr.AddressToMuxedAccount(channelAddr)
	require.NoError(t, err)
	assert.Equal(t, envelopeSourceAddr, envelope.SourceAccount())
	// ...but the InvokeHostFunction operation still names the business identity as its source
	ops := envelope.Operations()
	require.Len(t, ops, 1)
	_, ok := ops[0].Body.GetInvokeHostFunctionOp()
	require.True(t, ok)
	require.NotNil(t, ops[0].SourceAccount)
	businessSourceAddr, err := xdr.AddressToMuxedAccount(testStellarAccount)
	require.NoError(t, err)
	assert.Equal(t, businessSourceAddr, *ops[0].SourceAccount)
	// and the envelope carries a signature for both the channel account and the business identity
	channelKeypair, err := keypair.ParseAddress(channelAddr)
	require.NoError(t, err)
	businessKeypair, err := keypair.ParseAddress(testStellarAccount)
	require.NoError(t, err)
	signatures := envelope.Signatures()
	require.Len(t, signatures, 2)
	hints := []xdr.SignatureHint{signatures[0].Hint, signatures[1].Hint}
	assert.Contains(t, hints, xdr.SignatureHint(channelKeypair.Hint()))
	assert.Contains(t, hints, xdr.SignatureHint(businessKeypair.Hint()))
}

func TestStellarPrepareSubmissionInvalidPayload(t *testing.T) {
	ctx, submitter, _, done := newTestStellarSubmitter(t)
	defer done()
	ledger := submitter.ptm.baseLedger.(*mockStellarBaseLedger)
	// The real baseledger/stellar.Client.EstimateResources builds (and so validates) the
	// transaction before simulating - the mock reproduces that same failure mode here.
	ledger.estimateResources = func(ctx context.Context, tx *baseledger.UnsignedChainTx) (*baseledger.ResourceEstimate, error) {
		return nil, fmt.Errorf("invalid host function payload: bad xdr")
	}
	addr := *pldtypes.MustParseChainAddress(testStellarAccount)
	ptx := &DBPublicTxn{PublicTxnID: 1, From: addr, Nonce: confutil.P(uint64(7)), Data: []byte("nope")}
	_, err := submitter.PrepareSubmission(ctx, ptx, &baseledger.ResourceEstimate{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid host function payload")
}

func TestStellarSubmitBadSeq(t *testing.T) {
	ctx, submitter, _, done := newTestStellarSubmitter(t)
	defer done()
	ledger := submitter.ptm.baseLedger.(*mockStellarBaseLedger)
	resultXDR, err := xdr.MarshalBase64(xdr.TransactionResult{Result: xdr.TransactionResultResult{Code: xdr.TransactionResultCodeTxBadSeq}})
	require.NoError(t, err)
	ledger.submit = func(ctx context.Context, raw baseledger.SignedChainTx) (baseledger.TxID, error) {
		return pldtypes.MustParseBytes32("0x0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), &baseledgerstellar.SubmissionRejectedError{Status: "ERROR", ErrorResultXDR: resultXDR}
	}
	prepared := &PreparedSubmission{RawTransaction: []byte{0x01}, TransactionHash: func() *pldtypes.Bytes32 {
		h := pldtypes.MustParseBytes32("0x0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
		return &h
	}()}
	result, err := submitter.Submit(ctx, prepared)
	require.NoError(t, err)
	assert.Equal(t, SubmissionOutcomeNonceTooLow, result.Outcome)
}

func TestStellarSubmitInsufficientFeeRequiresRetry(t *testing.T) {
	ctx, submitter, _, done := newTestStellarSubmitter(t)
	defer done()
	ledger := submitter.ptm.baseLedger.(*mockStellarBaseLedger)
	resultXDR, err := xdr.MarshalBase64(xdr.TransactionResult{Result: xdr.TransactionResultResult{Code: xdr.TransactionResultCodeTxInsufficientFee}})
	require.NoError(t, err)
	ledger.submit = func(ctx context.Context, raw baseledger.SignedChainTx) (baseledger.TxID, error) {
		return pldtypes.MustParseBytes32("0x0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), &baseledgerstellar.SubmissionRejectedError{Status: "ERROR", ErrorResultXDR: resultXDR}
	}
	prepared := &PreparedSubmission{RawTransaction: []byte{0x01}, TransactionHash: func() *pldtypes.Bytes32 {
		h := pldtypes.MustParseBytes32("0x0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
		return &h
	}()}
	result, err := submitter.Submit(ctx, prepared)
	require.Error(t, err)
	assert.Equal(t, SubmissionOutcomeFailedRequiresRetry, result.Outcome)
	assert.Equal(t, xdr.TransactionResultCodeTxInsufficientFee.String(), result.ErrorReason)
}

func TestStellarPrepareSubmissionRequiresRestore(t *testing.T) {
	ctx, submitter, _, done := newTestStellarSubmitter(t)
	defer done()
	ledger := submitter.ptm.baseLedger.(*mockStellarBaseLedger)
	ledger.estimateResources = func(ctx context.Context, tx *baseledger.UnsignedChainTx) (*baseledger.ResourceEstimate, error) {
		return &baseledger.ResourceEstimate{Soroban: &baseledger.SorobanResources{
			RequiresRestore:                   true,
			RestorePreambleTransactionDataXDR: []byte{0x01, 0x02},
		}}, nil
	}
	addr := *pldtypes.MustParseChainAddress(testStellarAccount)
	ptx := &DBPublicTxn{PublicTxnID: 1, From: addr, Nonce: confutil.P(uint64(7)), Data: validStellarInvokeContractPayload(t)}

	prepared, err := submitter.PrepareSubmission(ctx, ptx, &baseledger.ResourceEstimate{})
	require.NoError(t, err)
	require.NotNil(t, prepared)
	assert.True(t, prepared.RequiresRestore)
	require.NotNil(t, prepared.RestoreSoroban)
	assert.Equal(t, []byte{0x01, 0x02}, prepared.RestoreSoroban.RestorePreambleTransactionDataXDR)
	assert.Empty(t, prepared.RawTransaction)
	assert.Nil(t, prepared.TransactionHash)
}

func TestStellarPrepareSubmissionClassicOpsSkipsSimulation(t *testing.T) {
	ctx, submitter, m, done := newTestStellarSubmitter(t)
	defer done()
	// The mock's estimateResources is left unset (returns "unused" if called) - proving classic
	// ops never reach EstimateResources, matching chapter 12 §12.3 (classic ops have no footprint).
	addr := *pldtypes.MustParseChainAddress(testStellarAccount)
	payload, err := baseledgerstellar.BuildChangeTrustPayload(testStellarAccount, txnbuildAsset(t), "1000")
	require.NoError(t, err)
	ptx := &DBPublicTxn{
		PublicTxnID: 1, From: addr, Nonce: confutil.P(uint64(7)), Data: payload,
		PayloadKind: pldapi.PublicTxPayloadKindXDRClassicOps.Enum(),
	}
	keyMapping := &pldapi.KeyMappingAndVerifier{
		KeyMappingWithPath: &pldapi.KeyMappingWithPath{KeyMapping: &pldapi.KeyMapping{Identifier: "stellar.key", KeyHandle: "m/44'/148'/0'"}},
		Verifier:           &pldapi.KeyVerifier{Verifier: testStellarAccount},
	}
	mockKeyManager := m.keyManager.(*componentsmocks.KeyManager)
	mockKeyManager.On("ReverseKeyLookup", mock.Anything, mock.Anything, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, testStellarAccount).Return(keyMapping, nil).Once()
	mockKeyManager.On("Sign", mock.Anything, keyMapping, signpayloads.OPAQUE_TO_EDDSA, mock.Anything).Return([]byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), nil).Once()

	prepared, err := submitter.PrepareSubmission(ctx, ptx, &baseledger.ResourceEstimate{})
	require.NoError(t, err)
	require.NotNil(t, prepared)
	assert.False(t, prepared.RequiresRestore)
	assert.NotEmpty(t, prepared.RawTransaction)
}

func txnbuildAsset(t *testing.T) txnbuild.Asset {
	issuer := keypair.MustRandom().Address()
	return txnbuild.CreditAsset{Code: "USDX", Issuer: issuer}
}

func TestStellarPrepareRestore(t *testing.T) {
	ctx, submitter, m, done := newTestStellarSubmitter(t)
	defer done()
	addr := *pldtypes.MustParseChainAddress(testStellarAccount)
	ptx := &DBPublicTxn{PublicTxnID: 1, From: addr, Nonce: confutil.P(uint64(7))}

	var sorobanData xdr.SorobanTransactionData
	sorobanDataXDR, err := sorobanData.MarshalBinary()
	require.NoError(t, err)
	soroban := &baseledger.SorobanResources{RestorePreambleTransactionDataXDR: sorobanDataXDR}

	keyMapping := &pldapi.KeyMappingAndVerifier{
		KeyMappingWithPath: &pldapi.KeyMappingWithPath{KeyMapping: &pldapi.KeyMapping{Identifier: "stellar.key", KeyHandle: "m/44'/148'/0'"}},
		Verifier:           &pldapi.KeyVerifier{Verifier: testStellarAccount},
	}
	mockKeyManager := m.keyManager.(*componentsmocks.KeyManager)
	mockKeyManager.On("ReverseKeyLookup", mock.Anything, mock.Anything, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, testStellarAccount).Return(keyMapping, nil).Once()
	mockKeyManager.On("Sign", mock.Anything, keyMapping, signpayloads.OPAQUE_TO_EDDSA, mock.Anything).Return([]byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), nil).Once()

	prepared, err := submitter.PrepareRestore(ctx, ptx, soroban)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	assert.Equal(t, uint64(1), prepared.PublicTxnID)
	require.NotNil(t, prepared.TransactionHash)
	assert.NotEmpty(t, prepared.RawTransaction)

	var envelope xdr.TransactionEnvelope
	require.NoError(t, envelope.UnmarshalBinary(prepared.RawTransaction))
	ops := envelope.Operations()
	require.Len(t, ops, 1)
	_, ok := ops[0].Body.GetRestoreFootprintOp()
	assert.True(t, ok)
}

func TestStellarPrepareRestoreWithChannelAccount(t *testing.T) {
	ctx, submitter, m, done := newTestStellarSubmitter(t)
	defer done()
	businessIdentity := *pldtypes.MustParseChainAddress(testStellarAccount)
	channelAddr := keypair.MustRandom().Address()
	channelAccount := *pldtypes.MustParseChainAddress(channelAddr)
	ptx := &DBPublicTxn{PublicTxnID: 1, From: businessIdentity, ChannelAccount: &channelAccount, Nonce: confutil.P(uint64(7))}

	var sorobanData xdr.SorobanTransactionData
	sorobanDataXDR, err := sorobanData.MarshalBinary()
	require.NoError(t, err)
	soroban := &baseledger.SorobanResources{RestorePreambleTransactionDataXDR: sorobanDataXDR}

	channelKeyMapping := &pldapi.KeyMappingAndVerifier{
		KeyMappingWithPath: &pldapi.KeyMappingWithPath{KeyMapping: &pldapi.KeyMapping{Identifier: "stellar.key.channel.0", KeyHandle: "m/44'/148'/1'"}},
		Verifier:           &pldapi.KeyVerifier{Verifier: channelAddr},
	}
	mockKeyManager := m.keyManager.(*componentsmocks.KeyManager)
	mockKeyManager.On("ReverseKeyLookup", mock.Anything, mock.Anything, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, channelAddr).Return(channelKeyMapping, nil).Once()
	mockKeyManager.On("Sign", mock.Anything, channelKeyMapping, signpayloads.OPAQUE_TO_EDDSA, mock.Anything).Return([]byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), nil).Once()

	prepared, err := submitter.PrepareRestore(ctx, ptx, soroban)
	require.NoError(t, err)
	require.NotNil(t, prepared)

	var envelope xdr.TransactionEnvelope
	require.NoError(t, envelope.UnmarshalBinary(prepared.RawTransaction))
	envelopeSourceAddr, err := xdr.AddressToMuxedAccount(channelAddr)
	require.NoError(t, err)
	assert.Equal(t, envelopeSourceAddr, envelope.SourceAccount())
	channelKeypair, err := keypair.ParseAddress(channelAddr)
	require.NoError(t, err)
	signatures := envelope.Signatures()
	require.Len(t, signatures, 1)
	assert.Equal(t, xdr.SignatureHint(channelKeypair.Hint()), signatures[0].Hint)
}

func TestStellarPrepareRestoreRequiresNonce(t *testing.T) {
	ctx, submitter, _, done := newTestStellarSubmitter(t)
	defer done()
	addr := *pldtypes.MustParseChainAddress(testStellarAccount)
	ptx := &DBPublicTxn{PublicTxnID: 1, From: addr}
	_, err := submitter.PrepareRestore(ctx, ptx, &baseledger.SorobanResources{RestorePreambleTransactionDataXDR: []byte{0x01}})
	require.ErrorContains(t, err, "sequence number")
}

func TestStellarPrepareRestoreRequiresPreamble(t *testing.T) {
	ctx, submitter, _, done := newTestStellarSubmitter(t)
	defer done()
	addr := *pldtypes.MustParseChainAddress(testStellarAccount)
	ptx := &DBPublicTxn{PublicTxnID: 1, From: addr, Nonce: confutil.P(uint64(7))}
	_, err := submitter.PrepareRestore(ctx, ptx, &baseledger.SorobanResources{})
	require.ErrorContains(t, err, "no restore preamble available")
}
