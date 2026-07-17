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

//! SAtomFactory (chapter 13 §13.5, Part B phase B.5) - deploys one SAtom instance per settlement
//! and registers it with `SaladinFactory` (`contracts/factory`), in the same invocation, matching
//! the book's own phrasing exactly ("Domain factories... deploy the instance... and register it
//! in the same invocation").
//!
//! `salt = hash(operations)` (book's own spec for SAtom's deploy salt) - computed here as
//! `sha256(operations.to_xdr())`, XDR being deterministic/canonical by spec (the same reasoning
//! `SALADIN_TYPED_DATA_V0`, chapter 13 §13.1, already relies on elsewhere in this codebase).
//!
//! Uses `deployer().with_current_contract(salt)`, not `with_address` - confirmed against
//! `soroban-sdk`'s own doc comments this session: `with_current_contract` deploys *as the calling
//! contract itself* (no separate auth needed), while `with_address` requires that address to
//! explicitly authorize the deployment. `SaladinFactory::register` is itself not `require_auth`-
//! gated (see `contracts/factory`'s own doc comment), so this whole flow needs no signatures
//! beyond whatever `SAtom::initialize` itself requires (none, today).
#![no_std]

use satom::AtomOperation;
use soroban_sdk::{
    contract, contractimpl, xdr::ToXdr, Address, Bytes, BytesN, Env, IntoVal, String, Symbol, Val,
    Vec,
};

#[contract]
pub struct Contract;

#[contractimpl]
impl Contract {
    /// Deploys a new SAtom instance for `operations`/`parties`, initializes it, and registers it
    /// with `saladin_factory` under `tx_id`/`config` (`SaladinFactory::register`'s own shape,
    /// `contracts/factory`). Returns the deployed instance's address.
    pub fn deploy_settlement(
        env: Env,
        wasm_hash: BytesN<32>,
        operations: Vec<AtomOperation>,
        parties: Vec<Address>,
        saladin_factory: Address,
        tx_id: BytesN<32>,
        config: Bytes,
    ) -> Address {
        let salt: BytesN<32> = env
            .crypto()
            .sha256(&operations.clone().to_xdr(&env))
            .to_bytes();

        let satom_address = env
            .deployer()
            .with_current_contract(salt)
            .deploy_v2(wasm_hash, ());

        let initialize = Symbol::new(&env, "initialize");
        let init_args: Vec<Val> =
            soroban_sdk::vec![&env, operations.into_val(&env), parties.into_val(&env),];
        let _: Val = env.invoke_contract(&satom_address, &initialize, init_args);

        let register = Symbol::new(&env, "register");
        let register_args: Vec<Val> = soroban_sdk::vec![
            &env,
            tx_id.into_val(&env),
            satom_address.into_val(&env),
            config.into_val(&env),
            String::from_str(&env, "").into_val(&env),
        ];
        let _: Val = env.invoke_contract(&saladin_factory, &register, register_args);

        satom_address
    }
}

#[cfg(test)]
mod test;
