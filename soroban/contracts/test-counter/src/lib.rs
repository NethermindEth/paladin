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

//! test-counter - not a production contract. Exists solely to give Sente's real-invocation wiring
//! (chapter 14 §14.3 S3, `domains/sente/crates/sente/src/domain.rs`'s `assemble_transaction`/
//! `endorse_transaction`) a genuinely *stateful* target to prove real `soroban-env-host`
//! recording-mode re-execution against: every other contract in this workspace either has no
//! persistent-storage side effects at all (`factory::register` - see its own doc comment) or
//! requires pre-existing state/`require_auth` that would complicate the proof unnecessarily
//! (`snoto`/`szeto`/`satom`). One unauthenticated, persistent-storage-mutating function is the
//! simplest real invocation that actually exercises `modified_entries`, mirroring exactly the
//! reasoning Phase 1's own spike used to pick `factory::register` in the first place.
#![no_std]

use soroban_sdk::{contract, contractimpl, symbol_short, Env, Symbol};

const COUNT: Symbol = symbol_short!("COUNT");

#[contract]
pub struct Contract;

#[contractimpl]
impl Contract {
    /// Increments a single persistent `u32` counter and returns the new value - deliberately not
    /// `require_auth`-gated, so this can be invoked in `soroban-simulation`'s recording mode with
    /// no signer available at assemble/endorse time, the same constraint Sente's own group
    /// `transition` call is under (see `domain.rs`'s own module doc comment).
    pub fn bump(env: Env) -> u32 {
        let count: u32 = env.storage().persistent().get(&COUNT).unwrap_or(0);
        let next = count + 1;
        env.storage().persistent().set(&COUNT, &next);
        next
    }
}

#[cfg(test)]
mod test {
    use super::*;
    use soroban_sdk::testutils::Address as _;
    use soroban_sdk::Address;

    #[test]
    fn bump_increments_persistent_counter() {
        let env = Env::default();
        let contract_id = env.register(Contract, ());
        let client = ContractClient::new(&env, &contract_id);

        assert_eq!(client.bump(), 1);
        assert_eq!(client.bump(), 2);
        assert_eq!(client.bump(), 3);
    }

    #[test]
    fn bump_needs_no_authorization() {
        let env = Env::default();
        let contract_id = env.register(Contract, ());
        let client = ContractClient::new(&env, &contract_id);

        // No env.mock_all_auths() and no require_auth call inside bump() - if this compiles and
        // passes, bump() genuinely needs no signer, matching the doc comment above.
        let caller = Address::generate(&env);
        let _ = caller;
        assert_eq!(client.bump(), 1);
    }
}
