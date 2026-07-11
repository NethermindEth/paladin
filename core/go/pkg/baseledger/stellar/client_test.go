// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package stellar

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/require"
)

const testAccount = "GAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAWHF"

// fakeRPC is a hand-rolled fake of the rpcClient interface, mirroring how baseledger/evm's own
// tests mock ethclient.EthClient - the real *rpcclient.Client satisfies this interface
// structurally, so no adapter is needed in production code.
type fakeRPC struct {
	loadAccount         func(ctx context.Context, address string) (txnbuild.Account, error)
	simulateTransaction func(ctx context.Context, req protocol.SimulateTransactionRequest) (protocol.SimulateTransactionResponse, error)
	sendTransaction     func(ctx context.Context, req protocol.SendTransactionRequest) (protocol.SendTransactionResponse, error)
	getTransaction      func(ctx context.Context, req protocol.GetTransactionRequest) (protocol.GetTransactionResponse, error)
	getLedgerEntries    func(ctx context.Context, req protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error)
}

func (f *fakeRPC) LoadAccount(ctx context.Context, address string) (txnbuild.Account, error) {
	return f.loadAccount(ctx, address)
}

func (f *fakeRPC) SimulateTransaction(ctx context.Context, req protocol.SimulateTransactionRequest) (protocol.SimulateTransactionResponse, error) {
	return f.simulateTransaction(ctx, req)
}

func (f *fakeRPC) SendTransaction(ctx context.Context, req protocol.SendTransactionRequest) (protocol.SendTransactionResponse, error) {
	return f.sendTransaction(ctx, req)
}

func (f *fakeRPC) GetTransaction(ctx context.Context, req protocol.GetTransactionRequest) (protocol.GetTransactionResponse, error) {
	return f.getTransaction(ctx, req)
}

func (f *fakeRPC) GetLedgerEntries(ctx context.Context, req protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error) {
	return f.getLedgerEntries(ctx, req)
}

// validInvokeContractPayload is a minimal, correctly XDR-encoded xdr.HostFunction (invoking a
// zero-value contract ID with no arguments) - enough to exercise buildTransaction's decode/encode
// path without needing a real deployed contract, since the fakes below never inspect the built
// transaction's content.
func validInvokeContractPayload(t *testing.T) []byte {
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

// validClassicOpsPayload is a minimal, correctly XDR-encoded []xdr.Operation containing a single
// Payment - enough to exercise the classic-ops decode path without needing the full asset/issuer
// plumbing exercised by classic_ops_test.go.
func validClassicOpsPayload(t *testing.T) []byte {
	payload, err := EncodeClassicOperations([]txnbuild.Operation{
		&txnbuild.Payment{Destination: testAccount, Amount: "1", Asset: txnbuild.NativeAsset{}},
	})
	require.NoError(t, err)
	return payload
}

func fakeAccountLoader(sequence int64) func(ctx context.Context, address string) (txnbuild.Account, error) {
	return func(ctx context.Context, address string) (txnbuild.Account, error) {
		account := txnbuild.NewSimpleAccount(address, sequence)
		return &account, nil
	}
}

func TestChainInfo(t *testing.T) {
	c := WrapClient(&fakeRPC{}, "Test SDF Network ; September 2015", nil)
	require.Equal(t, baseledger.ChainInfo{
		Kind:      baseledger.ChainKindStellar,
		NetworkID: "Test SDF Network ; September 2015",
	}, c.ChainInfo())
}

func TestClose(t *testing.T) {
	closed := false
	c := WrapClient(&fakeRPC{}, "", func() { closed = true })
	c.Close()
	require.True(t, closed)

	// must not panic when closeFn is nil
	WrapClient(&fakeRPC{}, "", nil).Close()
}

func TestCall(t *testing.T) {
	ctx := context.Background()
	from := *pldtypes.MustParseChainAddress(testAccount)
	returnValue := base64.StdEncoding.EncodeToString([]byte{0xde, 0xad, 0xbe, 0xef})

	rpc := &fakeRPC{
		loadAccount: fakeAccountLoader(41),
		simulateTransaction: func(ctx context.Context, req protocol.SimulateTransactionRequest) (protocol.SimulateTransactionResponse, error) {
			return protocol.SimulateTransactionResponse{
				Results: []protocol.SimulateHostFunctionResult{{ReturnValueXDR: &returnValue}},
			}, nil
		},
	}
	c := WrapClient(rpc, "Test SDF Network ; September 2015", nil)

	res, err := c.Call(ctx, &baseledger.CallRequest{
		From:        &from,
		PayloadKind: baseledger.PayloadEncodingXDRInvokeContractArgs,
		Payload:     validInvokeContractPayload(t),
	})
	require.NoError(t, err)
	require.Equal(t, []byte{0xde, 0xad, 0xbe, 0xef}, res.Data)
}

func TestCallReturnsSimulationError(t *testing.T) {
	ctx := context.Background()
	from := *pldtypes.MustParseChainAddress(testAccount)

	rpc := &fakeRPC{
		loadAccount: fakeAccountLoader(41),
		simulateTransaction: func(ctx context.Context, req protocol.SimulateTransactionRequest) (protocol.SimulateTransactionResponse, error) {
			return protocol.SimulateTransactionResponse{Error: "host invocation failed"}, nil
		},
	}
	c := WrapClient(rpc, "", nil)

	_, err := c.Call(ctx, &baseledger.CallRequest{
		From:        &from,
		PayloadKind: baseledger.PayloadEncodingXDRInvokeContractArgs,
		Payload:     validInvokeContractPayload(t),
	})
	require.ErrorContains(t, err, "host invocation failed")
}

func TestCallRequiresFrom(t *testing.T) {
	c := WrapClient(&fakeRPC{}, "", nil)
	_, err := c.Call(context.Background(), &baseledger.CallRequest{
		PayloadKind: baseledger.PayloadEncodingXDRInvokeContractArgs,
		Payload:     validInvokeContractPayload(t),
	})
	require.ErrorContains(t, err, "source account")
}

func TestCallRejectsUnsupportedPayloadKind(t *testing.T) {
	from := *pldtypes.MustParseChainAddress(testAccount)
	c := WrapClient(&fakeRPC{}, "", nil)
	_, err := c.Call(context.Background(), &baseledger.CallRequest{
		From:        &from,
		PayloadKind: baseledger.PayloadEncodingFunctionCallData,
		Payload:     []byte{0x01},
	})
	require.ErrorContains(t, err, "unsupported stellar payload kind")
}

func TestCallRejectsClassicOps(t *testing.T) {
	from := *pldtypes.MustParseChainAddress(testAccount)
	c := WrapClient(&fakeRPC{}, "", nil)
	_, err := c.Call(context.Background(), &baseledger.CallRequest{
		From:        &from,
		PayloadKind: baseledger.PayloadEncodingXDRClassicOps,
		Payload:     validClassicOpsPayload(t),
	})
	require.ErrorContains(t, err, "classic operations do not support Call")
}

func TestGetAccountInfo(t *testing.T) {
	ctx := context.Background()
	addr := *pldtypes.MustParseChainAddress(testAccount)

	rpc := &fakeRPC{loadAccount: fakeAccountLoader(100)}
	c := WrapClient(rpc, "", nil)

	info, err := c.GetAccountInfo(ctx, addr)
	require.NoError(t, err)
	require.Equal(t, addr, info.Address)
	require.NotNil(t, info.OrderingKey)
	require.EqualValues(t, 101, *info.OrderingKey)
}

func TestEstimateResources(t *testing.T) {
	ctx := context.Background()
	from := *pldtypes.MustParseChainAddress(testAccount)
	txData := base64.StdEncoding.EncodeToString([]byte{0x01, 0x02, 0x03})
	authEntry := base64.StdEncoding.EncodeToString([]byte{0x04, 0x05})
	authXDRs := []string{authEntry}

	rpc := &fakeRPC{
		loadAccount: fakeAccountLoader(41),
		simulateTransaction: func(ctx context.Context, req protocol.SimulateTransactionRequest) (protocol.SimulateTransactionResponse, error) {
			return protocol.SimulateTransactionResponse{
				TransactionDataXDR: txData,
				MinResourceFee:     12345,
				Results:            []protocol.SimulateHostFunctionResult{{AuthXDR: &authXDRs}},
			}, nil
		},
	}
	c := WrapClient(rpc, "", nil)

	res, err := c.EstimateResources(ctx, &baseledger.UnsignedChainTx{
		From:        from,
		PayloadKind: baseledger.PayloadEncodingXDRInvokeContractArgs,
		Payload:     validInvokeContractPayload(t),
	})
	require.NoError(t, err)
	require.NotNil(t, res.Soroban)
	require.Equal(t, []byte{0x01, 0x02, 0x03}, res.Soroban.TransactionDataXDR)
	require.EqualValues(t, 12345, res.Soroban.ResourceFee)
	require.False(t, res.Soroban.RequiresRestore)
	require.Equal(t, [][]byte{{0x04, 0x05}}, res.Soroban.AuthEntriesXDR)
}

func TestEstimateResourcesRequiresRestore(t *testing.T) {
	ctx := context.Background()
	from := *pldtypes.MustParseChainAddress(testAccount)

	rpc := &fakeRPC{
		loadAccount: fakeAccountLoader(41),
		simulateTransaction: func(ctx context.Context, req protocol.SimulateTransactionRequest) (protocol.SimulateTransactionResponse, error) {
			return protocol.SimulateTransactionResponse{
				RestorePreamble: &protocol.RestorePreamble{MinResourceFee: 999},
			}, nil
		},
	}
	c := WrapClient(rpc, "", nil)

	res, err := c.EstimateResources(ctx, &baseledger.UnsignedChainTx{
		From:        from,
		PayloadKind: baseledger.PayloadEncodingXDRInvokeContractArgs,
		Payload:     validInvokeContractPayload(t),
	})
	require.NoError(t, err)
	require.True(t, res.Soroban.RequiresRestore)
}

func TestEstimateResourcesClassicOpsSkipsSimulation(t *testing.T) {
	ctx := context.Background()
	from := *pldtypes.MustParseChainAddress(testAccount)

	// No loadAccount/simulateTransaction fakes are supplied - if EstimateResources called either
	// (rather than short-circuiting for classic ops), this test would nil-pointer panic.
	c := WrapClient(&fakeRPC{}, "", nil)

	res, err := c.EstimateResources(ctx, &baseledger.UnsignedChainTx{
		From:        from,
		PayloadKind: baseledger.PayloadEncodingXDRClassicOps,
		Payload:     validClassicOpsPayload(t),
	})
	require.NoError(t, err)
	require.Equal(t, &baseledger.ResourceEstimate{}, res)
}

func TestBuildTransactionClassicOps(t *testing.T) {
	ctx := context.Background()
	from := *pldtypes.MustParseChainAddress(testAccount)

	rpc := &fakeRPC{loadAccount: fakeAccountLoader(41)}
	c := WrapClient(rpc, "Test SDF Network ; September 2015", nil)

	payload, err := c.BuildTransaction(ctx, &baseledger.UnsignedChainTx{
		From:        from,
		PayloadKind: baseledger.PayloadEncodingXDRClassicOps,
		Payload:     validClassicOpsPayload(t),
	}, nil)
	require.NoError(t, err)
	// BuildTransaction must echo back the input payload kind rather than hardcoding the
	// Soroban-invoke kind - regression test for a bug caught during classic-ops integration.
	require.Equal(t, baseledger.PayloadEncodingXDRClassicOps, payload.PayloadKind)
	require.Len(t, payload.Payload, 32)
}

func TestBuildTransactionInternalClassicOpsOperationCount(t *testing.T) {
	ctx := context.Background()
	from := *pldtypes.MustParseChainAddress(testAccount)

	rpc := &fakeRPC{loadAccount: fakeAccountLoader(41)}
	c := WrapClient(rpc, "Test SDF Network ; September 2015", nil)

	payload, err := EncodeClassicOperations([]txnbuild.Operation{
		&txnbuild.Payment{Destination: testAccount, Amount: "1", Asset: txnbuild.NativeAsset{}},
		&txnbuild.CreateAccount{Destination: testAccount, Amount: "10"},
	})
	require.NoError(t, err)

	transaction, err := c.buildTransaction(ctx, &from, baseledger.PayloadEncodingXDRClassicOps, payload)
	require.NoError(t, err)
	require.Len(t, transaction.Operations(), 2)
}

func TestBuildTransactionReturnsSignaturePayload(t *testing.T) {
	ctx := context.Background()
	from := *pldtypes.MustParseChainAddress(testAccount)

	rpc := &fakeRPC{loadAccount: fakeAccountLoader(41)}
	c := WrapClient(rpc, "Test SDF Network ; September 2015", nil)

	payload, err := c.BuildTransaction(ctx, &baseledger.UnsignedChainTx{
		From:        from,
		PayloadKind: baseledger.PayloadEncodingXDRInvokeContractArgs,
		Payload:     validInvokeContractPayload(t),
	}, nil)
	require.NoError(t, err)
	require.Equal(t, baseledger.PayloadEncodingXDRInvokeContractArgs, payload.PayloadKind)
	require.Len(t, payload.Payload, 32)
}

func TestSubmitSuccess(t *testing.T) {
	ctx := context.Background()
	hash := strings.Repeat("0", 62) + "aa"

	rpc := &fakeRPC{
		sendTransaction: func(ctx context.Context, req protocol.SendTransactionRequest) (protocol.SendTransactionResponse, error) {
			require.NotEmpty(t, req.Transaction)
			return protocol.SendTransactionResponse{Status: "PENDING", Hash: hash}, nil
		},
	}
	c := WrapClient(rpc, "", nil)

	txID, err := c.Submit(ctx, []byte{0x01, 0x02})
	require.NoError(t, err)
	require.Equal(t, hash, txID.HexString())
}

func TestSubmitRejected(t *testing.T) {
	ctx := context.Background()
	hash := strings.Repeat("0", 62) + "aa"

	rpc := &fakeRPC{
		sendTransaction: func(ctx context.Context, req protocol.SendTransactionRequest) (protocol.SendTransactionResponse, error) {
			return protocol.SendTransactionResponse{Status: "ERROR", Hash: hash, ErrorResultXDR: "abc123"}, nil
		},
	}
	c := WrapClient(rpc, "", nil)

	_, err := c.Submit(ctx, []byte{0x01, 0x02})
	require.Error(t, err)
	var rejected *SubmissionRejectedError
	require.ErrorAs(t, err, &rejected)
	require.Equal(t, "ERROR", rejected.Status)
	require.Equal(t, "abc123", rejected.ErrorResultXDR)
}

func TestGetTransactionResultSuccess(t *testing.T) {
	ctx := context.Background()
	id := pldtypes.Bytes32{}

	rpc := &fakeRPC{
		getTransaction: func(ctx context.Context, req protocol.GetTransactionRequest) (protocol.GetTransactionResponse, error) {
			return protocol.GetTransactionResponse{
				TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusSuccess},
			}, nil
		},
	}
	c := WrapClient(rpc, "", nil)

	res, err := c.GetTransactionResult(ctx, id)
	require.NoError(t, err)
	require.True(t, res.Success)
}

func TestGetTransactionResultFailed(t *testing.T) {
	ctx := context.Background()
	id := pldtypes.Bytes32{}
	resultXDR := base64.StdEncoding.EncodeToString([]byte{0xaa, 0xbb})

	rpc := &fakeRPC{
		getTransaction: func(ctx context.Context, req protocol.GetTransactionRequest) (protocol.GetTransactionResponse, error) {
			return protocol.GetTransactionResponse{
				TransactionDetails: protocol.TransactionDetails{
					Status:    protocol.TransactionStatusFailed,
					ResultXDR: resultXDR,
				},
			}, nil
		},
	}
	c := WrapClient(rpc, "", nil)

	res, err := c.GetTransactionResult(ctx, id)
	require.NoError(t, err)
	require.False(t, res.Success)
	require.Equal(t, []byte{0xaa, 0xbb}, res.RevertData)
}

func TestGetTransactionResultNotFound(t *testing.T) {
	ctx := context.Background()
	id := pldtypes.Bytes32{}

	rpc := &fakeRPC{
		getTransaction: func(ctx context.Context, req protocol.GetTransactionRequest) (protocol.GetTransactionResponse, error) {
			return protocol.GetTransactionResponse{
				TransactionDetails: protocol.TransactionDetails{Status: protocol.TransactionStatusNotFound},
			}, nil
		},
	}
	c := WrapClient(rpc, "", nil)

	_, err := c.GetTransactionResult(ctx, id)
	require.ErrorContains(t, err, "not found")
}
