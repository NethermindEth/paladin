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

//! RepoTermsFactory (chapter 18 §18.3/§18.7) - deploys one `repo-terms` instance and registers it
//! with `SaladinFactory` (`contracts/factory`), in the same invocation. Structurally identical to
//! `contracts/snoto-factory`: salt is `tx_id` itself (one deployer per trade, `tx_id` already a
//! unique replay-protected `BytesN<32>`, same reasoning as `snoto-factory`'s own doc comment),
//! deploys `with_current_contract` (no separate auth needed), and `register` is itself not
//! `require_auth`-gated.
//!
//! **`identity_lookup` carries *both* counterparties, not one.** `SaladinFactory::register`'s
//! `identity_lookup` channel only ever carries a single opaque string (see that contract's own
//! doc comment) - Noto's single-notary case needs only one, but a repo trade has two symmetric
//! bilateral parties. Rather than changing `register`'s own shape (shared by every domain), the
//! caller (the `repo-terms` Go domain plugin's own deploy-prepare step) combines both Paladin
//! identity locators into one delimited string (e.g. `"bankA@node2|bankB@node3"`) before calling
//! `deploy` - this contract passes it through to `register` unexamined, exactly as
//! `snoto-factory` does with a single `notary_lookup`. The Go side's own `InitContract` is what
//! splits it back apart.
//!
//! No network-passphrase `config` bytes are threaded through `initialize` here, unlike
//! `snoto-factory`: `repo-terms::set_terms` does no on-chain signature verification at all (see
//! that function's own doc comment), so there is nothing on-chain that would ever need it. An
//! empty `Bytes` rides through to `register`'s own `config` parameter instead.
#![no_std]

use soroban_sdk::{
    contract, contractimpl, Address, Bytes, BytesN, Env, IntoVal, String, Symbol, Val, Vec,
};

#[contract]
pub struct Contract;

#[contractimpl]
impl Contract {
    /// Deploys a new `repo-terms` instance for `bank_a`/`bank_b`, initializes it, and registers it
    /// with `saladin_factory` under `tx_id`/`identity_lookup` (`SaladinFactory::register`'s own
    /// shape). Returns the deployed instance's address.
    pub fn deploy(
        env: Env,
        wasm_hash: BytesN<32>,
        bank_a: Address,
        bank_b: Address,
        saladin_factory: Address,
        tx_id: BytesN<32>,
        identity_lookup: String,
    ) -> Address {
        let repo_terms_address = env
            .deployer()
            .with_current_contract(tx_id.clone())
            .deploy_v2(wasm_hash, ());

        let initialize = Symbol::new(&env, "initialize");
        let init_args: Vec<Val> =
            soroban_sdk::vec![&env, bank_a.into_val(&env), bank_b.into_val(&env)];
        let _: Val = env.invoke_contract(&repo_terms_address, &initialize, init_args);

        let register = Symbol::new(&env, "register");
        let register_args: Vec<Val> = soroban_sdk::vec![
            &env,
            tx_id.into_val(&env),
            repo_terms_address.into_val(&env),
            Bytes::new(&env).into_val(&env),
            identity_lookup.into_val(&env),
        ];
        let _: Val = env.invoke_contract(&saladin_factory, &register, register_args);

        repo_terms_address
    }
}

#[cfg(test)]
mod test;
