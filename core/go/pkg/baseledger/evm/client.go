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
	"strconv"

	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	"github.com/LFDT-Paladin/paladin/core/pkg/ethclient"
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

func (c *Client) EstimateResources(ctx context.Context, tx *baseledger.UnsignedChainTx) (*baseledger.ResourceEstimate, error) {
	ethTX, err := evmUnsignedTxToTransaction(tx)
	if err != nil {
		return nil, err
	}
	estimate, err := c.eth.EstimateGasNoResolve(ctx, ethTX)
	if err != nil {
		return nil, err
	}
	gas := uint64(estimate.GasLimit)
	return &baseledger.ResourceEstimate{Gas: &gas}, nil
}

func (c *Client) BuildTransaction(_ context.Context, _ *baseledger.UnsignedChainTx, _ *baseledger.ResourceEstimate) (baseledger.SignablePayload, error) {
	return baseledger.SignablePayload{}, fmt.Errorf("EVM baseledger BuildTransaction is provided by publictxmgr signing until the ChainSubmitter seam is fully wired")
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

func (c *Client) GetTransactionResult(_ context.Context, id baseledger.TxID) (*baseledger.TxResult, error) {
	return nil, fmt.Errorf("GetTransactionResult(%s) is provided by the ledger indexer in chapter 11", id)
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
