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
//! **State model**: the group's own liveness/genesis bookkeeping is still a single hash-chain head
//! (`root`) - `transition` swaps `old_root` for `new_root`; a caller can never present a stale
//! `old_root`, because `old_root` is read from this contract's own storage, never taken as a
//! parameter - replaying an already-applied transition recomputes a payload over the *current*
//! (already-advanced) root, which no longer matches what members signed off-chain, so
//! `saladin_typed_data::verify` fails on the stale signatures.
//!
//! **`inputs`/`outputs` close R21 (ch. 16 §16.1): a real, content-addressed UTXO check, not just
//! positional ordering.** A bare hash chain over `root` enforces total ordering (no replay, no two
//! conflicting transitions can both land) but has no way to verify that a transition's claimed
//! business-state entries are the *correct current version* of anything - that used to be checked
//! exactly once, at endorsement time, with no independent backstop. `inputs`/`outputs` are opaque
//! commitment hashes (`domains/sente/crates/sente::domain::entry_commitment`, computed off-chain
//! over each touched `SenteEntry`'s own content - this contract never computes or interprets them)
//! checked against a real, persistent `storage::Unspent` set, the direct translation of
//! `PentePrivacyGroup.sol`'s own `_unspent` mapping: an input must currently be unspent (else
//! `sente: input not available`) and gets deleted; an output must not already be unspent (else
//! `sente: output already unspent`) and gets inserted. This independently catches a stale or wrong
//! reference regardless of what any endorser believed, the same backstop Pente's chain already had.
//! Empty for a root-only transition (no business invocation) - zero extra on-chain cost in that
//! case, exactly today's behavior.
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
/// against a transition claiming a different Paladin transaction id. `inputs`/`outputs` (R21, ch. 16
/// §16.1) are folded in too, so a signature can't be replayed against a transition claiming
/// different business-state effects than what members actually endorsed.
#[contracttype]
pub struct TransitionPayload(
    pub BytesN<32>,        /* tx_id */
    pub BytesN<32>,        /* old_root */
    pub BytesN<32>,        /* new_root */
    pub Vec<BytesN<32>>,   /* inputs */
    pub Vec<BytesN<32>>,   /* outputs */
    pub Vec<AtomOperation>,
);

/// `tx_id` (also `Genesis::tx_id` below) is what lets the Go-side event indexer correlate this
/// on-chain confirmation back to the private Paladin transaction that produced it (chapter 14
/// §14.3's Go-integration work) - the same `#[topic] tx_id` convention `factory::Registration`
/// already established. `inputs`/`outputs` sit in `data`, not `#[topic]`, since Soroban topics must
/// be simple scalar values, not vectors.
#[contractevent(topics = ["transition"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Transition {
    #[topic]
    pub tx_id: BytesN<32>,
    #[topic]
    pub old_root: BytesN<32>,
    pub new_root: BytesN<32>,
    pub inputs: Vec<BytesN<32>>,
    pub outputs: Vec<BytesN<32>>,
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
            // Idempotent, not a no-op: genesis-state creation on the Go side is purely event-
            // driven (no per-transaction "expected output state" is ever declared for a genesis
            // deploy - see `sente-factory`'s own doc comment on why `deploy_group` needed to
            // become idempotent in the first place), so a caller who reaches an already-live group
            // - a fresh node re-submitting `pgroup_createGroup` against a persistent chain, most
            // concretely - needs a `Genesis` event tied to *this* transaction's own `tx_id` to ever
            // get its local genesis state populated at all. Relying on that node's background
            // indexer to instead discover the *original* Genesis event via historical replay is
            // both unnecessary and unreliable in practice (confirmed empirically: this failed with
            // "group genesis state not found" even on a near-empty chain, not just a long one) -
            // real-time processing of a freshly-emitted event is both simpler and faster than
            // waiting on a historical backfill to catch up. Republishes the group's own actual
            // stored members/passphrase, not this call's arguments - by construction they're
            // identical (deploy_group's salt is derived from members, so any caller reaching this
            // address already agrees on them), but doing so anyway means a mismatched caller can
            // never poison this event with a false membership claim for an existing group.
            Genesis {
                tx_id,
                members: storage::members(&env),
                network_passphrase: storage::network_passphrase(&env),
            }
            .publish(&env);
            return;
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
    /// `SALADIN_TYPED_DATA_V0("sente.Transition", {old_root, new_root, inputs, outputs,
    /// external_calls})`, checks `inputs`/`outputs` against the real `Unspent` set (R21, ch. 16
    /// §16.1), advances the stored root, then invokes every `external_calls` leg directly - atomic
    /// for free via Soroban's own cross-contract panic-unwind semantics, the same property
    /// `satom::execute` already relies on.
    pub fn transition(
        env: Env,
        tx_id: BytesN<32>,
        new_root: BytesN<32>,
        inputs: Vec<BytesN<32>>,
        outputs: Vec<BytesN<32>>,
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
            inputs.clone(),
            outputs.clone(),
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

        // R21's content-addressed check: independent of what any endorser believed, the chain
        // itself verifies every claimed input is currently live and every claimed output isn't
        // already - exactly `PentePrivacyGroup.sol::_transition`'s own `_unspent` check, translated.
        for id in inputs.iter() {
            if !storage::is_unspent(&env, &id) {
                panic!("sente: input not available");
            }
            storage::mark_spent(&env, &id);
        }
        for id in outputs.iter() {
            if storage::is_unspent(&env, &id) {
                panic!("sente: output already unspent");
            }
            storage::mark_unspent(&env, &id);
        }

        storage::set_root(&env, &new_root);

        for op in external_calls.iter() {
            let _: soroban_sdk::Val = env.invoke_contract(&op.contract, &op.function, op.args.clone());
        }

        Transition {
            tx_id,
            old_root,
            new_root,
            inputs,
            outputs,
            external_call_count: external_calls.len(),
        }
        .publish(&env);
    }
}

#[cfg(test)]
mod test;

#[cfg(test)]
mod bench_test;
