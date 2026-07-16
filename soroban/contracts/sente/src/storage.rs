// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

use soroban_sdk::{contracttype, Bytes, BytesN, Env, Vec};

#[contracttype]
pub enum DataKey {
    Members,
    NetworkPassphrase,
    Root,
}

pub fn is_initialized(env: &Env) -> bool {
    env.storage().instance().has(&DataKey::Root)
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
