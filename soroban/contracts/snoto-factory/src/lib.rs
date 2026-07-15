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

//! SNotoFactory (chapter 13 §13.5, chapter 14 step 6) - deploys one SNoto instance and registers
//! it with `SaladinFactory` (`contracts/factory`), in the same invocation, matching the book's own
//! phrasing exactly ("Domain factories... deploy the instance... and register it in the same
//! invocation") - structurally identical to `contracts/satom-factory`'s `deploy_settlement`.
//!
//! **Salt is `tx_id` itself, not a derived hash.** `satom-factory` hashes its settlement
//! `operations` because multiple parties independently compute the same settlement's salt and
//! must agree on it without prior coordination. SNoto has exactly one deployer (the notary, the
//! only party ever authorized to create a new instance) and `tx_id` is already a unique,
//! replay-protected `BytesN<32>` (Paladin's own private-transaction ID) - so reusing it directly
//! as the deploy salt needs no extra hashing and no loss of security, the same reasoning already
//! applied to `snoto`'s own `lock_id = tx_id` simplification (`contracts/snoto/src/lib.rs`).
//!
//! Uses `deployer().with_current_contract(salt)`, not `with_address` - same reasoning as
//! `satom-factory`: it deploys *as the calling contract itself* (no separate auth needed), while
//! `SaladinFactory::register` is itself not `require_auth`-gated (`contracts/factory`'s own doc
//! comment), so this whole flow needs no signatures beyond whatever `SNoto::initialize` itself
//! requires (none, today).
#![no_std]

use soroban_sdk::{contract, contractimpl, Address, Bytes, BytesN, Env, IntoVal, Symbol, Val, Vec};

#[contract]
pub struct Contract;

#[contractimpl]
impl Contract {
    /// Deploys a new SNoto instance for `notary`/`config`/`sac`, initializes it, and registers it
    /// with `saladin_factory` under `tx_id`/`config` (`SaladinFactory::register`'s own shape,
    /// `contracts/factory`). Returns the deployed instance's address.
    pub fn deploy(
        env: Env,
        wasm_hash: BytesN<32>,
        notary: Address,
        config: Bytes,
        sac: Address,
        saladin_factory: Address,
        tx_id: BytesN<32>,
    ) -> Address {
        let snoto_address = env
            .deployer()
            .with_current_contract(tx_id.clone())
            .deploy_v2(wasm_hash, ());

        let initialize = Symbol::new(&env, "initialize");
        let init_args: Vec<Val> = soroban_sdk::vec![
            &env,
            notary.into_val(&env),
            config.into_val(&env),
            sac.into_val(&env),
        ];
        let _: Val = env.invoke_contract(&snoto_address, &initialize, init_args);

        let register = Symbol::new(&env, "register");
        let register_args: Vec<Val> = soroban_sdk::vec![
            &env,
            tx_id.into_val(&env),
            snoto_address.into_val(&env),
            config.into_val(&env),
        ];
        let _: Val = env.invoke_contract(&saladin_factory, &register, register_args);

        snoto_address
    }
}

#[cfg(test)]
mod test;
