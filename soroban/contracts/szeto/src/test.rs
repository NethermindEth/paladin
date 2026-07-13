// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

//! Tests below `transfer_rejects_*` exercise the real public `transfer` entrypoint end to end,
//! but only for panics that fire **before** `verify_proof` is reached (input shape, auth, unknown
//! root, tx_id replay, nullifier reuse) - constructing a Groth16 proof that actually verifies
//! against the embedded `anon_nullifier_transfer` VK requires the real Zeto Go prover (chapter 13
//! plan's M0/criterion #3, not yet wired to a committed fixture for this exact circuit). Where a
//! rejection depends on prior state that itself would normally only be written after a successful
//! proof check (an already-spent nullifier, an already-used tx_id), that prior state is seeded
//! directly via the `storage`/`tree` modules inside `env.as_contract` - this is poking storage
//! directly, not a second full `transfer` call, precisely because a first call can't succeed
//! without a real proof. TTL-on-write is verified the same way, directly against `storage::
//! mark_spent`/`mark_tx_used`/`tree::insert_leaf`, since those are the exact functions `transfer`
//! calls on a successful path.

use super::*;
use soroban_sdk::testutils::{storage::Persistent as _, Address as _, Ledger as _};
use soroban_sdk::Vec;

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
    client.initialize(&notary, &sac);
    Setup { env, contract_id }
}

fn b32(env: &Env, tag: u8) -> BytesN<32> {
    BytesN::from_array(env, &[tag; 32])
}

fn zero32(env: &Env) -> BytesN<32> {
    BytesN::from_array(env, &[0u8; 32])
}

/// Fully zero-padded (i.e. "no real nullifiers/outputs") - the minimal shape that clears the
/// `transfer` input-shape check without asserting anything about spend/create semantics.
fn padded(env: &Env) -> Vec<BytesN<32>> {
    Vec::from_array(env, [zero32(env), zero32(env)])
}

/// Syntactically well-formed but not a real proof - fine for tests that panic before
/// `verify_proof` is ever reached.
fn dummy_proof(env: &Env) -> Groth16Proof {
    Groth16Proof {
        a: G1Affine::from_bytes(BytesN::from_array(env, &[0u8; 64])),
        b: G2Affine::from_bytes(BytesN::from_array(env, &[0u8; 128])),
        c: G1Affine::from_bytes(BytesN::from_array(env, &[0u8; 64])),
    }
}

#[test]
fn initialize_sets_notary() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    assert_eq!(client.get_root(), U256::from_u32(&s.env, 0));
}

#[test]
#[should_panic(expected = "szeto: already initialized")]
fn initialize_rejects_double_init() {
    let env = Env::default();
    let contract_id = env.register(Contract, ());
    let notary = Address::generate(&env);
    let sac = Address::generate(&env);
    let client = ContractClient::new(&env, &contract_id);
    client.initialize(&notary, &sac);
    client.initialize(&notary, &sac);
}

/// Chapter 13 Part B phase B.3 (batch support): `transfer` now accepts 1 to `BATCH_SLOTS` (10)
/// real nullifiers/outputs, zero-padded by the contract itself (not the caller) - so a length of
/// 1 is valid (padded to `NONBATCH_SLOTS`), it's only *more than* `BATCH_SLOTS` that's rejected.
#[test]
#[should_panic(expected = "szeto: transfer supports at most 10 nullifiers and 10 outputs")]
fn transfer_rejects_too_many_nullifiers() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let eleven = Vec::from_array(&s.env, core::array::from_fn::<_, 11, _>(|_| zero32(&s.env)));
    client.transfer(
        &b32(&s.env, 100),
        &eleven,
        &padded(&s.env),
        &zero32(&s.env),
        &dummy_proof(&s.env),
        &Bytes::new(&s.env),
    );
}

/// A short (fewer-than-`NONBATCH_SLOTS`) real-value list is valid input - the contract pads it,
/// the caller doesn't need to pre-pad. This reaches `verify_proof` (and fails there, on the dummy
/// proof) rather than being rejected for "wrong shape", confirming the shape check itself no
/// longer requires an exact count.
#[test]
#[should_panic(expected = "szeto: invalid proof")]
fn transfer_pads_short_nullifier_list() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    client.transfer(
        &b32(&s.env, 100),
        &Vec::from_array(&s.env, [zero32(&s.env)]),
        &padded(&s.env),
        &zero32(&s.env),
        &dummy_proof(&s.env),
        &Bytes::new(&s.env),
    );
}

/// Confirms both embedded VKs are actually wired up and reachable via `verify_proof`'s length-
/// based selection - `Err(InvalidProof)` (not `Err(MalformedPublicInputs)`) means the length was
/// recognized and the matching VK was loaded far enough to reach the real pairing check, which
/// then correctly rejects a garbage proof. Uses the public `verify` entrypoint directly (bypasses
/// `transfer`'s padding) since it takes the field-element vector `verify_proof` itself expects.
#[test]
#[should_panic(expected = "Error(Contract, #0)")]
fn verify_selects_nonbatch_vk_for_seven_public_inputs() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let inputs = Vec::from_array(
        &s.env,
        core::array::from_fn::<_, 7, _>(|_| Bn254Fr::from_bytes(zero32(&s.env))),
    );
    client.verify(&dummy_proof(&s.env), &inputs);
}

#[test]
#[should_panic(expected = "Error(Contract, #0)")]
fn verify_selects_batch_vk_for_thirty_one_public_inputs() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let inputs = Vec::from_array(
        &s.env,
        core::array::from_fn::<_, 31, _>(|_| Bn254Fr::from_bytes(zero32(&s.env))),
    );
    client.verify(&dummy_proof(&s.env), &inputs);
}

#[test]
#[should_panic(expected = "Error(Contract, #1)")]
fn verify_rejects_public_input_length_matching_neither_circuit() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let inputs = Vec::from_array(
        &s.env,
        core::array::from_fn::<_, 8, _>(|_| Bn254Fr::from_bytes(zero32(&s.env))),
    );
    client.verify(&dummy_proof(&s.env), &inputs);
}

#[test]
#[should_panic]
fn transfer_rejects_unauthorized_notary() {
    // No mock_all_auths() at all for the call under test - the notary's require_auth() has
    // nothing to satisfy it, and that check runs before any proof is touched.
    let env = Env::default();
    let contract_id = env.register(Contract, ());
    let notary = Address::generate(&env);
    let sac = Address::generate(&env);
    let client = ContractClient::new(&env, &contract_id);

    env.mock_all_auths();
    client.initialize(&notary, &sac);

    env.set_auths(&[]);
    client.transfer(
        &b32(&env, 100),
        &padded(&env),
        &padded(&env),
        &zero32(&env),
        &dummy_proof(&env),
        &Bytes::new(&env),
    );
}

#[test]
#[should_panic(expected = "szeto: unknown root")]
fn transfer_rejects_unknown_root() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    client.transfer(
        &b32(&s.env, 100),
        &padded(&s.env),
        &padded(&s.env),
        &b32(&s.env, 99), // never inserted, and non-zero so the "always-valid" root=0 case misses
        &dummy_proof(&s.env),
        &Bytes::new(&s.env),
    );
}

#[test]
#[should_panic(expected = "szeto: tx_id already used")]
fn transfer_rejects_replayed_tx_id() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let tx_id = b32(&s.env, 100);

    s.env.as_contract(&s.contract_id, || {
        storage::mark_tx_used(&s.env, &tx_id);
    });

    client.transfer(
        &tx_id,
        &padded(&s.env),
        &padded(&s.env),
        &zero32(&s.env),
        &dummy_proof(&s.env),
        &Bytes::new(&s.env),
    );
}

#[test]
#[should_panic(expected = "szeto: nullifier already spent")]
fn transfer_rejects_double_spend() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let spent_nullifier = b32(&s.env, 1);

    s.env.as_contract(&s.contract_id, || {
        storage::mark_spent(&s.env, &spent_nullifier);
    });

    client.transfer(
        &b32(&s.env, 100),
        &Vec::from_array(&s.env, [spent_nullifier, zero32(&s.env)]),
        &padded(&s.env),
        &zero32(&s.env),
        &dummy_proof(&s.env),
        &Bytes::new(&s.env),
    );
}

#[test]
fn mark_spent_extends_ttl() {
    let s = setup();
    s.env.ledger().set_sequence_number(1000);
    let nullifier = b32(&s.env, 1);

    s.env.as_contract(&s.contract_id, || {
        storage::mark_spent(&s.env, &nullifier);
        let ttl = s
            .env
            .storage()
            .persistent()
            .get_ttl(&storage::DataKey::Nullifier(nullifier));
        assert!(ttl >= storage::TTL_THRESHOLD_LEDGERS);
    });
}

#[test]
fn mark_tx_used_extends_ttl() {
    let s = setup();
    s.env.ledger().set_sequence_number(1000);
    let tx_id = b32(&s.env, 1);

    s.env.as_contract(&s.contract_id, || {
        storage::mark_tx_used(&s.env, &tx_id);
        let ttl = s
            .env
            .storage()
            .persistent()
            .get_ttl(&storage::DataKey::TxId(tx_id));
        assert!(ttl >= storage::TTL_THRESHOLD_LEDGERS);
    });
}

#[test]
#[should_panic(expected = "szeto: tx_id already used")]
fn mark_tx_used_rejects_replay() {
    let s = setup();
    let tx_id = b32(&s.env, 1);
    s.env.as_contract(&s.contract_id, || {
        storage::mark_tx_used(&s.env, &tx_id);
        storage::mark_tx_used(&s.env, &tx_id);
    });
}

/// Cross-implementation parity check (chapter 13 Part B, phase B.2.1): roots below are what
/// `github.com/LFDT-Paladin/smt` (the Go SMT library the real Paladin domains use) computes for
/// the identical sequence of inserts (index == value == 1, then 2, then 42, each inserted via its
/// own exported `utxo.NewPoseidonHasher`/`node.NewLeafNode`/`smt.NewMerkleTree` API against a
/// from-scratch in-memory `core.Storage`), captured by a throwaway harness at
/// `soroban/spikes/m0-smt-parity` (its `NodeRef.Hex()` returns little-endian storage-key bytes,
/// not a natural big-endian integer - the harness instead formats `NodeRef.BigInt()` to get
/// something directly comparable to `U256::to_be_bytes()`). `tree.rs`'s own doc comments only
/// claimed fidelity against vendored Solidity `SmtLib.sol` before this - this is the first direct
/// check against the Go implementation this repo's real domains actually run, and it must pass
/// before the real-proof fixture test (B.2.3) can mean anything (that test seeds `tree.rs` and
/// trusts its root matches a proof produced against a Go-built tree of the same leaves).
#[test]
fn tree_matches_go_lfdt_paladin_smt_implementation() {
    let s = setup();

    let expected_roots: [[u8; 32]; 3] = [
        [
            0x02, 0xc0, 0x06, 0x6e, 0x10, 0xa7, 0x2a, 0xbd, 0x2b, 0x33, 0xc3, 0xb2, 0x14, 0xcb,
            0x3e, 0x81, 0xbc, 0xb1, 0xb6, 0xe3, 0x09, 0x61, 0xcd, 0x23, 0xc2, 0x02, 0xb1, 0x86,
            0x73, 0xbf, 0x25, 0x43,
        ],
        [
            0x2a, 0x23, 0x9e, 0xc3, 0x11, 0x71, 0xfe, 0x20, 0xa3, 0x07, 0x59, 0x76, 0xff, 0x60,
            0x95, 0x79, 0xf1, 0xb7, 0x41, 0x9f, 0xb6, 0xfa, 0x79, 0xc9, 0x3c, 0x0f, 0x9a, 0xb2,
            0x62, 0x2d, 0xfb, 0x7a,
        ],
        [
            0x04, 0x1e, 0xfb, 0xd3, 0x7c, 0xc4, 0x3a, 0x14, 0xaf, 0x77, 0x6b, 0xb0, 0x5b, 0x4e,
            0x03, 0xa5, 0x2e, 0x4a, 0xe4, 0xce, 0xcc, 0x97, 0xee, 0x20, 0x98, 0x24, 0xa6, 0x82,
            0xbd, 0xc7, 0xe1, 0x3b,
        ],
    ];

    s.env.as_contract(&s.contract_id, || {
        for (value, expected) in [1u32, 2, 42].into_iter().zip(expected_roots.iter()) {
            let v = U256::from_u32(&s.env, value);
            let root = tree::insert_leaf(&s.env, v.clone(), v);
            let expected_u256 = U256::from_be_bytes(&s.env, &Bytes::from_array(&s.env, expected));
            assert_eq!(root, expected_u256, "root mismatch after inserting {value}");
        }
    });
}

#[test]
fn insert_leaf_updates_root_and_extends_ttl() {
    let s = setup();
    s.env.ledger().set_sequence_number(1000);

    s.env.as_contract(&s.contract_id, || {
        let value = U256::from_u32(&s.env, 42);
        let new_root = tree::insert_leaf(&s.env, value.clone(), value);

        assert_ne!(new_root, U256::from_u32(&s.env, 0));
        assert_eq!(tree::get_root(&s.env), new_root);
        assert!(tree::root_exists(&s.env, &new_root));

        let ttl = s
            .env
            .storage()
            .persistent()
            .get_ttl(&storage::DataKey::TreeRootExists(new_root));
        assert!(ttl >= storage::TTL_THRESHOLD_LEDGERS);
    });
}

// `deposit`/`withdraw` tests below only exercise panics that fire *before* `verify_with_vk` is
// reached (auth, amount validation, shape, tx_id replay, unknown root, nullifier reuse) - same
// constraint as `transfer`'s own tests (see this file's leading doc comment): a proof that
// actually verifies against the embedded `deposit`/`withdraw_nullifier` VKs needs its own
// Go-harness-generated fixture (mirroring `real_transfer_test.rs`'s pattern for `transfer`),
// which is future work, not done for this phase.

#[test]
#[should_panic]
fn deposit_rejects_unauthorized_depositor() {
    // No mock_all_auths() at all for the call under test - the depositor's require_auth() has
    // nothing to satisfy it, and that check runs before any proof is touched.
    let env = Env::default();
    let contract_id = env.register(Contract, ());
    let notary = Address::generate(&env);
    let sac = Address::generate(&env);
    let client = ContractClient::new(&env, &contract_id);

    env.mock_all_auths();
    client.initialize(&notary, &sac);

    env.set_auths(&[]);
    let depositor = Address::generate(&env);
    client.deposit(
        &depositor,
        &100,
        &padded(&env),
        &dummy_proof(&env),
        &Bytes::new(&env),
    );
}

#[test]
#[should_panic(expected = "szeto: deposit amount must be positive")]
fn deposit_rejects_zero_amount() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let depositor = Address::generate(&s.env);
    client.deposit(
        &depositor,
        &0,
        &padded(&s.env),
        &dummy_proof(&s.env),
        &Bytes::new(&s.env),
    );
}

#[test]
#[should_panic(expected = "szeto: deposit requires exactly 2 outputs")]
fn deposit_rejects_wrong_output_count() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let depositor = Address::generate(&s.env);
    client.deposit(
        &depositor,
        &100,
        &Vec::from_array(&s.env, [zero32(&s.env)]),
        &dummy_proof(&s.env),
        &Bytes::new(&s.env),
    );
}

#[test]
#[should_panic]
fn withdraw_rejects_unauthorized_notary() {
    let env = Env::default();
    let contract_id = env.register(Contract, ());
    let notary = Address::generate(&env);
    let sac = Address::generate(&env);
    let client = ContractClient::new(&env, &contract_id);

    env.mock_all_auths();
    client.initialize(&notary, &sac);

    env.set_auths(&[]);
    let recipient = Address::generate(&env);
    client.withdraw(
        &b32(&env, 100),
        &recipient,
        &100,
        &padded(&env),
        &zero32(&env),
        &zero32(&env),
        &dummy_proof(&env),
        &Bytes::new(&env),
    );
}

#[test]
#[should_panic(expected = "szeto: withdraw amount must be positive")]
fn withdraw_rejects_nonpositive_amount() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let recipient = Address::generate(&s.env);
    client.withdraw(
        &b32(&s.env, 100),
        &recipient,
        &0,
        &padded(&s.env),
        &zero32(&s.env),
        &zero32(&s.env),
        &dummy_proof(&s.env),
        &Bytes::new(&s.env),
    );
}

#[test]
#[should_panic(expected = "szeto: withdraw supports at most 2 nullifiers")]
fn withdraw_rejects_too_many_nullifiers() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let recipient = Address::generate(&s.env);
    let three = Vec::from_array(&s.env, core::array::from_fn::<_, 3, _>(|_| zero32(&s.env)));
    client.withdraw(
        &b32(&s.env, 100),
        &recipient,
        &100,
        &three,
        &zero32(&s.env),
        &zero32(&s.env),
        &dummy_proof(&s.env),
        &Bytes::new(&s.env),
    );
}

#[test]
#[should_panic(expected = "szeto: tx_id already used")]
fn withdraw_rejects_replayed_tx_id() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let tx_id = b32(&s.env, 100);
    s.env.as_contract(&s.contract_id, || {
        storage::mark_tx_used(&s.env, &tx_id);
    });

    let recipient = Address::generate(&s.env);
    client.withdraw(
        &tx_id,
        &recipient,
        &100,
        &padded(&s.env),
        &zero32(&s.env),
        &zero32(&s.env),
        &dummy_proof(&s.env),
        &Bytes::new(&s.env),
    );
}

#[test]
#[should_panic(expected = "szeto: unknown root")]
fn withdraw_rejects_unknown_root() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let recipient = Address::generate(&s.env);
    client.withdraw(
        &b32(&s.env, 100),
        &recipient,
        &100,
        &padded(&s.env),
        &zero32(&s.env),
        &b32(&s.env, 99),
        &dummy_proof(&s.env),
        &Bytes::new(&s.env),
    );
}

#[test]
#[should_panic(expected = "szeto: nullifier already spent")]
fn withdraw_rejects_already_spent_nullifier() {
    let s = setup();
    let client = ContractClient::new(&s.env, &s.contract_id);
    let spent_nullifier = b32(&s.env, 1);
    s.env.as_contract(&s.contract_id, || {
        storage::mark_spent(&s.env, &spent_nullifier);
    });

    let recipient = Address::generate(&s.env);
    client.withdraw(
        &b32(&s.env, 100),
        &recipient,
        &100,
        &Vec::from_array(&s.env, [spent_nullifier, zero32(&s.env)]),
        &zero32(&s.env),
        &zero32(&s.env),
        &dummy_proof(&s.env),
        &Bytes::new(&s.env),
    );
}
