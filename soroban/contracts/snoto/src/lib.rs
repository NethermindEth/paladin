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

//! SNoto (chapter 13 §13.2) - the notarized token.
//!
//! Placeholder contract: this is chapter 13 Phase 0 (toolchain bootstrap) - it exists now so the
//! Cargo workspace, Gradle build, and CI pipeline are proven end-to-end (compile, unit test,
//! `stellar contract build`) before any real contract logic is written. The real `DataKey`/
//! `SNoto` trait implementation lands in Phase 3.
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
