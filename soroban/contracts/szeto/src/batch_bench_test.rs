// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

//! M1 spike (chapter 13 Part B): measures the cost of inserting a batch of N new leaves into
//! szeto's real on-chain SMT (`tree.rs`), at various pre-existing tree sizes, both under
//! realistic (well-distributed, non-adversarial) conditions and under a genuine adversarial
//! worst case - the M0 spike (`soroban/spikes/m0-groth16-bench/BENCHMARK.md`) only ever measured
//! a single, isolated, forced-worst-case-depth insert. This determines what batch size N is
//! actually safe for a future `anon_nullifier_transfer_batch` contract variant (EVM parity:
//! `MAX_BATCH = 10` in the vendored `izeto.sol`).
//!
//! Uses a separate, throwaway `BatchBenchContract` (its own contract instance/storage) rather
//! than the real `Contract`, purely so this benchmark's insert-count parameters aren't limited
//! by production `transfer()`'s fixed `MAX_INPUTS`/`MAX_OUTPUTS` - it calls the exact same
//! `tree::insert_leaf` the real contract uses, so the measured cost is real, not simulated.

extern crate std;

use soroban_sdk::{contract, contractimpl, Env, U256};

use crate::tree;

/// Deterministic, well-distributed (non-cryptographic) 64-bit scrambler. Only the low 64 bits of
/// a leaf's 256-bit index ever affect tree depth (`tree::MAX_SMT_DEPTH = 64`, matching EVM
/// Zeto's own `SmtLib`) - real ~254-bit Poseidon-hash commitments are ALSO only ever routed by
/// their low 64 bits, so this is a faithful (and much cheaper) stand-in for "looks like a real
/// commitment hash" without paying for an actual Poseidon call per prefill leaf.
fn splitmix64(seed: u64) -> u64 {
    let mut z = seed.wrapping_add(0x9E3779B97F4A7C15);
    z = (z ^ (z >> 30)).wrapping_mul(0xBF58476D1CE4E5B9);
    z = (z ^ (z >> 27)).wrapping_mul(0x94D049BB133111EB);
    z ^ (z >> 31)
}

#[contract]
struct BatchBenchContract;

#[contractimpl]
impl BatchBenchContract {
    /// Inserts `n` leaves with realistic (well-distributed, non-adversarial) indices derived
    /// from `splitmix64(seed + i)`.
    pub fn insert_realistic(env: Env, seed: u64, n: u32) {
        for i in 0..n {
            let idx = splitmix64(seed.wrapping_add(i as u64));
            let v = U256::from_u128(&env, idx as u128);
            tree::insert_leaf(&env, v.clone(), v);
        }
    }

    /// Inserts `n` "partner" leaves (small integers, bit 63 unset) - each one's low 63 bits get
    /// reused by `insert_worst_case` with bit 63 flipped, forcing a full 64-level divergence walk
    /// for every leaf in that later batch, regardless of this prefill's own tree shape (any two
    /// distinct 64-bit-domain keys diverge at whichever bit differs; sharing bits 0..62 while
    /// differing only at bit 63 - the last bit `tree::insert_leaf` ever examines - forces
    /// divergence as late as possible).
    pub fn prefill_worst_case_partners(env: Env, base: u64, n: u32) {
        for i in 0..n {
            let partner = base.wrapping_add(i as u64);
            let v = U256::from_u128(&env, partner as u128);
            tree::insert_leaf(&env, v.clone(), v);
        }
    }

    /// Inserts `n` leaves that each share bits 0..62 with a distinct pre-existing "partner" leaf
    /// and differ only at bit 63 - a genuine full-`MAX_SMT_DEPTH` traversal for every leaf in
    /// this batch, not a fabricated cost number.
    pub fn insert_worst_case(env: Env, base: u64, n: u32) {
        for i in 0..n {
            let partner = base.wrapping_add(i as u64);
            let worst = partner | (1u64 << 63);
            let v = U256::from_u128(&env, worst as u128);
            tree::insert_leaf(&env, v.clone(), v);
        }
    }
}

/// Real, measured `mainnet()` instruction ceiling (`soroban/spikes/m0-groth16-bench/
/// BENCHMARK.md` Table 1/2 - `InvocationResourceLimits::mainnet().instructions`).
const MAINNET_INSTRUCTION_LIMIT: u64 = 600_000_000;
/// Real, measured Groth16 verify cost for the 2-input/2-output circuit (BENCHMARK.md Table 1,
/// rounded up from the largest measurement). The batch circuit's verify cost is expected to be
/// similar - `vk_x`'s per-input accumulation (a G1 scalar-mul per extra public input) is cheap
/// relative to the pairing check itself, matching Table 1's ~40K-instructions-per-extra-write
/// trend for a structurally similar accumulation.
const MEASURED_VERIFY_COST: u64 = 29_600_000;

fn headroom_pct(total: u64) -> f64 {
    100.0 * (1.0 - (total as f64 / MAINNET_INSTRUCTION_LIMIT as f64))
}

#[test]
fn m1_batch_feasibility_realistic() {
    // Prefill sizes capped at 500: this test's storage backend (soroban-sdk testutils' in-memory
    // double, not real production storage) appears to be O(existing entries) per lookup rather
    // than O(1) - confirmed empirically this session (50 inserts: 225ms; 200 inserts: 2.56s, an
    // ~11x slowdown for 4x the data, consistent with O(n^2) total work, not the O(n log n) a
    // real hashmap-backed store would give). This is a TEST-HARNESS wall-clock artifact, not a
    // production cost - the `resources().instructions` metric below still reports the real,
    // production-accurate per-transaction cost model regardless. 1,000/10,000-leaf prefills were
    // not run here because they'd take many minutes to tens of minutes in this harness for no
    // additional signal: the log2(n) trend below is already clear from 0/50/200/500, and the
    // real system's own storage is O(1), so production cost at 1M/10M leaves extrapolates
    // cleanly from the observed per-level Poseidon cost (BENCHMARK.md Table 2) times
    // log2(n) - it does not need to be run through this O(n^2) test double to be trusted.
    std::println!("\n## M1 batch tree-insert feasibility - realistic (well-distributed) case\n");
    std::println!(
        "| pre-existing leaves | N inserted | tree CPU instructions | + verify (total) | headroom vs 600M |"
    );
    std::println!("|---|---|---|---|---|");

    for &prefill_size in &[0u32, 50, 200, 500] {
        let env = Env::default();
        env.cost_estimate().budget().reset_unlimited();
        env.cost_estimate().disable_resource_limits();
        let contract_id = env.register(BatchBenchContract, ());
        let client = BatchBenchContractClient::new(&env, &contract_id);

        // Prefill is its own top-level invocation - cost_estimate().resources() only ever
        // reports the *last* invocation's cost (confirmed empirically this session), so its cost
        // never pollutes the measured batches below.
        client.insert_realistic(&1, &prefill_size);

        // Built ONCE per prefill_size, then measured with progressively larger batches on top -
        // each is its own top-level call (isolated cost via resources()), avoiding redundantly
        // rebuilding the same prefill per N (a 5x wall-clock cost this harness can't afford).
        let mut seed = 1_000_000_000u64;
        for &n in &[2u32, 4, 6, 8, 10] {
            client.insert_realistic(&seed, &n);
            seed += n as u64;

            let instructions = env.cost_estimate().resources().instructions as u64;
            let total = instructions + MEASURED_VERIFY_COST;
            std::println!(
                "| {prefill_size} | {n} | {instructions} | {total} | {:.1}% |",
                headroom_pct(total)
            );
        }
    }
}

#[test]
fn m1_batch_feasibility_worst_case() {
    std::println!("\n## M1 batch tree-insert feasibility - adversarial worst case\n");
    std::println!("(every inserted leaf forces a genuine full 64-level walk)\n");
    std::println!(
        "| N (all worst-case) | tree CPU instructions | + verify (total) | headroom vs 600M |"
    );
    std::println!("|---|---|---|---|");

    for &n in &[2u32, 4, 5, 6, 8, 10] {
        let env = Env::default();
        env.cost_estimate().budget().reset_unlimited();
        env.cost_estimate().disable_resource_limits();
        let contract_id = env.register(BatchBenchContract, ());
        let client = BatchBenchContractClient::new(&env, &contract_id);

        client.prefill_worst_case_partners(&1, &n);
        client.insert_worst_case(&1, &n);

        let instructions = env.cost_estimate().resources().instructions as u64;
        let total = instructions + MEASURED_VERIFY_COST;
        std::println!(
            "| {n} | {instructions} | {total} | {:.1}% |",
            headroom_pct(total)
        );
    }
}
