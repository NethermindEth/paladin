// Copyright © 2024 Kaleido, Inc.
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

package pldapi

import (
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/google/uuid"
)

// These are user-supplied directly on the external interface (vs. calculated)
// If set these affect the submission of the public transaction.
// All are optional
type PublicTxOptions struct {
	Gas                *pldtypes.HexUint64  `docstruct:"PublicTxOptions" json:"gas,omitempty"`
	Value              *pldtypes.HexUint256 `docstruct:"PublicTxOptions" json:"value,omitempty"`
	PublicTxGasPricing                      // fixed when any of these are supplied - disabling the gas pricing engine for this TX
}

type PublicCallOptions struct {
	Block pldtypes.HexUint64OrString `docstruct:"PublicCallOptions" json:"block,omitempty"` // a number, or special strings like "latest"
}

type PublicTxGasPricing struct {
	MaxPriorityFeePerGas *pldtypes.HexUint256 `docstruct:"PublicTxGasPricing" json:"maxPriorityFeePerGas,omitempty"`
	MaxFeePerGas         *pldtypes.HexUint256 `docstruct:"PublicTxGasPricing" json:"maxFeePerGas,omitempty"`
}

// PublicTxPayloadKind distinguishes the shape of PublicTxInput/PublicTx.Data across base ledger
// chain kinds. EVM has exactly one kind (calldata); Stellar has two (a Soroban host-function
// invocation, or a plain XDR array of classic operations for native-asset/trustline plumbing,
// chapter 12 §12.3 of the Saladin port). Empty means the base ledger's implicit default kind
// (FUNCTION_CALL_DATA for EVM, XDR_INVOKE_CONTRACT_ARGS for Stellar) - matches the exact string
// values of core/go/pkg/baseledger.PayloadEncoding, which callers convert to/from at the
// publictxmgr boundary.
type PublicTxPayloadKind string

const (
	PublicTxPayloadKindFunctionCallData      PublicTxPayloadKind = "FUNCTION_CALL_DATA"
	PublicTxPayloadKindXDRInvokeContractArgs PublicTxPayloadKind = "XDR_INVOKE_CONTRACT_ARGS"
	PublicTxPayloadKindXDRClassicOps         PublicTxPayloadKind = "XDR_CLASSIC_OPS"
)

func (k PublicTxPayloadKind) Enum() pldtypes.Enum[PublicTxPayloadKind] {
	return pldtypes.Enum[PublicTxPayloadKind](k)
}

func (k PublicTxPayloadKind) Options() []string {
	return []string{
		string(PublicTxPayloadKindFunctionCallData),
		string(PublicTxPayloadKindXDRInvokeContractArgs),
		string(PublicTxPayloadKindXDRClassicOps),
	}
}

// Default lets pldtypes.Enum.Validate() (called by both the gorm driver.Valuer on write and the
// sql.Scanner on read) accept and round-trip an empty PayloadKind rather than rejecting it - "" is
// itself a legitimate, meaningful persisted value here (the base ledger's implicit default kind),
// not a placeholder for one specific kind, so it must not be coerced into one of the named
// constants above.
func (k PublicTxPayloadKind) Default() string {
	return ""
}

type PublicTxInput struct {
	From        *pldtypes.EthAddress               `docstruct:"PublicTxInput" json:"from"`                  // resolved signing account
	To          *pldtypes.EthAddress               `docstruct:"PublicTxInput" json:"to,omitempty"`          // target contract address, or nil for deploy
	Data        pldtypes.HexBytes                  `docstruct:"PublicTxInput" json:"data,omitempty"`        // the pre-encoded calldata
	PayloadKind pldtypes.Enum[PublicTxPayloadKind] `docstruct:"PublicTxInput" json:"payloadKind,omitempty"` // empty means the base ledger's implicit default kind
	PublicTxOptions
}

type PublicTxSubmission struct {
	From  pldtypes.EthAddress `docstruct:"PublicTxSubmission" json:"from"`
	Nonce pldtypes.HexUint64  `docstruct:"PublicTxSubmission" json:"nonce"`
	PublicTxSubmissionData
}

type PublicTxSubmissionData struct {
	Time            pldtypes.Timestamp `docstruct:"PublicTxSubmissionData" json:"time"`
	TransactionHash pldtypes.Bytes32   `docstruct:"PublicTxSubmissionData" json:"transactionHash"`
	PublicTxGasPricing
}

type PublicTx struct {
	LocalID         *uint64                            `docstruct:"PublicTx" json:"localId,omitempty"` // only a local DB identifier for the public transaction. Not directly related to nonce order
	To              *pldtypes.EthAddress               `docstruct:"PublicTx" json:"to,omitempty"`
	Data            pldtypes.HexBytes                  `docstruct:"PublicTx" json:"data,omitempty"`
	PayloadKind     pldtypes.Enum[PublicTxPayloadKind] `docstruct:"PublicTx" json:"payloadKind,omitempty"` // empty means the base ledger's implicit default kind
	From            pldtypes.EthAddress                `docstruct:"PublicTx" json:"from"`
	Nonce           *pldtypes.HexUint64                `docstruct:"PublicTx" json:"nonce"`
	Created         pldtypes.Timestamp                 `docstruct:"PublicTx" json:"created"`
	Dispatcher      string                             `docstruct:"PublicTx" json:"dispatcher"`
	CompletedAt     *pldtypes.Timestamp                `docstruct:"PublicTx" json:"completedAt,omitempty"` // only once confirmed
	TransactionHash *pldtypes.Bytes32                  `docstruct:"PublicTx" json:"transactionHash"`       // only once confirmed
	Success         *bool                              `docstruct:"PublicTx" json:"success,omitempty"`     // only once confirmed
	RevertData      pldtypes.HexBytes                  `docstruct:"PublicTx" json:"revertData,omitempty"`  // only once confirmed, if available
	Submissions     []*PublicTxSubmissionData          `docstruct:"PublicTx" json:"submissions,omitempty"`
	Activity        []TransactionActivityRecord        `docstruct:"PublicTx" json:"activity,omitempty"`
	PublicTxOptions
}

type PublicTxBinding struct {
	Transaction                uuid.UUID                      `docstruct:"PublicTxBinding" json:"transaction"`
	TransactionType            pldtypes.Enum[TransactionType] `docstruct:"PublicTxBinding" json:"transactionType"`
	TransactionSender          string                         `docstruct:"PublicTxBinding" json:"sender,omitempty"`
	TransactionContractAddress string                         `docstruct:"PublicTxBinding" json:"contractAddress,omitempty"`
}
type PublicTxWithBinding struct {
	*PublicTx
	PublicTxBinding
}
