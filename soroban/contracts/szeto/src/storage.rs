// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

use soroban_sdk::{contracttype, Address, BytesN, Env, U256};

/// Same TTL constants as SNoto (`soroban/contracts/snoto/src/storage.rs`), derived from
/// Stellar's ~5s ledger close time.
pub const TTL_THRESHOLD_LEDGERS: u32 = 1_555_200; // ~90 days at ~5s/ledger
pub const TTL_EXTEND_TO_LEDGERS: u32 = 3_110_400; // ~180 days at ~5s/ledger

#[contracttype]
pub enum DataKey {
    Notary,
    Nullifier(BytesN<32>),
    TxId(BytesN<32>),
    TreeNode(U256),
    TreeRoot,
    TreeRootExists(U256),
}

pub fn has_notary(env: &Env) -> bool {
    env.storage().instance().has(&DataKey::Notary)
}

pub fn init_notary(env: &Env, notary: &Address) {
    env.storage().instance().set(&DataKey::Notary, notary);
}

pub fn notary(env: &Env) -> Address {
    env.storage()
        .instance()
        .get(&DataKey::Notary)
        .unwrap_or_else(|| panic!("not initialized"))
}

pub fn is_spent(env: &Env, nullifier: &BytesN<32>) -> bool {
    env.storage()
        .persistent()
        .has(&DataKey::Nullifier(nullifier.clone()))
}

pub fn mark_spent(env: &Env, nullifier: &BytesN<32>) {
    let key = DataKey::Nullifier(nullifier.clone());
    env.storage().persistent().set(&key, &());
    env.storage()
        .persistent()
        .extend_ttl(&key, TTL_THRESHOLD_LEDGERS, TTL_EXTEND_TO_LEDGERS);
}

/// Panics if `tx_id` has already been used - replay protection for `transfer`, mirroring SNoto's
/// own `mark_tx_used`.
pub fn mark_tx_used(env: &Env, tx_id: &BytesN<32>) {
    let key = DataKey::TxId(tx_id.clone());
    if env.storage().persistent().has(&key) {
        panic!("szeto: tx_id already used");
    }
    env.storage().persistent().set(&key, &());
    env.storage()
        .persistent()
        .extend_ttl(&key, TTL_THRESHOLD_LEDGERS, TTL_EXTEND_TO_LEDGERS);
}
