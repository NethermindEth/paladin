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

package stellar

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/LFDT-Paladin/paladin/core/pkg/baseledger"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	protocol "github.com/stellar/go-stellar-sdk/protocols/rpc"
	"github.com/stellar/go-stellar-sdk/txnbuild"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// rpcClient is the minimal subset of *rpcclient.Client (github.com/stellar/go-stellar-sdk/clients/rpcclient)
// this package calls. rpcclient.Client is a concrete SDK struct, not an interface, so this local
// interface lets tests supply a small fake without needing an adapter - the real client
// satisfies it structurally.
type rpcClient interface {
	SimulateTransaction(ctx context.Context, req protocol.SimulateTransactionRequest) (protocol.SimulateTransactionResponse, error)
	SendTransaction(ctx context.Context, req protocol.SendTransactionRequest) (protocol.SendTransactionResponse, error)
	GetTransaction(ctx context.Context, req protocol.GetTransactionRequest) (protocol.GetTransactionResponse, error)
	LoadAccount(ctx context.Context, address string) (txnbuild.Account, error)
	GetLedgerEntries(ctx context.Context, req protocol.GetLedgerEntriesRequest) (protocol.GetLedgerEntriesResponse, error)
}

// Client implements baseledger.Client for Stellar/Soroban. Unlike the EVM client,
// GetTransactionResult is implemented for real: getTransaction is a direct hash lookup with no
// local block-indexer dependency (chapter 12's design note on this point).
type Client struct {
	rpc               rpcClient
	networkPassphrase string
	closeFn           func()
}

// WrapClient builds a baseledger.Client backed by the Stellar SDK's own RPC client, as
// constructed by stellarclient.NewClient. closeFn (also returned by stellarclient.NewClient)
// releases the underlying HTTP transport and may be nil.
func WrapClient(rpc rpcClient, networkPassphrase string, closeFn func()) *Client {
	return &Client{rpc: rpc, networkPassphrase: networkPassphrase, closeFn: closeFn}
}

func (c *Client) Close() {
	if c.closeFn != nil {
		c.closeFn()
	}
}

func (c *Client) ChainInfo() baseledger.ChainInfo {
	return baseledger.ChainInfo{
		Kind:      baseledger.ChainKindStellar,
		NetworkID: c.networkPassphrase,
	}
}

// buildTransaction decodes payload according to payloadKind and wraps the result in a transaction
// sourced from `from`'s current sequence number - the "build" half shared by Call,
// EstimateResources, and BuildTransaction. Two payload kinds are supported: an XDR-encoded
// xdr.HostFunction (PayloadEncodingXDRInvokeContractArgs, a single Soroban invocation) or a plain
// XDR array of classic operations (PayloadEncodingXDRClassicOps, chapter 12 §12.3 - see
// classic_ops.go).
func (c *Client) buildTransaction(ctx context.Context, from *pldtypes.ChainAddress, payloadKind baseledger.PayloadEncoding, payload []byte) (*txnbuild.Transaction, error) {
	if from == nil {
		return nil, fmt.Errorf("a source account (from) is required to build a stellar transaction")
	}
	fromAddr := from.String()
	var ops []txnbuild.Operation
	switch payloadKind {
	case baseledger.PayloadEncodingXDRInvokeContractArgs:
		var hostFunction xdr.HostFunction
		if err := hostFunction.UnmarshalBinary(payload); err != nil {
			return nil, fmt.Errorf("invalid host function payload: %w", err)
		}
		ops = []txnbuild.Operation{&txnbuild.InvokeHostFunction{
			HostFunction:  hostFunction,
			SourceAccount: fromAddr,
		}}
	case baseledger.PayloadEncodingXDRClassicOps:
		classicOps, err := DecodeClassicOperations(payload)
		if err != nil {
			return nil, err
		}
		ops = classicOps
	default:
		return nil, fmt.Errorf("unsupported stellar payload kind %q", payloadKind)
	}
	account, err := c.rpc.LoadAccount(ctx, fromAddr)
	if err != nil {
		return nil, err
	}
	return txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        account,
		IncrementSequenceNum: true,
		Operations:           ops,
		BaseFee:              txnbuild.MinBaseFee,
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(300)},
	})
}

func (c *Client) Call(ctx context.Context, req *baseledger.CallRequest) (*baseledger.CallResult, error) {
	if req.PayloadKind == baseledger.PayloadEncodingXDRClassicOps {
		// Classic operations (ChangeTrust, Payment, ...) have no return value to simulate - "call"
		// is a Soroban-invocation-only concept here.
		return nil, fmt.Errorf("classic operations do not support Call - only %q", baseledger.PayloadEncodingXDRInvokeContractArgs)
	}
	transaction, err := c.buildTransaction(ctx, req.From, req.PayloadKind, req.Payload)
	if err != nil {
		return nil, err
	}
	txBase64, err := transaction.Base64()
	if err != nil {
		return nil, err
	}
	resp, err := c.rpc.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{Transaction: txBase64})
	if err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("simulateTransaction failed: %s", resp.Error)
	}
	result := &baseledger.CallResult{}
	if len(resp.Results) > 0 && resp.Results[0].ReturnValueXDR != nil {
		data, err := base64.StdEncoding.DecodeString(*resp.Results[0].ReturnValueXDR)
		if err != nil {
			return nil, fmt.Errorf("invalid simulateTransaction return value: %w", err)
		}
		result.Data = data
	}
	return result, nil
}

func (c *Client) GetAccountInfo(ctx context.Context, addr pldtypes.ChainAddress) (*baseledger.AccountInfo, error) {
	account, err := c.rpc.LoadAccount(ctx, addr.String())
	if err != nil {
		return nil, err
	}
	sequence, err := account.GetSequenceNumber()
	if err != nil {
		return nil, err
	}
	nextSequence := pldtypes.HexUint64(sequence + 1) //nolint:gosec // sequence numbers are always positive
	return &baseledger.AccountInfo{
		Address:     addr,
		OrderingKey: &nextSequence,
		// Balance is not populated in this slice: the txnbuild.Account interface LoadAccount
		// returns does not expose it, and fetching it would require a separate getLedgerEntries
		// call decoding the classic AccountEntry XDR - deferred alongside trustline support
		// (chapter 12 §12.3).
	}, nil
}

func (c *Client) EstimateResources(ctx context.Context, tx *baseledger.UnsignedChainTx) (*baseledger.ResourceEstimate, error) {
	if tx.PayloadKind == baseledger.PayloadEncodingXDRClassicOps {
		// Classic operations have no footprint/resource fee to simulate (chapter 12 §12.3) - the
		// classic per-operation base fee (txnbuild.MinBaseFee, buildTransaction's default) is all
		// that's needed, so there's nothing to estimate beyond an empty ResourceEstimate.
		return &baseledger.ResourceEstimate{}, nil
	}
	transaction, err := c.buildTransaction(ctx, &tx.From, tx.PayloadKind, tx.Payload)
	if err != nil {
		return nil, err
	}
	txBase64, err := transaction.Base64()
	if err != nil {
		return nil, err
	}
	resp, err := c.rpc.SimulateTransaction(ctx, protocol.SimulateTransactionRequest{Transaction: txBase64})
	if err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("simulateTransaction failed: %s", resp.Error)
	}
	soroban := &baseledger.SorobanResources{
		ResourceFee:     uint64(resp.MinResourceFee), //nolint:gosec // fees are always positive
		RequiresRestore: resp.RestorePreamble != nil,
	}
	if resp.TransactionDataXDR != "" {
		data, err := base64.StdEncoding.DecodeString(resp.TransactionDataXDR)
		if err != nil {
			return nil, fmt.Errorf("invalid simulateTransaction transactionData: %w", err)
		}
		soroban.TransactionDataXDR = data
	}
	if len(resp.Results) > 0 && resp.Results[0].AuthXDR != nil {
		soroban.AuthEntriesXDR = make([][]byte, len(*resp.Results[0].AuthXDR))
		for i, authXDR := range *resp.Results[0].AuthXDR {
			auth, err := base64.StdEncoding.DecodeString(authXDR)
			if err != nil {
				return nil, fmt.Errorf("invalid simulateTransaction auth entry %d: %w", i, err)
			}
			soroban.AuthEntriesXDR[i] = auth
		}
	}
	return &baseledger.ResourceEstimate{Soroban: soroban}, nil
}

// BuildTransaction returns the transaction's network-ID-qualified signature payload
// (txnbuild.Transaction.Hash) as a SignablePayload. Note this exists as an independently correct
// capability of the Client interface, mirroring the EVM client's own BuildTransaction doc note:
// publictxmgr's Stellar ChainSubmitter builds and signs directly (its own buildTransaction call,
// sharing this same logic) rather than round-tripping through this method for submission, so
// that it can attach the resulting signature to the *same* in-memory transaction object rather
// than reconstructing it from an opaque hash.
func (c *Client) BuildTransaction(ctx context.Context, tx *baseledger.UnsignedChainTx, _ *baseledger.ResourceEstimate) (baseledger.SignablePayload, error) {
	transaction, err := c.buildTransaction(ctx, &tx.From, tx.PayloadKind, tx.Payload)
	if err != nil {
		return baseledger.SignablePayload{}, err
	}
	hash, err := transaction.Hash(c.networkPassphrase)
	if err != nil {
		return baseledger.SignablePayload{}, err
	}
	return baseledger.SignablePayload{
		PayloadKind: tx.PayloadKind,
		Payload:     hash[:],
	}, nil
}

// sendTransactionStatusError is SendTransactionResponse.Status's rejection value. Unlike
// GetTransactionResponse.Status (TransactionStatusSuccess/Failed/NotFound, exported constants in
// protocols/rpc), the SDK does not export named constants for SendTransactionResponse.Status -
// its doc comment references proto.TXStatusError/Pending/Duplicate/TryAgainLater, but no such
// identifiers exist in this package (verified against go-stellar-sdk v0.6.0 source and its own
// tests, which compare against the plain string literals below).
const (
	sendTransactionStatusError = "ERROR"
)

// SubmissionRejectedError is returned by Submit when stellar-rpc accepts the JSON-RPC call but
// reports the transaction itself was rejected (Status == sendTransactionStatusError). It carries
// the raw ErrorResultXDR so callers (the Stellar ChainSubmitter) can decode the specific
// xdr.TransactionResultCode for classification - the direct analogue of how ethclient.MapError
// classifies EVM JSON-RPC error text, but structured rather than string-matched.
type SubmissionRejectedError struct {
	Status         string
	ErrorResultXDR string
}

func (e *SubmissionRejectedError) Error() string {
	return fmt.Sprintf("stellar transaction submission rejected: status=%s", e.Status)
}

func (c *Client) Submit(ctx context.Context, raw baseledger.SignedChainTx) (baseledger.TxID, error) {
	resp, err := c.rpc.SendTransaction(ctx, protocol.SendTransactionRequest{
		Transaction: base64.StdEncoding.EncodeToString(raw),
	})
	if err != nil {
		return baseledger.TxID{}, err
	}
	if resp.Status == sendTransactionStatusError {
		var txHash baseledger.TxID
		if parsed, parseErr := pldtypes.ParseBytes32(resp.Hash); parseErr == nil {
			txHash = parsed
		}
		return txHash, &SubmissionRejectedError{Status: resp.Status, ErrorResultXDR: resp.ErrorResultXDR}
	}
	txHash, err := pldtypes.ParseBytes32(resp.Hash)
	if err != nil {
		return baseledger.TxID{}, fmt.Errorf("invalid transaction hash returned by sendTransaction: %w", err)
	}
	return txHash, nil
}

func (c *Client) GetTransactionResult(ctx context.Context, id baseledger.TxID) (*baseledger.TxResult, error) {
	resp, err := c.rpc.GetTransaction(ctx, protocol.GetTransactionRequest{Hash: id.HexString()})
	if err != nil {
		return nil, err
	}
	switch resp.Status {
	case protocol.TransactionStatusNotFound:
		return nil, fmt.Errorf("transaction %s not found (or outside the RPC retention window)", id)
	case protocol.TransactionStatusSuccess:
		return &baseledger.TxResult{ID: id, Success: true}, nil
	default: // TransactionStatusFailed
		result := &baseledger.TxResult{ID: id, Success: false}
		if resp.ResultXDR != "" {
			if data, decErr := base64.StdEncoding.DecodeString(resp.ResultXDR); decErr == nil {
				result.RevertData = data
			}
		}
		return result, nil
	}
}
