// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

extern crate std;

use super::*;
use soroban_sdk::testutils::{Address as _, Events as _};
use soroban_sdk::Event;

/// The real compiled `satom` contract Wasm - built via `cargo build --target wasm32v1-none
/// --release -p satom` (run that first if this fails to find the file; `cargo test -p
/// satom-factory` alone won't trigger a wasm32 build of a sibling crate). Using the genuine
/// artifact (not a stub) means this test exercises the actual `deployer().with_current_contract
/// (salt).deploy_v2(...)` mechanism for real, not a test-only shortcut.
const SATOM_WASM: &[u8] = include_bytes!(concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../../target/wasm32v1-none/release/satom.wasm"
));

#[test]
fn deploy_settlement_deploys_initializes_and_registers() {
    let env = Env::default();
    env.mock_all_auths();

    let factory_contract_id = env.register(factory::Contract, ());
    let satom_factory_id = env.register(Contract, ());

    let wasm_hash = env.deployer().upload_contract_wasm(SATOM_WASM);

    let leg_contract = Address::generate(&env);
    let operations = Vec::from_array(
        &env,
        [AtomOperation {
            contract: leg_contract,
            function: Symbol::new(&env, "keepalive"),
            args: Vec::new(&env),
        }],
    );
    let party = Address::generate(&env);
    let parties = Vec::from_array(&env, [party]);
    let tx_id = BytesN::from_array(&env, &[7u8; 32]);
    let config = Bytes::from_slice(&env, b"config-bytes");

    let client = ContractClient::new(&env, &satom_factory_id);
    let satom_address = client.deploy_settlement(
        &wasm_hash,
        &operations,
        &parties,
        &factory_contract_id,
        &tx_id,
        &config.clone(),
    );

    // `env.events().all()` only retains events from the *most recent* top-level invocation
    // (confirmed against `contracts/factory`'s own test comment) - assert this immediately,
    // before the confirmation call below starts a new one.
    let expected_registration = factory::Registration {
        tx_id,
        instance: satom_address.clone(),
        config,
    };
    assert_eq!(
        env.events().all(),
        std::vec![expected_registration.to_xdr(&env, &factory_contract_id)]
    );

    // Confirms the deployed address is a real, initialized SAtom (not just some placeholder
    // address the salt computation happened to produce) - a successful `cancel()` call requires
    // `initialize` to have genuinely run first (`storage::parties`/`storage::transition` both
    // panic on an uninitialized instance).
    let satom_client = satom::ContractClient::new(&env, &satom_address);
    satom_client.cancel(&parties.get(0).unwrap());
}
