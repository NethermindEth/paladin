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
//! - `lock` accepts an optional unlocked-remainder `outputs` list alongside `locked_outputs`,
//!   matching EVM `Noto`/`ILockableCapability`'s three-list `inputs`/`locked_outputs`/`outputs`
//!   shape (a partial lock spends more input value than it locks, returning the rest as ordinary
//!   unspent states in the same call) - these reuse the same `Unspent` storage `transfer` already
//!   writes its own `outputs` to, so no new storage key is needed.
//! - `prepare_unlock`/`unlock`/`cancel_unlock` implement EVM's commit-reveal pattern
//!   (`spendCommitment`/`cancelCommitment` checked via `_unlockHash` in `Noto.sol`) using
//!   `saladin_typed_data::digest()` in place of EIP-712. `LockInfo.spend_commitment`/
//!   `cancel_commitment` being `None` means "unrestricted", matching EVM's `commitment == 0`
//!   convention.
//! - `prepare_unlock`/`delegate_lock`/`unlock` take their own `tx_id` (distinct from `lock_id`,
//!   unlike `lock`'s own tx_id-as-lock_id shortcut above) purely so Paladin's off-chain
//!   confirmation pipeline has a transaction identifier to correlate the on-chain event back to
//!   the private transaction that submitted it - chapter 14's Go domain event handling needs this
//!   on every event it must confirm a transaction from. `unlock` in particular has no other field
//!   that could serve this purpose: `lock_id` identifies the *lock*, not this spend transaction,
//!   and reusing it as the confirmation key would misattribute the unlock's confirmed states to
//!   whichever transaction originally created the lock.
#![no_std]

mod storage;

use saladin_typed_data::{current_contract_id, digest};
use soroban_sdk::xdr::ToXdr;
use soroban_sdk::{
    contract, contractevent, contractimpl, contracttype, token, Address, Bytes, BytesN, Env,
    MuxedAddress, Vec,
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
    pub outputs: Vec<BytesN<32>>,
    pub signature: Bytes,
    pub data: Bytes,
    /// The Paladin-side lockInfoV1 private state ID assembled off-chain alongside this lock -
    /// opaque to the contract (just echoed back, like inputs/outputs), mirroring Noto.sol's
    /// `NotoLockCreated.newLockState`. Also persisted as this lock's own `state_id` (see
    /// storage::LockInfo), so later prepare_unlock/delegate_lock/unlock/cancel_unlock calls can
    /// report their own transitions of it.
    pub new_lock_state: BytesN<32>,
}

/// `data` carries Paladin's off-chain info-states manifest (encodeTransactionData), same role as
/// transfer/lock/unlock's own `data` field - required so the Go event indexer can confirm the
/// prepared lock's info states (chapter 14 step 7). Paladin's own state transition (the new
/// LockInfo with spend/cancel commitments set) already happened off-chain during Assemble; nothing
/// about `data` is checked on-chain here, mirroring lock/transfer/unlock's own opaque treatment.
#[contractevent(topics = ["prepare_unlock"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PrepareUnlock {
    #[topic]
    pub lock_id: BytesN<32>,
    pub tx_id: BytesN<32>,
    pub spend_commitment: BytesN<32>,
    pub cancel_commitment: BytesN<32>,
    pub data: Bytes,
    /// See `Lock::new_lock_state`'s own doc comment - old_lock_state is this lock's prior
    /// state_id (about to be superseded), new_lock_state is what it becomes.
    pub old_lock_state: BytesN<32>,
    pub new_lock_state: BytesN<32>,
    /// The locked-coin state ID(s) this prepare_unlock references, echoed back opaque (like
    /// lock's own locked_outputs) - mirrors EVM's NotoLockUpdated.contents. Without this, the Go
    /// event indexer (applyLockUpdatedEvent) has no on-chain signal to confirm the locked coin as
    /// a "read" state for this transaction, so BuildReceipt's own lockedInputIDs extraction (used
    /// to build the unlock/cancel externalCalls args) comes back empty - a real bug found live:
    /// SNoto's own on-chain `unlock` then traps with "locked_inputs" empty when it shouldn't be.
    pub contents: Vec<BytesN<32>>,
}

/// See `PrepareUnlock`'s own doc comment for why `data` is here too.
#[contractevent(topics = ["delegate_lock"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct DelegateLock {
    #[topic]
    pub lock_id: BytesN<32>,
    pub tx_id: BytesN<32>,
    pub delegate: Address,
    pub data: Bytes,
    /// See `PrepareUnlock`'s own doc comment for old/new_lock_state.
    pub old_lock_state: BytesN<32>,
    pub new_lock_state: BytesN<32>,
}

#[contractevent(topics = ["unlock"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Unlock {
    #[topic]
    pub lock_id: BytesN<32>,
    pub tx_id: BytesN<32>,
    pub locked_inputs: Vec<BytesN<32>>,
    pub outputs: Vec<BytesN<32>>,
    pub data: Bytes,
    /// The lock's own state_id (storage::LockInfo) at the moment it's fully spent - there's no
    /// new_lock_state counterpart, since unlock consumes the lock entirely rather than
    /// transitioning it to a successor.
    pub old_lock_state: BytesN<32>,
}

/// `tx_id` exists purely for Paladin's off-chain confirmation correlation, same reason `unlock`
/// needed it added (see this module's own doc comment) - `lock_id` alone identifies the *lock*,
/// not this specific cancel transaction.
#[contractevent(topics = ["cancel_unlock"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CancelUnlock {
    #[topic]
    pub lock_id: BytesN<32>,
    pub tx_id: BytesN<32>,
    pub locked_inputs: Vec<BytesN<32>>,
    pub cancel_outputs: Vec<BytesN<32>>,
    pub data: Bytes,
    /// See `Unlock::old_lock_state`'s own doc comment.
    pub old_lock_state: BytesN<32>,
}

/// Shield disclosure profile matches EVM Zeto's own `deposit` (book §13.6): amount and depositor
/// are public. SNoto has no privacy layer at all (coin data already lives entirely off-chain), so
/// this is simply `transfer` with zero real inputs plus a real SAC pull - no ZK proof involved.
#[contractevent(topics = ["deposit"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Deposit {
    #[topic]
    pub tx_id: BytesN<32>,
    pub from: Address,
    pub amount: i128,
    pub outputs: Vec<BytesN<32>>,
    pub data: Bytes,
}

#[contractevent(topics = ["withdraw"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Withdraw {
    #[topic]
    pub tx_id: BytesN<32>,
    pub recipient: Address,
    pub amount: i128,
    pub inputs: Vec<BytesN<32>>,
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
    pub fn initialize(env: Env, notary: Address, config: Bytes, sac: Address) {
        if storage::has_notary(&env) {
            panic!("already initialized");
        }
        storage::init_notary(&env, &notary);
        storage::init_network_passphrase(&env, &config);
        storage::init_sac(&env, &sac);
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
        outputs: Vec<BytesN<32>>,
        signature: Bytes,
        data: Bytes,
        new_lock_state: BytesN<32>,
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
        for id in outputs.iter() {
            storage::mark_unspent(&env, &id);
        }
        storage::set_lock(
            &env,
            &lock_id,
            &LockInfo {
                delegate: notary,
                spend_commitment: None,
                cancel_commitment: None,
                state_id: new_lock_state.clone(),
            },
        );

        Lock {
            lock_id,
            inputs,
            locked_outputs,
            outputs,
            signature,
            data,
            new_lock_state,
        }
        .publish(&env);
    }

    pub fn prepare_unlock(
        env: Env,
        tx_id: BytesN<32>,
        lock_id: BytesN<32>,
        spend_commitment: BytesN<32>,
        cancel_commitment: BytesN<32>,
        data: Bytes,
        new_lock_state: BytesN<32>,
        contents: Vec<BytesN<32>>,
    ) {
        let notary = storage::notary(&env);
        notary.require_auth();
        storage::mark_tx_used(&env, &tx_id);

        let mut lock = storage::get_lock(&env, &lock_id).unwrap_or_else(|| panic!("no such lock"));
        if lock.delegate != notary {
            panic!("lock already delegated");
        }
        let old_lock_state = lock.state_id.clone();
        lock.spend_commitment = Some(spend_commitment.clone());
        lock.cancel_commitment = Some(cancel_commitment.clone());
        lock.state_id = new_lock_state.clone();
        storage::set_lock(&env, &lock_id, &lock);

        PrepareUnlock {
            lock_id,
            tx_id,
            spend_commitment,
            cancel_commitment,
            data,
            old_lock_state,
            new_lock_state,
            contents,
        }
        .publish(&env);
    }

    pub fn delegate_lock(
        env: Env,
        tx_id: BytesN<32>,
        lock_id: BytesN<32>,
        delegate: Address,
        data: Bytes,
        new_lock_state: BytesN<32>,
    ) {
        let mut lock = storage::get_lock(&env, &lock_id).unwrap_or_else(|| panic!("no such lock"));
        lock.delegate.require_auth();
        if lock.spend_commitment.is_none() || lock.cancel_commitment.is_none() {
            panic!("lock not yet prepared");
        }
        storage::mark_tx_used(&env, &tx_id);
        let old_lock_state = lock.state_id.clone();
        lock.delegate = delegate.clone();
        lock.state_id = new_lock_state.clone();
        storage::set_lock(&env, &lock_id, &lock);

        DelegateLock {
            lock_id,
            tx_id,
            delegate,
            data,
            old_lock_state,
            new_lock_state,
        }
        .publish(&env);
    }

    pub fn unlock(
        env: Env,
        tx_id: BytesN<32>,
        lock_id: BytesN<32>,
        locked_inputs: Vec<BytesN<32>>,
        outputs: Vec<BytesN<32>>,
        data: Bytes,
    ) {
        let lock = storage::get_lock(&env, &lock_id).unwrap_or_else(|| panic!("no such lock"));
        lock.delegate.require_auth();
        storage::mark_tx_used(&env, &tx_id);

        check_commitment(
            &env,
            &lock_id,
            &locked_inputs,
            &outputs,
            &data,
            &lock.spend_commitment,
            "snoto.Unlock",
        );

        let old_lock_state = lock.state_id.clone();
        spend_locked_states(&env, &lock_id, &locked_inputs);
        for id in outputs.iter() {
            storage::mark_unspent(&env, &id);
        }
        storage::remove_lock(&env, &lock_id);

        Unlock {
            lock_id,
            tx_id,
            locked_inputs,
            outputs,
            data,
            old_lock_state,
        }
        .publish(&env);
    }

    pub fn cancel_unlock(
        env: Env,
        tx_id: BytesN<32>,
        lock_id: BytesN<32>,
        locked_inputs: Vec<BytesN<32>>,
        cancel_outputs: Vec<BytesN<32>>,
        data: Bytes,
    ) {
        let lock = storage::get_lock(&env, &lock_id).unwrap_or_else(|| panic!("no such lock"));
        lock.delegate.require_auth();
        storage::mark_tx_used(&env, &tx_id);

        check_commitment(
            &env,
            &lock_id,
            &locked_inputs,
            &cancel_outputs,
            &data,
            &lock.cancel_commitment,
            "snoto.CancelUnlock",
        );

        let old_lock_state = lock.state_id.clone();
        spend_locked_states(&env, &lock_id, &locked_inputs);
        for id in cancel_outputs.iter() {
            storage::mark_unspent(&env, &id);
        }
        storage::remove_lock(&env, &lock_id);

        CancelUnlock {
            lock_id,
            tx_id,
            locked_inputs,
            cancel_outputs,
            data,
            old_lock_state,
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

    /// Shields (deposits) `amount` of the pooled SAC asset, admitting `outputs` as new unspent
    /// states - book §13.6. `from` authorizes the real SAC pull via a standalone SEP-41 `approve`
    /// call it submits itself beforehand (mirroring EVM Zeto's own `approve`+`transferFrom`
    /// pattern - `domains/zeto/internal/zeto/fungible/handler_deposit.go`'s `RequiredSigner`),
    /// rather than a bundled non-invoker authorization entry in this same call: this contract
    /// itself is the approved `spender`, so `transfer_from`'s own `spender.require_auth()` passes
    /// via ordinary Soroban invoker authorization (this contract calling into the token contract
    /// as itself) with zero explicit signatures needed here - the same mechanism already proven
    /// for SAtom/Sente's delegated-unlock pattern. The notary also authorizes admission of the
    /// output states, matching every other write path here. No ZK proof is involved - SNoto's coin
    /// data already lives entirely off-chain (see this module's doc comment), so this is exactly
    /// `transfer` with zero real inputs plus a real SAC pull.
    pub fn deposit(
        env: Env,
        tx_id: BytesN<32>,
        from: Address,
        amount: i128,
        outputs: Vec<BytesN<32>>,
        data: Bytes,
    ) {
        if amount <= 0 {
            panic!("deposit amount must be positive");
        }
        storage::notary(&env).require_auth();
        storage::mark_tx_used(&env, &tx_id);

        let sac = storage::sac(&env);
        token::TokenClient::new(&env, &sac).transfer_from(
            &env.current_contract_address(),
            &from,
            &env.current_contract_address(),
            &amount,
        );

        for id in outputs.iter() {
            storage::mark_unspent(&env, &id);
        }

        Deposit {
            tx_id,
            from,
            amount,
            outputs,
            data,
        }
        .publish(&env);
    }

    /// Unshields (withdraws) `amount` of the pooled SAC asset to `recipient`, spending `inputs` -
    /// book §13.6. Notary-authorized and `tx_id`-replay-guarded like `transfer` (there is no
    /// separately-authorizing real party here besides the notary, submitted via an anonymous
    /// channel account per chapter 12's model). The node's own trustline pre-flight (ch. 12) is
    /// meant to reject a `recipient` without an authorized trustline *before* assembly, so
    /// failures there are early and clear; but even if a `G…` recipient without one reaches this
    /// call, the SAC's own `transfer` fails with a genuine decoded `Error(Contract, #13)`
    /// (`TrustlineMissingError`) here too, not an undecodable host trap - see
    /// `test::withdraw_rejects_recipient_without_trustline`.
    pub fn withdraw(
        env: Env,
        tx_id: BytesN<32>,
        recipient: Address,
        amount: i128,
        inputs: Vec<BytesN<32>>,
        data: Bytes,
    ) {
        if amount <= 0 {
            panic!("withdraw amount must be positive");
        }
        storage::notary(&env).require_auth();
        storage::mark_tx_used(&env, &tx_id);

        for id in inputs.iter() {
            if !storage::is_unspent(&env, &id) {
                panic!("input not unspent");
            }
            storage::spend(&env, &id);
        }

        let sac = storage::sac(&env);
        token::TokenClient::new(&env, &sac).transfer(
            &env.current_contract_address(),
            MuxedAddress::from(recipient.clone()),
            &amount,
        );

        Withdraw {
            tx_id,
            recipient,
            amount,
            inputs,
            data,
        }
        .publish(&env);
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
