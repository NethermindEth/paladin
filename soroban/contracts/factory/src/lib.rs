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

use soroban_sdk::{contract, contractevent, contractimpl, Address, Bytes, BytesN, Env, String};

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

/// Carries an off-chain identity-locator string (e.g. Noto's notary, `"notary@node1"`) alongside a
/// registration, published from this same `SaladinFactory` context so it lands at the same
/// address domainmgr's registry event stream already watches (chapter 14 §14.3's own `config`
/// channel is committed to carrying whatever bytes the registering contract's on-chain crypto
/// needs unchanged - e.g. a network passphrase - so it can't *also* carry identity metadata;
/// this is the separate, dedicated channel for that, mirroring `Registration`'s own shape rather
/// than inventing an unrelated one). Only published when `identity_lookup` is non-empty, so
/// existing callers that don't need this (Sente, SAtom) are unaffected by passing an empty string.
#[contractevent(topics = ["idreg"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct IdentityRegistered {
    #[topic]
    pub tx_id: BytesN<32>,
    pub identity_lookup: String,
}

#[contract]
pub struct Contract;

#[contractimpl]
impl Contract {
    pub fn register(
        env: Env,
        tx_id: BytesN<32>,
        instance: Address,
        config: Bytes,
        identity_lookup: String,
    ) {
        Registration {
            tx_id: tx_id.clone(),
            instance,
            config,
        }
        .publish(&env);
        if !identity_lookup.is_empty() {
            IdentityRegistered {
                tx_id,
                identity_lookup,
            }
            .publish(&env);
        }
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
        let empty_identity_lookup = String::from_str(&env, "");

        client.register(&tx_id, &instance, &config, &empty_identity_lookup);

        // An empty identity_lookup means no IdentityRegistered event - only "reg" is published,
        // unaffected by this new, optional parameter.
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
    fn register_with_identity_lookup_also_publishes_idreg_event() {
        let env = Env::default();
        let contract_id = env.register(Contract, ());
        let client = ContractClient::new(&env, &contract_id);

        let tx_id = BytesN::from_array(&env, &[8u8; 32]);
        let instance = Address::generate(&env);
        let config = Bytes::from_slice(&env, b"config-bytes");
        let identity_lookup = String::from_str(&env, "notary@node1");

        client.register(&tx_id, &instance, &config, &identity_lookup);

        let expected_registration = Registration {
            tx_id: tx_id.clone(),
            instance,
            config,
        };
        let expected_identity_registered = IdentityRegistered {
            tx_id,
            identity_lookup,
        };
        assert_eq!(
            env.events().all(),
            std::vec![
                expected_registration.to_xdr(&env, &contract_id),
                expected_identity_registered.to_xdr(&env, &contract_id),
            ]
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
        let empty_identity_lookup = String::from_str(&env, "");
        client.register(
            &BytesN::from_array(&env, &[1u8; 32]),
            &Address::generate(&env),
            &Bytes::from_slice(&env, b"a"),
            &empty_identity_lookup,
        );
        assert_eq!(env.events().all().events().len(), 1);

        client.register(
            &BytesN::from_array(&env, &[2u8; 32]),
            &Address::generate(&env),
            &Bytes::from_slice(&env, b"b"),
            &empty_identity_lookup,
        );
        assert_eq!(env.events().all().events().len(), 1);
    }
}
