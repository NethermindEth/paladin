/*
 * Copyright © 2026 Kaleido, Inc.
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

package noto

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/common/go/pkg/log"
	"github.com/LFDT-Paladin/paladin/domains/noto/internal/msgs"
	"github.com/LFDT-Paladin/paladin/domains/noto/pkg/types"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/algorithms"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/domain"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/verifiers"
	"github.com/hyperledger/firefly-signer/pkg/abi"
	"github.com/hyperledger/firefly-signer/pkg/eip712"
	"github.com/hyperledger/firefly-signer/pkg/ethtypes"
	"github.com/hyperledger/firefly-signer/pkg/secp256k1"
)

// evmChainIO is the (today, sole) EVM implementation of chainIO - every method here is moved
// verbatim from what used to be a *Noto method or free function of the same behavior, per the
// chapter 14 step 3 refactor. No logic changes; only the receiver type changes.
type evmChainIO struct {
	chainID int64
}

func newEVMChainIO(chainID int64) *evmChainIO {
	return &evmChainIO{chainID: chainID}
}

func (e *evmChainIO) ChainKind() string { return "evm" }

func (e *evmChainIO) SigningAlgorithm() string { return algorithms.ECDSA_SECP256K1 }
func (e *evmChainIO) VerifierType() string     { return verifiers.ETH_ADDRESS }

func (e *evmChainIO) ResolveIdentity(ctx context.Context, errorDescription, lookup string, verifierList []*prototk.ResolvedVerifier) (*identityPair, error) {
	verifier := domain.FindVerifier(lookup, algorithms.ECDSA_SECP256K1, verifiers.ETH_ADDRESS, verifierList)
	if verifier == nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorVerifyingAddress, errorDescription)
	}
	address, err := pldtypes.ParseEthAddress(verifier.Verifier)
	if err != nil {
		return nil, err
	}
	return &identityPair{identifier: lookup, address: address, chainAddress: pldtypes.NewEVMChainAddress(*address)}, nil
}

// VerifySignature recovers the signer address from a secp256k1 signature (EVM signatures are
// recoverable, unlike ed25519's) and compares it against the expected verifier string.
func (e *evmChainIO) VerifySignature(ctx context.Context, payload []byte, signature []byte, expectedVerifier string) (bool, error) {
	sig, err := secp256k1.DecodeCompactRSV(ctx, signature)
	if err != nil {
		return false, err
	}
	recovered, err := sig.RecoverDirect(ethtypes.HexBytes0xPrefix(payload), e.chainID)
	if err != nil {
		return false, err
	}
	return recovered.String() == expectedVerifier, nil
}

func (e *evmChainIO) eip712Domain(contract *ethtypes.Address0xHex) map[string]any {
	return map[string]any{
		"name":              EIP712DomainName,
		"version":           EIP712DomainVersion,
		"chainId":           e.chainID,
		"verifyingContract": contract,
	}
}

func (e *evmChainIO) encodeNotoCoins(coins []*types.NotoCoin) ([]any, error) {
	encodedCoins := make([]any, len(coins))
	for i, coin := range coins {
		// coin.Owner is chain-neutral (*pldtypes.ChainAddress) since step 4 - unwrap back to the
		// concrete EVM address the EIP-712 "address" type needs, so the digest bytes stay
		// byte-identical to before this migration.
		if coin.Owner == nil {
			return nil, fmt.Errorf("coin has no owner")
		}
		owner, err := coin.Owner.EthAddress()
		if err != nil {
			return nil, err
		}
		encodedCoins[i] = map[string]any{
			"salt":   coin.Salt,
			"owner":  owner,
			"amount": coin.Amount.String(),
		}
	}
	return encodedCoins, nil
}

func (e *evmChainIO) encodeNotoLockedCoins(coins []*types.NotoLockedCoin) []any {
	encodedCoins := make([]any, len(coins))
	for i, coin := range coins {
		encodedCoins[i] = map[string]any{
			"salt":   coin.Salt,
			"lockId": coin.LockID,
			"owner":  coin.Owner,
			"amount": coin.Amount.String(),
		}
	}
	return encodedCoins
}

func (e *evmChainIO) EncodeTransferUnmasked(ctx context.Context, contract *ethtypes.Address0xHex, inputs, outputs []*types.NotoCoin) (ethtypes.HexBytes0xPrefix, error) {
	encodedInputs, err := e.encodeNotoCoins(inputs)
	if err != nil {
		return nil, err
	}
	encodedOutputs, err := e.encodeNotoCoins(outputs)
	if err != nil {
		return nil, err
	}
	return eip712.EncodeTypedDataV4(ctx, &eip712.TypedData{
		Types:       NotoTransferUnmaskedTypeSet,
		PrimaryType: "Transfer",
		Domain:      e.eip712Domain(contract),
		Message: map[string]any{
			"inputs":  encodedInputs,
			"outputs": encodedOutputs,
		},
	})
}

func (e *evmChainIO) EncodeTransferMasked(ctx context.Context, contract *ethtypes.Address0xHex, inputs, outputs []*pldapi.StateEncoded, data pldtypes.HexBytes) (ethtypes.HexBytes0xPrefix, error) {
	return eip712.EncodeTypedDataV4(ctx, &eip712.TypedData{
		Types:       NotoTransferMaskedTypeSet,
		PrimaryType: "Transfer",
		Domain:      e.eip712Domain(contract),
		Message: map[string]any{
			"inputs":  stringToAny(encodedStateIDs(inputs)),
			"outputs": stringToAny(encodedStateIDs(outputs)),
			"data":    data,
		},
	})
}

func (e *evmChainIO) EncodeLock(ctx context.Context, contract *ethtypes.Address0xHex, inputs, outputs []*types.NotoCoin, lockedOutputs []*types.NotoLockedCoin) (ethtypes.HexBytes0xPrefix, error) {
	encodedInputs, err := e.encodeNotoCoins(inputs)
	if err != nil {
		return nil, err
	}
	encodedOutputs, err := e.encodeNotoCoins(outputs)
	if err != nil {
		return nil, err
	}
	return eip712.EncodeTypedDataV4(ctx, &eip712.TypedData{
		Types:       NotoLockTypeSet,
		PrimaryType: "Lock",
		Domain:      e.eip712Domain(contract),
		Message: map[string]any{
			"inputs":        encodedInputs,
			"outputs":       encodedOutputs,
			"lockedOutputs": e.encodeNotoLockedCoins(lockedOutputs),
		},
	})
}

func (e *evmChainIO) EncodeUnlock(ctx context.Context, contract *ethtypes.Address0xHex, lockedInputs, lockedOutputs []*types.NotoLockedCoin, outputs []*types.NotoCoin) (ethtypes.HexBytes0xPrefix, error) {
	encodedOutputs, err := e.encodeNotoCoins(outputs)
	if err != nil {
		return nil, err
	}
	return eip712.EncodeTypedDataV4(ctx, &eip712.TypedData{
		Types:       NotoUnlockTypeSet,
		PrimaryType: "Unlock",
		Domain:      e.eip712Domain(contract),
		Message: map[string]any{
			"lockedInputs":  e.encodeNotoLockedCoins(lockedInputs),
			"lockedOutputs": e.encodeNotoLockedCoins(lockedOutputs),
			"outputs":       encodedOutputs,
		},
	})
}

func (e *evmChainIO) UnlockHashFromIDsV0(ctx context.Context, contract *ethtypes.Address0xHex, lockedInputs, lockedOutputs, outputs []string, data pldtypes.HexBytes) (ethtypes.HexBytes0xPrefix, error) {
	return eip712.EncodeTypedDataV4(ctx, &eip712.TypedData{
		Types:       NotoUnlockMaskedTypeSet_V0,
		PrimaryType: "Unlock",
		Domain:      e.eip712Domain(contract),
		Message: map[string]any{
			"lockedInputs":  stringToAny(lockedInputs),
			"lockedOutputs": stringToAny(lockedOutputs),
			"outputs":       stringToAny(outputs),
			"data":          data,
		},
	})
}

func (e *evmChainIO) UnlockHashFromIDsV1(ctx context.Context, contract *ethtypes.Address0xHex, lockID pldtypes.Bytes32, txId string, lockedInputs, outputs []string, data pldtypes.HexBytes) (encoded pldtypes.Bytes32, err error) {
	msg := map[string]any{
		"txId":         txId,
		"lockedInputs": stringToAny(lockedInputs),
		"outputs":      stringToAny(outputs),
		"data":         data,
	}
	b, err := eip712.EncodeTypedDataV4(ctx, &eip712.TypedData{
		Types:       NotoUnlockMaskedTypeSet_V1,
		PrimaryType: "Unlock",
		Domain:      e.eip712Domain(contract),
		Message:     msg,
	})
	if err == nil {
		copy(encoded[:], b[0:32])
		jsonMsg, _ := json.Marshal(msg)
		log.L(ctx).Infof("Encoded outcome hash '%s' for unlock operation %s: %s", encoded, lockID, jsonMsg)
	}
	return encoded, err
}

func (e *evmChainIO) EncodeDelegateLock(ctx context.Context, contract *ethtypes.Address0xHex, lockID pldtypes.Bytes32, delegate *pldtypes.EthAddress, data pldtypes.HexBytes) (ethtypes.HexBytes0xPrefix, error) {
	return eip712.EncodeTypedDataV4(ctx, &eip712.TypedData{
		Types:       NotoDelegateLockTypeSet,
		PrimaryType: "DelegateLock",
		Domain:      e.eip712Domain(contract),
		Message: map[string]any{
			"lockId":   lockID,
			"delegate": delegate,
			"data":     data,
		},
	})
}

func (e *evmChainIO) SelectFactoryABI(factoryVersion int64) abi.ABI {
	// Default to the V0 NotoFactory ABI if no version is specified
	switch factoryVersion {
	case 1:
		return factoryV1Build.ABI
	case 2:
		return factoryV2Build.ABI
	default:
		return factoryV0Build.ABI
	}
}

func (e *evmChainIO) SelectInterfaceABI(variant pldtypes.HexUint64) abi.ABI {
	if variant == types.NotoVariantV0 {
		return interfaceV0Build.ABI
	}
	if variant == types.NotoVariantV1 {
		return interfaceV1Build.ABI
	}
	return interfaceV2Build.ABI
}

// ComputeLockID computes the lockId the same way the contract does:
// keccak256(abi.encode(address(this), msg.sender, txId))
func (e *evmChainIO) ComputeLockID(ctx context.Context, contractAddress, notaryAddress *pldtypes.EthAddress, txId string) (pldtypes.Bytes32, error) {
	params := abi.ParameterArray{
		{Name: "contract", Type: "address"},
		{Name: "notary", Type: "address"},
		{Name: "txId", Type: "bytes32"},
	}

	paramsJSON := map[string]any{
		"contract": contractAddress.String(),
		"notary":   notaryAddress.String(),
		"txId":     txId,
	}

	jsonData, err := json.Marshal(paramsJSON)
	if err != nil {
		return pldtypes.Bytes32{}, err
	}

	encoded, err := params.EncodeABIDataJSONCtx(ctx, jsonData)
	if err != nil {
		return pldtypes.Bytes32{}, err
	}

	return pldtypes.Bytes32Keccak(encoded), nil
}
