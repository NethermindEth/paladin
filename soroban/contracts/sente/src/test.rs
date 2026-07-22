// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

extern crate std;

use super::*;
use ed25519_dalek::{Signer as _, SigningKey};
use soroban_sdk::testutils::{Address as _, Events as _};
use soroban_sdk::{Address, Event, IntoVal, Symbol};

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
/// `satom/src/test.rs`'s own `keepalive_op` convention for an atomicity-only check). Targets a
/// real, pre-existing state ID (a coin created via a genuine `transfer` below) rather than an
/// empty vec - `keepalive_one` (soroban/contracts/snoto/src/storage.rs) only actually reaches its
/// `extend_ttl` calls for ids that exist, so an empty vec alone never exercises that code path.
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

    // A real coin state, the same way Paladin's own "mint" ABI maps to SNoto's `transfer` with
    // no inputs (domains/noto/internal/noto/handler_mint.go's own stellarBaseLedgerInvoke) - the
    // output id is just an opaque 32-byte value from SNoto's own perspective, so any chosen id is
    // as real/valid as one Paladin would allocate off-chain.
    let coin_id = BytesN::from_array(&env, &[9u8; 32]);
    let no_inputs: Vec<BytesN<32>> = Vec::new(&env);
    let outputs = soroban_sdk::vec![&env, coin_id.clone()];
    snoto::ContractClient::new(&env, &snoto_id).transfer(
        &BytesN::from_array(&env, &GENESIS_TX_ID),
        &no_inputs,
        &outputs,
        &Bytes::new(&env),
        &Bytes::new(&env),
    );

    let m1 = member(&env, 1);
    let contract_id = setup(&env, &[&m1]);
    let client = ContractClient::new(&env, &contract_id);

    let tx_id = BytesN::from_array(&env, &[1u8; 32]);
    let old_root = BytesN::from_array(&env, &GENESIS_ROOT);
    let new_root = BytesN::from_array(&env, &[1u8; 32]);
    let real_ids = soroban_sdk::vec![&env, coin_id.clone()];
    let external_calls = soroban_sdk::vec![
        &env,
        AtomOperation {
            contract: snoto_id.clone(),
            function: Symbol::new(&env, "keepalive"),
            args: soroban_sdk::vec![&env, real_ids.into_val(&env)],
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

/// Mirrors `satom/src/test.rs`'s own `commitment_for` helper: computes the same
/// `SALADIN_TYPED_DATA_V0` digest SNoto's `unlock` checks, off-chain, exactly as a real
/// assembling node would.
fn snoto_unlock_commitment(
    env: &Env,
    snoto_id: &Address,
    lock_id: &BytesN<32>,
    locked_output: &BytesN<32>,
    output: &BytesN<32>,
) -> BytesN<32> {
    let payload = snoto::UnlockPayload(
        lock_id.clone(),
        soroban_sdk::Vec::from_array(env, [locked_output.clone()]),
        soroban_sdk::Vec::from_array(env, [output.clone()]),
        Bytes::new(env),
    );
    let payload_xdr = payload.to_xdr(env).to_alloc_vec();
    let contract_id_bytes = saladin_typed_data::address_contract_id(snoto_id).to_array();
    let computed = saladin_typed_data::digest(
        NETWORK_PASSPHRASE,
        &contract_id_bytes,
        "snoto.Unlock",
        &payload_xdr,
    );
    BytesN::from_array(env, &computed)
}

/// The load-bearing test for the repo demo (ch. 18): a real SNoto lock, delegated to a Sente
/// group's own contract address, unlocked purely via a `transition`'s `external_calls` -
/// `env.set_auths(&[])` before `transition()` proves this succeeds through genuine Soroban
/// invoker authorization, not because mocked auths are papering over a missing check. Mirrors
/// `satom/src/test.rs`'s own `snoto_lock_unlocks_via_atom_execute_with_invoker_auth_only`, with
/// Sente's `transition` standing in for SAtom's `execute()`.
#[test]
fn transition_unlocks_delegated_snoto_lock_via_invoker_auth_only() {
    let env = Env::default();
    env.mock_all_auths();

    let snoto_id = env.register(snoto::Contract, ());
    let notary = Address::generate(&env);
    let sac = Address::generate(&env);
    let snoto_client = snoto::ContractClient::new(&env, &snoto_id);
    snoto_client.initialize(&notary, &Bytes::from_slice(&env, NETWORK_PASSPHRASE), &sac);

    let input = BytesN::from_array(&env, &[1u8; 32]);
    snoto_client.transfer(
        &BytesN::from_array(&env, &[100u8; 32]),
        &Vec::new(&env),
        &soroban_sdk::Vec::from_array(&env, [input.clone()]),
        &Bytes::new(&env),
        &Bytes::new(&env),
    );

    let lock_id = BytesN::from_array(&env, &[101u8; 32]);
    let locked_output = BytesN::from_array(&env, &[2u8; 32]);
    snoto_client.lock(
        &lock_id,
        &soroban_sdk::Vec::from_array(&env, [input]),
        &soroban_sdk::Vec::from_array(&env, [locked_output.clone()]),
        &Vec::new(&env),
        &Bytes::new(&env),
        &Bytes::new(&env),
        &BytesN::from_array(&env, &[110u8; 32]),
    );

    let spend_output = BytesN::from_array(&env, &[3u8; 32]);
    let spend_commitment =
        snoto_unlock_commitment(&env, &snoto_id, &lock_id, &locked_output, &spend_output);
    // Never checked by this test path (only spend/unlock is exercised) - any value satisfies
    // prepare_unlock's requirement that both commitments be set together.
    let cancel_commitment = BytesN::from_array(&env, &[254u8; 32]);
    snoto_client.prepare_unlock(
        &BytesN::from_array(&env, &[105u8; 32]),
        &lock_id,
        &spend_commitment,
        &cancel_commitment,
        &Bytes::new(&env),
        &BytesN::from_array(&env, &[111u8; 32]),
        &Vec::from_array(&env, [locked_output.clone()]),
    );

    let m1 = member(&env, 1);
    let contract_id = setup(&env, &[&m1]);

    snoto_client.delegate_lock(
        &BytesN::from_array(&env, &[106u8; 32]),
        &lock_id,
        &contract_id,
        &Bytes::new(&env),
        &BytesN::from_array(&env, &[112u8; 32]),
    );

    let unlock_args = soroban_sdk::vec![
        &env,
        BytesN::from_array(&env, &[107u8; 32]).into_val(&env),
        lock_id.clone().into_val(&env),
        soroban_sdk::Vec::from_array(&env, [locked_output]).into_val(&env),
        soroban_sdk::Vec::from_array(&env, [spend_output.clone()]).into_val(&env),
        Bytes::new(&env).into_val(&env),
    ];
    let external_calls = soroban_sdk::vec![
        &env,
        AtomOperation {
            contract: snoto_id.clone(),
            function: Symbol::new(&env, "unlock"),
            args: unlock_args,
        },
    ];

    let tx_id = BytesN::from_array(&env, &[1u8; 32]);
    let old_root = BytesN::from_array(&env, &GENESIS_ROOT);
    let new_root = BytesN::from_array(&env, &[1u8; 32]);
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
    let client = ContractClient::new(&env, &contract_id);

    // Clears every mocked auth - if `unlock`'s `lock.delegate.require_auth()` succeeded only
    // because of `mock_all_auths()`, this call would now fail. It doesn't, because Sente's own
    // `transition` is the *direct* caller of SNoto's `unlock` (via `external_calls`) and the
    // delegate is Sente's own contract address - the same invoker-authorization property already
    // proven for SAtom, now proven for Sente too.
    env.set_auths(&[]);
    client.transition(&tx_id, &new_root, &external_calls, &signatures);

    assert_eq!(client.root(), new_root);

    // Confirm the unlock really ran: the spend output is unspent and freely transferable. Auths
    // are re-mocked here purely for this confirmation transfer's own unrelated notary auth check
    // - the load-bearing assertion (transition() succeeding with auths cleared) already happened
    // above.
    env.mock_all_auths();
    snoto_client.transfer(
        &BytesN::from_array(&env, &[102u8; 32]),
        &soroban_sdk::Vec::from_array(&env, [spend_output]),
        &soroban_sdk::Vec::from_array(&env, [BytesN::from_array(&env, &[4u8; 32])]),
        &Bytes::new(&env),
        &Bytes::new(&env),
    );
}

/// `sente-factory::deploy_group` depends on this: a repeat `initialize` call against an
/// already-initialized group must not panic, must not reset state, and must re-publish `Genesis`
/// under the *new* call's own `tx_id` - because Sente's genesis-state creation on the Go side is
/// purely event-driven (no per-transaction "expected output state" the way an ordinary `transition`
/// does), a node that never itself observed the *original* `Genesis` event has no other way to
/// populate its own local genesis state than a fresh event tied to its own transaction.
#[test]
fn initialize_on_already_initialized_group_republishes_genesis_without_resetting_state() {
    let env = Env::default();
    env.mock_all_auths();
    let m1 = member(&env, 1);
    let contract_id = setup(&env, &[&m1]);

    let second_tx_id = BytesN::from_array(&env, &[0x66u8; 32]);
    let client = ContractClient::new(&env, &contract_id);
    client.initialize(
        &member_keys(&env, &[&m1]),
        &Bytes::from_slice(&env, NETWORK_PASSPHRASE),
        &second_tx_id,
    );

    let expected_genesis = Genesis {
        tx_id: second_tx_id,
        members: member_keys(&env, &[&m1]),
        network_passphrase: Bytes::from_slice(&env, NETWORK_PASSPHRASE),
    };
    assert_eq!(
        env.events().all(),
        std::vec![expected_genesis.to_xdr(&env, &contract_id)]
    );

    assert_eq!(client.root(), BytesN::from_array(&env, &GENESIS_ROOT));
    assert_eq!(client.members(), member_keys(&env, &[&m1]));
}
