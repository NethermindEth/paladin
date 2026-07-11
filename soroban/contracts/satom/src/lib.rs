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

//! SAtom (chapter 13 §13.4) - atomic multi-domain settlement.
//!
//! Placeholder contract: scaffolded as a workspace member during chapter 13 Phase 0 (toolchain
//! bootstrap) so later phases don't need to edit the workspace member list. Real logic follows
//! once SNoto (and likely SZeto) exist for it to settle against - see the chapter 13 plan,
//! Phase 5.
#![no_std]

use soroban_sdk::{contract, contractimpl, Env};

#[contract]
pub struct Contract;

#[contractimpl]
impl Contract {
    pub fn ping(_env: Env) -> u32 {
        1
    }
}

#[cfg(test)]
mod test {
    use super::*;
    use soroban_sdk::Env;

    #[test]
    fn ping_works() {
        let env = Env::default();
        let contract_id = env.register(Contract, ());
        let client = ContractClient::new(&env, &contract_id);
        assert_eq!(client.ping(), 1);
    }
}
