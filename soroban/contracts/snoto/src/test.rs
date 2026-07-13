// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

extern crate std;

use super::*;
use soroban_sdk::testutils::{storage::Persistent as _, Address as _, Ledger as _};
use soroban_sdk::Env;

const NETWORK_PASSPHRASE: &[u8] = b"Test SDF Network ; September 2015";

struct Setup {
    env: Env,
    contract_id: Address,
}

fn setup() -> Setup {
    let env = Env::default();
    env.mock_all_auths();
    let contract_id = env.register(Contract, ());
    let notary = Address::generate(&env);
    let sac = Address::generate(&env);
    let client = ContractClient::new(&env, &contract_id);
    client.initialize(&notary, &Bytes::from_slice(&env, NETWORK_PASSPHRASE), &sac);
    Setup { env, contract_id }
}

fn state_id(env: &Env, tag: u8) -> BytesN<32> {
    BytesN::from_array(env, &[tag; 32])
}

#[test]
fn transfer_moves_unspent_to_new_outputs() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);

    let input = state_id(&s.env, 1);
    let output = state_id(&s.env, 2);

    // Mint: no inputs, one output.
    client.transfer(
        &state_id(&s.env, 100),
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [input.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );

    client.transfer(
        &state_id(&s.env, 101),
        &Vec::from_array(&s.env, [input]),
        &Vec::from_array(&s.env, [output]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );
}

#[test]
#[should_panic(expected = "input not unspent")]
fn transfer_rejects_double_spend() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);

    let input = state_id(&s.env, 1);
    client.transfer(
        &state_id(&s.env, 100),
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [input.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );
    client.transfer(
        &state_id(&s.env, 101),
        &Vec::from_array(&s.env, [input.clone()]),
        &Vec::from_array(&s.env, [state_id(&s.env, 2)]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );
    // Reusing the already-spent input a second time must fail.
    client.transfer(
        &state_id(&s.env, 102),
        &Vec::from_array(&s.env, [input]),
        &Vec::from_array(&s.env, [state_id(&s.env, 3)]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );
}

#[test]
#[should_panic(expected = "tx_id already used")]
fn transfer_rejects_replayed_tx_id() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);

    let tx_id = state_id(&s.env, 100);
    client.transfer(
        &tx_id,
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [state_id(&s.env, 1)]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );
    client.transfer(
        &tx_id,
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [state_id(&s.env, 2)]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );
}

#[test]
#[should_panic]
fn transfer_rejects_unauthorized_notary() {
    // No mock_all_auths() at all - the notary's require_auth() has nothing to satisfy it.
    let env = Env::default();
    let contract_id = env.register(Contract, ());
    let notary = Address::generate(&env);
    let sac = Address::generate(&env);
    let client = ContractClient::new(&env, &contract_id);

    env.mock_all_auths();
    client.initialize(&notary, &Bytes::from_slice(&env, NETWORK_PASSPHRASE), &sac);

    env.set_auths(&[]); // clear mocked auths before the call under test
    client.transfer(
        &state_id(&env, 100),
        &Vec::new(&env),
        &Vec::from_array(&env, [state_id(&env, 1)]),
        &Bytes::new(&env),
        &Bytes::new(&env),
    );
}

#[test]
fn transfer_extends_ttl_on_write() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    s.env.ledger().set_sequence_number(1000);

    let output = state_id(&s.env, 1);
    client.transfer(
        &state_id(&s.env, 100),
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [output.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );

    s.env.as_contract(&s.contract_id, || {
        let ttl = s
            .env
            .storage()
            .persistent()
            .get_ttl(&storage::DataKey::Unspent(output));
        assert!(ttl >= storage::TTL_THRESHOLD_LEDGERS);
    });
}

#[test]
fn lock_lifecycle_spend() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);

    let input = state_id(&s.env, 1);
    client.transfer(
        &state_id(&s.env, 100),
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [input.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );

    let lock_id = state_id(&s.env, 101);
    let locked_output = state_id(&s.env, 2);
    client.lock(
        &lock_id,
        &Vec::from_array(&s.env, [input]),
        &Vec::from_array(&s.env, [locked_output.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );

    let spend_output = state_id(&s.env, 3);
    let spend_commitment = commitment_for(
        &s.env,
        &s.contract_id,
        "snoto.Unlock",
        &lock_id,
        &locked_output,
        &spend_output,
    );
    // Never checked by this test path (only spend is exercised) - any value satisfies
    // prepare_unlock's requirement that both commitments be set together.
    let cancel_commitment = state_id(&s.env, 254);
    client.prepare_unlock(&lock_id, &spend_commitment, &cancel_commitment);

    let delegate = Address::generate(&s.env);
    client.delegate_lock(&lock_id, &delegate);

    client.unlock(
        &lock_id,
        &Vec::from_array(&s.env, [locked_output]),
        &Vec::from_array(&s.env, [spend_output]),
        &Bytes::new(&s.env),
    );
}

#[test]
fn lock_lifecycle_cancel() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);

    let input = state_id(&s.env, 1);
    client.transfer(
        &state_id(&s.env, 100),
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [input.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );

    let lock_id = state_id(&s.env, 101);
    let locked_output = state_id(&s.env, 2);
    client.lock(
        &lock_id,
        &Vec::from_array(&s.env, [input]),
        &Vec::from_array(&s.env, [locked_output.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );

    let cancel_output = state_id(&s.env, 4);
    // Never checked by this test path (only cancel is exercised) - any value satisfies
    // prepare_unlock's requirement that both commitments be set together.
    let spend_commitment = state_id(&s.env, 253);
    let cancel_commitment = commitment_for(
        &s.env,
        &s.contract_id,
        "snoto.CancelUnlock",
        &lock_id,
        &locked_output,
        &cancel_output,
    );
    client.prepare_unlock(&lock_id, &spend_commitment, &cancel_commitment);

    let delegate = Address::generate(&s.env);
    client.delegate_lock(&lock_id, &delegate);

    client.cancel_unlock(
        &lock_id,
        &Vec::from_array(&s.env, [locked_output]),
        &Vec::from_array(&s.env, [cancel_output]),
        &Bytes::new(&s.env),
    );
}

#[test]
#[should_panic(expected = "commitment mismatch")]
fn unlock_rejects_wrong_preimage() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);

    let input = state_id(&s.env, 1);
    client.transfer(
        &state_id(&s.env, 100),
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [input.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );

    let lock_id = state_id(&s.env, 101);
    let locked_output = state_id(&s.env, 2);
    client.lock(
        &lock_id,
        &Vec::from_array(&s.env, [input]),
        &Vec::from_array(&s.env, [locked_output.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );

    let spend_output = state_id(&s.env, 3);
    let spend_commitment = commitment_for(
        &s.env,
        &s.contract_id,
        "snoto.Unlock",
        &lock_id,
        &locked_output,
        &spend_output,
    );
    let cancel_commitment = state_id(&s.env, 254);
    client.prepare_unlock(&lock_id, &spend_commitment, &cancel_commitment);

    let delegate = Address::generate(&s.env);
    client.delegate_lock(&lock_id, &delegate);

    // Wrong output (doesn't match the committed spend_output) must be rejected.
    client.unlock(
        &lock_id,
        &Vec::from_array(&s.env, [locked_output]),
        &Vec::from_array(&s.env, [state_id(&s.env, 200)]),
        &Bytes::new(&s.env),
    );
}

#[test]
fn keepalive_skips_nonexistent_ids_silently() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);

    let output = state_id(&s.env, 1);
    client.transfer(
        &state_id(&s.env, 100),
        &Vec::new(&s.env),
        &Vec::from_array(&s.env, [output.clone()]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );

    // A mixed batch: one real id, one that was never created. Must not panic.
    client.keepalive(&Vec::from_array(&s.env, [output, state_id(&s.env, 250)]));
}

/// Registers a real (testutils) Stellar Asset Contract instead of the plain generated `Address`
/// `setup()` uses - `deposit`/`withdraw` genuinely call through to it (no ZK proof gates them,
/// unlike SZeto's), so full success-path assertions on real token balances are possible here.
struct ShieldSetup {
    env: Env,
    contract_id: Address,
    sac: Address,
}

fn shield_setup() -> ShieldSetup {
    let env = Env::default();
    env.mock_all_auths();
    let admin = Address::generate(&env);
    let sac = env.register_stellar_asset_contract_v2(admin).address();
    let contract_id = env.register(Contract, ());
    let notary = Address::generate(&env);
    let client = ContractClient::new(&env, &contract_id);
    client.initialize(&notary, &Bytes::from_slice(&env, NETWORK_PASSPHRASE), &sac);
    ShieldSetup {
        env,
        contract_id,
        sac,
    }
}

#[test]
fn deposit_shields_amount_and_admits_output() {
    let s = shield_setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let token = soroban_sdk::token::StellarAssetClient::new(&s.env, &s.sac);
    let token_balance = soroban_sdk::token::TokenClient::new(&s.env, &s.sac);

    let depositor = Address::generate(&s.env);
    token.mint(&depositor, &1_000);

    let output = state_id(&s.env, 1);
    client.deposit(
        &state_id(&s.env, 100),
        &depositor,
        &500,
        &Vec::from_array(&s.env, [output.clone()]),
        &Bytes::new(&s.env),
    );

    assert_eq!(token_balance.balance(&depositor), 500);
    assert_eq!(token_balance.balance(&s.contract_id), 500);

    // The output is now spendable via a normal transfer - confirms `deposit` admitted it exactly
    // like `transfer` would.
    client.transfer(
        &state_id(&s.env, 101),
        &Vec::from_array(&s.env, [output]),
        &Vec::from_array(&s.env, [state_id(&s.env, 2)]),
        &Bytes::new(&s.env),
        &Bytes::new(&s.env),
    );
}

#[test]
#[should_panic]
fn deposit_rejects_unauthorized_depositor() {
    let s = shield_setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let depositor = Address::generate(&s.env);

    s.env.set_auths(&[]); // clear the mocked auths from shield_setup() for the call under test
    client.deposit(
        &state_id(&s.env, 100),
        &depositor,
        &500,
        &Vec::from_array(&s.env, [state_id(&s.env, 1)]),
        &Bytes::new(&s.env),
    );
}

#[test]
#[should_panic(expected = "deposit amount must be positive")]
fn deposit_rejects_nonpositive_amount() {
    let s = shield_setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let depositor = Address::generate(&s.env);
    client.deposit(
        &state_id(&s.env, 100),
        &depositor,
        &0,
        &Vec::from_array(&s.env, [state_id(&s.env, 1)]),
        &Bytes::new(&s.env),
    );
}

#[test]
fn withdraw_unshields_amount_to_recipient() {
    let s = shield_setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let token = soroban_sdk::token::StellarAssetClient::new(&s.env, &s.sac);
    let token_balance = soroban_sdk::token::TokenClient::new(&s.env, &s.sac);

    let depositor = Address::generate(&s.env);
    token.mint(&depositor, &1_000);
    let input = state_id(&s.env, 1);
    client.deposit(
        &state_id(&s.env, 100),
        &depositor,
        &500,
        &Vec::from_array(&s.env, [input.clone()]),
        &Bytes::new(&s.env),
    );

    let recipient = Address::generate(&s.env);
    client.withdraw(
        &state_id(&s.env, 101),
        &recipient,
        &500,
        &Vec::from_array(&s.env, [input]),
        &Bytes::new(&s.env),
    );

    assert_eq!(token_balance.balance(&recipient), 500);
    assert_eq!(token_balance.balance(&s.contract_id), 0);
}

#[test]
#[should_panic]
fn withdraw_rejects_unauthorized_notary() {
    let env = Env::default();
    let admin = Address::generate(&env);
    let sac = env.register_stellar_asset_contract_v2(admin).address();
    let contract_id = env.register(Contract, ());
    let notary = Address::generate(&env);
    let client = ContractClient::new(&env, &contract_id);

    env.mock_all_auths();
    client.initialize(&notary, &Bytes::from_slice(&env, NETWORK_PASSPHRASE), &sac);

    env.set_auths(&[]);
    let recipient = Address::generate(&env);
    client.withdraw(
        &state_id(&env, 100),
        &recipient,
        &500,
        &Vec::from_array(&env, [state_id(&env, 1)]),
        &Bytes::new(&env),
    );
}

#[test]
#[should_panic(expected = "input not unspent")]
fn withdraw_rejects_unknown_input() {
    let s = shield_setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let recipient = Address::generate(&s.env);
    client.withdraw(
        &state_id(&s.env, 100),
        &recipient,
        &500,
        &Vec::from_array(&s.env, [state_id(&s.env, 1)]),
        &Bytes::new(&s.env),
    );
}

/// Computes the exact same commitment digest `check_commitment` in `lib.rs` recomputes on-chain -
/// this is the off-chain (here, test-side) half of the commit-reveal pattern: whoever calls
/// `prepare_unlock` in real usage (chapter 14's Go domain, not built yet) must independently
/// compute this same digest to know what to commit to.
fn commitment_for(
    env: &Env,
    contract_id: &Address,
    type_name: &str,
    lock_id: &BytesN<32>,
    locked_output: &BytesN<32>,
    output: &BytesN<32>,
) -> BytesN<32> {
    let payload = UnlockPayload(
        lock_id.clone(),
        Vec::from_array(env, [locked_output.clone()]),
        Vec::from_array(env, [output.clone()]),
        Bytes::new(env),
    );
    let payload_xdr = payload.to_xdr(env).to_alloc_vec();
    // `address_contract_id` works from plain test code (no active contract-invocation context
    // needed), unlike `current_contract_id`, which requires an `as_contract` scope.
    let contract_id_bytes = saladin_typed_data::address_contract_id(contract_id).to_array();
    let computed = saladin_typed_data::digest(
        NETWORK_PASSPHRASE,
        &contract_id_bytes,
        type_name,
        &payload_xdr,
    );
    BytesN::from_array(env, &computed)
}
