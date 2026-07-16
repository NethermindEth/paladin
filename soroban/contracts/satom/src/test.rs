// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

extern crate std;

use super::*;
use soroban_sdk::testutils::Address as _;
use soroban_sdk::xdr::ToXdr;
use soroban_sdk::{Bytes, BytesN, IntoVal, Symbol};

const NETWORK_PASSPHRASE: &[u8] = b"Test SDF Network ; September 2015";

/// A trivial, harmless leg - SNoto's own `keepalive` accepts an empty `Vec<BytesN<32>>` and
/// requires no auth, so it's useful here purely to exercise `execute`'s looping/atomicity
/// mechanics without needing a real lock/commitment setup (that's what the dedicated
/// cross-contract test below is for).
fn keepalive_op(env: &Env, snoto_id: &Address) -> AtomOperation {
    let empty_ids: soroban_sdk::Vec<BytesN<32>> = soroban_sdk::Vec::new(env);
    AtomOperation {
        contract: snoto_id.clone(),
        function: Symbol::new(env, "keepalive"),
        args: soroban_sdk::vec![env, empty_ids.into_val(env)],
    }
}

fn setup_snoto(env: &Env) -> Address {
    let snoto_id = env.register(snoto::Contract, ());
    let notary = Address::generate(env);
    let sac = Address::generate(env);
    let client = snoto::ContractClient::new(env, &snoto_id);
    client.initialize(&notary, &Bytes::from_slice(env, NETWORK_PASSPHRASE), &sac);
    snoto_id
}

#[test]
fn execute_runs_operations_and_marks_executed() {
    let env = Env::default();
    env.mock_all_auths();
    let snoto_id = setup_snoto(&env);

    let satom_id = env.register(Contract, ());
    let party = Address::generate(&env);
    let client = ContractClient::new(&env, &satom_id);
    client.initialize(
        &soroban_sdk::Vec::from_array(&env, [keepalive_op(&env, &snoto_id)]),
        &soroban_sdk::Vec::from_array(&env, [party]),
    );

    client.execute();
}

#[test]
#[should_panic(expected = "satom: already settled")]
fn execute_rejects_double_execute() {
    let env = Env::default();
    env.mock_all_auths();
    let snoto_id = setup_snoto(&env);

    let satom_id = env.register(Contract, ());
    let party = Address::generate(&env);
    let client = ContractClient::new(&env, &satom_id);
    client.initialize(
        &soroban_sdk::Vec::from_array(&env, [keepalive_op(&env, &snoto_id)]),
        &soroban_sdk::Vec::from_array(&env, [party]),
    );

    client.execute();
    client.execute();
}

#[test]
#[should_panic(expected = "satom: already settled")]
fn execute_rejects_after_cancel() {
    let env = Env::default();
    env.mock_all_auths();
    let snoto_id = setup_snoto(&env);

    let satom_id = env.register(Contract, ());
    let party = Address::generate(&env);
    let client = ContractClient::new(&env, &satom_id);
    client.initialize(
        &soroban_sdk::Vec::from_array(&env, [keepalive_op(&env, &snoto_id)]),
        &soroban_sdk::Vec::from_array(&env, [party.clone()]),
    );

    client.cancel(&party);
    client.execute();
}

#[test]
fn cancel_marks_cancelled() {
    let env = Env::default();
    env.mock_all_auths();
    let snoto_id = setup_snoto(&env);

    let satom_id = env.register(Contract, ());
    let party = Address::generate(&env);
    let client = ContractClient::new(&env, &satom_id);
    client.initialize(
        &soroban_sdk::Vec::from_array(&env, [keepalive_op(&env, &snoto_id)]),
        &soroban_sdk::Vec::from_array(&env, [party.clone()]),
    );

    client.cancel(&party);
}

#[test]
#[should_panic(expected = "satom: canceller is not a party to this settlement")]
fn cancel_rejects_non_party() {
    let env = Env::default();
    env.mock_all_auths();
    let snoto_id = setup_snoto(&env);

    let satom_id = env.register(Contract, ());
    let party = Address::generate(&env);
    let not_a_party = Address::generate(&env);
    let client = ContractClient::new(&env, &satom_id);
    client.initialize(
        &soroban_sdk::Vec::from_array(&env, [keepalive_op(&env, &snoto_id)]),
        &soroban_sdk::Vec::from_array(&env, [party]),
    );

    client.cancel(&not_a_party);
}

#[test]
#[should_panic]
fn cancel_rejects_unauthorized_party() {
    let env = Env::default();
    let snoto_id = env.register(snoto::Contract, ());
    // initialize snoto with mocked auth, then clear before the call under test
    env.mock_all_auths();
    let notary = Address::generate(&env);
    let sac = Address::generate(&env);
    snoto::ContractClient::new(&env, &snoto_id).initialize(
        &notary,
        &Bytes::from_slice(&env, NETWORK_PASSPHRASE),
        &sac,
    );

    let satom_id = env.register(Contract, ());
    let party = Address::generate(&env);
    let client = ContractClient::new(&env, &satom_id);
    client.initialize(
        &soroban_sdk::Vec::from_array(&env, [keepalive_op(&env, &snoto_id)]),
        &soroban_sdk::Vec::from_array(&env, [party.clone()]),
    );

    env.set_auths(&[]);
    client.cancel(&party);
}

/// Computes the same commitment digest SNoto's `check_commitment` recomputes on-chain -
/// mirrors `snoto/src/test.rs`'s own `commitment_for` helper exactly (duplicated here rather than
/// shared, since it's test-only code in a crate SAtom only depends on for tests).
fn commitment_for(
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

/// The load-bearing test: a real SNoto lock, delegated to a real SAtom instance, unlocked purely
/// via `execute()`'s cross-contract call - `env.set_auths(&[])` before `execute()` proves this
/// succeeds through genuine Soroban invoker authorization, not because mocked auths are papering
/// over a missing check.
#[test]
fn snoto_lock_unlocks_via_atom_execute_with_invoker_auth_only() {
    let env = Env::default();
    env.mock_all_auths();
    let snoto_id = setup_snoto(&env);
    let snoto_client = snoto::ContractClient::new(&env, &snoto_id);

    let input = BytesN::from_array(&env, &[1u8; 32]);
    snoto_client.transfer(
        &BytesN::from_array(&env, &[100u8; 32]),
        &soroban_sdk::Vec::new(&env),
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
        &soroban_sdk::Vec::new(&env),
        &Bytes::new(&env),
        &Bytes::new(&env),
    );

    let spend_output = BytesN::from_array(&env, &[3u8; 32]);
    let spend_commitment = commitment_for(&env, &snoto_id, &lock_id, &locked_output, &spend_output);
    // Never checked by this test path (only spend/unlock is exercised) - any value satisfies
    // prepare_unlock's requirement that both commitments be set together.
    let cancel_commitment = BytesN::from_array(&env, &[254u8; 32]);
    snoto_client.prepare_unlock(&lock_id, &spend_commitment, &cancel_commitment);

    let satom_id = env.register(Contract, ());
    snoto_client.delegate_lock(&lock_id, &satom_id);

    let unlock_args = soroban_sdk::vec![
        &env,
        lock_id.clone().into_val(&env),
        soroban_sdk::Vec::from_array(&env, [locked_output.clone()]).into_val(&env),
        soroban_sdk::Vec::from_array(&env, [spend_output.clone()]).into_val(&env),
        Bytes::new(&env).into_val(&env),
    ];
    let op = AtomOperation {
        contract: snoto_id.clone(),
        function: Symbol::new(&env, "unlock"),
        args: unlock_args,
    };

    let party = Address::generate(&env);
    let satom_client = ContractClient::new(&env, &satom_id);
    satom_client.initialize(
        &soroban_sdk::Vec::from_array(&env, [op]),
        &soroban_sdk::Vec::from_array(&env, [party]),
    );

    // Clears every mocked auth - if `unlock`'s `lock.delegate.require_auth()` succeeded only
    // because of `mock_all_auths()`, this call would now fail. It doesn't, because SAtom is the
    // *direct* caller of SNoto's `unlock` and the delegate is SAtom's own address.
    env.set_auths(&[]);
    satom_client.execute();

    // Confirm the unlock really ran: the spend output is unspent and freely transferable. Auths
    // are re-mocked here purely for this confirmation transfer's own unrelated notary auth check
    // - the load-bearing assertion (execute() succeeding with auths cleared) already happened
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
