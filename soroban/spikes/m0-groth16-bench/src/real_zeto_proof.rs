//! M0 spike step 3: verify a REAL Zeto proof (not a synthetic ark-groth16 one) through the ported
//! BN254 verifier. `fixtures/real_zeto_proof.json` was produced by Zeto's own, completely
//! unmodified Go prover (see the throwaway generator referenced in the chapter 13 plan's M0
//! section - not committed with production code) against the real `anon` circuit's wasm/zkey
//! (`domains/zeto/zkp/anon_js/anon.wasm`, `domains/zeto/zkp/anon.zkey`). `fixtures/anon-vkey.json`
//! is Zeto's own real, unmodified verification key for that circuit.
//!
//! This is the load-bearing M0 check: if this returns `Ok(true)`, the BN254 pairing check and the
//! snarkjs-decimal-to-Soroban-bytes conversion (independently re-implemented in
//! `real_zeto_fixtures.rs`; the production version lives in `sdk/go/pkg/groth16bn254`, since
//! that's where the real conversion needs to happen - at Go-side transaction-assembly time) are
//! both correct against a real, non-synthetic Circom/snarkjs artifact - not just the reference's
//! own ark-groth16 smoke test.
extern crate alloc;
extern crate std;

use super::*;
use crate::real_zeto_fixtures::{fe_bytes, g1_bytes, g2_bytes, load_fixtures};
use soroban_sdk::BytesN;

#[test]
fn verifies_real_zeto_proof() {
    let env = Env::default();
    let (proof, vk_raw) = load_fixtures();

    let groth16_proof = Groth16Proof {
        a: G1Affine::from_bytes(BytesN::from_array(
            &env,
            &g1_bytes(&proof.proof.pi_a[0], &proof.proof.pi_a[1]),
        )),
        b: G2Affine::from_bytes(BytesN::from_array(
            &env,
            &g2_bytes(
                &proof.proof.pi_b[0][0],
                &proof.proof.pi_b[0][1],
                &proof.proof.pi_b[1][0],
                &proof.proof.pi_b[1][1],
            ),
        )),
        c: G1Affine::from_bytes(BytesN::from_array(
            &env,
            &g1_bytes(&proof.proof.pi_c[0], &proof.proof.pi_c[1]),
        )),
    };

    let mut ic_vec: Vec<G1Affine> = Vec::new(&env);
    for pt in vk_raw.ic.iter() {
        ic_vec.push_back(G1Affine::from_bytes(BytesN::from_array(
            &env,
            &g1_bytes(&pt[0], &pt[1]),
        )));
    }

    let vk = VerificationKey {
        alpha: G1Affine::from_bytes(BytesN::from_array(
            &env,
            &g1_bytes(&vk_raw.vk_alpha_1[0], &vk_raw.vk_alpha_1[1]),
        )),
        beta: G2Affine::from_bytes(BytesN::from_array(
            &env,
            &g2_bytes(
                &vk_raw.vk_beta_2[0][0],
                &vk_raw.vk_beta_2[0][1],
                &vk_raw.vk_beta_2[1][0],
                &vk_raw.vk_beta_2[1][1],
            ),
        )),
        gamma: G2Affine::from_bytes(BytesN::from_array(
            &env,
            &g2_bytes(
                &vk_raw.vk_gamma_2[0][0],
                &vk_raw.vk_gamma_2[0][1],
                &vk_raw.vk_gamma_2[1][0],
                &vk_raw.vk_gamma_2[1][1],
            ),
        )),
        delta: G2Affine::from_bytes(BytesN::from_array(
            &env,
            &g2_bytes(
                &vk_raw.vk_delta_2[0][0],
                &vk_raw.vk_delta_2[0][1],
                &vk_raw.vk_delta_2[1][0],
                &vk_raw.vk_delta_2[1][1],
            ),
        )),
        ic: ic_vec,
    };

    let mut public_inputs: Vec<Bn254Fr> = Vec::new(&env);
    for signal in proof.pub_signals.iter() {
        public_inputs.push_back(Bn254Fr::from_bytes(BytesN::from_array(
            &env,
            &fe_bytes(signal),
        )));
    }

    let result = verify_with_vk(&env, &vk, groth16_proof, public_inputs);
    assert_eq!(result, Ok(true));
}

#[test]
fn rejects_real_zeto_proof_with_tampered_public_input() {
    let env = Env::default();
    let (proof, vk_raw) = load_fixtures();

    let groth16_proof = Groth16Proof {
        a: G1Affine::from_bytes(BytesN::from_array(
            &env,
            &g1_bytes(&proof.proof.pi_a[0], &proof.proof.pi_a[1]),
        )),
        b: G2Affine::from_bytes(BytesN::from_array(
            &env,
            &g2_bytes(
                &proof.proof.pi_b[0][0],
                &proof.proof.pi_b[0][1],
                &proof.proof.pi_b[1][0],
                &proof.proof.pi_b[1][1],
            ),
        )),
        c: G1Affine::from_bytes(BytesN::from_array(
            &env,
            &g1_bytes(&proof.proof.pi_c[0], &proof.proof.pi_c[1]),
        )),
    };

    let mut ic_vec: Vec<G1Affine> = Vec::new(&env);
    for pt in vk_raw.ic.iter() {
        ic_vec.push_back(G1Affine::from_bytes(BytesN::from_array(
            &env,
            &g1_bytes(&pt[0], &pt[1]),
        )));
    }
    let vk = VerificationKey {
        alpha: G1Affine::from_bytes(BytesN::from_array(
            &env,
            &g1_bytes(&vk_raw.vk_alpha_1[0], &vk_raw.vk_alpha_1[1]),
        )),
        beta: G2Affine::from_bytes(BytesN::from_array(
            &env,
            &g2_bytes(
                &vk_raw.vk_beta_2[0][0],
                &vk_raw.vk_beta_2[0][1],
                &vk_raw.vk_beta_2[1][0],
                &vk_raw.vk_beta_2[1][1],
            ),
        )),
        gamma: G2Affine::from_bytes(BytesN::from_array(
            &env,
            &g2_bytes(
                &vk_raw.vk_gamma_2[0][0],
                &vk_raw.vk_gamma_2[0][1],
                &vk_raw.vk_gamma_2[1][0],
                &vk_raw.vk_gamma_2[1][1],
            ),
        )),
        delta: G2Affine::from_bytes(BytesN::from_array(
            &env,
            &g2_bytes(
                &vk_raw.vk_delta_2[0][0],
                &vk_raw.vk_delta_2[0][1],
                &vk_raw.vk_delta_2[1][0],
                &vk_raw.vk_delta_2[1][1],
            ),
        )),
        ic: ic_vec,
    };

    let mut public_inputs: Vec<Bn254Fr> = Vec::new(&env);
    for (i, signal) in proof.pub_signals.iter().enumerate() {
        if i == 0 {
            let mut bytes = fe_bytes(signal);
            bytes[31] ^= 0x01;
            public_inputs.push_back(Bn254Fr::from_bytes(BytesN::from_array(&env, &bytes)));
        } else {
            public_inputs.push_back(Bn254Fr::from_bytes(BytesN::from_array(
                &env,
                &fe_bytes(signal),
            )));
        }
    }

    let result = verify_with_vk(&env, &vk, groth16_proof, public_inputs);
    assert_eq!(result, Err(Groth16Error::InvalidProof));
}
