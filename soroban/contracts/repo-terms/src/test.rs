// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

extern crate std;

use super::*;
use soroban_sdk::testutils::{Address as _, Events as _};
use soroban_sdk::Event;

struct Setup {
    env: Env,
    contract_id: Address,
    bank_a: Address,
    bank_b: Address,
}

fn setup() -> Setup {
    let env = Env::default();
    let contract_id = env.register(Contract, ());
    let bank_a = Address::generate(&env);
    let bank_b = Address::generate(&env);
    let client = ContractClient::new(&env, &contract_id);
    client.initialize(&bank_a, &bank_b);
    Setup {
        env,
        contract_id,
        bank_a,
        bank_b,
    }
}

fn id(env: &Env, tag: u8) -> BytesN<32> {
    BytesN::from_array(env, &[tag; 32])
}

#[test]
fn set_terms_needs_no_authorization() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);

    // No env.mock_all_auths() and no require_auth call inside set_terms() - if this compiles and
    // passes, set_terms() genuinely needs no signer from either counterparty, matching the trust
    // boundary documented on set_terms itself (Paladin's own off-chain ENDORSE/threshold=2 plan).
    client.set_terms(&id(&s.env, 1), &id(&s.env, 2));
}

#[test]
fn set_terms_publishes_expected_event() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);

    let tx_id = id(&s.env, 1);
    let terms_state_id = id(&s.env, 2);
    client.set_terms(&tx_id, &terms_state_id);

    let expected = SetTerms {
        tx_id,
        terms_state_id,
    };
    assert_eq!(
        s.env.events().all(),
        std::vec![expected.to_xdr(&s.env, &s.contract_id)]
    );
}

#[test]
#[should_panic(expected = "repo terms already set for this instance")]
fn set_terms_rejects_a_second_call() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);

    client.set_terms(&id(&s.env, 1), &id(&s.env, 2));
    // A second call - even with different ids - must be rejected: one instance is exactly one
    // trade, terms fixed at inception, no amend path in v1.
    client.set_terms(&id(&s.env, 3), &id(&s.env, 4));
}

#[test]
fn initialize_stores_parties_without_gating_set_terms() {
    let s = setup();
    let _ = (&s.bank_a, &s.bank_b); // parties are recorded for on-chain transparency only

    let client = ContractClient::new(&s.env, &s.contract_id);
    client.set_terms(&id(&s.env, 1), &id(&s.env, 2));
}
