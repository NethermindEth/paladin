/*
 * Copyright © 2024 Kaleido, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with
 * the License. You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on
 * an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the
 * specific language governing permissions and limitations under the License.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package types

import (
	_ "embed"

	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/solutils"
	"github.com/hyperledger/firefly-signer/pkg/abi"
)

//go:embed abis/INotoPrivate.json
var notoPrivateJSON []byte

//go:embed abis/INotoPrivate_V0.json
var notoV0PrivateJSON []byte

var NotoABI = solutils.MustParseBuildABI(notoPrivateJSON)
var NotoV0ABI = solutils.MustParseBuildABI(notoV0PrivateJSON)

// NotoStellarOnlyABI covers deposit/withdraw (SNoto's real SAC shield/unshield gateway, ch.14
// §14.1/ch.13 §13.6) - hand-written rather than embedded from a compiled Solidity artifact like
// NotoABI/NotoV0ABI above, since these two functions have no EVM equivalent at all (confirmed:
// no INotoPrivate.sol interface entry exists, and none should - adding one would require Noto.sol
// itself to implement them as EVM stubs purely to satisfy Solidity's interface-conformance rules,
// pulling the Solidity/Hardhat build pipeline into a change that only ever matters for Stellar).
// Checked as a second lookup source in noto.go's validateTransactionCommon, alongside NotoABI -
// see that function's own doc comment for why a function must appear in *some* ABI to dispatch at
// all, regardless of chain.
var NotoStellarOnlyABI = abi.ABI{
	{
		Type: abi.Function,
		Name: "deposit",
		Inputs: abi.ParameterArray{
			{Name: "from", Type: "string"},
			{Name: "amount", Type: "uint256"},
			{Name: "data", Type: "bytes"},
		},
		Outputs: abi.ParameterArray{},
	},
	{
		Type: abi.Function,
		Name: "withdraw",
		Inputs: abi.ParameterArray{
			{Name: "recipient", Type: "string"},
			{Name: "amount", Type: "uint256"},
			{Name: "data", Type: "bytes"},
		},
		Outputs: abi.ParameterArray{},
	},
}

var NotoABIFunctionsBySolSignature = abiFunctionsBySolSignature(NotoV0ABI, NotoABI, NotoStellarOnlyABI)

type ConstructorParams struct {
	Name           string      `json:"name,omitempty"`           // Name of the token
	Symbol         string      `json:"symbol,omitempty"`         // Symbol of the token
	Notary         string      `json:"notary"`                   // Lookup string for the notary identity
	NotaryMode     NotaryMode  `json:"notaryMode"`               // Notary mode (basic or hooks)
	Implementation string      `json:"implementation,omitempty"` // Use a specific implementation of Noto that was registered to the factory (blank to use default)
	Options        NotoOptions `json:"options"`                  // Configure options for the chosen notary mode
}

type NotaryMode string

const (
	NotaryModeBasic NotaryMode = "basic"
	NotaryModeHooks NotaryMode = "hooks"
)

func abiFunctionsBySolSignature(abis ...abi.ABI) map[string]*abi.Entry {
	bySignature := make(map[string]*abi.Entry)
	for _, a := range abis {
		for _, entry := range a {
			if entry.Type == abi.Function {
				bySignature[entry.SolString()] = entry
			}
		}
	}
	return bySignature
}

func (tt NotaryMode) Enum() pldtypes.Enum[NotaryMode] {
	return pldtypes.Enum[NotaryMode](tt)
}

func (tt NotaryMode) Options() []string {
	return []string{
		string(NotaryModeBasic),
		string(NotaryModeHooks),
	}
}

type MintParams struct {
	To     string               `json:"to"`
	Amount *pldtypes.HexUint256 `json:"amount"`
	Data   pldtypes.HexBytes    `json:"data"`
}

type BurnParams struct {
	Amount *pldtypes.HexUint256 `json:"amount"`
	Data   pldtypes.HexBytes    `json:"data"`
}

type BurnFromParams struct {
	From   string               `json:"from"`
	Amount *pldtypes.HexUint256 `json:"amount"`
	Data   pldtypes.HexBytes    `json:"data"`
}

type TransferParams struct {
	To     string               `json:"to"`
	Amount *pldtypes.HexUint256 `json:"amount"`
	Data   pldtypes.HexBytes    `json:"data"`
}

type TransferFromParams struct {
	From   string               `json:"from"`
	To     string               `json:"to"`
	Amount *pldtypes.HexUint256 `json:"amount"`
	Data   pldtypes.HexBytes    `json:"data"`
}

type ApproveParams struct {
	Inputs   []*pldapi.StateEncoded `json:"inputs"`
	Outputs  []*pldapi.StateEncoded `json:"outputs"`
	Data     pldtypes.HexBytes      `json:"data"`
	Delegate *pldtypes.EthAddress   `json:"delegate"`
}

type LockParams struct {
	Amount *pldtypes.HexUint256 `json:"amount"`
	Data   pldtypes.HexBytes    `json:"data"`
}

// DepositParams/WithdrawParams are Stellar-only (chapter 14 §14.1 / SNoto's real SAC shield/
// unshield, soroban/contracts/snoto/src/lib.rs's deposit/withdraw) - no EVM equivalent exists,
// unlike every other params struct in this file.
type DepositParams struct {
	From   string               `json:"from"` // chain-neutral lookup: real on-chain SAC source AND off-chain recipient of the new outputs
	Amount *pldtypes.HexUint256 `json:"amount"`
	Data   pldtypes.HexBytes    `json:"data"`
}

type WithdrawParams struct {
	Recipient string               `json:"recipient"` // chain-neutral lookup: real on-chain SAC destination
	Amount    *pldtypes.HexUint256 `json:"amount"`
	Data      pldtypes.HexBytes    `json:"data"`
}

type PrepareUnlockParams struct {
	UnlockParams
	UnlockData pldtypes.HexBytes `json:"unlockData"`
	Data       pldtypes.HexBytes `json:"data"`
}

type UnlockParams struct {
	LockID     pldtypes.Bytes32   `json:"lockId"`
	From       string             `json:"from"`
	Recipients []*UnlockRecipient `json:"recipients"`
	Data       pldtypes.HexBytes  `json:"data"`
}

type CreateTransferLockParams struct {
	From       string             `json:"from"`
	Recipients []*UnlockRecipient `json:"recipients"`
	UnlockData pldtypes.HexBytes  `json:"unlockData"`
	Data       pldtypes.HexBytes  `json:"data"`
}

type CreateMintLockParams struct {
	Recipients []*UnlockRecipient `json:"recipients"`
	UnlockData pldtypes.HexBytes  `json:"unlockData"`
	Data       pldtypes.HexBytes  `json:"data"`
}

type CreateBurnLockParams struct {
	From       string               `json:"from"`
	Amount     *pldtypes.HexUint256 `json:"amount"`
	UnlockData pldtypes.HexBytes    `json:"unlockData"`
	Data       pldtypes.HexBytes    `json:"data"`
}

type PrepareMintUnlockParams struct {
	LockID     pldtypes.Bytes32   `json:"lockId"`
	Recipients []*UnlockRecipient `json:"recipients"`
	UnlockData pldtypes.HexBytes  `json:"unlockData"`
	Data       pldtypes.HexBytes  `json:"data"`
}

type PrepareBurnUnlockParams struct {
	LockID     pldtypes.Bytes32     `json:"lockId"`
	From       string               `json:"from"`
	Amount     *pldtypes.HexUint256 `json:"amount"`
	UnlockData pldtypes.HexBytes    `json:"unlockData"`
	Data       pldtypes.HexBytes    `json:"data"`
}

// CancelLockParams is the chain-neutral request to invoke a lock's already-committed cancel path
// (EVM's cancelLock/Stellar's cancel_unlock) - not to be confused with the low-level per-chain ABI
// wrapper struct of the same name in the noto package (mirrors SpendLockParams). Unlike
// UnlockParams there is no Recipients list: the cancel outputs were already fixed (returning the
// full locked amount to the lock's original owner) when the lock was created or prepared - see
// handler_cancel_lock.go.
type CancelLockParams struct {
	LockID pldtypes.Bytes32  `json:"lockId"`
	From   string            `json:"from"`
	Data   pldtypes.HexBytes `json:"data"`
}

type DelegateLockParams struct {
	LockID   pldtypes.Bytes32    `json:"lockId"`
	Unlock   *UnlockPublicParams `json:"unlock,omitempty"` // Required for V0, omitted for V1
	Delegate string              `json:"delegate"`         // chain-neutral lookup, resolved in Assemble/Endorse (chapter 14 step 7)
	Data     pldtypes.HexBytes   `json:"data"`
}

type UnlockRecipient struct {
	To     string               `json:"to"`
	Amount *pldtypes.HexUint256 `json:"amount"`
}

type UnlockPublicParams struct {
	TxId          string            `json:"txId"`
	LockedInputs  []string          `json:"lockedInputs"`
	LockedOutputs []string          `json:"lockedOutputs"`
	Outputs       []string          `json:"outputs"`
	Signature     pldtypes.HexBytes `json:"signature"`
	Data          pldtypes.HexBytes `json:"data"`
}

type SpendLockPublicParams struct {
	LockID pldtypes.Bytes32  `json:"lockId"`
	Data   pldtypes.HexBytes `json:"data"`
}

type BalanceOfParam struct {
	Account string `json:"account"`
}

type BalanceOfResult struct {
	TotalBalance *pldtypes.HexUint256 `json:"totalBalance"`
	TotalStates  *pldtypes.HexUint256 `json:"totalStates"`
	Overflow     bool                 `json:"overflow"`
}

// Encoded args for Noto_V1 implementation of NotoCreateLockArgs
type NotoCreateLockArgs_V1 struct {
	TxId         string            `json:"txId"`
	Inputs       []string          `json:"inputs"`
	Outputs      []string          `json:"outputs"`
	Contents     []string          `json:"contents"`
	NewLockState pldtypes.Bytes32  `json:"newLockState"`
	Proof        pldtypes.HexBytes `json:"proof"`
}

// Encoded args for Noto_V1 implementation of NotoUpdateLockArgs
type NotoUpdateLockArgs_V1 struct {
	TxId         string            `json:"txId"`
	OldLockState pldtypes.Bytes32  `json:"oldLockState"`
	NewLockState pldtypes.Bytes32  `json:"newLockState"`
	Proof        pldtypes.HexBytes `json:"proof"`
}

// Encoded args for Noto implementation of ILockableCapability.createLock()
type NotoCreateLockArgs struct {
	TxId         string            `json:"txId"`
	Inputs       []string          `json:"inputs"`
	Outputs      []string          `json:"outputs"`
	Contents     []string          `json:"contents"`
	NewLockState pldtypes.Bytes32  `json:"newLockState"`
	Options      *NotoLockOptions  `json:"options"`
	Proof        pldtypes.HexBytes `json:"proof"`
}

// Encoded args for Noto implementation of ILockableCapability.updateLock()
type NotoUpdateLockArgs struct {
	TxId         string            `json:"txId"`
	Contents     []string          `json:"contents"`
	OldLockState pldtypes.Bytes32  `json:"oldLockState"`
	NewLockState pldtypes.Bytes32  `json:"newLockState"`
	Options      NotoLockOptions   `json:"options"`
	Proof        pldtypes.HexBytes `json:"proof"`
}

// Encoded args for Noto implementation of ILockableCapability.spendLock()/cancelLock()
type NotoSpendLockArgs struct {
	TxId    string            `json:"txId"`
	Inputs  []string          `json:"inputs"`
	Outputs []string          `json:"outputs"`
	Data    pldtypes.HexBytes `json:"data"`
	Proof   pldtypes.HexBytes `json:"proof"`
}

// Encoded args for Noto implementation of ILockableCapability.delegateLock()
type NotoDelegateLockArgs struct {
	TxId         string            `json:"txId"`
	OldLockState pldtypes.Bytes32  `json:"oldLockState"`
	NewLockState pldtypes.Bytes32  `json:"newLockState"`
	Proof        pldtypes.HexBytes `json:"proof"`
}

var NotoCreateLockArgsABI_V1 = abi.ParameterArray{
	{
		Type:         "tuple",
		InternalType: "struct NotoCreateLockArgs",
		Components: abi.ParameterArray{
			{Name: "txId", Type: "bytes32"},
			{Name: "inputs", Type: "bytes32[]"},
			{Name: "outputs", Type: "bytes32[]"},
			{Name: "contents", Type: "bytes32[]"},
			{Name: "newLockState", Type: "bytes32"},
			{Name: "proof", Type: "bytes"},
		},
	},
}

var NotoUpdateLockArgsABI_V1 = abi.ParameterArray{
	{
		Type:         "tuple",
		InternalType: "struct NotoUpdateLockArgs",
		Components: abi.ParameterArray{
			{Name: "txId", Type: "bytes32"},
			{Name: "oldLockState", Type: "bytes32"},
			{Name: "newLockState", Type: "bytes32"},
			{Name: "proof", Type: "bytes"},
		},
	},
}

var NotoCreateLockArgsABI = abi.ParameterArray{
	{
		Type:         "tuple",
		InternalType: "struct NotoCreateLockArgs",
		Components: abi.ParameterArray{
			{Name: "txId", Type: "bytes32"},
			{Name: "inputs", Type: "bytes32[]"},
			{Name: "outputs", Type: "bytes32[]"},
			{Name: "contents", Type: "bytes32[]"},
			{Name: "newLockState", Type: "bytes32"},
			{
				Name:         "options",
				Type:         "tuple",
				InternalType: "struct NotoLockOptions",
				Components: abi.ParameterArray{
					{Name: "spendTxId", Type: "bytes32"},
				},
			},
			{Name: "proof", Type: "bytes"},
		},
	},
}

var NotoUpdateLockArgsABI = abi.ParameterArray{
	{
		Type:         "tuple",
		InternalType: "struct NotoUpdateLockArgs",
		Components: abi.ParameterArray{
			{Name: "txId", Type: "bytes32"},
			{Name: "contents", Type: "bytes32[]"},
			{Name: "oldLockState", Type: "bytes32"},
			{Name: "newLockState", Type: "bytes32"},
			{
				Name:         "options",
				Type:         "tuple",
				InternalType: "struct NotoLockOptions",
				Components: abi.ParameterArray{
					{Name: "spendTxId", Type: "bytes32"},
				},
			},
			{Name: "proof", Type: "bytes"},
		},
	},
}

var NotoDelegateLockArgsABI = abi.ParameterArray{
	{
		Type:         "tuple",
		InternalType: "struct NotoDelegateLockArgs",
		Components: abi.ParameterArray{
			{Name: "txId", Type: "bytes32"},
			{Name: "oldLockState", Type: "bytes32"},
			{Name: "newLockState", Type: "bytes32"},
			{Name: "proof", Type: "bytes"},
		},
	},
}

var NotoSpendLockArgsABI = abi.ParameterArray{
	{
		Type:         "tuple",
		InternalType: "struct NotoSpendLockArgs",
		Components: abi.ParameterArray{
			{Name: "txId", Type: "bytes32"},
			{Name: "inputs", Type: "bytes32[]"},
			{Name: "outputs", Type: "bytes32[]"},
			{Name: "data", Type: "bytes"},
			{Name: "proof", Type: "bytes"},
		},
	},
}

type NotoLockOptions struct {
	SpendTxId pldtypes.Bytes32 `json:"spendTxId"`
}

var NotoLockOptionsABI = abi.ParameterArray{
	{
		Type:         "tuple",
		InternalType: "struct NotoLockOptions",
		Components: abi.ParameterArray{
			{Name: "spendTxId", Type: "bytes32"},
		},
	},
}
