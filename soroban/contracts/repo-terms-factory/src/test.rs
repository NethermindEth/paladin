// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

extern crate std;

use super::*;
use soroban_sdk::testutils::{Address as _, Events as _};
use soroban_sdk::Event;

/// The real compiled `repo-terms` contract Wasm - built via `cargo build --target wasm32v1-none
/// --release -p repo-terms` (run that first if this fails to find the file; `cargo test -p
/// repo-terms-factory` alone won't trigger a wasm32 build of a sibling crate).
const REPO_TERMS_WASM: &[u8] = include_bytes!(concat!(
    env!("CARGO_MANIFEST_DIR"),
    "/../../target/wasm32v1-none/release/repo_terms.wasm"
));

#[test]
fn deploy_deploys_initializes_and_registers() {
    let env = Env::default();
    env.mock_all_auths();

    let factory_contract_id = env.register(factory::Contract, ());
    let repo_terms_factory_id = env.register(Contract, ());

    let wasm_hash = env.deployer().upload_contract_wasm(REPO_TERMS_WASM);

    let bank_a = Address::generate(&env);
    let bank_b = Address::generate(&env);
    let tx_id = BytesN::from_array(&env, &[7u8; 32]);
    let identity_lookup = String::from_str(&env, "bankA@node2|bankB@node3");

    let client = ContractClient::new(&env, &repo_terms_factory_id);
    let repo_terms_address = client.deploy(
        &wasm_hash,
        &bank_a,
        &bank_b,
        &factory_contract_id,
        &tx_id,
        &identity_lookup,
    );

    // `env.events().all()` only retains events from the *most recent* top-level invocation - both
    // events below are published from `register`'s own context (SaladinFactory), not
    // repo-terms-factory, matching `snoto-factory`'s own test.
    let expected_registration = factory::Registration {
        tx_id: tx_id.clone(),
        instance: repo_terms_address.clone(),
        config: Bytes::new(&env),
    };
    let expected_identity_registered = factory::IdentityRegistered {
        tx_id,
        identity_lookup,
    };
    assert_eq!(
        env.events().all(),
        std::vec![
            expected_registration.to_xdr(&env, &factory_contract_id),
            expected_identity_registered.to_xdr(&env, &factory_contract_id),
        ]
    );

    // Confirms the deployed address is a real, initialized repo-terms instance: `initialize`
    // itself has no "already initialized" guard (unlike `snoto::initialize`), but `set_terms`
    // does have the "already set" one - a successful first `set_terms` call is the precondition
    // for the second-call panic test below to be meaningful at all.
    let repo_terms_client = repo_terms::ContractClient::new(&env, &repo_terms_address);
    repo_terms_client.set_terms(
        &BytesN::from_array(&env, &[1u8; 32]),
        &BytesN::from_array(&env, &[2u8; 32]),
    );
    let result = repo_terms_client.try_set_terms(
        &BytesN::from_array(&env, &[3u8; 32]),
        &BytesN::from_array(&env, &[4u8; 32]),
    );
    assert!(result.is_err());
}

#[test]
fn deploy_uses_tx_id_as_salt_so_reusing_tx_id_fails() {
    let env = Env::default();
    env.mock_all_auths();

    let factory_contract_id = env.register(factory::Contract, ());
    let repo_terms_factory_id = env.register(Contract, ());

    let wasm_hash = env.deployer().upload_contract_wasm(REPO_TERMS_WASM);

    let bank_a = Address::generate(&env);
    let bank_b = Address::generate(&env);
    let tx_id = BytesN::from_array(&env, &[9u8; 32]);
    let identity_lookup = String::from_str(&env, "bankA@node2|bankB@node3");

    let client = ContractClient::new(&env, &repo_terms_factory_id);
    client.deploy(
        &wasm_hash,
        &bank_a,
        &bank_b,
        &factory_contract_id,
        &tx_id,
        &identity_lookup,
    );

    // Redeploying at the same tx_id (same salt) must fail before `initialize`/`register` are ever
    // reached, matching `snoto-factory`'s own equivalent test.
    let result = client.try_deploy(
        &wasm_hash,
        &bank_a,
        &bank_b,
        &factory_contract_id,
        &tx_id,
        &identity_lookup,
    );
    assert!(result.is_err());
}
