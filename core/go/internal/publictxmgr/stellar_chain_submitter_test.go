// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package publictxmgr

import (
	"context"
	"fmt"
	"testing"

	"github.com/LFDT-Paladin/paladin/config/pkg/confutil"
	"github.com/LFDT-Paladin/paladin/config/pkg/pldconf"
	"github.com/LFDT-Paladin/paladin/core/mocks/componentsmocks"
	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	baseledgerstellar "github.com/LFDT-Paladin/paladin/core/pkg/baseledger/stellar"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
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
			if getAccountInfoCalls == 1 {
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
