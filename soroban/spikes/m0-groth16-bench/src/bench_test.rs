//! M0 spike step 4: R2 resource benchmark, using soroban-sdk's own testutils cost-estimation API
//! (`env.cost_estimate()`) rather than a live network deploy - `stellar contract deploy` against
//! every available local network in this environment fails generically at the upload/create step
//! (confirmed: even the already-verified, non-bn254 `factory.wasm` from chapter 13 Phase 2 fails
//! identically), consistent with a protocol-version mismatch between the pinned soroban-sdk
//! 27.0.0 toolchain and the available local quickstart network (protocol 25) / stellar-cli
//! (26.0.0) - not a cryptography issue. `cost_estimate()` measures the same CPU/memory/read-write
//! metering the network would apply, without needing a live node - the officially documented way
//! to do exactly this kind of resource benchmarking in this soroban-sdk version.
extern crate alloc;
extern crate std;

use soroban_env_host::InvocationResourceLimits;
use soroban_sdk::{testutils::cost_estimate::NetworkInvocationResourceLimits, BytesN, Env, Vec};

use crate::bench::BenchContractClient;
use crate::real_zeto_fixtures::{fe_bytes, g1_bytes, g2_bytes, load_fixtures};

struct RealFixtureHex {
    vk_alpha: [u8; 64],
    vk_beta: [u8; 128],
    vk_gamma: [u8; 128],
    vk_delta: [u8; 128],
    vk_ic: alloc::vec::Vec<[u8; 64]>,
    proof_a: [u8; 64],
    proof_b: [u8; 128],
    proof_c: [u8; 64],
    public_inputs: alloc::vec::Vec<[u8; 32]>,
}

fn load_real_fixture() -> RealFixtureHex {
    let (proof, vk_raw) = load_fixtures();

    RealFixtureHex {
        vk_alpha: g1_bytes(&vk_raw.vk_alpha_1[0], &vk_raw.vk_alpha_1[1]),
        vk_beta: g2_bytes(
            &vk_raw.vk_beta_2[0][0],
            &vk_raw.vk_beta_2[0][1],
            &vk_raw.vk_beta_2[1][0],
            &vk_raw.vk_beta_2[1][1],
        ),
        vk_gamma: g2_bytes(
            &vk_raw.vk_gamma_2[0][0],
            &vk_raw.vk_gamma_2[0][1],
            &vk_raw.vk_gamma_2[1][0],
            &vk_raw.vk_gamma_2[1][1],
        ),
        vk_delta: g2_bytes(
            &vk_raw.vk_delta_2[0][0],
            &vk_raw.vk_delta_2[0][1],
            &vk_raw.vk_delta_2[1][0],
            &vk_raw.vk_delta_2[1][1],
        ),
        vk_ic: vk_raw
            .ic
            .iter()
            .map(|pt| g1_bytes(&pt[0], &pt[1]))
            .collect(),
        proof_a: g1_bytes(&proof.proof.pi_a[0], &proof.proof.pi_a[1]),
        proof_b: g2_bytes(
            &proof.proof.pi_b[0][0],
            &proof.proof.pi_b[0][1],
            &proof.proof.pi_b[1][0],
            &proof.proof.pi_b[1][1],
        ),
        proof_c: g1_bytes(&proof.proof.pi_c[0], &proof.proof.pi_c[1]),
        public_inputs: proof.pub_signals.iter().map(|s| fe_bytes(s)).collect(),
    }
}

fn run_bench(env: &Env, client: &BenchContractClient, fixture: &RealFixtureHex, n: u32) -> bool {
    let mut vk_ic_vec: Vec<BytesN<64>> = Vec::new(env);
    for b in fixture.vk_ic.iter() {
        vk_ic_vec.push_back(BytesN::from_array(env, b));
    }

    let mut public_inputs: Vec<BytesN<32>> = Vec::new(env);
    for b in fixture.public_inputs.iter() {
        public_inputs.push_back(BytesN::from_array(env, b));
    }

    let mut nullifiers: Vec<BytesN<32>> = Vec::new(env);
    for i in 0..n {
        let mut bytes = [0u8; 32];
        bytes[28..].copy_from_slice(&i.to_be_bytes());
        nullifiers.push_back(BytesN::from_array(env, &bytes));
    }

    client.bench(
        &BytesN::from_array(env, &fixture.vk_alpha),
        &BytesN::from_array(env, &fixture.vk_beta),
        &BytesN::from_array(env, &fixture.vk_gamma),
        &BytesN::from_array(env, &fixture.vk_delta),
        &vk_ic_vec,
        &BytesN::from_array(env, &fixture.proof_a),
        &BytesN::from_array(env, &fixture.proof_b),
        &BytesN::from_array(env, &fixture.proof_c),
        &public_inputs,
        &nullifiers,
    )
}

#[test]
fn r2_resource_benchmark() {
    let fixture = load_real_fixture();
    let mainnet = InvocationResourceLimits::mainnet();

    std::println!("\n| N | CPU instructions | write_entries | write_bytes | fee (stroops) |");
    std::println!("|---|---|---|---|---|");

    for n in [2u32, 10, 20, 50] {
        let env = Env::default();
        env.cost_estimate().disable_resource_limits();
        let contract_id = env.register(crate::bench::BenchContract, ());
        let client = BenchContractClient::new(&env, &contract_id);

        let ok = run_bench(&env, &client, &fixture, n);
        assert!(ok, "verify failed for N={n}");

        let resources = env.cost_estimate().resources();
        let fee = env.cost_estimate().fee();

        std::println!(
            "| {n} | {} | {} | {} | {} |",
            resources.instructions,
            resources.write_entries,
            resources.write_bytes,
            fee.total
        );

        let insn_headroom = 1.0 - (resources.instructions as f64 / mainnet.instructions as f64);
        let write_entries_headroom =
            1.0 - (resources.write_entries as f64 / mainnet.write_entries as f64);
        std::println!(
            "  N={n}: instruction headroom {:.1}%, write_entries headroom {:.1}%",
            insn_headroom * 100.0,
            write_entries_headroom * 100.0
        );
    }
}

#[test]
fn poseidon_tree_maintenance_cost() {
    // Estimates the cost of maintaining a real on-chain incremental Merkle tree (matching EVM
    // Zeto's SmtLib.addLeaf behavior - MAX_SMT_DEPTH=64 Poseidon(t=3) branch hashes per insert,
    // confirmed against `@iden3/contracts`' actual Solidity source) - NOT part of the originally
    // scoped M0 benchmark (which only measured flat nullifier writes).
    //
    // `cost_estimate().budget().reset_unlimited()` removes the *test harness's own* default CPU
    // ceiling (a safety net against runaway loops in ordinary tests, unrelated to any real
    // network limit) - without it, N=128 panics with `Error(Budget, ExceededLimit)` well below
    // the real `mainnet()` 600M-instruction limit (128 calls cost ~187M, comfortably under
    // 600M). `disable_resource_limits()` alone (as an earlier pass through this file assumed)
    // does NOT lift this - that call only disables the SDK-level `InvocationResourceLimits`
    // check (which defaults to enforcing `mainnet()`), a separate mechanism from the lower-level
    // host `Budget`'s own default ceiling.
    std::println!("\n| tree depth (N poseidon calls) | CPU instructions |");
    std::println!("|---|---|");
    for n in [1u32, 64, 128, 192, 256] {
        let env = Env::default();
        env.cost_estimate().budget().reset_unlimited();
        env.cost_estimate().disable_resource_limits();
        let contract_id = env.register(crate::bench::BenchContract, ());
        let client = BenchContractClient::new(&env, &contract_id);
        let _ = client.bench_poseidon(&n);
        let resources = env.cost_estimate().resources();
        std::println!("| {n} | {} |", resources.instructions);
    }
}
