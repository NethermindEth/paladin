//! M0 spike step 4 (R2 resource benchmark): a minimal contract combining one Groth16 verify with
//! N nullifier writes, deployed to a real local Stellar network for `stellar contract invoke
//! --cost` measurement. Not a production contract - VK/proof/public-inputs are passed as raw
//! bytes (no compile-time embedding) purely to keep this benchmark's CLI invocation simple.
use soroban_sdk::{
    contract, contractimpl, contracttype,
    crypto::bn254::{Bn254Fr, Bn254G1Affine as G1Affine, Bn254G2Affine as G2Affine},
    BytesN, Env, Vec,
};

use crate::{verify_with_vk, Groth16Proof, VerificationKey};

#[contracttype]
pub enum DataKey {
    Nullifier(BytesN<32>),
}

#[contract]
pub struct BenchContract;

#[contractimpl]
impl BenchContract {
    /// Verifies one Groth16 proof, then writes `nullifiers.len()` persistent entries - the
    /// combined per-transfer cost shape SZeto's real `transfer` would incur.
    #[allow(clippy::too_many_arguments)]
    pub fn bench(
        env: Env,
        vk_alpha: BytesN<64>,
        vk_beta: BytesN<128>,
        vk_gamma: BytesN<128>,
        vk_delta: BytesN<128>,
        vk_ic: Vec<BytesN<64>>,
        proof_a: BytesN<64>,
        proof_b: BytesN<128>,
        proof_c: BytesN<64>,
        public_inputs: Vec<BytesN<32>>,
        nullifiers: Vec<BytesN<32>>,
    ) -> bool {
        let mut ic_vec: Vec<G1Affine> = Vec::new(&env);
        for b in vk_ic.iter() {
            ic_vec.push_back(G1Affine::from_bytes(b));
        }
        let vk = VerificationKey {
            alpha: G1Affine::from_bytes(vk_alpha),
            beta: G2Affine::from_bytes(vk_beta),
            gamma: G2Affine::from_bytes(vk_gamma),
            delta: G2Affine::from_bytes(vk_delta),
            ic: ic_vec,
        };
        let proof = Groth16Proof {
            a: G1Affine::from_bytes(proof_a),
            b: G2Affine::from_bytes(proof_b),
            c: G1Affine::from_bytes(proof_c),
        };
        let mut inputs: Vec<Bn254Fr> = Vec::new(&env);
        for b in public_inputs.iter() {
            inputs.push_back(Bn254Fr::from_bytes(b));
        }

        let ok = verify_with_vk(&env, &vk, proof, inputs) == Ok(true);
        if !ok {
            return false;
        }

        for n in nullifiers.iter() {
            env.storage()
                .persistent()
                .set(&DataKey::Nullifier(n), &true);
        }
        true
    }
}

#[contractimpl]
impl BenchContract {
    /// Chapter 13 Phase 5 follow-up: measures the cost of N sequential Poseidon(t=3) calls, to
    /// estimate the real cost of maintaining an on-chain incremental Merkle tree (à la EVM Zeto's
    /// SmtLib.addLeaf) - NOT part of the originally scoped M0 benchmark, which only measured flat
    /// nullifier writes.
    pub fn bench_poseidon(env: Env, n: u32) -> soroban_sdk::U256 {
        use soroban_poseidon::poseidon_hash;
        let mut acc = soroban_sdk::U256::from_u32(&env, 1);
        for _ in 0..n {
            let inputs = soroban_sdk::vec![&env, acc.clone(), soroban_sdk::U256::from_u32(&env, 2)];
            acc = poseidon_hash::<3, Bn254Fr>(&env, &inputs);
        }
        acc
    }
}
