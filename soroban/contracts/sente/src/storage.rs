// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

use soroban_sdk::{contracttype, Bytes, BytesN, Env, Vec};

#[contracttype]
pub enum DataKey {
    Members,
    NetworkPassphrase,
    Root,
    /// One persistent entry per currently-live business-state commitment hash (chapter 16 §16.1
    /// R21's fix) - the direct Soroban translation of `PentePrivacyGroup.sol`'s own
    /// `mapping(bytes32 => bool) _unspent`: each hash is its own storage key (a real, independent
    /// ledger entry), not one big serialized set, so membership is a plain O(1) get/set with no
    /// hashing of its own. Deliberately separate from `Root`: root-only transitions (no business
    /// invocation) touch zero `Unspent` entries and cost exactly what they cost today.
    Unspent(BytesN<32>),
}

pub fn is_initialized(env: &Env) -> bool {
    env.storage().instance().has(&DataKey::Root)
}

/// R21's content-addressed check: does the chain currently consider `id` a live, unspent
/// commitment? Unlike `Root` (a single hash-chain head), this can independently catch a
/// stale/wrong reference regardless of what any endorser believed - see
/// `saladin-book/part-2-saladin/14-domain-ports.md` §14.3 and `16-risk-map.md` R21.
pub fn is_unspent(env: &Env, id: &BytesN<32>) -> bool {
    env.storage()
        .persistent()
        .get(&DataKey::Unspent(id.clone()))
        .unwrap_or(false)
}

pub fn mark_spent(env: &Env, id: &BytesN<32>) {
    env.storage().persistent().remove(&DataKey::Unspent(id.clone()));
}

pub fn mark_unspent(env: &Env, id: &BytesN<32>) {
    env.storage()
        .persistent()
        .set(&DataKey::Unspent(id.clone()), &true);
}

/// Genesis root is all-zero, the same "no prior transition" convention a fresh hash-chain head
/// uses elsewhere in this codebase's design notes. Membership is fixed here and never revisited -
/// there is no add/remove-member entry point, matching Pente v1's own restriction.
pub fn init(env: &Env, members: &Vec<BytesN<32>>, network_passphrase: &Bytes) {
    env.storage().instance().set(&DataKey::Members, members);
    env.storage()
        .instance()
        .set(&DataKey::NetworkPassphrase, network_passphrase);
    env.storage()
        .instance()
        .set(&DataKey::Root, &BytesN::from_array(env, &[0u8; 32]));
}

pub fn members(env: &Env) -> Vec<BytesN<32>> {
    env.storage()
        .instance()
        .get(&DataKey::Members)
        .unwrap_or_else(|| panic!("sente: not initialized"))
}

pub fn network_passphrase(env: &Env) -> Bytes {
    env.storage()
        .instance()
        .get(&DataKey::NetworkPassphrase)
        .unwrap_or_else(|| panic!("sente: not initialized"))
}

pub fn root(env: &Env) -> BytesN<32> {
    env.storage()
        .instance()
        .get(&DataKey::Root)
        .unwrap_or_else(|| panic!("sente: not initialized"))
}

pub fn set_root(env: &Env, new_root: &BytesN<32>) {
    env.storage().instance().set(&DataKey::Root, new_root);
}
