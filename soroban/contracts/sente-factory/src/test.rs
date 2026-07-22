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
    // confirmation call below starts a new one. `initialize` publishes `Genesis` before
    // `deploy_group` calls `register`, so it appears first.
    let expected_genesis = sente::Genesis {
        tx_id: tx_id.clone(),
        members: members.clone(),
        network_passphrase: config.clone(),
    };
    let expected_registration = factory::Registration {
        tx_id,
        instance: sente_address.clone(),
        config,
    };
    assert_eq!(
        env.events().all(),
        std::vec![
            expected_genesis.to_xdr(&env, &sente_address),
            expected_registration.to_xdr(&env, &factory_contract_id),
        ]
    );

    // Confirms the deployed address is a real, initialized SentePrivacyGroup (not just some
    // placeholder address the salt computation happened to produce) - the genesis root must be
    // the all-zero convention `sente::storage::init` sets.
    let sente_client = sente::ContractClient::new(&env, &sente_address);
    assert_eq!(sente_client.root(), BytesN::from_array(&env, &[0u8; 32]));
    assert_eq!(sente_client.members(), members);
}

/// The scenario this proves is real, not hypothetical: a second, independent transaction (e.g. a
/// re-run of a demo test against a persistent network, or a fresh node re-submitting
/// `pgroup_createGroup` without knowing another node's already succeeded) reaching `deploy_group`
/// for the *same* `members` must get back the *same*, already-live group - not a trap, not a
/// silently re-initialized (state-reset) group, and (unlike an earlier version of this fix) not a
/// group whose second caller ends up with no `Genesis` event to populate its own local genesis
/// state from either - see `sente::initialize`'s own doc comment for why skipping the repeat
/// `initialize` call entirely turned out to be wrong.
#[test]
fn deploy_group_redeploy_at_the_same_salt_is_idempotent() {
    let env = Env::default();
    env.mock_all_auths();

    let factory_contract_id = env.register(factory::Contract, ());
    let sente_factory_id = env.register(Contract, ());
    let wasm_hash = env.deployer().upload_contract_wasm(SENTE_WASM);

    let member = BytesN::from_array(&env, &[9u8; 32]);
    let members = Vec::from_array(&env, [member]);
    let config = Bytes::from_slice(&env, b"Test SDF Network ; September 2015");
    let first_tx_id = BytesN::from_array(&env, &[1u8; 32]);
    let second_tx_id = BytesN::from_array(&env, &[2u8; 32]);

    let client = ContractClient::new(&env, &sente_factory_id);
    let first_address = client.deploy_group(
        &wasm_hash,
        &members,
        &config.clone(),
        &factory_contract_id,
        &first_tx_id,
    );

    // Same `members` -> same salt -> must resolve to the same, already-live address, not trap.
    let second_address = client.deploy_group(
        &wasm_hash,
        &members,
        &config.clone(),
        &factory_contract_id,
        &second_tx_id,
    );
    assert_eq!(first_address, second_address);

    // `env.events().all()` only retains events from the *most recent* top-level invocation
    // (`contracts/factory`'s own test comment) - assert this immediately, before the confirmation
    // calls below start new ones. Both `Genesis` (from `initialize`, still called unconditionally)
    // and `register` fire again, both correlated with the *second* transaction's own tx_id (not the
    // first) - this is what lets a caller who didn't create the group populate its own local genesis
    // state and resolve the group's address from its own transaction's on-chain events.
    let expected_genesis = sente::Genesis {
        tx_id: second_tx_id.clone(),
        members: members.clone(),
        network_passphrase: config.clone(),
    };
    let expected_registration = factory::Registration {
        tx_id: second_tx_id,
        instance: second_address.clone(),
        config,
    };
    assert_eq!(
        env.events().all(),
        std::vec![
            expected_genesis.to_xdr(&env, &second_address),
            expected_registration.to_xdr(&env, &factory_contract_id),
        ]
    );

    // The existing group's state wasn't reset back to genesis by the repeat `initialize` call.
    let sente_client = sente::ContractClient::new(&env, &first_address);
    assert_eq!(sente_client.root(), BytesN::from_array(&env, &[0u8; 32]));
    assert_eq!(sente_client.members(), members);
}
