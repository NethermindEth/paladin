// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

extern crate std;

use super::*;
use soroban_sdk::testutils::{Address as _, Events as _};
use soroban_sdk::Event;

/// The real compiled `snoto` contract Wasm - built via `cargo build --target wasm32v1-none
/// --release -p snoto` (run that first if this fails to find the file; `cargo test -p
/// snoto-factory` alone won't trigger a wasm32 build of a sibling crate). Using the genuine
/// artifact (not a stub) means this test exercises the actual `deployer().with_current_contract
/// (salt).deploy_v2(...)` mechanism for real, not a test-only shortcut.
const SNOTO_WASM: &[u8] = include_bytes!(concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../../target/wasm32v1-none/release/snoto.wasm"
));

#[test]
fn deploy_deploys_initializes_and_registers() {
    let env = Env::default();
    env.mock_all_auths();

    let factory_contract_id = env.register(factory::Contract, ());
    let snoto_factory_id = env.register(Contract, ());

    let wasm_hash = env.deployer().upload_contract_wasm(SNOTO_WASM);

    let notary = Address::generate(&env);
    let sac = Address::generate(&env);
    let config = Bytes::from_slice(&env, b"config-bytes");
    let tx_id = BytesN::from_array(&env, &[7u8; 32]);
    let notary_lookup = String::from_str(&env, "notary@node1");

    let client = ContractClient::new(&env, &snoto_factory_id);
    let snoto_address = client.deploy(
        &wasm_hash,
        &notary,
        &config.clone(),
        &sac,
        &factory_contract_id,
        &tx_id,
        &notary_lookup,
    );

    // `env.events().all()` only retains events from the *most recent* top-level invocation
    // (confirmed against `contracts/factory`'s own test comment) - assert this immediately,
    // before the double-initialize check below starts a new one. Both events are published from
    // `register`'s own context (SaladinFactory), not snoto-factory - see `deploy`'s own doc
    // comment on why `notary_lookup` rides through `register` rather than being published here
    // directly.
    let expected_registration = factory::Registration {
        tx_id: tx_id.clone(),
        instance: snoto_address.clone(),
        config,
    };
    let expected_identity_registered = factory::IdentityRegistered {
        tx_id,
        identity_lookup: notary_lookup,
    };
    assert_eq!(
        env.events().all(),
        std::vec![
            expected_registration.to_xdr(&env, &factory_contract_id),
            expected_identity_registered.to_xdr(&env, &factory_contract_id),
        ]
    );

    // Confirms the deployed address is a real, initialized SNoto instance (not just some
    // placeholder address the salt computation happened to produce): SNoto's own `initialize`
    // panics on a second call ("already initialized" - storage::has_notary), so a successful
    // deploy-and-initialize the first time is the precondition for this to fail as expected.
    let snoto_client = snoto::ContractClient::new(&env, &snoto_address);
    let notary2 = Address::generate(&env);
    let result = snoto_client.try_initialize(&notary2, &Bytes::from_slice(&env, b"other"), &sac);
    assert!(result.is_err());
}

#[test]
fn deploy_uses_tx_id_as_salt_so_reusing_tx_id_fails() {
    let env = Env::default();
    env.mock_all_auths();

    let factory_contract_id = env.register(factory::Contract, ());
    let snoto_factory_id = env.register(Contract, ());

    let wasm_hash = env.deployer().upload_contract_wasm(SNOTO_WASM);

    let notary = Address::generate(&env);
    let sac = Address::generate(&env);
    let config = Bytes::from_slice(&env, b"config-bytes");
    let tx_id = BytesN::from_array(&env, &[9u8; 32]);
    let notary_lookup = String::from_str(&env, "notary@node1");

    let client = ContractClient::new(&env, &snoto_factory_id);
    client.deploy(&wasm_hash, &notary, &config, &sac, &factory_contract_id, &tx_id, &notary_lookup);

    // Redeploying at the same tx_id (same salt) must fail before `initialize`/`register` are
    // ever reached - this is what makes `contracts/factory`'s own "no persistent storage, no
    // dedup entry needed" design safe (chapter 13 §13.5, factory/src/lib.rs's own doc comment):
    // idempotency already comes from this deployment step, not from the registry.
    let result = client.try_deploy(&wasm_hash, &notary, &config, &sac, &factory_contract_id, &tx_id, &notary_lookup);
    assert!(result.is_err());
}
