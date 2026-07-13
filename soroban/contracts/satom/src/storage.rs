// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

use soroban_sdk::{contracttype, Address, Env, Vec};

use crate::AtomOperation;

/// A settlement instance is short-lived and single-purpose (one specific set of legs, deployed
/// per-settlement by `SAtomFactory` - book §13.4), so everything lives in instance storage, not
/// persistent - there's no long-lived state to archive/TTL-manage once `execute`/`cancel` runs.
#[contracttype]
pub enum DataKey {
    Operations,
    Parties,
    Status,
}

#[contracttype]
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Status {
    Pending,
    Executed,
    Cancelled,
}

pub fn is_initialized(env: &Env) -> bool {
    env.storage().instance().has(&DataKey::Status)
}

/// `parties` is a genuine design decision the book leaves unspecified ("cancel... by any party"
/// without saying how SAtom itself knows who the parties are) - resolved here as an explicit list
/// passed at `initialize`, since `operations`' target *contracts* aren't necessarily the
/// authorizing *parties* (e.g. a lock's real owner, not the domain contract itself).
pub fn init(env: &Env, operations: &Vec<AtomOperation>, parties: &Vec<Address>) {
    env.storage()
        .instance()
        .set(&DataKey::Operations, operations);
    env.storage().instance().set(&DataKey::Parties, parties);
    env.storage()
        .instance()
        .set(&DataKey::Status, &Status::Pending);
}

pub fn operations(env: &Env) -> Vec<AtomOperation> {
    env.storage()
        .instance()
        .get(&DataKey::Operations)
        .unwrap_or_else(|| panic!("satom: not initialized"))
}

pub fn parties(env: &Env) -> Vec<Address> {
    env.storage()
        .instance()
        .get(&DataKey::Parties)
        .unwrap_or_else(|| panic!("satom: not initialized"))
}

pub fn status(env: &Env) -> Status {
    env.storage()
        .instance()
        .get(&DataKey::Status)
        .unwrap_or_else(|| panic!("satom: not initialized"))
}

/// Panics unless the settlement is still pending, then marks it `Executed`/`Cancelled` - the
/// check-then-set happens together so `execute`/`cancel` can never run twice, nor run after the
/// other has already settled the outcome.
pub fn transition(env: &Env, to: Status) {
    if status(env) != Status::Pending {
        panic!("satom: already settled");
    }
    env.storage().instance().set(&DataKey::Status, &to);
}
