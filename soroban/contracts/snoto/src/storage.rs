// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

use soroban_sdk::{contracttype, Address, Bytes, BytesN, Env};

/// Ledger-count TTL constants derived from Stellar's ~5s ledger close time. Comment the
/// assumption here explicitly so a future protocol change to close time doesn't leave a silent
/// stale constant.
pub const TTL_THRESHOLD_LEDGERS: u32 = 1_555_200; // ~90 days at ~5s/ledger
pub const TTL_EXTEND_TO_LEDGERS: u32 = 3_110_400; // ~180 days at ~5s/ledger

/// Book §13.2's `DataKey` enum, verbatim in shape: `Notary` (instance storage), and
/// `Unspent`/`Locked`/`Lock`/`TxId` (each persistent, individually TTL-managed). `NetworkPassphrase`
/// is this implementation's own addition (instance storage, alongside `Notary`) - see
/// `lib.rs::initialize`'s doc comment for why it's needed.
#[contracttype]
pub enum DataKey {
    Notary,
    NetworkPassphrase,
    Sac,
    Unspent(BytesN<32>),
    Locked(BytesN<32>),
    Lock(BytesN<32>),
    TxId(BytesN<32>),
}

/// Storage-only type (never fed through `saladin_typed_data`, so a named struct is fine - the
/// tuple-struct XDR-canonicalization constraint only applies to hashed/signed payloads).
///
/// No separate `owner` field, unlike EVM Noto's `owner`/`spender` split: Soroban's SNoto only
/// ever has one caller capable of creating a lock (the fixed notary), so `delegate` alone -
/// initially the notary itself, until handed off via `delegate_lock` - is sufficient.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct LockInfo {
    pub delegate: Address,
    pub spend_commitment: Option<BytesN<32>>,
    pub cancel_commitment: Option<BytesN<32>>,
    /// The Paladin-side lockInfoV1 private state ID currently representing this lock off-chain -
    /// set by lock()/prepare_unlock()/delegate_lock() (each of which supersedes the prior state
    /// with a new one), and read back by unlock()/cancel_unlock() to report which state is being
    /// spent. Mirrors Noto.sol's own on-chain `_lockStates[lockId]` mapping - EVM's contract
    /// tracks this itself for the exact same reason (spendLock/cancelLock need to emit it, but
    /// have no other way to know it, since they aren't the call that created/last updated it).
    pub state_id: BytesN<32>,
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

pub fn init_network_passphrase(env: &Env, passphrase: &Bytes) {
    env.storage()
        .instance()
        .set(&DataKey::NetworkPassphrase, passphrase);
}

pub fn network_passphrase(env: &Env) -> Bytes {
    env.storage()
        .instance()
        .get(&DataKey::NetworkPassphrase)
        .unwrap_or_else(|| panic!("not initialized"))
}

/// The pooled Stellar Asset Contract (SAC) address backing shielded balances - chapter 13 Part B
/// phase B.4 (§13.6 native-asset shield/unshield). Set once at `initialize`, never mutated.
pub fn init_sac(env: &Env, sac: &Address) {
    env.storage().instance().set(&DataKey::Sac, sac);
}

pub fn sac(env: &Env) -> Address {
    env.storage()
        .instance()
        .get(&DataKey::Sac)
        .unwrap_or_else(|| panic!("no SAC configured"))
}

pub fn is_unspent(env: &Env, id: &BytesN<32>) -> bool {
    env.storage()
        .persistent()
        .has(&DataKey::Unspent(id.clone()))
}

pub fn mark_unspent(env: &Env, id: &BytesN<32>) {
    let key = DataKey::Unspent(id.clone());
    env.storage().persistent().set(&key, &());
    env.storage()
        .persistent()
        .extend_ttl(&key, TTL_THRESHOLD_LEDGERS, TTL_EXTEND_TO_LEDGERS);
}

pub fn spend(env: &Env, id: &BytesN<32>) {
    env.storage()
        .persistent()
        .remove(&DataKey::Unspent(id.clone()));
}

pub fn locked_owner(env: &Env, id: &BytesN<32>) -> Option<BytesN<32>> {
    env.storage().persistent().get(&DataKey::Locked(id.clone()))
}

pub fn mark_locked(env: &Env, id: &BytesN<32>, lock_id: &BytesN<32>) {
    let key = DataKey::Locked(id.clone());
    env.storage().persistent().set(&key, lock_id);
    env.storage()
        .persistent()
        .extend_ttl(&key, TTL_THRESHOLD_LEDGERS, TTL_EXTEND_TO_LEDGERS);
}

pub fn clear_locked(env: &Env, id: &BytesN<32>) {
    env.storage()
        .persistent()
        .remove(&DataKey::Locked(id.clone()));
}

pub fn get_lock(env: &Env, lock_id: &BytesN<32>) -> Option<LockInfo> {
    env.storage()
        .persistent()
        .get(&DataKey::Lock(lock_id.clone()))
}

pub fn set_lock(env: &Env, lock_id: &BytesN<32>, info: &LockInfo) {
    let key = DataKey::Lock(lock_id.clone());
    env.storage().persistent().set(&key, info);
    env.storage()
        .persistent()
        .extend_ttl(&key, TTL_THRESHOLD_LEDGERS, TTL_EXTEND_TO_LEDGERS);
}

pub fn remove_lock(env: &Env, lock_id: &BytesN<32>) {
    env.storage()
        .persistent()
        .remove(&DataKey::Lock(lock_id.clone()));
}

/// Panics if `tx_id` has already been used - the replay-protection check shared by every
/// notary-authorized entry point (`transfer` and `lock` both consume from this same namespace).
pub fn mark_tx_used(env: &Env, tx_id: &BytesN<32>) {
    let key = DataKey::TxId(tx_id.clone());
    if env.storage().persistent().has(&key) {
        panic!("tx_id already used");
    }
    env.storage().persistent().set(&key, &());
    env.storage()
        .persistent()
        .extend_ttl(&key, TTL_THRESHOLD_LEDGERS, TTL_EXTEND_TO_LEDGERS);
}

/// Extends the TTL of whichever of `Unspent(id)`/`Locked(id)` exist for a given state id,
/// silently doing nothing for an id that is neither (matches `keepalive`'s "anyone may extend
/// TTLs... skip nonexistent ids rather than panicking on a mixed batch" behavior). Lock records
/// and `TxId` replay markers are intentionally not covered here - `keepalive` targets the
/// long-lived coin states the ch.12 `ttlJanitor` is built to sweep, not administrative records.
pub fn keepalive_one(env: &Env, id: &BytesN<32>) {
    let unspent_key = DataKey::Unspent(id.clone());
    if env.storage().persistent().has(&unspent_key) {
        env.storage().persistent().extend_ttl(
            &unspent_key,
            TTL_THRESHOLD_LEDGERS,
            TTL_EXTEND_TO_LEDGERS,
        );
    }
    let locked_key = DataKey::Locked(id.clone());
    if env.storage().persistent().has(&locked_key) {
        env.storage().persistent().extend_ttl(
            &locked_key,
            TTL_THRESHOLD_LEDGERS,
            TTL_EXTEND_TO_LEDGERS,
        );
    }
}
