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
	"math/big"

	"github.com/LFDT-Paladin/paladin/common/go/pkg/i18n"
	"github.com/LFDT-Paladin/paladin/domains/noto/internal/msgs"
	"github.com/LFDT-Paladin/paladin/domains/noto/pkg/types"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/saladintypes"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/scspec"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/algorithms"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/domain"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/signpayloads"
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

func (s *stellarChainIO) SigningAlgorithm() string     { return algorithms.EDDSA_ED25519 }
func (s *stellarChainIO) VerifierType() string         { return verifiers.STELLAR_ADDRESS }
func (s *stellarChainIO) SignaturePayloadType() string { return signpayloads.OPAQUE_TO_EDDSA }

func (s *stellarChainIO) NetworkPassphrase() string { return s.networkPassphrase }

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

// scValAddress builds an Address SCVal from a strkey string (account "G..." or contract "C...").
// Reuses scspec.AddressFromStrkey rather than re-deriving xdr.ScAddress from strkey bytes here.
func scValAddress(strkeyAddr string) (xdr.ScVal, error) {
	addr, err := scspec.AddressFromStrkey(strkeyAddr)
	if err != nil {
		return xdr.ScVal{}, err
	}
	return xdr.NewScVal(xdr.ScValTypeScvAddress, addr)
}

// scValI128 encodes a non-negative amount as an i128 SCVal (SNoto's deposit/withdraw "amount"
// shape - the only place this codebase encodes a real numeric on-chain value; transfer/lock/
// unlock only ever pass opaque state IDs, since coin amounts otherwise live entirely off-chain).
func scValI128(amount *big.Int) (xdr.ScVal, error) {
	if amount.Sign() < 0 {
		return xdr.ScVal{}, fmt.Errorf("amount must be non-negative, got %s", amount.String())
	}
	lo := new(big.Int).And(amount, new(big.Int).SetUint64(^uint64(0))).Uint64()
	hi := new(big.Int).Rsh(amount, 64)
	if !hi.IsInt64() {
		return xdr.ScVal{}, fmt.Errorf("amount %s exceeds i128 range", amount.String())
	}
	return xdr.NewScVal(xdr.ScValTypeScvI128, xdr.Int128Parts{
		Hi: xdr.Int64(hi.Int64()),
		Lo: xdr.Uint64(lo),
	})
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

// Not yet implemented for Stellar - transfer(masked) doesn't have a Stellar branch (nothing in the
// Go handler layer calls it - mint/transfer/lock/unlock all use EncodeTransferUnmasked/EncodeLock/
// EncodeUnlock instead; only chainIO's interface conformance requires it to exist).
func (s *stellarChainIO) EncodeTransferMasked(ctx context.Context, contract *ethtypes.Address0xHex, inputs, outputs []*pldapi.StateEncoded, data pldtypes.HexBytes) (ethtypes.HexBytes0xPrefix, error) {
	return nil, fmt.Errorf("stellarChainIO: EncodeTransferMasked not yet implemented (not exercised by any Go handler)")
}

// encodeLockedCoinScVal encodes a single NotoLockedCoin's (salt, lockId, owner, amount) as an
// ScVec - mirrors encodeCoinScVal, this is Paladin's own off-chain endorsement payload shape, not
// required to match any on-chain Rust struct (SNoto's on-chain state is opaque 32-byte state IDs).
func encodeLockedCoinScVal(coin *types.NotoLockedCoin) (xdr.ScVal, error) {
	if coin.Owner == nil {
		return xdr.ScVal{}, fmt.Errorf("locked coin has no owner")
	}
	salt, err := scValBytes(coin.Salt[:])
	if err != nil {
		return xdr.ScVal{}, err
	}
	lockID, err := scValBytes(coin.LockID[:])
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
	return scValVec([]xdr.ScVal{salt, lockID, owner, amount})
}

func encodeLockedCoinsScVal(coins []*types.NotoLockedCoin) (xdr.ScVal, error) {
	items := make([]xdr.ScVal, len(coins))
	for i, coin := range coins {
		v, err := encodeLockedCoinScVal(coin)
		if err != nil {
			return xdr.ScVal{}, err
		}
		items[i] = v
	}
	return scValVec(items)
}

// EncodeLock hashes (inputs, outputs [unlocked remainder], lockedOutputs) coins - the off-chain
// endorsement payload the sender signs, mirroring EncodeTransferUnmasked. Real on-chain calls use
// encodeSNotoLockArgs instead (state IDs, not coin data).
func (s *stellarChainIO) EncodeLock(ctx context.Context, contract *ethtypes.Address0xHex, inputs, outputs []*types.NotoCoin, lockedOutputs []*types.NotoLockedCoin) (ethtypes.HexBytes0xPrefix, error) {
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
	lockedOutputsVal, err := encodeLockedCoinsScVal(lockedOutputs)
	if err != nil {
		return nil, err
	}
	payload, err := scValVec([]xdr.ScVal{inputsVal, outputsVal, lockedOutputsVal})
	if err != nil {
		return nil, err
	}
	payloadXDR, err := marshalScVal(payload)
	if err != nil {
		return nil, err
	}
	digest, err := saladintypes.DigestXDR(s.networkPassphrase, contractID, "snoto.Lock", payloadXDR)
	if err != nil {
		return nil, err
	}
	return ethtypes.HexBytes0xPrefix(digest[:]), nil
}

// EncodeUnlock hashes (lockedInputs, lockedOutputs, outputs) coins - the off-chain endorsement
// payload the sender signs. Uses type name "snoto.UnlockEndorsement", not "snoto.Unlock" - the
// contract's own check_commitment (soroban/contracts/snoto/src/lib.rs) already uses "snoto.Unlock"
// for a different digest, over raw state IDs rather than coin data; reusing the same type name for
// this payload would be confusing even though the differing payload bytes mean no actual collision.
func (s *stellarChainIO) EncodeUnlock(ctx context.Context, contract *ethtypes.Address0xHex, lockedInputs, lockedOutputs []*types.NotoLockedCoin, outputs []*types.NotoCoin) (ethtypes.HexBytes0xPrefix, error) {
	contractID, err := placeholderContractID(contract)
	if err != nil {
		return nil, err
	}
	lockedInputsVal, err := encodeLockedCoinsScVal(lockedInputs)
	if err != nil {
		return nil, err
	}
	lockedOutputsVal, err := encodeLockedCoinsScVal(lockedOutputs)
	if err != nil {
		return nil, err
	}
	outputsVal, err := encodeCoinsScVal(outputs)
	if err != nil {
		return nil, err
	}
	payload, err := scValVec([]xdr.ScVal{lockedInputsVal, lockedOutputsVal, outputsVal})
	if err != nil {
		return nil, err
	}
	payloadXDR, err := marshalScVal(payload)
	if err != nil {
		return nil, err
	}
	digest, err := saladintypes.DigestXDR(s.networkPassphrase, contractID, "snoto.UnlockEndorsement", payloadXDR)
	if err != nil {
		return nil, err
	}
	return ethtypes.HexBytes0xPrefix(digest[:]), nil
}

// V0 is EVM-only (never extended to Stellar - decodeStellarConfig always sets NotoVariantV1), so
// this stays a stub: nothing on the Stellar path ever calls it.
func (s *stellarChainIO) UnlockHashFromIDsV0(ctx context.Context, contract *ethtypes.Address0xHex, lockedInputs, lockedOutputs, outputs []string, data pldtypes.HexBytes) (ethtypes.HexBytes0xPrefix, error) {
	return nil, fmt.Errorf("stellarChainIO: UnlockHashFromIDsV0 not yet implemented (Stellar variant is always V1 - decodeStellarConfig)")
}

// unlockPayloadTypeName maps the shared "spend"/"cancel" purpose to the on-chain type_name
// check_commitment (soroban/contracts/snoto/src/lib.rs) actually recomputes against - two
// distinct names for the identical UnlockPayload(lock_id, locked_inputs, outputs, data) tuple
// shape, matching unlock's "snoto.Unlock" vs cancel_unlock's "snoto.CancelUnlock" call sites.
func unlockPayloadTypeName(purpose string) (string, error) {
	switch purpose {
	case "spend":
		return "snoto.Unlock", nil
	case "cancel":
		return "snoto.CancelUnlock", nil
	default:
		return "", fmt.Errorf("stellarChainIO: unknown unlock commitment purpose %q", purpose)
	}
}

// UnlockHashFromIDsV1 computes the spend_commitment/cancel_commitment SNoto's prepare_unlock
// takes as direct BytesN<32> args - the real contract address is required here (not
// placeholderContractID), since check_commitment recomputes this exact digest on-chain from
// current_contract_id(env) and must match byte-for-byte.
func (s *stellarChainIO) UnlockHashFromIDsV1(ctx context.Context, contract *ethtypes.Address0xHex, lockID pldtypes.Bytes32, txId string, lockedInputs, outputs []string, data pldtypes.HexBytes, purpose string, realContractID string) (pldtypes.Bytes32, error) {
	typeName, err := unlockPayloadTypeName(purpose)
	if err != nil {
		return pldtypes.Bytes32{}, err
	}
	lockedInputsB32, err := parseBytes32List(ctx, lockedInputs)
	if err != nil {
		return pldtypes.Bytes32{}, err
	}
	outputsB32, err := parseBytes32List(ctx, outputs)
	if err != nil {
		return pldtypes.Bytes32{}, err
	}
	lockIDVal, err := scValBytes(lockID[:])
	if err != nil {
		return pldtypes.Bytes32{}, err
	}
	lockedInputsVal, err := scValBytes32Vec(lockedInputsB32)
	if err != nil {
		return pldtypes.Bytes32{}, err
	}
	outputsVal, err := scValBytes32Vec(outputsB32)
	if err != nil {
		return pldtypes.Bytes32{}, err
	}
	dataVal, err := scValBytes(data)
	if err != nil {
		return pldtypes.Bytes32{}, err
	}
	payload, err := scValVec([]xdr.ScVal{lockIDVal, lockedInputsVal, outputsVal, dataVal})
	if err != nil {
		return pldtypes.Bytes32{}, err
	}
	payloadXDR, err := marshalScVal(payload)
	if err != nil {
		return pldtypes.Bytes32{}, err
	}
	return saladintypes.DigestXDR(s.networkPassphrase, realContractID, typeName, payloadXDR)
}

// EncodeDelegateLock hashes (lockId, delegate, data) - the off-chain endorsement payload the
// sender signs, exercised regardless of chain kind since Assemble/Endorse run unconditionally
// (see this file's own doc comment). delegate is chain-neutral; its StrKey string form is used
// directly (mirrors encodeCoinScVal's owner encoding), unlike EVM's raw "address" field.
func (s *stellarChainIO) EncodeDelegateLock(ctx context.Context, contract *ethtypes.Address0xHex, lockID pldtypes.Bytes32, delegate *pldtypes.ChainAddress, data pldtypes.HexBytes) (ethtypes.HexBytes0xPrefix, error) {
	contractID, err := placeholderContractID(contract)
	if err != nil {
		return nil, err
	}
	lockIDVal, err := scValBytes(lockID[:])
	if err != nil {
		return nil, err
	}
	var delegateStr string
	if delegate != nil {
		delegateStr = delegate.String()
	}
	delegateVal, err := scValString(delegateStr)
	if err != nil {
		return nil, err
	}
	dataVal, err := scValBytes(data)
	if err != nil {
		return nil, err
	}
	payload, err := scValVec([]xdr.ScVal{lockIDVal, delegateVal, dataVal})
	if err != nil {
		return nil, err
	}
	payloadXDR, err := marshalScVal(payload)
	if err != nil {
		return nil, err
	}
	digest, err := saladintypes.DigestXDR(s.networkPassphrase, contractID, "snoto.DelegateLock", payloadXDR)
	if err != nil {
		return nil, err
	}
	return ethtypes.HexBytes0xPrefix(digest[:]), nil
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

// encodeSNotoLockArgs builds the real Soroban call args for SNoto's
// `lock(tx_id, inputs, locked_outputs, outputs, signature, data, new_lock_state)`
// (soroban/contracts/snoto/src/lib.rs) - the `outputs` (unlocked remainder) list was added to the
// contract for this phase, to match EVM Noto's three-list inputs/locked_outputs/outputs shape.
// newLockState is the assembled lockInfoV1 private state's own ID (see stellarBaseLedgerInvokeLock's
// caller) - opaque to the contract, which just echoes it in the Lock event and stores it against
// the lock_id for prepare_unlock/delegate_lock/unlock/cancel_unlock to read back later (mirrors
// Noto.sol's own on-chain `_lockStates[lockId]`).
func encodeSNotoLockArgs(txID pldtypes.Bytes32, inputs, lockedOutputs, outputs []pldtypes.Bytes32, signature []byte, data []byte, newLockState pldtypes.Bytes32) (argsXDR []byte, argsJSON string, err error) {
	txIDVal, err := scValBytes(txID[:])
	if err != nil {
		return nil, "", err
	}
	inputsVal, err := scValBytes32Vec(inputs)
	if err != nil {
		return nil, "", err
	}
	lockedOutputsVal, err := scValBytes32Vec(lockedOutputs)
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
	newLockStateVal, err := scValBytes(newLockState[:])
	if err != nil {
		return nil, "", err
	}

	args := xdr.ScVec{txIDVal, inputsVal, lockedOutputsVal, outputsVal, sigVal, dataVal, newLockStateVal}
	var buf bytes.Buffer
	if _, err := xdr.Marshal(&buf, args); err != nil {
		return nil, "", err
	}

	argsJSONBytes, err := json.Marshal(map[string]any{
		"tx_id":          txID.String(),
		"inputs":         bytes32Strings(inputs),
		"locked_outputs": bytes32Strings(lockedOutputs),
		"outputs":        bytes32Strings(outputs),
		"signature":      pldtypes.HexBytes(signature).String(),
		"data":           pldtypes.HexBytes(data).String(),
		"new_lock_state": newLockState.String(),
	})
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), string(argsJSONBytes), nil
}

// encodeSNotoUnlockArgs builds the real Soroban call args for SNoto's
// `unlock(tx_id, lock_id, locked_inputs, outputs, data)` (soroban/contracts/snoto/src/lib.rs) -
// note there is no signature/proof slot at all: the sender's signature has no on-chain role for
// unlock (only for off-chain endorsement, already checked by Endorse/validateSignature via
// EncodeUnlock above), and the commit-reveal digest is recomputed on-chain from
// (lock_id, locked_inputs, outputs, data) only, not passed in. tx_id exists purely for Paladin's
// off-chain confirmation correlation (see lib.rs's own module doc comment) - lock_id can't serve
// that role since it identifies the original lock-creation transaction, not this unlock.
func encodeSNotoUnlockArgs(txID, lockID pldtypes.Bytes32, lockedInputs, outputs []pldtypes.Bytes32, data []byte) (argsXDR []byte, argsJSON string, err error) {
	txIDVal, err := scValBytes(txID[:])
	if err != nil {
		return nil, "", err
	}
	lockIDVal, err := scValBytes(lockID[:])
	if err != nil {
		return nil, "", err
	}
	lockedInputsVal, err := scValBytes32Vec(lockedInputs)
	if err != nil {
		return nil, "", err
	}
	outputsVal, err := scValBytes32Vec(outputs)
	if err != nil {
		return nil, "", err
	}
	dataVal, err := scValBytes(data)
	if err != nil {
		return nil, "", err
	}

	args := xdr.ScVec{txIDVal, lockIDVal, lockedInputsVal, outputsVal, dataVal}
	var buf bytes.Buffer
	if _, err := xdr.Marshal(&buf, args); err != nil {
		return nil, "", err
	}

	argsJSONBytes, err := json.Marshal(map[string]any{
		"tx_id":         txID.String(),
		"lock_id":       lockID.String(),
		"locked_inputs": bytes32Strings(lockedInputs),
		"outputs":       bytes32Strings(outputs),
		"data":          pldtypes.HexBytes(data).String(),
	})
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), string(argsJSONBytes), nil
}

// encodeSNotoCancelUnlockArgs builds the real Soroban call args for SNoto's
// `cancel_unlock(tx_id, lock_id, locked_inputs, cancel_outputs, data)`
// (soroban/contracts/snoto/src/lib.rs) - mirrors encodeSNotoUnlockArgs exactly (same shape, same
// no-signature-slot reasoning: the cancel-commitment digest is recomputed on-chain from
// (lock_id, locked_inputs, cancel_outputs, data) only, not passed in).
func encodeSNotoCancelUnlockArgs(txID, lockID pldtypes.Bytes32, lockedInputs, cancelOutputs []pldtypes.Bytes32, data []byte) (argsXDR []byte, argsJSON string, err error) {
	txIDVal, err := scValBytes(txID[:])
	if err != nil {
		return nil, "", err
	}
	lockIDVal, err := scValBytes(lockID[:])
	if err != nil {
		return nil, "", err
	}
	lockedInputsVal, err := scValBytes32Vec(lockedInputs)
	if err != nil {
		return nil, "", err
	}
	cancelOutputsVal, err := scValBytes32Vec(cancelOutputs)
	if err != nil {
		return nil, "", err
	}
	dataVal, err := scValBytes(data)
	if err != nil {
		return nil, "", err
	}

	args := xdr.ScVec{txIDVal, lockIDVal, lockedInputsVal, cancelOutputsVal, dataVal}
	var buf bytes.Buffer
	if _, err := xdr.Marshal(&buf, args); err != nil {
		return nil, "", err
	}

	argsJSONBytes, err := json.Marshal(map[string]any{
		"tx_id":          txID.String(),
		"lock_id":        lockID.String(),
		"locked_inputs":  bytes32Strings(lockedInputs),
		"cancel_outputs": bytes32Strings(cancelOutputs),
		"data":           pldtypes.HexBytes(data).String(),
	})
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), string(argsJSONBytes), nil
}

// encodeSNotoDepositArgs builds the real Soroban call args for SNoto's
// `deposit(tx_id, from, amount, outputs, data)` (soroban/contracts/snoto/src/lib.rs) - the real
// on-chain SAC shield. from is the resolved chain-neutral StrKey string of the depositing party
// (both the real SAC-transfer source and the off-chain recipient of the new NotoCoin outputs).
func encodeSNotoDepositArgs(txID pldtypes.Bytes32, from string, amount *big.Int, outputs []pldtypes.Bytes32, data []byte) (argsXDR []byte, argsJSON string, err error) {
	txIDVal, err := scValBytes(txID[:])
	if err != nil {
		return nil, "", err
	}
	fromVal, err := scValAddress(from)
	if err != nil {
		return nil, "", err
	}
	amountVal, err := scValI128(amount)
	if err != nil {
		return nil, "", err
	}
	outputsVal, err := scValBytes32Vec(outputs)
	if err != nil {
		return nil, "", err
	}
	dataVal, err := scValBytes(data)
	if err != nil {
		return nil, "", err
	}

	args := xdr.ScVec{txIDVal, fromVal, amountVal, outputsVal, dataVal}
	var buf bytes.Buffer
	if _, err := xdr.Marshal(&buf, args); err != nil {
		return nil, "", err
	}

	argsJSONBytes, err := json.Marshal(map[string]any{
		"tx_id":   txID.String(),
		"from":    from,
		"amount":  amount.String(),
		"outputs": bytes32Strings(outputs),
		"data":    pldtypes.HexBytes(data).String(),
	})
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), string(argsJSONBytes), nil
}

// encodeSNotoWithdrawArgs builds the real Soroban call args for SNoto's
// `withdraw(tx_id, recipient, amount, inputs, data)` (soroban/contracts/snoto/src/lib.rs) - the
// real on-chain SAC unshield. recipient is the resolved chain-neutral StrKey string of the real
// on-chain destination.
func encodeSNotoWithdrawArgs(txID pldtypes.Bytes32, recipient string, amount *big.Int, inputs []pldtypes.Bytes32, data []byte) (argsXDR []byte, argsJSON string, err error) {
	txIDVal, err := scValBytes(txID[:])
	if err != nil {
		return nil, "", err
	}
	recipientVal, err := scValAddress(recipient)
	if err != nil {
		return nil, "", err
	}
	amountVal, err := scValI128(amount)
	if err != nil {
		return nil, "", err
	}
	inputsVal, err := scValBytes32Vec(inputs)
	if err != nil {
		return nil, "", err
	}
	dataVal, err := scValBytes(data)
	if err != nil {
		return nil, "", err
	}

	args := xdr.ScVec{txIDVal, recipientVal, amountVal, inputsVal, dataVal}
	var buf bytes.Buffer
	if _, err := xdr.Marshal(&buf, args); err != nil {
		return nil, "", err
	}

	argsJSONBytes, err := json.Marshal(map[string]any{
		"tx_id":     txID.String(),
		"recipient": recipient,
		"amount":    amount.String(),
		"inputs":    bytes32Strings(inputs),
		"data":      pldtypes.HexBytes(data).String(),
	})
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), string(argsJSONBytes), nil
}

// encodeSNotoPrepareUnlockArgs builds the real Soroban call args for SNoto's
// `prepare_unlock(tx_id, lock_id, spend_commitment, cancel_commitment, data)`
// (soroban/contracts/snoto/src/lib.rs) - locked_inputs/outputs aren't passed on-chain at all, only
// baked into the commitment digests themselves (spendCommitment/cancelCommitment, computed via
// UnlockHashFromIDsV1), mirroring EVM's own updateLock args shape.
// newLockState is the updated lockInfoV1 private state's own ID - see encodeSNotoLockArgs's own
// doc comment on the mechanism this is part of. contents is the locked-coin state ID(s) this
// prepare_unlock references (echoed back in the PrepareUnlock event's own "contents" field) -
// without it, the Go event indexer has no on-chain signal to confirm the locked coin as a read
// state for this transaction, so BuildReceipt's own lockedInputIDs extraction (used to build the
// unlock/cancel externalCalls args) comes back empty.
func encodeSNotoPrepareUnlockArgs(txID, lockID, spendCommitment, cancelCommitment pldtypes.Bytes32, data []byte, newLockState pldtypes.Bytes32, contents []pldtypes.Bytes32) (argsXDR []byte, argsJSON string, err error) {
	txIDVal, err := scValBytes(txID[:])
	if err != nil {
		return nil, "", err
	}
	lockIDVal, err := scValBytes(lockID[:])
	if err != nil {
		return nil, "", err
	}
	spendCommitmentVal, err := scValBytes(spendCommitment[:])
	if err != nil {
		return nil, "", err
	}
	cancelCommitmentVal, err := scValBytes(cancelCommitment[:])
	if err != nil {
		return nil, "", err
	}
	dataVal, err := scValBytes(data)
	if err != nil {
		return nil, "", err
	}
	newLockStateVal, err := scValBytes(newLockState[:])
	if err != nil {
		return nil, "", err
	}
	contentsVal, err := scValBytes32Vec(contents)
	if err != nil {
		return nil, "", err
	}

	args := xdr.ScVec{txIDVal, lockIDVal, spendCommitmentVal, cancelCommitmentVal, dataVal, newLockStateVal, contentsVal}
	var buf bytes.Buffer
	if _, err := xdr.Marshal(&buf, args); err != nil {
		return nil, "", err
	}

	argsJSONBytes, err := json.Marshal(map[string]any{
		"tx_id":             txID.String(),
		"lock_id":           lockID.String(),
		"spend_commitment":  spendCommitment.String(),
		"cancel_commitment": cancelCommitment.String(),
		"data":              pldtypes.HexBytes(data).String(),
		"new_lock_state":    newLockState.String(),
		"contents":          bytes32Strings(contents),
	})
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), string(argsJSONBytes), nil
}

// encodeSNotoDelegateLockArgs builds the real Soroban call args for SNoto's
// `delegate_lock(tx_id, lock_id, delegate, data)` (soroban/contracts/snoto/src/lib.rs). delegate is
// the resolved chain-neutral StrKey string (chapter 14 step 7's Delegate migration).
// newLockState is the updated lockInfoV1 private state's own ID - see encodeSNotoLockArgs's own
// doc comment on the mechanism this is part of.
func encodeSNotoDelegateLockArgs(txID, lockID pldtypes.Bytes32, delegate string, data []byte, newLockState pldtypes.Bytes32) (argsXDR []byte, argsJSON string, err error) {
	txIDVal, err := scValBytes(txID[:])
	if err != nil {
		return nil, "", err
	}
	lockIDVal, err := scValBytes(lockID[:])
	if err != nil {
		return nil, "", err
	}
	delegateVal, err := scValAddress(delegate)
	if err != nil {
		return nil, "", err
	}
	dataVal, err := scValBytes(data)
	if err != nil {
		return nil, "", err
	}
	newLockStateVal, err := scValBytes(newLockState[:])
	if err != nil {
		return nil, "", err
	}

	args := xdr.ScVec{txIDVal, lockIDVal, delegateVal, dataVal, newLockStateVal}
	var buf bytes.Buffer
	if _, err := xdr.Marshal(&buf, args); err != nil {
		return nil, "", err
	}

	argsJSONBytes, err := json.Marshal(map[string]any{
		"tx_id":          txID.String(),
		"lock_id":        lockID.String(),
		"delegate":       delegate,
		"data":           pldtypes.HexBytes(data).String(),
		"new_lock_state": newLockState.String(),
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
