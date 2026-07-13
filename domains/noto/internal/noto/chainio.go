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
// chapter 14 §14.1's "chain-kind switch, not a rewrite" plan. evmChainIO (chainio_evm.go) is the
// sole implementer today - this is a pure refactor, no behavior change; every existing call path
// still resolves to the exact same logic that used to live directly on *Noto, just one hop
// through this interface. A chainio_stellar.go implementer, and the actual chain-kind switch that
// picks between them, is separate, later work (chapter 14 step 4).
//
// Deliberately NOT covered by this interface (see the chapter 14 revision plan for the full
// rationale): identity/address *representation* (identityPair.address, ParsedTransaction.
// ContractAddress, and the persisted NotoCoin/NotoLockedCoin.Owner fields all stay
// *pldtypes.EthAddress - changing that is a data-model change, not a logic seam); TransactionWrapper
// (handlers.go) and its .prepare()/.encode() methods (no real chain-specific decision happens
// there today - it's pure proto marshaling of whatever fields a handler already set; the actual
// prepared-tx *shape* fork is step 4's job once a Soroban alternative exists); buildEndorsePlan
// (handlers.go), a free function called directly by all 12 handler files with a fixed signature -
// changing it would mean touching every handler file, which this pass deliberately avoids; and
// hooks.go's Pente-private-invoke notary mode, which is EVM/Pente-only until Sente exists (tracked
// as a leftover in chapter 14 rather than silently handled here).
type chainIO interface {
	// SigningAlgorithm and VerifierType identify the signing scheme this chain kind uses for
	// sender/notary attestations - today's algorithms.ECDSA_SECP256K1/verifiers.ETH_ADDRESS.
	SigningAlgorithm() string
	VerifierType() string

	// ResolveIdentity resolves a lookup to an address-bearing identity from a verifier list -
	// today's findEthAddressVerifier.
	ResolveIdentity(ctx context.Context, errorDescription, lookup string, verifierList []*prototk.ResolvedVerifier) (*identityPair, error)

	// RecoverSignature recovers the signer address from a signature over an already-encoded
	// payload - today's recoverSignature (secp256k1 direct recovery against the chain ID).
	RecoverSignature(ctx context.Context, payload ethtypes.HexBytes0xPrefix, signature []byte) (*ethtypes.Address0xHex, error)

	// State/message hashing family - today's EIP-712 encoders in states.go. A Soroban chainIO
	// will encode SALADIN_TYPED_DATA_V0 digests here instead (chapter 13 §13.1).
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

	// ComputeLockID mirrors the on-chain contract's own lock-ID derivation - today's
	// keccak256(abi.encode(address(this), msg.sender, txId)) convention. A real Stellar
	// implementation would be trivial here (SNoto's Rust contract already uses lock_id = tx_id,
	// no derivation needed - chapter 13 §13.2), not a sign this method doesn't belong.
	ComputeLockID(ctx context.Context, contractAddress, notaryAddress *pldtypes.EthAddress, txId string) (pldtypes.Bytes32, error)
}
