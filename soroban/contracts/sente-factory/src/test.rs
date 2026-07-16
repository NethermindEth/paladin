// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

extern crate std;

use super::*;
use soroban_sdk::testutils::Events as _;
use soroban_sdk::Event;

/// The real compiled `sente` contract Wasm - built via `cargo build --target wasm32v1-none
/// --release -p sente` (run that first if this fails to find the file; `cargo test -p
/// sente-factory` alone won't trigger a wasm32 build of a sibling crate). Using the genuine
/// artifact means this test exercises the actual `deployer().with_current_contract(salt).
/// deploy_v2(...)` mechanism for real, not a test-only shortcut.
const SENTE_WASM: &[u8] = include_bytes!(concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../../target/wasm32v1-none/release/sente.wasm"
));

#[test]
fn deploy_group_deploys_initializes_and_registers() {
    let env = Env::default();
    env.mock_all_auths();

    let factory_contract_id = env.register(factory::Contract, ());
    let sente_factory_id = env.register(Contract, ());

    let wasm_hash = env.deployer().upload_contract_wasm(SENTE_WASM);

    let member = BytesN::from_array(&env, &[9u8; 32]);
    let members = Vec::from_array(&env, [member]);
    let tx_id = BytesN::from_array(&env, &[7u8; 32]);
    let config = Bytes::from_slice(&env, b"Test SDF Network ; September 2015");

    let client = ContractClient::new(&env, &sente_factory_id);
    let sente_address = client.deploy_group(
        &wasm_hash,
        &members,
        &config.clone(),
        &factory_contract_id,
        &tx_id,
    );

    // `env.events().all()` only retains events from the *most recent* top-level invocation
    // (`contracts/factory`'s own test comment) - assert this immediately, before the
    // confirmation call below starts a new one.
    let expected_registration = factory::Registration {
        tx_id,
        instance: sente_address.clone(),
        config,
    };
    assert_eq!(
        env.events().all(),
        std::vec![expected_registration.to_xdr(&env, &factory_contract_id)]
    );

    // Confirms the deployed address is a real, initialized SentePrivacyGroup (not just some
    // placeholder address the salt computation happened to produce) - the genesis root must be
    // the all-zero convention `sente::storage::init` sets.
    let sente_client = sente::ContractClient::new(&env, &sente_address);
    assert_eq!(sente_client.root(), BytesN::from_array(&env, &[0u8; 32]));
    assert_eq!(sente_client.members(), members);
}

#[test]
#[should_panic]
fn deploy_group_rejects_redeploy_at_the_same_salt() {
    let env = Env::default();
    env.mock_all_auths();

    let factory_contract_id = env.register(factory::Contract, ());
    let sente_factory_id = env.register(Contract, ());
    let wasm_hash = env.deployer().upload_contract_wasm(SENTE_WASM);

    let member = BytesN::from_array(&env, &[9u8; 32]);
    let members = Vec::from_array(&env, [member]);
    let config = Bytes::from_slice(&env, b"Test SDF Network ; September 2015");

    let client = ContractClient::new(&env, &sente_factory_id);
    client.deploy_group(
        &wasm_hash,
        &members,
        &config.clone(),
        &factory_contract_id,
        &BytesN::from_array(&env, &[1u8; 32]),
    );
    // Same `members` -> same salt -> the second deploy at an already-occupied address fails.
    client.deploy_group(
        &wasm_hash,
        &members,
        &config,
        &factory_contract_id,
        &BytesN::from_array(&env, &[2u8; 32]),
    );
}
