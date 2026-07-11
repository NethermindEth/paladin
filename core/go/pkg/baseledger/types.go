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

package baseledger

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
)

type ChainKind string

const (
	ChainKindEVM     ChainKind = "evm"
	ChainKindStellar ChainKind = "stellar"
)

type PayloadEncoding string

const (
	PayloadEncodingFunctionCallData      PayloadEncoding = "FUNCTION_CALL_DATA"
	PayloadEncodingXDRInvokeContractArgs PayloadEncoding = "XDR_INVOKE_CONTRACT_ARGS"
	// PayloadEncodingXDRClassicOps is a deliberately narrow payload kind (chapter 12 §12.3) for
	// classic Stellar operations needed for native-asset/trustline plumbing - not a gateway to
	// classic-Stellar features generally. Payload is a plain XDR array of xdr.Operation; see
	// baseledger/stellar.DecodeClassicOperations/EncodeClassicOperations for the codec, which
	// only supports CreateAccount/Payment/ChangeTrust/SetTrustLineFlags.
	PayloadEncodingXDRClassicOps PayloadEncoding = "XDR_CLASSIC_OPS"
)

type TxID = pldtypes.Bytes32

type SignedChainTx = pldtypes.HexBytes

type Client interface {
	Close()
	ChainInfo() ChainInfo
	Call(ctx context.Context, req *CallRequest) (*CallResult, error)
	GetAccountInfo(ctx context.Context, addr pldtypes.ChainAddress) (*AccountInfo, error)
	EstimateResources(ctx context.Context, tx *UnsignedChainTx) (*ResourceEstimate, error)
	BuildTransaction(ctx context.Context, tx *UnsignedChainTx, est *ResourceEstimate) (SignablePayload, error)
	Submit(ctx context.Context, raw SignedChainTx) (TxID, error)
	GetTransactionResult(ctx context.Context, id TxID) (*TxResult, error)
}

// GasPricingCapability is implemented by ledgers that can natively answer public gas-pricing queries.
type GasPricingCapability interface {
	DetectZeroGasPrice(ctx context.Context) (bool, error)
	EstimateGasPricing(ctx context.Context, req *GasPricingRequest) (*pldapi.PublicTxGasPricing, error)
}

type ChainInfo struct {
	Kind       ChainKind `json:"kind"`
	NetworkID  string    `json:"networkId"`
	EVMChainID int64     `json:"evmChainId,omitempty"`
}

type CallRequest struct {
	From        *pldtypes.ChainAddress `json:"from,omitempty"`
	To          *pldtypes.ChainAddress `json:"to,omitempty"`
	PayloadKind PayloadEncoding        `json:"payloadKind"`
	Payload     []byte                 `json:"payload"`
}

type CallResult struct {
	Data       []byte `json:"data,omitempty"`
	RevertData []byte `json:"revertData,omitempty"`
}

type AccountInfo struct {
	Address     pldtypes.ChainAddress  `json:"address"`
	Balance     *pldtypes.HexUint256   `json:"balance,omitempty"`
	OrderingKey *pldtypes.HexUint64    `json:"orderingKey,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

type UnsignedChainTx struct {
	From        pldtypes.ChainAddress  `json:"from"`
	To          *pldtypes.ChainAddress `json:"to,omitempty"`
	Nonce       *uint64                `json:"nonce,omitempty"` // evm: required to build a submittable transaction; absent for read-only calls/estimates
	PayloadKind PayloadEncoding        `json:"payloadKind"`
	Payload     []byte                 `json:"payload"`
	Intent      json.RawMessage        `json:"intent,omitempty"`
}

type ResourceEstimate struct {
	Gas        *uint64                    `json:"gas,omitempty"`
	GasPricing *pldapi.PublicTxGasPricing `json:"gasPricing,omitempty"`
	RevertData []byte                     `json:"revertData,omitempty"`
	Soroban    *SorobanResources          `json:"soroban,omitempty"`
}

type GasPricingRequest struct {
	PriorityFeePercentile int `json:"priorityFeePercentile"`
	HistoryBlockCount     int `json:"historyBlockCount"`
	BaseFeeBufferFactor   int `json:"baseFeeBufferFactor"`
}

type EmptyGasPricingDataError struct {
	BaseFeeCount int
	RewardCount  int
}

func (e *EmptyGasPricingDataError) Error() string {
	return fmt.Sprintf("fee history returned empty data: len(baseFeePerGas)=%d, len(reward)=%d", e.BaseFeeCount, e.RewardCount)
}

type NoValidGasPricingTipsError struct{}

func (e *NoValidGasPricingTipsError) Error() string {
	return "no valid tips found in fee history"
}

type SorobanResources struct {
	TransactionDataXDR []byte   `json:"transactionDataXDR,omitempty"`
	ResourceFee        uint64   `json:"resourceFee,omitempty"`
	AuthEntriesXDR     [][]byte `json:"authEntriesXDR,omitempty"`
	RequiresRestore    bool     `json:"requiresRestore,omitempty"`
	// RestorePreambleTransactionDataXDR/RestorePreambleMinResourceFee are populated only when
	// RequiresRestore is true: the xdr.SorobanTransactionData (the footprint of evicted entries)
	// and resource fee simulateTransaction reports are needed - unchanged - to build the
	// standalone RestoreFootprintOp transaction (chapter 12 §12.2's restore-preamble stage).
	RestorePreambleTransactionDataXDR []byte `json:"restorePreambleTransactionDataXDR,omitempty"`
	RestorePreambleMinResourceFee     uint64 `json:"restorePreambleMinResourceFee,omitempty"`
}

type SignablePayload struct {
	PayloadKind PayloadEncoding `json:"payloadKind"`
	Payload     []byte          `json:"payload"`
}

type TxResult struct {
	ID         TxID            `json:"id"`
	Success    bool            `json:"success"`
	RevertData []byte          `json:"revertData,omitempty"`
	Raw        json.RawMessage `json:"raw,omitempty"`
}

type BackfillCapability string

const (
	BackfillNone    BackfillCapability = "none"
	BackfillArchive BackfillCapability = "archive"
	BackfillFull    BackfillCapability = "full"
)

type LedgerCheckpoint struct {
	Sequence uint64           `json:"sequence"`
	Hash     pldtypes.Bytes32 `json:"hash"`
}

// Ingestor and the types below it (LedgerUnit, IndexedChainTx, IndexedChainEvent) are chapter 12
// scaffolding for the planned core/go/internal/ledgerindexer split (chapter 11 §11.2/§11.3):
// intentionally unreferenced elsewhere in the codebase until that refactor lands. Kept here
// (rather than deleted) because chapter 11's own text shows this shape as the target design.
type Ingestor interface {
	StreamLedgers(ctx context.Context, from LedgerCheckpoint) (<-chan *LedgerUnit, error)
	BackfillSource() BackfillCapability
	TipHeight(ctx context.Context) (uint64, error)
}

type LedgerUnit struct {
	Sequence  uint64               `json:"sequence"`
	Hash      pldtypes.Bytes32     `json:"hash"`
	Timestamp pldtypes.Timestamp   `json:"timestamp"`
	Txs       []*IndexedChainTx    `json:"txs,omitempty"`
	Events    []*IndexedChainEvent `json:"events,omitempty"`
}

type IndexedChainTx struct {
	TxID       TxID                  `json:"txId"`
	From       pldtypes.ChainAddress `json:"from"`
	Nonce      uint64                `json:"nonce"` // sequence number for Stellar; nonce for EVM
	Result     string                `json:"result,omitempty"`
	RevertData []byte                `json:"revertData,omitempty"`
	TxIndex    int64                 `json:"txIndex"`
}

type IndexedChainEvent struct {
	Sequence   uint64                `json:"sequence"`
	TxIndex    int64                 `json:"txIndex"`
	EventIndex int64                 `json:"eventIndex"`
	Emitter    pldtypes.ChainAddress `json:"emitter"`
	Selector   pldtypes.Bytes32      `json:"selector"`
	Topics     [][]byte              `json:"topics,omitempty"`
	Data       []byte                `json:"data,omitempty"`
}
