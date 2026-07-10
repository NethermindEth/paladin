// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

package evm

import (
	"context"
	"errors"
	"testing"

	"github.com/LFDT-Paladin/paladin/core/mocks/ethclientmocks"
	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	"github.com/LFDT-Paladin/paladin/core/pkg/ethclient"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestWrapClientLifecycleAndMetadata(t *testing.T) {
	mockEth := ethclientmocks.NewEthClient(t)
	mockEth.On("ChainID").Return(int64(8453))
	mockEth.On("Close").Return()

	c := WrapClient(mockEth)
	require.Equal(t, baseledger.ChainInfo{
		Kind:       baseledger.ChainKindEVM,
		NetworkID:  "8453",
		EVMChainID: 8453,
	}, c.ChainInfo())

	c.Close()
}

func TestCall(t *testing.T) {
	ctx := context.Background()
	from := pldtypes.MustEthAddress("0x1d0cd5b99d2e2a380e52b4000377dd507c6df754").ChainAddress()
	to := pldtypes.MustEthAddress("0x2d0cd5b99d2e2a380e52b4000377dd507c6df754").ChainAddress()

	mockEth := ethclientmocks.NewEthClient(t)
	mockEth.On("CallContractNoResolve", ctx, mock.AnythingOfType("*ethsigner.Transaction"), "latest").Return(ethclient.CallResult{
		Data:       pldtypes.HexBytes{0x12, 0x34},
		RevertData: pldtypes.HexBytes{0xab, 0xcd},
	}, nil)

	c := WrapClient(mockEth)
	res, err := c.Call(ctx, &baseledger.CallRequest{
		From:        &from,
		To:          &to,
		PayloadKind: baseledger.PayloadEncodingFunctionCallData,
		Payload:     []byte{0x01, 0x02},
	})
	require.NoError(t, err)
	require.Equal(t, []byte{0x12, 0x34}, res.Data)
	require.Equal(t, []byte{0xab, 0xcd}, res.RevertData)
}

func TestGetAccountInfo(t *testing.T) {
	ctx := context.Background()
	addr := pldtypes.MustEthAddress("0x1d0cd5b99d2e2a380e52b4000377dd507c6df754").ChainAddress()
	ethAddr := pldtypes.MustEthAddress("0x1d0cd5b99d2e2a380e52b4000377dd507c6df754")
	balance := pldtypes.Uint64ToUint256(12345)
	nonce := pldtypes.HexUint64(99)

	mockEth := ethclientmocks.NewEthClient(t)
	mockEth.On("GetBalance", ctx, *ethAddr, "latest").Return(balance, nil)
	mockEth.On("GetTransactionCount", ctx, *ethAddr).Return(&nonce, nil)

	c := WrapClient(mockEth)
	info, err := c.GetAccountInfo(ctx, addr)
	require.NoError(t, err)
	require.Equal(t, addr, info.Address)
	require.Equal(t, balance, info.Balance)
	require.Equal(t, &nonce, info.OrderingKey)
}

func TestEstimateResources(t *testing.T) {
	ctx := context.Background()
	from := pldtypes.MustEthAddress("0x1d0cd5b99d2e2a380e52b4000377dd507c6df754").ChainAddress()
	to := pldtypes.MustEthAddress("0x2d0cd5b99d2e2a380e52b4000377dd507c6df754").ChainAddress()

	mockEth := ethclientmocks.NewEthClient(t)
	mockEth.On("EstimateGasNoResolve", ctx, mock.AnythingOfType("*ethsigner.Transaction")).Return(ethclient.EstimateGasResult{
		GasLimit: pldtypes.HexUint64(456),
	}, nil)

	c := WrapClient(mockEth)
	res, err := c.EstimateResources(ctx, &baseledger.UnsignedChainTx{
		From:        from,
		To:          &to,
		PayloadKind: baseledger.PayloadEncodingFunctionCallData,
		Payload:     []byte{0x01, 0x02},
	})
	require.NoError(t, err)
	require.NotNil(t, res.Gas)
	require.EqualValues(t, 456, *res.Gas)
}

func TestEstimateResourcesReturnsRevertDataOnError(t *testing.T) {
	ctx := context.Background()
	from := pldtypes.MustEthAddress("0x1d0cd5b99d2e2a380e52b4000377dd507c6df754").ChainAddress()
	revertData := []byte{0xaa, 0xbb}

	mockEth := ethclientmocks.NewEthClient(t)
	mockEth.On("EstimateGasNoResolve", ctx, mock.AnythingOfType("*ethsigner.Transaction")).Return(ethclient.EstimateGasResult{
		RevertData: revertData,
	}, errors.New("execution reverted")).Once()

	c := WrapClient(mockEth)
	res, err := c.EstimateResources(ctx, &baseledger.UnsignedChainTx{
		From:        from,
		PayloadKind: baseledger.PayloadEncodingFunctionCallData,
		Payload:     []byte{0x01, 0x02},
	})
	require.EqualError(t, err, "execution reverted")
	require.NotNil(t, res)
	require.Equal(t, revertData, res.RevertData)
	require.Nil(t, res.Gas)
}

func TestBuildTransaction(t *testing.T) {
	ctx := context.Background()
	from := pldtypes.MustEthAddress("0x1d0cd5b99d2e2a380e52b4000377dd507c6df754").ChainAddress()
	to := pldtypes.MustEthAddress("0x2d0cd5b99d2e2a380e52b4000377dd507c6df754").ChainAddress()
	nonce := uint64(7)
	gas := uint64(21000)

	mockEth := ethclientmocks.NewEthClient(t)
	mockEth.On("ChainID").Return(int64(8453))

	c := WrapClient(mockEth)
	payload, err := c.BuildTransaction(ctx, &baseledger.UnsignedChainTx{
		From:        from,
		To:          &to,
		Nonce:       &nonce,
		PayloadKind: baseledger.PayloadEncodingFunctionCallData,
		Payload:     []byte{0x01, 0x02},
	}, &baseledger.ResourceEstimate{Gas: &gas})
	require.NoError(t, err)
	require.Equal(t, baseledger.PayloadEncodingFunctionCallData, payload.PayloadKind)
	require.NotEmpty(t, payload.Payload)
}

func TestBuildTransactionRejectsNonEVMAddress(t *testing.T) {
	from, err := pldtypes.NewStellarAccountAddress("GABC")
	require.NoError(t, err)

	c := WrapClient(ethclientmocks.NewEthClient(t))
	_, err = c.BuildTransaction(context.Background(), &baseledger.UnsignedChainTx{
		From:        from,
		PayloadKind: baseledger.PayloadEncodingFunctionCallData,
	}, &baseledger.ResourceEstimate{})
	require.EqualError(t, err, `chain address kind "stellar_account" is not an EVM address`)
}

func TestSubmit(t *testing.T) {
	ctx := context.Background()
	expected := pldtypes.MustParseBytes32("0x0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	mockEth := ethclientmocks.NewEthClient(t)
	mockEth.On("SendRawTransaction", ctx, pldtypes.HexBytes{0xaa, 0xbb}).Return(&expected, nil)

	c := WrapClient(mockEth)
	txID, err := c.Submit(ctx, baseledger.SignedChainTx{0xaa, 0xbb})
	require.NoError(t, err)
	require.Equal(t, expected, txID)
}

func TestSubmitCalculatesHashWhenClientReturnsNil(t *testing.T) {
	ctx := context.Background()
	expected := pldtypes.MustParseBytes32("0x65b043cdd93fde12ee6629de2d9ce786ba7d5b4c514afecea4d1b4b2c740087c")

	mockEth := ethclientmocks.NewEthClient(t)
	mockEth.On("SendRawTransaction", ctx, pldtypes.HexBytes{0xaa, 0xbb}).Return(nil, nil)

	c := WrapClient(mockEth)
	txID, err := c.Submit(ctx, baseledger.SignedChainTx{0xaa, 0xbb})
	require.NoError(t, err)
	require.Equal(t, expected, txID)
}

func TestGetTransactionResultReturnsNotWiredError(t *testing.T) {
	c := WrapClient(ethclientmocks.NewEthClient(t))
	id := pldtypes.MustParseBytes32("0x0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	_, err := c.GetTransactionResult(context.Background(), id)
	require.EqualError(t, err, "GetTransactionResult(0x0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef) requires the ledger-indexer dependency planned for chapter 11's baseledger.Ingestor split, not yet wired into this client")
}

func TestEVMUnsignedTxToTransaction(t *testing.T) {
	from := pldtypes.MustEthAddress("0x1d0cd5b99d2e2a380e52b4000377dd507c6df754").ChainAddress()
	to := pldtypes.MustEthAddress("0x2d0cd5b99d2e2a380e52b4000377dd507c6df754").ChainAddress()

	tx, err := evmUnsignedTxToTransaction(&baseledger.UnsignedChainTx{
		From:        from,
		To:          &to,
		PayloadKind: baseledger.PayloadEncodingFunctionCallData,
		Payload:     []byte{0x01, 0x02},
	})
	require.NoError(t, err)
	require.JSONEq(t, `"0x1d0cd5b99d2e2a380e52b4000377dd507c6df754"`, string(tx.From))
	require.Equal(t, "0x2d0cd5b99d2e2a380e52b4000377dd507c6df754", tx.To.String())
	require.Equal(t, "0x0102", tx.Data.String())
}

func TestEVMCallRequestToTransactionRejectsUnsupportedPayload(t *testing.T) {
	_, err := evmCallRequestToTransaction(&baseledger.CallRequest{
		PayloadKind: baseledger.PayloadEncodingXDRInvokeContractArgs,
	})
	require.EqualError(t, err, `unsupported EVM payload kind "XDR_INVOKE_CONTRACT_ARGS"`)
}

func TestEVMCallRequestToTransactionRejectsNonEVMAddresses(t *testing.T) {
	from, err := pldtypes.NewStellarAccountAddress("GABC")
	require.NoError(t, err)
	_, err = evmCallRequestToTransaction(&baseledger.CallRequest{
		From:        &from,
		PayloadKind: baseledger.PayloadEncodingFunctionCallData,
	})
	require.EqualError(t, err, `chain address kind "stellar_account" is not an EVM address`)
}

func TestCallRejectsNonEVMToAddress(t *testing.T) {
	to, err := pldtypes.NewStellarAccountAddress("GABC")
	require.NoError(t, err)

	c := WrapClient(ethclientmocks.NewEthClient(t))
	_, err = c.Call(context.Background(), &baseledger.CallRequest{
		To:          &to,
		PayloadKind: baseledger.PayloadEncodingFunctionCallData,
	})
	require.EqualError(t, err, `chain address kind "stellar_account" is not an EVM address`)
}

func TestGetAccountInfoRejectsNonEVMAddress(t *testing.T) {
	addr, err := pldtypes.NewStellarAccountAddress("GABC")
	require.NoError(t, err)

	c := WrapClient(ethclientmocks.NewEthClient(t))
	_, err = c.GetAccountInfo(context.Background(), addr)
	require.EqualError(t, err, `chain address kind "stellar_account" is not an EVM address`)
}

func TestEstimateResourcesRejectsNonEVMToAddress(t *testing.T) {
	from := pldtypes.MustEthAddress("0x1d0cd5b99d2e2a380e52b4000377dd507c6df754").ChainAddress()
	to, err := pldtypes.NewStellarAccountAddress("GABC")
	require.NoError(t, err)

	c := WrapClient(ethclientmocks.NewEthClient(t))
	_, err = c.EstimateResources(context.Background(), &baseledger.UnsignedChainTx{
		From:        from,
		To:          &to,
		PayloadKind: baseledger.PayloadEncodingFunctionCallData,
	})
	require.EqualError(t, err, `chain address kind "stellar_account" is not an EVM address`)
}

func TestEVMCallRequestToTransactionRejectsNonEVMToAddress(t *testing.T) {
	to, err := pldtypes.NewStellarAccountAddress("GABC")
	require.NoError(t, err)

	_, err = evmCallRequestToTransaction(&baseledger.CallRequest{
		To:          &to,
		PayloadKind: baseledger.PayloadEncodingFunctionCallData,
	})
	require.EqualError(t, err, `chain address kind "stellar_account" is not an EVM address`)
}

func TestClientPropagatesUnderlyingErrors(t *testing.T) {
	ctx := context.Background()
	from := pldtypes.MustEthAddress("0x1d0cd5b99d2e2a380e52b4000377dd507c6df754").ChainAddress()
	to := pldtypes.MustEthAddress("0x2d0cd5b99d2e2a380e52b4000377dd507c6df754").ChainAddress()
	ethAddr := pldtypes.MustEthAddress("0x1d0cd5b99d2e2a380e52b4000377dd507c6df754")

	mockEth := ethclientmocks.NewEthClient(t)
	mockEth.On("CallContractNoResolve", ctx, mock.AnythingOfType("*ethsigner.Transaction"), "latest").Return(ethclient.CallResult{}, errors.New("call pop")).Once()
	mockEth.On("GetBalance", ctx, *ethAddr, "latest").Return((*pldtypes.HexUint256)(nil), errors.New("balance pop")).Once()
	mockEth.On("EstimateGasNoResolve", ctx, mock.AnythingOfType("*ethsigner.Transaction")).Return(ethclient.EstimateGasResult{}, errors.New("estimate pop")).Once()
	mockEth.On("SendRawTransaction", ctx, pldtypes.HexBytes{0xcc}).Return((*pldtypes.Bytes32)(nil), errors.New("submit pop")).Once()

	c := WrapClient(mockEth)

	_, err := c.Call(ctx, &baseledger.CallRequest{
		From:        &from,
		To:          &to,
		PayloadKind: baseledger.PayloadEncodingFunctionCallData,
		Payload:     []byte{0x01},
	})
	require.EqualError(t, err, "call pop")

	_, err = c.GetAccountInfo(ctx, from)
	require.EqualError(t, err, "balance pop")

	_, err = c.EstimateResources(ctx, &baseledger.UnsignedChainTx{
		From:        from,
		To:          &to,
		PayloadKind: baseledger.PayloadEncodingFunctionCallData,
		Payload:     []byte{0x02},
	})
	require.EqualError(t, err, "estimate pop")

	_, err = c.Submit(ctx, baseledger.SignedChainTx{0xcc})
	require.EqualError(t, err, "submit pop")
}

func TestCallRequestToTransactionSetsOnlyDataWhenOptionalAddressesOmitted(t *testing.T) {
	tx, err := evmCallRequestToTransaction(&baseledger.CallRequest{
		PayloadKind: baseledger.PayloadEncodingFunctionCallData,
		Payload:     []byte{0x01},
	})
	require.NoError(t, err)
	require.Nil(t, tx.From)
	require.Nil(t, tx.To)
	require.Equal(t, "0x01", tx.Data.String())
}
