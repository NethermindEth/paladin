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

	"github.com/LFDT-Paladin/paladin/domains/noto/pkg/types"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldapi"
	"github.com/LFDT-Paladin/paladin/sdk/go/pkg/pldtypes"
	"github.com/LFDT-Paladin/paladin/toolkit/pkg/prototk"
	"github.com/hyperledger/firefly-signer/pkg/abi"
	"github.com/hyperledger/firefly-signer/pkg/ethtypes"
)

// chainIO isolates domains/noto's base-ledger-specific logic behind a small interface, per
// chapter 14 §14.1's "chain-kind switch, not a rewrite" plan. evmChainIO (chainio_evm.go) and
// stellarChainIO (chainio_stellar.go) are the two implementers - step 3 landed the interface with
// EVM as sole implementer (a pure refactor); step 4 adds a real Stellar implementer, scoped to
// what the mint path actually exercises (identity resolution, the sender-signature
// verify/recover step, and EncodeTransferUnmasked's state hashing) - transfer(masked)/lock/unlock
// (EncodeTransferMasked/EncodeLock/EncodeUnlock/UnlockHashFromIDs*/EncodeDelegateLock) and
// deploy/invoke ABI selection (SelectFactoryABI/SelectInterfaceABI) are NOT exercised by mint and
// remain explicit "not yet implemented for Stellar" stubs on stellarChainIO - real work for when
// this extends past mint, per chapter 14's own phrasing.
//
// Deliberately NOT covered by this interface (see the chapter 14 revision plan for the full
// rationale): ParsedTransaction.ContractAddress (shared toolkit type, used by both Noto and Zeto)
// stays *ethtypes.Address0xHex - changing it is toolkit-wide, cross-domain work, out of scope
// here. Every EIP-712-family method below still receives this EVM-shaped 20-byte value as its
// "contract" parameter regardless of chain kind (since call sites pass tx.ContractAddress
// unchanged) - stellarChainIO's real estate-hashing methods (like EncodeTransferUnmasked) treat
// it as an opaque placeholder seed for the SALADIN_TYPED_DATA_V0 contract_id (zero-padded to 32
// bytes), NOT a real Stellar contract ID, clearly documented at the implementation. Identity
// *representation* (NotoCoin.Owner, identityPair's new chainAddress field) now uses
// pldtypes.ChainAddress (step 4); TransactionWrapper (handlers.go) stays untouched, as does
// hooks.go's Pente-private-invoke notary mode (EVM/Pente-only until Sente exists, tracked as a
// leftover in chapter 14). buildEndorsePlan (handlers.go, called by all 12 handler files) *is* now
// chain-aware (SigningAlgorithm/VerifierType/SignaturePayloadType above) - endorsement couldn't
// otherwise succeed on Stellar, since a real endorser independently re-verifies the sender's
// signature (stellarChainIO.VerifySignature), which fails outright against an EVM-shaped one.
type chainIO interface {
	// ChainKind identifies which base_ledger.ChainKind this implementer serves ("evm"/"stellar")
	// - lets Prepare() branch on the prepared-tx shape without a new field on *Noto.
	ChainKind() string

	// SigningAlgorithm, VerifierType, and SignaturePayloadType identify the signing scheme this
	// chain kind uses for sender/notary attestations - EVM's ECDSA_SECP256K1/ETH_ADDRESS/
	// OPAQUE_TO_RSV vs Stellar's EDDSA_ED25519/STELLAR_ADDRESS/OPAQUE_TO_EDDSA (a raw 64-byte R||S
	// signature - ed25519 has no recovery/V byte, so it can't share EVM's opaque:rsv shape).
	SigningAlgorithm() string
	VerifierType() string
	SignaturePayloadType() string

	// ResolveIdentity resolves a lookup to an identity from a verifier list - today's
	// findEthAddressVerifier. Populates identityPair.chainAddress (chain-neutral) as well as the
	// legacy identityPair.address (EVM-only, used pervasively by lock/unlock/burn/transfer code
	// this pass doesn't touch) - stellarChainIO leaves .address nil.
	ResolveIdentity(ctx context.Context, errorDescription, lookup string, verifierList []*prototk.ResolvedVerifier) (*identityPair, error)

	// VerifySignature checks a signature over an already-encoded payload against the identity
	// string Paladin resolved as the expected signer (AttestationResult.Verifier.Verifier).
	// Deliberately "verify", not "recover": EVM/secp256k1 signatures are recoverable (get the
	// signer's address back from the signature alone, then compare strings), but Stellar accounts
	// use ed25519, which has no recovery - verification requires the claimed public key up front.
	// A single recover-shaped method can't express both; this shape can.
	VerifySignature(ctx context.Context, payload []byte, signature []byte, expectedVerifier string) (bool, error)

	// State/message hashing family - today's EIP-712 encoders in states.go for EVM. Stellar's
	// EncodeTransferUnmasked/EncodeLock/EncodeUnlock compute real SALADIN_TYPED_DATA_V0 digests
	// (chapter 13 §13.1, sdk/go/pkg/saladintypes.DigestXDR) - EncodeTransferMasked/
	// UnlockHashFromIDs*/EncodeDelegateLock remain stubs (not exercised by mint/transfer/lock/
	// unlock's base handlers; real work for delegate_lock/prepare_unlock/the create-lock variants).
	EncodeTransferUnmasked(ctx context.Context, contract *ethtypes.Address0xHex, inputs, outputs []*types.NotoCoin) (ethtypes.HexBytes0xPrefix, error)
	EncodeTransferMasked(ctx context.Context, contract *ethtypes.Address0xHex, inputs, outputs []*pldapi.StateEncoded, data pldtypes.HexBytes) (ethtypes.HexBytes0xPrefix, error)
	EncodeLock(ctx context.Context, contract *ethtypes.Address0xHex, inputs, outputs []*types.NotoCoin, lockedOutputs []*types.NotoLockedCoin) (ethtypes.HexBytes0xPrefix, error)
	EncodeUnlock(ctx context.Context, contract *ethtypes.Address0xHex, lockedInputs, lockedOutputs []*types.NotoLockedCoin, outputs []*types.NotoCoin) (ethtypes.HexBytes0xPrefix, error)
	UnlockHashFromIDsV0(ctx context.Context, contract *ethtypes.Address0xHex, lockedInputs, lockedOutputs, outputs []string, data pldtypes.HexBytes) (ethtypes.HexBytes0xPrefix, error)
	UnlockHashFromIDsV1(ctx context.Context, contract *ethtypes.Address0xHex, lockID pldtypes.Bytes32, txId string, lockedInputs, outputs []string, data pldtypes.HexBytes) (pldtypes.Bytes32, error)
	EncodeDelegateLock(ctx context.Context, contract *ethtypes.Address0xHex, lockID pldtypes.Bytes32, delegate *pldtypes.EthAddress, data pldtypes.HexBytes) (ethtypes.HexBytes0xPrefix, error)

	// SelectFactoryABI/SelectInterfaceABI pick the on-chain contract build for deploy/invoke -
	// today's factoryV0/V1/V2Build switch in PrepareDeploy and getInterfaceABI.
	SelectFactoryABI(factoryVersion int64) abi.ABI
	SelectInterfaceABI(variant pldtypes.HexUint64) abi.ABI

	// NetworkPassphrase is Stellar-only: SNoto's own initialize() defines "config" as the raw
	// network passphrase bytes (soroban/contracts/snoto/src/lib.rs's own doc comment), needed
	// on-chain to recompute SALADIN_TYPED_DATA_V0 digests. Empty/unused on EVM.
	NetworkPassphrase() string

	// ComputeLockID mirrors the on-chain contract's own lock-ID derivation - today's
	// keccak256(abi.encode(address(this), msg.sender, txId)) convention. A real Stellar
	// implementation would be trivial here (SNoto's Rust contract already uses lock_id = tx_id,
	// no derivation needed - chapter 13 §13.2), not a sign this method doesn't belong.
	ComputeLockID(ctx context.Context, contractAddress, notaryAddress *pldtypes.EthAddress, txId string) (pldtypes.Bytes32, error)
}
