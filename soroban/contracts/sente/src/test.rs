// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

extern crate std;

use super::*;
use ed25519_dalek::{Signer as _, SigningKey};
use soroban_sdk::testutils::Address as _;
use soroban_sdk::{Address, IntoVal, Symbol};

const NETWORK_PASSPHRASE: &[u8] = b"Test SDF Network ; September 2015";
const GENESIS_ROOT: [u8; 32] = [0u8; 32];
const GENESIS_TX_ID: [u8; 32] = [0x55u8; 32];

struct Member {
    signing_key: SigningKey,
    public_key: BytesN<32>,
}

fn member(env: &Env, seed: u8) -> Member {
    let signing_key = SigningKey::from_bytes(&[seed; 32]);
    let public_key = BytesN::from_array(env, &signing_key.verifying_key().to_bytes());
    Member {
        signing_key,
        public_key,
    }
}

fn member_keys(env: &Env, members: &[&Member]) -> Vec<BytesN<32>> {
    let mut keys = Vec::new(env);
    for m in members {
        keys.push_back(m.public_key.clone());
    }
    keys
}

fn setup(env: &Env, members: &[&Member]) -> Address {
    let contract_id = env.register(Contract, ());
    let client = ContractClient::new(env, &contract_id);
    client.initialize(
        &member_keys(env, members),
        &Bytes::from_slice(env, NETWORK_PASSPHRASE),
        &BytesN::from_array(env, &GENESIS_TX_ID),
    );
    contract_id
}

/// Independently recomputes the same digest `transition` verifies on-chain and signs it -
/// mirrors `satom/src/test.rs`'s own `commitment_for` helper: compute the payload/digest with
/// `saladin_typed_data::digest` directly, off-chain, exactly as a real assembling node would.
#[allow(clippy::too_many_arguments)]
fn sign_transition(
    env: &Env,
    contract_id: &Address,
    tx_id: &BytesN<32>,
    old_root: &BytesN<32>,
    new_root: &BytesN<32>,
    external_calls: &Vec<AtomOperation>,
    signer: &SigningKey,
) -> BytesN<64> {
    let payload = TransitionPayload(
        tx_id.clone(),
        old_root.clone(),
        new_root.clone(),
        external_calls.clone(),
    );
    let payload_xdr = payload.to_xdr(env).to_alloc_vec();
    let contract_id_bytes = saladin_typed_data::address_contract_id(contract_id).to_array();
    let digest = saladin_typed_data::digest(
        NETWORK_PASSPHRASE,
        &contract_id_bytes,
        "sente.Transition",
        &payload_xdr,
    );
    BytesN::from_array(env, &signer.sign(&digest).to_bytes())
}

#[test]
fn transition_with_unanimous_signatures_advances_root() {
    let env = Env::default();
    let m1 = member(&env, 1);
    let m2 = member(&env, 2);
    let contract_id = setup(&env, &[&m1, &m2]);
    let client = ContractClient::new(&env, &contract_id);

    let tx_id = BytesN::from_array(&env, &[1u8; 32]);
    let old_root = BytesN::from_array(&env, &GENESIS_ROOT);
    let new_root = BytesN::from_array(&env, &[1u8; 32]);
    let external_calls: Vec<AtomOperation> = Vec::new(&env);
    let signatures = soroban_sdk::vec![
        &env,
        (
            m1.public_key.clone(),
            sign_transition(
                &env,
                &contract_id,
                &tx_id,
                &old_root,
                &new_root,
                &external_calls,
                &m1.signing_key
            )
        ),
        (
            m2.public_key.clone(),
            sign_transition(
                &env,
                &contract_id,
                &tx_id,
                &old_root,
                &new_root,
                &external_calls,
                &m2.signing_key
            )
        ),
    ];

    client.transition(&tx_id, &new_root, &external_calls, &signatures);

    assert_eq!(client.root(), new_root);
}

#[test]
#[should_panic(expected = "sente: all members must endorse a transition")]
fn transition_rejects_below_threshold() {
    let env = Env::default();
    let m1 = member(&env, 1);
    let m2 = member(&env, 2);
    let contract_id = setup(&env, &[&m1, &m2]);
    let client = ContractClient::new(&env, &contract_id);

    let tx_id = BytesN::from_array(&env, &[1u8; 32]);
    let old_root = BytesN::from_array(&env, &GENESIS_ROOT);
    let new_root = BytesN::from_array(&env, &[1u8; 32]);
    let external_calls: Vec<AtomOperation> = Vec::new(&env);
    // Only one of two members signs.
    let signatures = soroban_sdk::vec![
        &env,
        (
            m1.public_key.clone(),
            sign_transition(
                &env,
                &contract_id,
                &tx_id,
                &old_root,
                &new_root,
                &external_calls,
                &m1.signing_key
            )
        ),
    ];

    client.transition(&tx_id, &new_root, &external_calls, &signatures);
}

#[test]
#[should_panic(expected = "sente: duplicate signer")]
fn transition_rejects_duplicate_signer() {
    let env = Env::default();
    let m1 = member(&env, 1);
    let m2 = member(&env, 2);
    let contract_id = setup(&env, &[&m1, &m2]);
    let client = ContractClient::new(&env, &contract_id);

    let tx_id = BytesN::from_array(&env, &[1u8; 32]);
    let old_root = BytesN::from_array(&env, &GENESIS_ROOT);
    let new_root = BytesN::from_array(&env, &[1u8; 32]);
    let external_calls: Vec<AtomOperation> = Vec::new(&env);
    let sig1 = sign_transition(
        &env,
        &contract_id,
        &tx_id,
        &old_root,
        &new_root,
        &external_calls,
        &m1.signing_key,
    );
    // m1 signs twice; m2 never signs at all - still two entries, so the length check passes,
    // but this must still be rejected on the duplicate-signer check.
    let signatures = soroban_sdk::vec![
        &env,
        (m1.public_key.clone(), sig1.clone()),
        (m1.public_key.clone(), sig1),
    ];

    client.transition(&tx_id, &new_root, &external_calls, &signatures);
}

#[test]
#[should_panic(expected = "sente: signer is not a member of this group")]
fn transition_rejects_non_member_signer() {
    let env = Env::default();
    let m1 = member(&env, 1);
    let m2 = member(&env, 2);
    let outsider = member(&env, 99);
    let contract_id = setup(&env, &[&m1, &m2]);
    let client = ContractClient::new(&env, &contract_id);

    let tx_id = BytesN::from_array(&env, &[1u8; 32]);
    let old_root = BytesN::from_array(&env, &GENESIS_ROOT);
    let new_root = BytesN::from_array(&env, &[1u8; 32]);
    let external_calls: Vec<AtomOperation> = Vec::new(&env);
    let signatures = soroban_sdk::vec![
        &env,
        (
            m1.public_key.clone(),
            sign_transition(
                &env,
                &contract_id,
                &tx_id,
                &old_root,
                &new_root,
                &external_calls,
                &m1.signing_key
            )
        ),
        (
            outsider.public_key.clone(),
            sign_transition(
                &env,
                &contract_id,
                &tx_id,
                &old_root,
                &new_root,
                &external_calls,
                &outsider.signing_key
            )
        ),
    ];

    client.transition(&tx_id, &new_root, &external_calls, &signatures);
}

#[test]
#[should_panic]
fn transition_rejects_replay_after_root_advanced() {
    let env = Env::default();
    let m1 = member(&env, 1);
    let contract_id = setup(&env, &[&m1]);
    let client = ContractClient::new(&env, &contract_id);

    let tx_id = BytesN::from_array(&env, &[1u8; 32]);
    let old_root = BytesN::from_array(&env, &GENESIS_ROOT);
    let new_root = BytesN::from_array(&env, &[1u8; 32]);
    let external_calls: Vec<AtomOperation> = Vec::new(&env);
    let sig = sign_transition(
        &env,
        &contract_id,
        &tx_id,
        &old_root,
        &new_root,
        &external_calls,
        &m1.signing_key,
    );
    let signatures = soroban_sdk::vec![&env, (m1.public_key.clone(), sig.clone())];
    client.transition(&tx_id, &new_root, &external_calls, &signatures);

    // Replaying the exact same call: the contract now recomputes the payload against its
    // *current* root (`new_root`, not the original `old_root`), so the signature - computed
    // over the original `old_root` - no longer verifies.
    let signatures_again = soroban_sdk::vec![&env, (m1.public_key.clone(), sig)];
    client.transition(&tx_id, &new_root, &external_calls, &signatures_again);
}

/// The load-bearing test: a transition's `external_calls` really do execute, atomically alongside
/// the root update - this is S3's exit criterion in miniature ("group transition ... with an
/// external SNoto call"), using SNoto's harmless no-auth `keepalive` as the leg (mirrors
/// `satom/src/test.rs`'s own `keepalive_op` convention for an atomicity-only check).
#[test]
fn transition_executes_external_call_atomically() {
    let env = Env::default();
    env.mock_all_auths();

    let snoto_id = env.register(snoto::Contract, ());
    let notary = Address::generate(&env);
    let sac = Address::generate(&env);
    snoto::ContractClient::new(&env, &snoto_id).initialize(
        &notary,
        &Bytes::from_slice(&env, NETWORK_PASSPHRASE),
        &sac,
    );

    let m1 = member(&env, 1);
    let contract_id = setup(&env, &[&m1]);
    let client = ContractClient::new(&env, &contract_id);

    let tx_id = BytesN::from_array(&env, &[1u8; 32]);
    let old_root = BytesN::from_array(&env, &GENESIS_ROOT);
    let new_root = BytesN::from_array(&env, &[1u8; 32]);
    let empty_ids: Vec<BytesN<32>> = Vec::new(&env);
    let external_calls = soroban_sdk::vec![
        &env,
        AtomOperation {
            contract: snoto_id.clone(),
            function: Symbol::new(&env, "keepalive"),
            args: soroban_sdk::vec![&env, empty_ids.into_val(&env)],
        },
    ];
    let sig = sign_transition(
        &env,
        &contract_id,
        &tx_id,
        &old_root,
        &new_root,
        &external_calls,
        &m1.signing_key,
    );
    let signatures = soroban_sdk::vec![&env, (m1.public_key.clone(), sig)];

    client.transition(&tx_id, &new_root, &external_calls, &signatures);

    assert_eq!(client.root(), new_root);
}
