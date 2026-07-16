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

//! SAtom (chapter 13 §13.4) - atomic multi-domain settlement. Chapter 13 Part B phase B.5.
//!
//! Deployed per-settlement by `SAtomFactory` (`satom-factory` crate), salt = `hash(operations)`.
//! `execute()` loops `env.invoke_contract` over each leg - Soroban's own cross-contract call
//! semantics already make this atomic (any panic anywhere unwinds the entire top-level
//! invocation), so no special rollback/two-phase logic is needed here at all.
//!
//! **Authorization, confirmed against real `soroban-sdk` source/doc comments this session**: a
//! leg's callee (e.g. SNoto's `unlock`) calls `require_auth()` on its stored `delegate: Address`.
//! When that delegate was set (via SNoto's own `delegate_lock`) to *this* SAtom instance's own
//! contract address, `require_auth()` is satisfied automatically with zero explicit auth entries,
//! per `soroban-sdk`'s own doc comment on invoker authorization: "All the direct calls that the
//! current contract performs are always considered to have been authorized." This matches the
//! book's two design rules exactly: all party authorization was already consumed at lock/prepare
//! time (chapter 13 §13.4), and `execute()` itself needs no signatures from possibly-offline
//! parties - only the direct-call invoker auth SAtom provides for free.
//!
//! `cancel`'s "any party" check is a genuine design decision the book leaves unspecified (its own
//! operations' target *contracts* aren't necessarily the authorizing *parties* - e.g. a lock's
//! real owner is not the domain contract itself) - resolved here as an explicit `parties: Vec
//! <Address>` list passed at `initialize`, checked against the caller-supplied canceller in
//! `cancel`.
#![no_std]

mod storage;

pub use atom_operation::AtomOperation;
use soroban_sdk::{contract, contractevent, contractimpl, Address, Env, Val, Vec};
use storage::Status;

#[contractevent(topics = ["executed"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Executed {
    pub operation_count: u32,
}

#[contractevent(topics = ["cancelled"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Cancelled {
    pub canceller: Address,
}

#[contract]
pub struct Contract;

#[contractimpl]
impl Contract {
    pub fn initialize(env: Env, operations: Vec<AtomOperation>, parties: Vec<Address>) {
        if storage::is_initialized(&env) {
            panic!("satom: already initialized");
        }
        storage::init(&env, &operations, &parties);
    }

    /// Executes every leg in order, atomically - any panic in any leg unwinds the whole call (a
    /// property of Soroban's own cross-contract semantics, not something this function
    /// implements itself). Rejects if already executed or cancelled (`storage::transition`'s
    /// check-then-set), so this can never run twice nor run after `cancel`.
    pub fn execute(env: Env) {
        let operations = storage::operations(&env);
        storage::transition(&env, Status::Executed);

        for op in operations.iter() {
            let _: Val = env.invoke_contract(&op.contract, &op.function, op.args.clone());
        }

        Executed {
            operation_count: operations.len(),
        }
        .publish(&env);
    }

    /// `canceller` must be one of the parties passed to `initialize`, and must authorize the
    /// call itself - there is no single Soroban primitive for "any one of N addresses
    /// authorizes", so the caller names which party is cancelling and that specific address is
    /// checked against the allow-list and then `require_auth()`-ed.
    pub fn cancel(env: Env, canceller: Address) {
        let parties = storage::parties(&env);
        if !parties.contains(&canceller) {
            panic!("satom: canceller is not a party to this settlement");
        }
        canceller.require_auth();

        storage::transition(&env, Status::Cancelled);

        Cancelled { canceller }.publish(&env);
    }
}

#[cfg(test)]
mod test;
