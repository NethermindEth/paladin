// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

use soroban_sdk::{contracttype, BytesN, Env};

use crate::Identity;

pub const TTL_THRESHOLD_LEDGERS: u32 = 1_555_200; // ~90 days at ~5s/ledger
pub const TTL_EXTEND_TO_LEDGERS: u32 = 3_110_400; // ~180 days at ~5s/ledger

#[contracttype]
pub enum DataKey {
    /// Instance storage: `true` means the root identity's owner check is bypassed when
    /// registering root's direct children - the Soroban-idiomatic equivalent of EVM's
    /// sentinel-owner-address trick (Soroban has no analogous "any address" sentinel value safe
    /// to construct, so this is a plain bool flag checked specifically against the root id,
    /// rather than a generic per-identity "owner equals sentinel" field).
    Rootless,
    /// Persistent, individually TTL-managed, one entry per identity.
    Identity(BytesN<32>),
}

pub fn set_rootless(env: &Env, rootless: bool) {
    env.storage().instance().set(&DataKey::Rootless, &rootless);
}

pub fn is_rootless(env: &Env) -> bool {
    env.storage()
        .instance()
        .get(&DataKey::Rootless)
        .unwrap_or(false)
}

pub fn has_identity(env: &Env, id: &BytesN<32>) -> bool {
    env.storage()
        .persistent()
        .has(&DataKey::Identity(id.clone()))
}

pub fn get_identity(env: &Env, id: &BytesN<32>) -> Option<Identity> {
    env.storage()
        .persistent()
        .get(&DataKey::Identity(id.clone()))
}

pub fn set_identity(env: &Env, id: &BytesN<32>, identity: &Identity) {
    let key = DataKey::Identity(id.clone());
    env.storage().persistent().set(&key, identity);
    env.storage()
        .persistent()
        .extend_ttl(&key, TTL_THRESHOLD_LEDGERS, TTL_EXTEND_TO_LEDGERS);
}
