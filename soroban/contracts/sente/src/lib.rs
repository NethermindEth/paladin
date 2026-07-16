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

//! SentePrivacyGroup (chapter 14 §14.3, phase S3) - the on-chain anchor for a Sente private
//! Soroban group, the Stellar translation of `PentePrivacyGroup.sol`.
//!
//! **State model**: unlike Pente's per-account UTXO `_unspent` mapping, Sente's off-chain state is
//! already the per-*ledger-entry* `SenteEntry` model built in S2 - so the on-chain contract only
//! needs a single hash-chain head (`root`), not a UTXO set. `transition` swaps `old_root` for
//! `new_root`; a caller can never present a stale `old_root`, because `old_root` is read from this
//! contract's own storage, never taken as a parameter - replaying an already-applied transition
//! recomputes a payload over the *current* (already-advanced) root, which no longer matches what
//! members signed off-chain, so `saladin_typed_data::verify` fails on the stale signatures. No
//! separate nonce/tx-id tracking is needed for replay protection.
//!
//! **Signatures**: ed25519 (Stellar's native scheme) in place of Pente's ECDSA/secp256k1,
//! `SALADIN_TYPED_DATA_V0("sente.Transition", ...)` in place of EIP-712 - otherwise the same
//! `validateEndorsements` shape as `PentePrivacyGroup.sol`: every member must sign (100% threshold,
//! "as Pente v1"), duplicate signers are rejected, non-member signers are rejected.
//!
//! **External calls**: Pente indirects through an event a Solidity contract running inside the
//! private EVM emits, later parsed by the domain plugin. Soroban contracts can call other
//! contracts directly and atomically, so `transition` invokes each `AtomOperation` in
//! `external_calls` itself via `env.invoke_contract` - no log-parsing indirection needed. The
//! `AtomOperation{contract, function, args}` type is the same one `satom` uses, factored out into
//! the contract-free `atom-operation` crate rather than depended on from `satom` directly -
//! depending on `satom` itself pulls its `#[contract]` code into this crate's own wasm build and
//! collides on exported symbol names (`initialize` is exported by both contracts); a plain rlib
//! with no `#[contract]` has nothing to collide.
#![no_std]

mod storage;

use atom_operation::AtomOperation;
use saladin_typed_data::{current_contract_id, verify};
use soroban_sdk::xdr::ToXdr;
use soroban_sdk::{
    contract, contractevent, contractimpl, contracttype, Bytes, BytesN, Env, String, Vec,
};

/// A **tuple** struct, not a named-field one - same reasoning as `snoto::UnlockPayload`: XDR
/// encoding of a tuple struct is a plain positional `ScVal::Vec`, trivial to reproduce from any
/// off-chain language computing the same digest, unlike a named struct's by-field-name sort order.
/// `tx_id` is folded into the signed payload (not just the event) so a signature can't be replayed
/// against a transition claiming a different Paladin transaction id.
#[contracttype]
pub struct TransitionPayload(
    pub BytesN<32>, /* tx_id */
    pub BytesN<32>, /* old_root */
    pub BytesN<32>, /* new_root */
    pub Vec<AtomOperation>,
);

/// `tx_id` (also `Genesis::tx_id` below) is what lets the Go-side event indexer correlate this
/// on-chain confirmation back to the private Paladin transaction that produced it (chapter 14
/// §14.3's Go-integration work) - the same `#[topic] tx_id` convention `factory::Registration`
/// already established.
#[contractevent(topics = ["transition"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Transition {
    #[topic]
    pub tx_id: BytesN<32>,
    #[topic]
    pub old_root: BytesN<32>,
    pub new_root: BytesN<32>,
    pub external_call_count: u32,
}

/// Published once, by `initialize` - the only event a group's genesis produces (`initialize`
/// itself has no on-chain concept of "config" beyond its own arguments, unlike `factory::register`
/// which carries an opaque `config` blob). Lets the Go-side event indexer construct the group's
/// genesis `SenteEntry` (root = `[0; 32]`) directly from the deploy transaction's own event batch,
/// without needing a separate out-of-band state-population mechanism - see
/// `saladin-book/part-2-saladin/14-domain-ports.md` §14.3 S3's Go-integration section.
#[contractevent(topics = ["genesis"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Genesis {
    #[topic]
    pub tx_id: BytesN<32>,
    pub members: Vec<BytesN<32>>,
    pub network_passphrase: Bytes,
}

#[contract]
pub struct Contract;

#[contractimpl]
impl Contract {
    /// Membership fixed at genesis, "as Pente v1" - there is no add/remove-member entry point.
    /// `network_passphrase` is needed on-chain to recompute `SALADIN_TYPED_DATA_V0` digests, the
    /// same `config`-as-raw-passphrase-bytes convention `snoto::initialize` already established.
    /// `tx_id` is the deploying Paladin transaction's id, passed straight through from
    /// `sente-factory::deploy_group` - carried only to publish on `Genesis` (see that event's own
    /// doc comment), not stored.
    pub fn initialize(env: Env, members: Vec<BytesN<32>>, network_passphrase: Bytes, tx_id: BytesN<32>) {
        if storage::is_initialized(&env) {
            panic!("sente: already initialized");
        }
        if members.is_empty() {
            panic!("sente: a privacy group needs at least one member");
        }
        storage::init(&env, &members, &network_passphrase);
        Genesis {
            tx_id,
            members,
            network_passphrase,
        }
        .publish(&env);
    }

    /// The current hash-chain head - what an assembling node must treat as `old_root` when
    /// building the next transition's payload.
    pub fn root(env: Env) -> BytesN<32> {
        storage::root(&env)
    }

    pub fn members(env: Env) -> Vec<BytesN<32>> {
        storage::members(&env)
    }

    /// Verifies 100% of members' ed25519 signatures over
    /// `SALADIN_TYPED_DATA_V0("sente.Transition", {old_root, new_root, external_calls})`, advances
    /// the stored root, then invokes every `external_calls` leg directly - atomic for free via
    /// Soroban's own cross-contract panic-unwind semantics, the same property `satom::execute`
    /// already relies on.
    pub fn transition(
        env: Env,
        tx_id: BytesN<32>,
        new_root: BytesN<32>,
        external_calls: Vec<AtomOperation>,
        signatures: Vec<(BytesN<32>, BytesN<64>)>,
    ) {
        let members = storage::members(&env);
        if signatures.len() != members.len() {
            panic!("sente: all members must endorse a transition");
        }

        let old_root = storage::root(&env);
        let payload = TransitionPayload(
            tx_id.clone(),
            old_root.clone(),
            new_root.clone(),
            external_calls.clone(),
        );
        let payload_xdr = payload.to_xdr(&env);
        let passphrase = storage::network_passphrase(&env);
        let contract_id = current_contract_id(&env);
        let type_name = String::from_str(&env, "sente.Transition");

        let mut seen: Vec<BytesN<32>> = Vec::new(&env);
        for (public_key, signature) in signatures.iter() {
            if !members.contains(&public_key) {
                panic!("sente: signer is not a member of this group");
            }
            if seen.contains(&public_key) {
                panic!("sente: duplicate signer");
            }
            seen.push_back(public_key.clone());
            verify(
                &env,
                &public_key,
                &passphrase,
                &contract_id,
                &type_name,
                &payload_xdr,
                &signature,
            );
        }

        storage::set_root(&env, &new_root);

        for op in external_calls.iter() {
            let _: soroban_sdk::Val = env.invoke_contract(&op.contract, &op.function, op.args.clone());
        }

        Transition {
            tx_id,
            old_root,
            new_root,
            external_call_count: external_calls.len(),
        }
        .publish(&env);
    }
}

#[cfg(test)]
mod test;
