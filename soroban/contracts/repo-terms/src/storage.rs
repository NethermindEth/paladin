// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

use soroban_sdk::{contracttype, Address, BytesN, Env};

/// Same ~5s ledger close time assumption as `snoto::storage` - see that module's own comment.
pub const TTL_THRESHOLD_LEDGERS: u32 = 1_555_200; // ~90 days at ~5s/ledger
pub const TTL_EXTEND_TO_LEDGERS: u32 = 3_110_400; // ~180 days at ~5s/ledger

#[contracttype]
pub enum DataKey {
    BankA,
    BankB,
    TermsStateId,
}

pub fn init_parties(env: &Env, bank_a: &Address, bank_b: &Address) {
    env.storage().instance().set(&DataKey::BankA, bank_a);
    env.storage().instance().set(&DataKey::BankB, bank_b);
}

/// `true` once `set_terms` has been called - a repo-terms instance represents exactly one trade,
/// so this alone is both the idempotency guard and the "no amend path in v1" invariant, unlike
/// `snoto::storage::mark_tx_used`'s per-tx_id replay namespace (which guards many calls over a
/// coin's whole lifetime, not a single one-shot instance).
pub fn has_terms(env: &Env) -> bool {
    env.storage().persistent().has(&DataKey::TermsStateId)
}

pub fn set_terms_state_id(env: &Env, terms_state_id: &BytesN<32>) {
    env.storage()
        .persistent()
        .set(&DataKey::TermsStateId, terms_state_id);
    env.storage().persistent().extend_ttl(
        &DataKey::TermsStateId,
        TTL_THRESHOLD_LEDGERS,
        TTL_EXTEND_TO_LEDGERS,
    );
}
