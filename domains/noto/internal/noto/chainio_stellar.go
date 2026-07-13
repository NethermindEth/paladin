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
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/domains/noto/internal/msgs"
	"github.com/LFDT-Paladin/paladin/domains/noto/pkg/types"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/saladintypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/algorithms"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/domain"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/verifiers"
	"github.com/hyperledger/firefly-signer/pkg/abi"
	"github.com/hyperledger/firefly-signer/pkg/ethtypes"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// stellarChainIO is the Stellar/Soroban implementation of chainIO (chapter 14 step 4), scoped to
// exactly what the mint walking skeleton exercises: identity resolution, the sender-signature
// verify step (Assemble/Endorse run unconditionally regardless of chain kind - see chainio.go),
// and EncodeTransferUnmasked's state hashing (now a real SALADIN_TYPED_DATA_V0 digest, chapter 13
// §13.1). transfer(masked)/lock/unlock and deploy/invoke ABI selection aren't exercised by mint
// and remain explicit stubs - real work for when this extends past mint.
type stellarChainIO struct {
	// networkPassphrase comes from ChainInfo.NetworkId (ch. 11: "stellar: network passphrase"),
	// populated generically since domainmgr's chain-kind fix (chapter 14 step 1).
	networkPassphrase string
}

func newStellarChainIO(networkPassphrase string) *stellarChainIO {
	return &stellarChainIO{networkPassphrase: networkPassphrase}
}

func (s *stellarChainIO) ChainKind() string { return "stellar" }

func (s *stellarChainIO) SigningAlgorithm() string { return algorithms.EDDSA_ED25519 }
func (s *stellarChainIO) VerifierType() string     { return verifiers.STELLAR_ADDRESS }

func (s *stellarChainIO) ResolveIdentity(ctx context.Context, errorDescription, lookup string, verifierList []*prototk.ResolvedVerifier) (*identityPair, error) {
	verifier := domain.FindVerifier(lookup, algorithms.EDDSA_ED25519, verifiers.STELLAR_ADDRESS, verifierList)
	if verifier == nil {
		return nil, i18n.NewError(ctx, msgs.MsgErrorVerifyingAddress, errorDescription)
	}
	chainAddress, err := pldtypes.NewStellarAccountAddress(verifier.Verifier)
	if err != nil {
		return nil, err
	}
	return &identityPair{identifier: lookup, chainAddress: chainAddress}, nil
}

// VerifySignature verifies an ed25519 signature against the expected verifier's Stellar account
// address (StrKey "G..."). Deliberately "verify", not "recover": unlike EVM/secp256k1, ed25519
// signatures don't support recovering the signer's public key from the signature alone - the
// claimed public key must be supplied up front.
func (s *stellarChainIO) VerifySignature(ctx context.Context, payload []byte, signature []byte, expectedVerifier string) (bool, error) {
	pubKeyBytes, err := strkey.Decode(strkey.VersionByteAccountID, expectedVerifier)
	if err != nil {
		return false, err
	}
	return ed25519.Verify(ed25519.PublicKey(pubKeyBytes), payload, signature), nil
}

// placeholderContractID derives a deterministic, StrKey-encoded Soroban contract ID from the
// EVM-shaped 20-byte address every chainIO method still receives as "contract"
// (ParsedTransaction.ContractAddress is a shared toolkit type, used by both Noto and Zeto -
// generalizing it is out of scope for this pass). This is NOT a real Stellar contract identity,
// just a deterministic stand-in so SALADIN_TYPED_DATA_V0 digests are computable and stable for
// this walking skeleton - a real deployment needs the domain instance's actual Soroban contract
// ID here instead, which requires that toolkit-wide generalization.
func placeholderContractID(contract *ethtypes.Address0xHex) (string, error) {
	var padded [32]byte
	if contract != nil {
		copy(padded[12:], contract[:])
	}
	return strkey.Encode(strkey.VersionByteContract, padded[:])
}

// scValBytes/scValString/scValVec build xdr.ScVal values by hand for the small, known-shape
// payloads this file needs - no need for the full spec-driven scspec package (which expects a
// formal contract-spec XDR, not ad hoc known-shape values).
func scValBytes(b []byte) (xdr.ScVal, error) {
	return xdr.NewScVal(xdr.ScValTypeScvBytes, xdr.ScBytes(b))
}

func scValString(str string) (xdr.ScVal, error) {
	return xdr.NewScVal(xdr.ScValTypeScvString, xdr.ScString(str))
}

func scValVec(items []xdr.ScVal) (xdr.ScVal, error) {
	vec := xdr.ScVec(items)
	return xdr.NewScVal(xdr.ScValTypeScvVec, &vec)
}

func marshalScVal(v xdr.ScVal) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := xdr.Marshal(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encodeCoinScVal encodes a single NotoCoin's (salt, owner, amount) as an ScVec - this is
// Paladin's own off-chain endorsement payload shape, not required to match any on-chain Rust
// struct (SNoto's on-chain state is opaque 32-byte state IDs; coin data never reaches the
// contract - chapter 13 §13.2), so this shape is free to define, kept simple and self-describing.
func encodeCoinScVal(coin *types.NotoCoin) (xdr.ScVal, error) {
	if coin.Owner == nil {
		return xdr.ScVal{}, fmt.Errorf("coin has no owner")
	}
	salt, err := scValBytes(coin.Salt[:])
	if err != nil {
		return xdr.ScVal{}, err
	}
	owner, err := scValString(coin.Owner.String())
	if err != nil {
		return xdr.ScVal{}, err
	}
	amount, err := scValString(coin.Amount.String())
	if err != nil {
		return xdr.ScVal{}, err
	}
	return scValVec([]xdr.ScVal{salt, owner, amount})
}

func encodeCoinsScVal(coins []*types.NotoCoin) (xdr.ScVal, error) {
	items := make([]xdr.ScVal, len(coins))
	for i, coin := range coins {
		v, err := encodeCoinScVal(coin)
		if err != nil {
			return xdr.ScVal{}, err
		}
		items[i] = v
	}
	return scValVec(items)
}

func (s *stellarChainIO) EncodeTransferUnmasked(ctx context.Context, contract *ethtypes.Address0xHex, inputs, outputs []*types.NotoCoin) (ethtypes.HexBytes0xPrefix, error) {
	contractID, err := placeholderContractID(contract)
	if err != nil {
		return nil, err
	}
	inputsVal, err := encodeCoinsScVal(inputs)
	if err != nil {
		return nil, err
	}
	outputsVal, err := encodeCoinsScVal(outputs)
	if err != nil {
		return nil, err
	}
	payload, err := scValVec([]xdr.ScVal{inputsVal, outputsVal})
	if err != nil {
		return nil, err
	}
	payloadXDR, err := marshalScVal(payload)
	if err != nil {
		return nil, err
	}
	digest, err := saladintypes.DigestXDR(s.networkPassphrase, contractID, "snoto.Transfer", payloadXDR)
	if err != nil {
		return nil, err
	}
	return ethtypes.HexBytes0xPrefix(digest[:]), nil
}

// Not yet implemented for Stellar - transfer(masked)/lock/unlock don't have a Stellar branch yet
// (mint only, chapter 14 step 4). Real implementations land when this extends past mint.
func (s *stellarChainIO) EncodeTransferMasked(ctx context.Context, contract *ethtypes.Address0xHex, inputs, outputs []*pldapi.StateEncoded, data pldtypes.HexBytes) (ethtypes.HexBytes0xPrefix, error) {
	return nil, fmt.Errorf("stellarChainIO: EncodeTransferMasked not yet implemented (mint-only walking skeleton, chapter 14 step 4)")
}

func (s *stellarChainIO) EncodeLock(ctx context.Context, contract *ethtypes.Address0xHex, inputs, outputs []*types.NotoCoin, lockedOutputs []*types.NotoLockedCoin) (ethtypes.HexBytes0xPrefix, error) {
	return nil, fmt.Errorf("stellarChainIO: EncodeLock not yet implemented (mint-only walking skeleton, chapter 14 step 4)")
}

func (s *stellarChainIO) EncodeUnlock(ctx context.Context, contract *ethtypes.Address0xHex, lockedInputs, lockedOutputs []*types.NotoLockedCoin, outputs []*types.NotoCoin) (ethtypes.HexBytes0xPrefix, error) {
	return nil, fmt.Errorf("stellarChainIO: EncodeUnlock not yet implemented (mint-only walking skeleton, chapter 14 step 4)")
}

func (s *stellarChainIO) UnlockHashFromIDsV0(ctx context.Context, contract *ethtypes.Address0xHex, lockedInputs, lockedOutputs, outputs []string, data pldtypes.HexBytes) (ethtypes.HexBytes0xPrefix, error) {
	return nil, fmt.Errorf("stellarChainIO: UnlockHashFromIDsV0 not yet implemented (mint-only walking skeleton, chapter 14 step 4)")
}

func (s *stellarChainIO) UnlockHashFromIDsV1(ctx context.Context, contract *ethtypes.Address0xHex, lockID pldtypes.Bytes32, txId string, lockedInputs, outputs []string, data pldtypes.HexBytes) (pldtypes.Bytes32, error) {
	return pldtypes.Bytes32{}, fmt.Errorf("stellarChainIO: UnlockHashFromIDsV1 not yet implemented (mint-only walking skeleton, chapter 14 step 4)")
}

func (s *stellarChainIO) EncodeDelegateLock(ctx context.Context, contract *ethtypes.Address0xHex, lockID pldtypes.Bytes32, delegate *pldtypes.EthAddress, data pldtypes.HexBytes) (ethtypes.HexBytes0xPrefix, error) {
	return nil, fmt.Errorf("stellarChainIO: EncodeDelegateLock not yet implemented (mint-only walking skeleton, chapter 14 step 4)")
}

// SelectFactoryABI/SelectInterfaceABI are not applicable to Soroban at all - there's no ABI
// concept for contract deployment/invocation selection (functions are invoked by name), unlike
// EVM's versioned Solidity factory/interface builds. Not called by mint's baseLedgerInvoke on the
// Stellar branch - a call here would be a real bug, hence panic rather than a quiet nil/zero ABI.
func (s *stellarChainIO) SelectFactoryABI(factoryVersion int64) abi.ABI {
	panic("stellarChainIO: SelectFactoryABI is not applicable to Soroban (no ABI-selection concept)")
}

func (s *stellarChainIO) SelectInterfaceABI(variant pldtypes.HexUint64) abi.ABI {
	panic("stellarChainIO: SelectInterfaceABI is not applicable to Soroban (no ABI-selection concept)")
}

// ComputeLockID: SNoto's actual Rust contract uses lock_id = tx_id directly (chapter 13 §13.2,
// snoto/src/lib.rs) - no derivation needed, unlike EVM's
// keccak256(abi.encode(address(this), msg.sender, txId)). contractAddress/notaryAddress are
// unused here (kept in the signature only because the interface is shared with evmChainIO).
func (s *stellarChainIO) ComputeLockID(ctx context.Context, contractAddress, notaryAddress *pldtypes.EthAddress, txId string) (pldtypes.Bytes32, error) {
	return pldtypes.ParseBytes32Ctx(ctx, txId)
}

// parseBytes32List parses a list of hex state IDs (as produced by endorsableStateIDs) into
// pldtypes.Bytes32 - shared by every stellarBaseLedgerInvoke* method building SorobanInvoke args.
func parseBytes32List(ctx context.Context, ids []string) ([]pldtypes.Bytes32, error) {
	result := make([]pldtypes.Bytes32, len(ids))
	for i, id := range ids {
		b, err := pldtypes.ParseBytes32Ctx(ctx, id)
		if err != nil {
			return nil, err
		}
		result[i] = b
	}
	return result, nil
}

// encodeSNotoTransferArgs builds the real Soroban call args for SNoto's
// `transfer(tx_id, inputs, outputs, signature, data)` (soroban/contracts/snoto/src/lib.rs) - mint
// is a transfer with empty inputs (chapter 13 §13.2). args_xdr is the XDR-encoded Vec<SCVal> per
// chapter 11's SorobanInvoke.args_xdr field comment; args_json mirrors it for observability.
func encodeSNotoTransferArgs(txID pldtypes.Bytes32, inputs, outputs []pldtypes.Bytes32, signature []byte, data []byte) (argsXDR []byte, argsJSON string, err error) {
	txIDVal, err := scValBytes(txID[:])
	if err != nil {
		return nil, "", err
	}
	inputsVal, err := scValBytes32Vec(inputs)
	if err != nil {
		return nil, "", err
	}
	outputsVal, err := scValBytes32Vec(outputs)
	if err != nil {
		return nil, "", err
	}
	sigVal, err := scValBytes(signature)
	if err != nil {
		return nil, "", err
	}
	dataVal, err := scValBytes(data)
	if err != nil {
		return nil, "", err
	}

	args := xdr.ScVec{txIDVal, inputsVal, outputsVal, sigVal, dataVal}
	var buf bytes.Buffer
	if _, err := xdr.Marshal(&buf, args); err != nil {
		return nil, "", err
	}

	argsJSONBytes, err := json.Marshal(map[string]any{
		"tx_id":     txID.String(),
		"inputs":    bytes32Strings(inputs),
		"outputs":   bytes32Strings(outputs),
		"signature": pldtypes.HexBytes(signature).String(),
		"data":      pldtypes.HexBytes(data).String(),
	})
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), string(argsJSONBytes), nil
}

func scValBytes32Vec(items []pldtypes.Bytes32) (xdr.ScVal, error) {
	vals := make([]xdr.ScVal, len(items))
	for i, b := range items {
		v, err := scValBytes(b[:])
		if err != nil {
			return xdr.ScVal{}, err
		}
		vals[i] = v
	}
	return scValVec(vals)
}

func bytes32Strings(items []pldtypes.Bytes32) []string {
	out := make([]string, len(items))
	for i, b := range items {
		out[i] = b.String()
	}
	return out
}
