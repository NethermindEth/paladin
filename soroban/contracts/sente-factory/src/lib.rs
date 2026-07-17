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

//! SenteFactory (chapter 14 §14.3, phase S3) - deploys one `SentePrivacyGroup` instance per group
//! genesis and registers it with `SaladinFactory` (`contracts/factory`), in the same invocation -
//! structurally identical to `contracts/snoto-factory`/`contracts/satom-factory`'s own
//! `deploy`/`deploy_settlement`.
//!
//! **`salt = sha256(members.to_xdr())`, not `tx_id`.** `snoto-factory` reuses `tx_id` directly
//! because SNoto has exactly one deployer (the fixed notary). A Sente privacy group's genesis is
//! the multi-party case `satom-factory` already established the pattern for: every member
//! independently assembles/endorses the genesis transaction and must arrive at the same deployed
//! address without prior coordination, so the salt is derived from the content
//! (`members`) they all already agree on, not from a value only the assembling node picks.
//!
//! Uses `deployer().with_current_contract(salt)`, not `with_address` - deploys *as this factory
//! contract itself*, needing no separate deployment auth (`SaladinFactory::register` is itself not
//! `require_auth`-gated either, per `contracts/factory`'s own doc comment).
//!
//! **`config` doubles as `SentePrivacyGroup::initialize`'s `network_passphrase`, and as the
//! registration event's `config`** - the same "config is the raw network passphrase bytes"
//! convention `snoto-factory`/`snoto::initialize` already established, needed on-chain for
//! `SentePrivacyGroup::transition`'s `SALADIN_TYPED_DATA_V0` digest recomputation.
#![no_std]

use soroban_sdk::{
    contract, contractimpl, xdr::ToXdr, Address, Bytes, BytesN, Env, IntoVal, String, Symbol, Val,
    Vec,
};

#[contract]
pub struct Contract;

#[contractimpl]
impl Contract {
    /// Deploys a new `SentePrivacyGroup` instance for `members`/`config`, initializes it, and
    /// registers it with `saladin_factory` under `tx_id`/`config`
    /// (`SaladinFactory::register`'s own shape, `contracts/factory`). Returns the deployed
    /// instance's address.
    pub fn deploy_group(
        env: Env,
        wasm_hash: BytesN<32>,
        members: Vec<BytesN<32>>,
        config: Bytes,
        saladin_factory: Address,
        tx_id: BytesN<32>,
    ) -> Address {
        let salt: BytesN<32> = env.crypto().sha256(&members.clone().to_xdr(&env)).to_bytes();

        let sente_address = env
            .deployer()
            .with_current_contract(salt)
            .deploy_v2(wasm_hash, ());

        let initialize = Symbol::new(&env, "initialize");
        let init_args: Vec<Val> = soroban_sdk::vec![
            &env,
            members.into_val(&env),
            config.into_val(&env),
            tx_id.into_val(&env),
        ];
        let _: Val = env.invoke_contract(&sente_address, &initialize, init_args);

        let register = Symbol::new(&env, "register");
        let register_args: Vec<Val> = soroban_sdk::vec![
            &env,
            tx_id.into_val(&env),
            sente_address.into_val(&env),
            config.into_val(&env),
            String::from_str(&env, "").into_val(&env),
        ];
        let _: Val = env.invoke_contract(&saladin_factory, &register, register_args);

        sente_address
    }
}

#[cfg(test)]
mod test;
