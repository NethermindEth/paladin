// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

//! R21 benchmark spike (chapter 16 §16.1): measures the real, metered on-chain cost of the new
//! content-addressed `inputs`/`outputs` check added to `SentePrivacyGroup::transition`. Each
//! input/output is its own persistent-storage `Unspent` entry (`storage.rs`), so a transition
//! spending N business `SenteEntry` states and creating N new ones now costs roughly 2N *extra*
//! write entries on top of the 1 the root advance already cost - determines whether that pushes a
//! realistic transition close to Soroban's real mainnet resource ceiling
//! (`InvocationResourceLimits::mainnet()`), mirroring `szeto`'s own `batch_bench_test.rs` (ch. 13's
//! R2 spike methodology: measure the real metered cost, don't estimate it).

extern crate std;

use ed25519_dalek::{Signer as _, SigningKey};

const NETWORK_PASSPHRASE: &[u8] = b"Test SDF Network ; September 2015";
const GENESIS_TX_ID: [u8; 32] = [0x55u8; 32];

use super::*;
use soroban_sdk::Address;

fn entry_id(env: &Env, label: &str) -> BytesN<32> {
    let hash = env.crypto().sha256(&Bytes::from_slice(env, label.as_bytes()));
    BytesN::from_array(env, &hash.to_array())
}

fn ids(env: &Env, tag: &str, n: u32) -> Vec<BytesN<32>> {
    let mut v = Vec::new(env);
    for i in 0..n {
        v.push_back(entry_id(env, &std::format!("{tag}-{i}")));
    }
    v
}

/// Same construction as `test.rs`'s own `sign_transition`, duplicated here (not shared) so this
/// benchmark stays a fully self-contained, throwaway measurement - the same convention `szeto`'s
/// own `batch_bench_test.rs` uses (its own doc comment: a separate contract/harness "purely so
/// this benchmark's ... parameters aren't limited by production" concerns).
#[allow(clippy::too_many_arguments)]
fn sign_transition(
    env: &Env,
    contract_id: &Address,
    tx_id: &BytesN<32>,
    old_root: &BytesN<32>,
    new_root: &BytesN<32>,
    inputs: &Vec<BytesN<32>>,
    outputs: &Vec<BytesN<32>>,
    external_calls: &Vec<AtomOperation>,
    signer: &SigningKey,
) -> BytesN<64> {
    let payload = TransitionPayload(
        tx_id.clone(),
        old_root.clone(),
        new_root.clone(),
        inputs.clone(),
        outputs.clone(),
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

/// `MAINNET_INSTRUCTION_LIMIT` matches `soroban_sdk::testutils::cost_estimate`'s own
/// `NetworkInvocationResourceLimits::mainnet()` (600M instructions), hardcoded the same way
/// `szeto`'s own `batch_bench_test.rs` does - avoids an extra import for a constant that doesn't
/// change often.
///
/// `MAINNET_WRITE_ENTRIES_LIMIT` deliberately does NOT match that same SDK helper - its own doc
/// comment admits "this is not pulling the values dynamically, so updating the SDK is necessary
/// to pick up the most recent values", and it reports `write_entries: 50`, which is stale: a live
/// check of Stellar mainnet's own network config shows `tx_max_write_ledger_entries: 200`, not 50
/// (confirmed independently - this is also what R2's ch. 16 §16.1 write-up was using before this
/// correction). Since this is a live, validator-voted network parameter (not a compile-time
/// constant), treat 200 as "the value observed at time of writing", not a permanent fact - re-check
/// against the real network before relying on headroom numbers derived from it.
const MAINNET_INSTRUCTION_LIMIT: u64 = 600_000_000;
const MAINNET_WRITE_ENTRIES_LIMIT: u32 = 200;

fn headroom_pct(used: u64, limit: u64) -> f64 {
    100.0 * (1.0 - (used as f64 / limit as f64))
}

/// Two members - the real bilateral group size the repo demo (ch. 18) uses - held fixed across
/// every `n`, since signature-verification cost doesn't scale with `n` and isn't what this spike
/// isolates.
#[test]
fn r21_transition_input_output_cost() {
    std::println!(
        "\n## R21 spike - real cost of `transition()`'s new inputs/outputs check (ch. 16 §16.1)\n"
    );
    std::println!(
        "Each input/output is its own persistent `Unspent` entry - N inputs + N outputs costs 2N \
         extra write entries on top of the 1 the root advance (shared instance storage) already \
         costs. Measures a steady-state transition: spend N entries created by an earlier \
         (unmeasured) transition, create N new ones.\n"
    );
    std::println!(
        "| N | CPU instructions | write_entries | write_bytes | headroom vs 600M instr | headroom vs 200 write_entries |"
    );
    std::println!("|---|---|---|---|---|---|");

    for &n in &[0u32, 1, 2, 5, 10, 20, 50, 75, 99, 100, 120] {
        let env = Env::default();
        env.cost_estimate().budget().reset_unlimited();
        env.cost_estimate().disable_resource_limits();

        let signing_key_1 = SigningKey::from_bytes(&[1u8; 32]);
        let signing_key_2 = SigningKey::from_bytes(&[2u8; 32]);
        let pk1 = BytesN::from_array(&env, &signing_key_1.verifying_key().to_bytes());
        let pk2 = BytesN::from_array(&env, &signing_key_2.verifying_key().to_bytes());
        let members = soroban_sdk::vec![&env, pk1.clone(), pk2.clone()];

        let contract_id = env.register(Contract, ());
        let client = ContractClient::new(&env, &contract_id);
        client.initialize(
            &members,
            &Bytes::from_slice(&env, NETWORK_PASSPHRASE),
            &BytesN::from_array(&env, &GENESIS_TX_ID),
        );

        let no_ids: Vec<BytesN<32>> = Vec::new(&env);
        let external_calls: Vec<AtomOperation> = Vec::new(&env);
        let root_0 = BytesN::from_array(&env, &[0u8; 32]);
        let root_1 = BytesN::from_array(&env, &[1u8; 32]);
        let root_2 = BytesN::from_array(&env, &[2u8; 32]);

        // Bootstrap (own top-level call, unmeasured - cost_estimate().resources() only ever
        // reports the *last* invocation): create N entries as outputs.
        let bootstrap_outputs = ids(&env, "v1", n);
        let tx_1 = BytesN::from_array(&env, &[1u8; 32]);
        let sig_1a = sign_transition(
            &env, &contract_id, &tx_1, &root_0, &root_1, &no_ids, &bootstrap_outputs,
            &external_calls, &signing_key_1,
        );
        let sig_1b = sign_transition(
            &env, &contract_id, &tx_1, &root_0, &root_1, &no_ids, &bootstrap_outputs,
            &external_calls, &signing_key_2,
        );
        client.transition(
            &tx_1,
            &root_1,
            &no_ids,
            &bootstrap_outputs,
            &external_calls,
            &soroban_sdk::vec![
                &env,
                (pk1.clone(), sig_1a),
                (pk2.clone(), sig_1b),
            ],
        );

        // Measured: spend the N bootstrapped entries, create N new ones - the realistic
        // steady-state shape a real business invocation with N touched ledger entries produces.
        let measured_outputs = ids(&env, "v2", n);
        let tx_2 = BytesN::from_array(&env, &[2u8; 32]);
        let sig_2a = sign_transition(
            &env, &contract_id, &tx_2, &root_1, &root_2, &bootstrap_outputs, &measured_outputs,
            &external_calls, &signing_key_1,
        );
        let sig_2b = sign_transition(
            &env, &contract_id, &tx_2, &root_1, &root_2, &bootstrap_outputs, &measured_outputs,
            &external_calls, &signing_key_2,
        );
        client.transition(
            &tx_2,
            &root_2,
            &bootstrap_outputs,
            &measured_outputs,
            &external_calls,
            &soroban_sdk::vec![
                &env,
                (pk1, sig_2a),
                (pk2, sig_2b),
            ],
        );

        let resources = env.cost_estimate().resources();
        let instructions = resources.instructions as u64;
        let write_entries = resources.write_entries;
        let write_bytes = resources.write_bytes;
        std::println!(
            "| {n} | {instructions} | {write_entries} | {write_bytes} | {:.1}% | {:.1}% |",
            headroom_pct(instructions, MAINNET_INSTRUCTION_LIMIT),
            headroom_pct(write_entries as u64, MAINNET_WRITE_ENTRIES_LIMIT as u64),
        );
    }
}
