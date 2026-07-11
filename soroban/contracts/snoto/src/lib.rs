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

//! SNoto (chapter 13 §13.2) - the notarized token, a faithful port of the EVM `Noto` domain's
//! semantics onto Soroban. On-chain state is opaque 32-byte state IDs only - the actual coin
//! data (owner/amount/salt) lives off-chain in Paladin's state store, exactly as it does for
//! EVM Noto; this contract never sees or checks it.
//!
//! Several method shapes are elided with `...` in the book and are real decisions made during
//! implementation (cross-checked against EVM `Noto.sol`/`ILockableCapability.sol`, not settled
//! book fact) - each documented at its call site below:
//! - `transfer`'s `signature: Bytes` is **opaque on-chain**, mirroring EVM Noto exactly (its own
//!   doc comment: "A signature over the original request to the notary, opaque to the
//!   blockchain"). The sender's typed-data signature is checked off-chain during private
//!   endorsement (chapter 14); this contract only relays it through the emitted event.
//! - `lock`'s `lock_id` is `tx_id` itself, not a separately-derived value. EVM computes
//!   `keccak256(address(this), msg.sender, txId)` because multiple distinct callers could create
//!   locks; SNoto only ever has one caller (the fixed notary), so `tx_id`'s own replay-protected
//!   uniqueness already serves as a unique lock identifier with no loss of security.
//! - `prepare_unlock`/`unlock`/`cancel_unlock` implement EVM's commit-reveal pattern
//!   (`spendCommitment`/`cancelCommitment` checked via `_unlockHash` in `Noto.sol`) using
//!   `saladin_typed_data::digest()` in place of EIP-712. `LockInfo.spend_commitment`/
//!   `cancel_commitment` being `None` means "unrestricted", matching EVM's `commitment == 0`
//!   convention.
#![no_std]

mod storage;

use saladin_typed_data::{current_contract_id, digest};
use soroban_sdk::xdr::ToXdr;
use soroban_sdk::{
    contract, contractevent, contractimpl, contracttype, Address, Bytes, BytesN, Env, Vec,
};
use storage::LockInfo;

/// The unlock/cancel_unlock commitment-check payload. A **tuple** struct, not a named-field one -
/// per chapter 13 Phase 1's finding, a named struct's XDR encoding sorts fields by name (an
/// off-chain re-computation of this same commitment would have to replicate that sort exactly),
/// while a tuple struct encodes as a plain positional `ScVal::Vec`, trivial to match from any
/// other language.
#[contracttype]
pub struct UnlockPayload(
    pub BytesN<32>,
    pub Vec<BytesN<32>>,
    pub Vec<BytesN<32>>,
    pub Bytes,
);

#[contractevent(topics = ["transfer"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Transfer {
    #[topic]
    pub tx_id: BytesN<32>,
    pub inputs: Vec<BytesN<32>>,
    pub outputs: Vec<BytesN<32>>,
    pub signature: Bytes,
    pub data: Bytes,
}

#[contractevent(topics = ["lock"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Lock {
    #[topic]
    pub lock_id: BytesN<32>,
    pub inputs: Vec<BytesN<32>>,
    pub locked_outputs: Vec<BytesN<32>>,
    pub signature: Bytes,
    pub data: Bytes,
}

#[contractevent(topics = ["prepare_unlock"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PrepareUnlock {
    #[topic]
    pub lock_id: BytesN<32>,
    pub spend_commitment: BytesN<32>,
    pub cancel_commitment: BytesN<32>,
}

#[contractevent(topics = ["delegate_lock"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct DelegateLock {
    #[topic]
    pub lock_id: BytesN<32>,
    pub delegate: Address,
}

#[contractevent(topics = ["unlock"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Unlock {
    #[topic]
    pub lock_id: BytesN<32>,
    pub locked_inputs: Vec<BytesN<32>>,
    pub outputs: Vec<BytesN<32>>,
    pub data: Bytes,
}

#[contractevent(topics = ["cancel_unlock"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CancelUnlock {
    #[topic]
    pub lock_id: BytesN<32>,
    pub locked_inputs: Vec<BytesN<32>>,
    pub cancel_outputs: Vec<BytesN<32>>,
    pub data: Bytes,
}

#[contract]
pub struct Contract;

#[contractimpl]
impl Contract {
    /// `config` is defined, for this implementation, as the raw network passphrase bytes -
    /// needed on-chain to recompute `SALADIN_TYPED_DATA_V0` digests for the unlock/
    /// cancel_unlock commitment checks below (the chapter 13 crate's `digest()` takes the raw
    /// passphrase, not a pre-hashed value, so it can't be derived from `env.ledger().
    /// network_id()` alone). The book leaves `config` unspecified; this is the concrete meaning
    /// assigned here.
    pub fn initialize(env: Env, notary: Address, config: Bytes) {
        if storage::has_notary(&env) {
            panic!("already initialized");
        }
        storage::init_notary(&env, &notary);
        storage::init_network_passphrase(&env, &config);
    }

    pub fn transfer(
        env: Env,
        tx_id: BytesN<32>,
        inputs: Vec<BytesN<32>>,
        outputs: Vec<BytesN<32>>,
        signature: Bytes,
        data: Bytes,
    ) {
        storage::notary(&env).require_auth();
        storage::mark_tx_used(&env, &tx_id);

        for id in inputs.iter() {
            if !storage::is_unspent(&env, &id) {
                panic!("input not unspent");
            }
            storage::spend(&env, &id);
        }
        for id in outputs.iter() {
            storage::mark_unspent(&env, &id);
        }

        Transfer {
            tx_id,
            inputs,
            outputs,
            signature,
            data,
        }
        .publish(&env);
    }

    pub fn lock(
        env: Env,
        tx_id: BytesN<32>,
        inputs: Vec<BytesN<32>>,
        locked_outputs: Vec<BytesN<32>>,
        signature: Bytes,
        data: Bytes,
    ) {
        let notary = storage::notary(&env);
        notary.require_auth();
        storage::mark_tx_used(&env, &tx_id);

        for id in inputs.iter() {
            if !storage::is_unspent(&env, &id) {
                panic!("input not unspent");
            }
            storage::spend(&env, &id);
        }

        let lock_id = tx_id.clone();
        for id in locked_outputs.iter() {
            storage::mark_locked(&env, &id, &lock_id);
        }
        storage::set_lock(
            &env,
            &lock_id,
            &LockInfo {
                delegate: notary,
                spend_commitment: None,
                cancel_commitment: None,
            },
        );

        Lock {
            lock_id,
            inputs,
            locked_outputs,
            signature,
            data,
        }
        .publish(&env);
    }

    pub fn prepare_unlock(
        env: Env,
        lock_id: BytesN<32>,
        spend_commitment: BytesN<32>,
        cancel_commitment: BytesN<32>,
    ) {
        let notary = storage::notary(&env);
        notary.require_auth();

        let mut lock = storage::get_lock(&env, &lock_id).unwrap_or_else(|| panic!("no such lock"));
        if lock.delegate != notary {
            panic!("lock already delegated");
        }
        lock.spend_commitment = Some(spend_commitment.clone());
        lock.cancel_commitment = Some(cancel_commitment.clone());
        storage::set_lock(&env, &lock_id, &lock);

        PrepareUnlock {
            lock_id,
            spend_commitment,
            cancel_commitment,
        }
        .publish(&env);
    }

    pub fn delegate_lock(env: Env, lock_id: BytesN<32>, delegate: Address) {
        let mut lock = storage::get_lock(&env, &lock_id).unwrap_or_else(|| panic!("no such lock"));
        lock.delegate.require_auth();
        if lock.spend_commitment.is_none() || lock.cancel_commitment.is_none() {
            panic!("lock not yet prepared");
        }
        lock.delegate = delegate.clone();
        storage::set_lock(&env, &lock_id, &lock);

        DelegateLock { lock_id, delegate }.publish(&env);
    }

    pub fn unlock(
        env: Env,
        lock_id: BytesN<32>,
        locked_inputs: Vec<BytesN<32>>,
        outputs: Vec<BytesN<32>>,
        data: Bytes,
    ) {
        let lock = storage::get_lock(&env, &lock_id).unwrap_or_else(|| panic!("no such lock"));
        lock.delegate.require_auth();

        check_commitment(
            &env,
            &lock_id,
            &locked_inputs,
            &outputs,
            &data,
            &lock.spend_commitment,
            "snoto.Unlock",
        );

        spend_locked_states(&env, &lock_id, &locked_inputs);
        for id in outputs.iter() {
            storage::mark_unspent(&env, &id);
        }
        storage::remove_lock(&env, &lock_id);

        Unlock {
            lock_id,
            locked_inputs,
            outputs,
            data,
        }
        .publish(&env);
    }

    pub fn cancel_unlock(
        env: Env,
        lock_id: BytesN<32>,
        locked_inputs: Vec<BytesN<32>>,
        cancel_outputs: Vec<BytesN<32>>,
        data: Bytes,
    ) {
        let lock = storage::get_lock(&env, &lock_id).unwrap_or_else(|| panic!("no such lock"));
        lock.delegate.require_auth();

        check_commitment(
            &env,
            &lock_id,
            &locked_inputs,
            &cancel_outputs,
            &data,
            &lock.cancel_commitment,
            "snoto.CancelUnlock",
        );

        spend_locked_states(&env, &lock_id, &locked_inputs);
        for id in cancel_outputs.iter() {
            storage::mark_unspent(&env, &id);
        }
        storage::remove_lock(&env, &lock_id);

        CancelUnlock {
            lock_id,
            locked_inputs,
            cancel_outputs,
            data,
        }
        .publish(&env);
    }

    /// Anyone may extend TTLs - no `require_auth()` at all. Silently skips ids that are neither
    /// `Unspent` nor `Locked` rather than panicking, so a single stale id in a large batch
    /// doesn't abort the whole keepalive sweep.
    pub fn keepalive(env: Env, state_ids: Vec<BytesN<32>>) {
        for id in state_ids.iter() {
            storage::keepalive_one(&env, &id);
        }
    }
}

fn spend_locked_states(env: &Env, lock_id: &BytesN<32>, locked_inputs: &Vec<BytesN<32>>) {
    for id in locked_inputs.iter() {
        let owner = storage::locked_owner(env, &id).unwrap_or_else(|| panic!("state not locked"));
        if &owner != lock_id {
            panic!("state locked by a different lock");
        }
        storage::clear_locked(env, &id);
    }
}

/// `None` (unset) means unrestricted - matching EVM's `commitment == 0` convention - so no check
/// is performed. Otherwise recomputes the commitment digest over `(lock_id, locked_inputs,
/// outputs, data)` and panics on mismatch, exactly mirroring `Noto.sol`'s `_unlockHash`/
/// `NotoInvalidUnlockHash` commit-reveal check.
fn check_commitment(
    env: &Env,
    lock_id: &BytesN<32>,
    locked_inputs: &Vec<BytesN<32>>,
    outputs: &Vec<BytesN<32>>,
    data: &Bytes,
    commitment: &Option<BytesN<32>>,
    type_name: &str,
) {
    let Some(expected) = commitment else {
        return;
    };
    let payload = UnlockPayload(
        lock_id.clone(),
        locked_inputs.clone(),
        outputs.clone(),
        data.clone(),
    );
    let payload_xdr = payload.to_xdr(env).to_alloc_vec();
    let passphrase = storage::network_passphrase(env).to_alloc_vec();
    let contract_id = current_contract_id(env).to_array();
    let computed = digest(&passphrase, &contract_id, type_name, &payload_xdr);
    if computed != expected.to_array() {
        panic!("commitment mismatch");
    }
}

#[cfg(test)]
mod test;
