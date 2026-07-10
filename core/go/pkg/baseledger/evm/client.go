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

package evm

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"

	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	"github.com/LFDT-Paladin/paladin/core/pkg/ethclient"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/hyperledger/firefly-signer/pkg/ethsigner"
	"github.com/hyperledger/firefly-signer/pkg/ethtypes"
	"golang.org/x/crypto/sha3"
)

type Client struct {
	eth ethclient.EthClient
}

func WrapClient(eth ethclient.EthClient) *Client {
	return &Client{eth: eth}
}

func (c *Client) Close() {
	c.eth.Close()
}

func (c *Client) ChainInfo() baseledger.ChainInfo {
	chainID := c.eth.ChainID()
	return baseledger.ChainInfo{
		Kind:       baseledger.ChainKindEVM,
		NetworkID:  strconv.FormatInt(chainID, 10),
		EVMChainID: chainID,
	}
}

func (c *Client) Call(ctx context.Context, req *baseledger.CallRequest) (*baseledger.CallResult, error) {
	tx, err := evmCallRequestToTransaction(req)
	if err != nil {
		return nil, err
	}
	res, err := c.eth.CallContractNoResolve(ctx, tx, "latest")
	if err != nil {
		return nil, err
	}
	return &baseledger.CallResult{
		Data:       res.Data,
		RevertData: res.RevertData,
	}, nil
}

func (c *Client) GetAccountInfo(ctx context.Context, addr pldtypes.ChainAddress) (*baseledger.AccountInfo, error) {
	ethAddr, err := addr.EthAddress()
	if err != nil {
		return nil, err
	}
	balance, err := c.eth.GetBalance(ctx, *ethAddr, "latest")
	if err != nil {
		return nil, err
	}
	nonce, err := c.eth.GetTransactionCount(ctx, *ethAddr)
	if err != nil {
		return nil, err
	}
	return &baseledger.AccountInfo{
		Address:     addr,
		Balance:     balance,
		OrderingKey: nonce,
	}, nil
}

func (c *Client) DetectZeroGasPrice(ctx context.Context) (bool, error) {
	gasPrice, err := c.eth.GasPrice(ctx)
	if err != nil {
		return false, err
	}
	if gasPrice == nil {
		return false, nil
	}
	return gasPrice.Int().Sign() == 0, nil
}

func (c *Client) EstimateGasPricing(ctx context.Context, req *baseledger.GasPricingRequest) (*pldapi.PublicTxGasPricing, error) {
	feeHistory, err := c.eth.FeeHistory(ctx, req.HistoryBlockCount, "latest", []float64{float64(req.PriorityFeePercentile)})
	if err != nil {
		return nil, err
	}
	if len(feeHistory.BaseFeePerGas) == 0 || len(feeHistory.Reward) == 0 {
		return nil, &baseledger.EmptyGasPricingDataError{BaseFeeCount: len(feeHistory.BaseFeePerGas), RewardCount: len(feeHistory.Reward)}
	}
	tips := make([]*big.Int, 0, len(feeHistory.Reward))
	for _, blockRewards := range feeHistory.Reward {
		if len(blockRewards) > 0 {
			tips = append(tips, blockRewards[0].Int())
		}
	}
	if len(tips) == 0 {
		return nil, &baseledger.NoValidGasPricingTipsError{}
	}
	maxPriorityFeePerGas := new(big.Int).Set(tips[0])
	for _, tip := range tips[1:] {
		if tip.Cmp(maxPriorityFeePerGas) > 0 {
			maxPriorityFeePerGas = tip
		}
	}
	nextBlockBaseFee := feeHistory.BaseFeePerGas[len(feeHistory.BaseFeePerGas)-1].Int()
	bufferedBaseFee := new(big.Int).Mul(nextBlockBaseFee, big.NewInt(int64(req.BaseFeeBufferFactor)))
	maxFeePerGas := new(big.Int).Add(bufferedBaseFee, maxPriorityFeePerGas)
	return &pldapi.PublicTxGasPricing{
		MaxFeePerGas:         (*pldtypes.HexUint256)(maxFeePerGas),
		MaxPriorityFeePerGas: (*pldtypes.HexUint256)(maxPriorityFeePerGas),
	}, nil
}

func (c *Client) EstimateResources(ctx context.Context, tx *baseledger.UnsignedChainTx) (*baseledger.ResourceEstimate, error) {
	ethTX, err := evmUnsignedTxToTransaction(tx)
	if err != nil {
		return nil, err
	}
	estimate, err := c.eth.EstimateGasNoResolve(ctx, ethTX)
	res := &baseledger.ResourceEstimate{
		RevertData: estimate.RevertData,
	}
	if estimate.GasLimit > 0 {
		gas := uint64(estimate.GasLimit)
		res.Gas = &gas
	}
	if err != nil {
		return res, err
	}
	return res, nil
}

// BuildTransaction returns the EIP-1559 signature payload for an unsigned transaction plus its
// resource estimate. Note this returns the bytes-to-sign, not a finalized raw transaction: EIP-1559
// finalization (ethsigner.Transaction.FinalizeEIP1559WithSignature) needs the live unsigned-transaction
// object alongside its derived signature payload, which this chain-neutral opaque-bytes return type
// cannot carry. publictxmgr's ChainSubmitter therefore builds and finalizes directly (transaction_submission
// design, chapter 11) rather than round-tripping through this method for submission; this method exists
// as an independently correct capability of the Client interface (e.g. for external/offline signing flows).
func (c *Client) BuildTransaction(_ context.Context, tx *baseledger.UnsignedChainTx, est *baseledger.ResourceEstimate) (baseledger.SignablePayload, error) {
	ethTx, err := evmUnsignedTxToTransaction(tx)
	if err != nil {
		return baseledger.SignablePayload{}, err
	}
	if tx.Nonce != nil {
		ethTx.Nonce = ethtypes.NewHexIntegerU64(*tx.Nonce)
	}
	if est != nil {
		if est.Gas != nil {
			ethTx.GasLimit = ethtypes.NewHexIntegerU64(*est.Gas)
		}
		if est.GasPricing != nil {
			ethTx.MaxFeePerGas = (*ethtypes.HexInteger)(est.GasPricing.MaxFeePerGas)
			ethTx.MaxPriorityFeePerGas = (*ethtypes.HexInteger)(est.GasPricing.MaxPriorityFeePerGas)
		}
	}
	sigPayload := ethTx.SignaturePayloadEIP1559(c.ChainInfo().EVMChainID)
	return baseledger.SignablePayload{
		PayloadKind: baseledger.PayloadEncodingFunctionCallData,
		Payload:     sigPayload.Bytes(),
	}, nil
}

func (c *Client) Submit(ctx context.Context, raw baseledger.SignedChainTx) (baseledger.TxID, error) {
	txHash, err := c.eth.SendRawTransaction(ctx, pldtypes.HexBytes(raw))
	if err != nil {
		return pldtypes.Bytes32{}, err
	}
	if txHash == nil {
		return hashSignedTransaction(raw), nil
	}
	return *txHash, nil
}

func hashSignedTransaction(raw baseledger.SignedChainTx) pldtypes.Bytes32 {
	msgHash := sha3.NewLegacyKeccak256()
	msgHash.Write(raw)
	return pldtypes.MustParseBytes32(hex.EncodeToString(msgHash.Sum(nil)))
}

// GetTransactionResult requires a block-indexer dependency that is not yet wired into the EVM
// baseledger client: correlating a transaction hash to its on-chain result is the job of the
// chain-neutral ledger indexer split described in chapter 11 §11.3 ("core/go/internal/ledgerindexer"),
// which has not yet been implemented. Deferred rather than stubbed with a fake block-indexer wiring
// here, to avoid threading a blockindexer.BlockIndexer dependency through every WrapClient call site
// ahead of that refactor actually landing.
func (c *Client) GetTransactionResult(_ context.Context, id baseledger.TxID) (*baseledger.TxResult, error) {
	return nil, fmt.Errorf("GetTransactionResult(%s) requires the ledger-indexer dependency planned for chapter 11's baseledger.Ingestor split, not yet wired into this client", id)
}

func evmCallRequestToTransaction(req *baseledger.CallRequest) (*ethsigner.Transaction, error) {
	if req.PayloadKind != baseledger.PayloadEncodingFunctionCallData {
		return nil, fmt.Errorf("unsupported EVM payload kind %q", req.PayloadKind)
	}
	tx := &ethsigner.Transaction{
		Data: ethtypes.HexBytes0xPrefix(req.Payload),
	}
	if req.From != nil {
		from, err := req.From.EthAddress()
		if err != nil {
			return nil, err
		}
		tx.From = json.RawMessage(pldtypes.JSONString(from))
	}
	if req.To != nil {
		to, err := req.To.EthAddress()
		if err != nil {
			return nil, err
		}
		tx.To = to.Address0xHex()
	}
	return tx, nil
}

func evmUnsignedTxToTransaction(tx *baseledger.UnsignedChainTx) (*ethsigner.Transaction, error) {
	req := &baseledger.CallRequest{
		From:        &tx.From,
		To:          tx.To,
		PayloadKind: tx.PayloadKind,
		Payload:     tx.Payload,
	}
	return evmCallRequestToTransaction(req)
}
