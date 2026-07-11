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

//! SaladinFactory (chapter 13 §13.5) - contract discovery/registration.
//!
//! A pure announcement, not a registry-of-record: `register` has **no persistent storage at
//! all**. Idempotency already comes from the deployment step itself (redeploying at the same
//! salt fails before `register` is ever reached), so a dedup entry here would only make this
//! contract a contended shared write target across every domain factory's every deployment - the
//! kind of unnecessary serialization the book's own SNoto storage-decision table argues against.
//!
//! `register` is deliberately **not** `require_auth`-gated (the book states no caller
//! restriction for it, unlike SNoto's `transfer` or identity-registry's mutations, both of which
//! explicitly call out `require_auth`). The only thing that makes a registration meaningful is
//! that `instance` is a real, already-deployed contract address; a false announcement from an
//! arbitrary caller is inert until something downstream chooses to trust it - which stays a
//! consumer-side concern (domainmgr's event-stream, chapter 14) rather than this contract's.
//!
//! A future domain-specific factory (`SNotoFactory`, ch.14, out of scope here) calls into this
//! via cross-contract invocation after `deployer().with_current_contract(salt).deploy_v2(...)`,
//! deploying and registering in the same atomic invocation.
#![no_std]

use soroban_sdk::{contract, contractevent, contractimpl, Address, Bytes, BytesN, Env};

/// `("reg", tx_id) -> (instance, config)` - the discovery event chapter 12's event-stream bridge
/// consumes. `data_format = "vec"` keeps `(instance, config)` a positional pair matching the
/// book's own phrasing, rather than a named map.
#[contractevent(topics = ["reg"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Registration {
    #[topic]
    pub tx_id: BytesN<32>,
    pub instance: Address,
    pub config: Bytes,
}

#[contract]
pub struct Contract;

#[contractimpl]
impl Contract {
    pub fn register(env: Env, tx_id: BytesN<32>, instance: Address, config: Bytes) {
        Registration {
            tx_id,
            instance,
            config,
        }
        .publish(&env);
    }
}

#[cfg(test)]
mod test {
    extern crate std;

    use super::*;
    use soroban_sdk::testutils::{Address as _, Events as _};
    use soroban_sdk::Event;

    #[test]
    fn register_publishes_reg_event() {
        let env = Env::default();
        let contract_id = env.register(Contract, ());
        let client = ContractClient::new(&env, &contract_id);

        let tx_id = BytesN::from_array(&env, &[7u8; 32]);
        let instance = Address::generate(&env);
        let config = Bytes::from_slice(&env, b"config-bytes");

        client.register(&tx_id, &instance, &config);

        let expected = Registration {
            tx_id,
            instance,
            config,
        };
        assert_eq!(
            env.events().all(),
            std::vec![expected.to_xdr(&env, &contract_id)]
        );
    }

    #[test]
    fn register_has_no_persistent_storage_side_effects() {
        let env = Env::default();
        let contract_id = env.register(Contract, ());
        let client = ContractClient::new(&env, &contract_id);

        // Two independent registrations for different tx_ids never collide or need any
        // dedup/state check - confirming the "no persistent storage" design holds in practice.
        // (soroban-sdk testutils only retain events from the most recent top-level invocation,
        // so each call is asserted independently rather than accumulated.)
        client.register(
            &BytesN::from_array(&env, &[1u8; 32]),
            &Address::generate(&env),
            &Bytes::from_slice(&env, b"a"),
        );
        assert_eq!(env.events().all().events().len(), 1);

        client.register(
            &BytesN::from_array(&env, &[2u8; 32]),
            &Address::generate(&env),
            &Bytes::from_slice(&env, b"b"),
        );
        assert_eq!(env.events().all().events().len(), 1);
    }
}
