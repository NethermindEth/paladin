//! Smoke test confirming the ported verifier (see `lib.rs`'s attribution comment) compiles and
//! verifies correctly against this repo's own pinned soroban-sdk 27.0.0 / rustc 1.92.0 - the
//! reference implementation this was ported from pins soroban-sdk "26". Uses a synthetic
//! ark-groth16 circuit (not a real Zeto/circom circuit) purely to smoke-test the BN254 pairing
//! check and byte serialization end-to-end in a real (non-mocked) Soroban host - mirrors the
//! reference's own `verifies_valid_proof` test. A *real* Zeto circuit's proof is verified
//! separately (see the chapter 13 plan's M0 spike section, step 3) - that is the load-bearing
//! check; this one only confirms the port itself didn't regress across the soroban-sdk version
//! difference.
extern crate std;

use super::*;
use ark_bn254::{Bn254, Fr as ArkFr};
use ark_ff::{BigInteger, Field, PrimeField};
use ark_groth16::{Groth16, Proof};
use ark_relations::gr1cs::{ConstraintSynthesizer, ConstraintSystemRef, SynthesisError, Variable};
use ark_std::rand::{rngs::StdRng, SeedableRng};
use soroban_sdk::BytesN;

/// Trivial circuit exposing a handful of public inputs - enough to exercise the `ic`
/// accumulator loop without needing a real circuit.
#[derive(Clone)]
struct SmallCircuit<F: Field> {
    inputs: [F; 3],
}

impl<F: Field> ConstraintSynthesizer<F> for SmallCircuit<F> {
    fn generate_constraints(self, cs: ConstraintSystemRef<F>) -> Result<(), SynthesisError> {
        let mut input_vars = alloc::vec::Vec::with_capacity(self.inputs.len());
        for value in self.inputs {
            input_vars.push(cs.new_input_variable(|| Ok(value))?);
        }
        let witness = cs.new_witness_variable(|| Ok(self.inputs[0]))?;
        cs.enforce_r1cs_constraint(
            || witness.into(),
            || Variable::One.into(),
            || input_vars[0].into(),
        )?;
        Ok(())
    }
}

fn fr_from_ark(env: &Env, value: ArkFr) -> Bn254Fr {
    let bytes = value.into_bigint().to_bytes_be();
    let mut buf = [0u8; 32];
    buf.copy_from_slice(&bytes);
    Bn254Fr::from_bytes(BytesN::from_array(env, &buf))
}

fn bigint_to_be_32<B: BigInteger>(b: B) -> [u8; 32] {
    let bytes = b.to_bytes_be();
    let mut out = [0u8; 32];
    out.copy_from_slice(&bytes);
    out
}

/// `G1Affine` -> Soroban's 64-byte (x || y) big-endian layout. Same math as the reference's
/// `circuit_keys::g1_to_soroban_bytes`.
fn g1_bytes_from_ark(p: ark_bn254::g1::G1Affine) -> [u8; 64] {
    let (x, y) = (p.x, p.y);
    let mut out = [0u8; 64];
    out[..32].copy_from_slice(&bigint_to_be_32(x.into_bigint()));
    out[32..].copy_from_slice(&bigint_to_be_32(y.into_bigint()));
    out
}

/// `G2Affine` -> Soroban's 128-byte `c1||c0` big-endian layout. Same math as the reference's
/// `circuit_keys::g2_to_soroban_bytes`.
fn g2_bytes_from_ark(p: ark_bn254::g2::G2Affine) -> [u8; 128] {
    let (x, y) = (p.x, p.y);
    let mut out = [0u8; 128];
    out[..32].copy_from_slice(&bigint_to_be_32(x.c1.into_bigint()));
    out[32..64].copy_from_slice(&bigint_to_be_32(x.c0.into_bigint()));
    out[64..96].copy_from_slice(&bigint_to_be_32(y.c1.into_bigint()));
    out[96..].copy_from_slice(&bigint_to_be_32(y.c0.into_bigint()));
    out
}

fn groth16_proof_from_ark(env: &Env, proof: &Proof<Bn254>) -> Groth16Proof {
    Groth16Proof {
        a: G1Affine::from_bytes(BytesN::from_array(env, &g1_bytes_from_ark(proof.a))),
        b: G2Affine::from_bytes(BytesN::from_array(env, &g2_bytes_from_ark(proof.b))),
        c: G1Affine::from_bytes(BytesN::from_array(env, &g1_bytes_from_ark(proof.c))),
    }
}

fn seeded_rng() -> StdRng {
    StdRng::seed_from_u64(7)
}

#[test]
fn verifies_synthetic_proof() {
    let env = Env::default();
    let mut rng = seeded_rng();

    let inputs = [ArkFr::from(11u64), ArkFr::from(22u64), ArkFr::from(33u64)];
    let circuit = SmallCircuit { inputs };

    let params =
        Groth16::<Bn254>::generate_random_parameters_with_reduction(circuit.clone(), &mut rng)
            .expect("params failed to generate");
    let proof = Groth16::<Bn254>::create_random_proof_with_reduction(circuit, &params, &mut rng)
        .expect("proof failed");

    let mut public_inputs: Vec<Bn254Fr> = Vec::new(&env);
    for value in inputs {
        public_inputs.push_back(fr_from_ark(&env, value));
    }

    let mut ic_vec: Vec<G1Affine> = Vec::new(&env);
    for point in params.vk.gamma_abc_g1.iter() {
        ic_vec.push_back(G1Affine::from_bytes(BytesN::from_array(
            &env,
            &g1_bytes_from_ark(*point),
        )));
    }
    let vk = VerificationKey {
        alpha: G1Affine::from_bytes(BytesN::from_array(
            &env,
            &g1_bytes_from_ark(params.vk.alpha_g1),
        )),
        beta: G2Affine::from_bytes(BytesN::from_array(
            &env,
            &g2_bytes_from_ark(params.vk.beta_g2),
        )),
        gamma: G2Affine::from_bytes(BytesN::from_array(
            &env,
            &g2_bytes_from_ark(params.vk.gamma_g2),
        )),
        delta: G2Affine::from_bytes(BytesN::from_array(
            &env,
            &g2_bytes_from_ark(params.vk.delta_g2),
        )),
        ic: ic_vec,
    };

    let groth16_proof = groth16_proof_from_ark(&env, &proof);

    let result = verify_with_vk(&env, &vk, groth16_proof, public_inputs);
    assert_eq!(result, Ok(true));
}

#[test]
fn rejects_wrong_public_input_length() {
    let env = Env::default();
    let mut rng = seeded_rng();

    let inputs = [ArkFr::from(11u64), ArkFr::from(22u64), ArkFr::from(33u64)];
    let circuit = SmallCircuit { inputs };

    let params =
        Groth16::<Bn254>::generate_random_parameters_with_reduction(circuit.clone(), &mut rng)
            .expect("params failed to generate");
    let proof = Groth16::<Bn254>::create_random_proof_with_reduction(circuit, &params, &mut rng)
        .expect("proof failed");

    let mut ic_vec: Vec<G1Affine> = Vec::new(&env);
    for point in params.vk.gamma_abc_g1.iter() {
        ic_vec.push_back(G1Affine::from_bytes(BytesN::from_array(
            &env,
            &g1_bytes_from_ark(*point),
        )));
    }
    let vk = VerificationKey {
        alpha: G1Affine::from_bytes(BytesN::from_array(
            &env,
            &g1_bytes_from_ark(params.vk.alpha_g1),
        )),
        beta: G2Affine::from_bytes(BytesN::from_array(
            &env,
            &g2_bytes_from_ark(params.vk.beta_g2),
        )),
        gamma: G2Affine::from_bytes(BytesN::from_array(
            &env,
            &g2_bytes_from_ark(params.vk.gamma_g2),
        )),
        delta: G2Affine::from_bytes(BytesN::from_array(
            &env,
            &g2_bytes_from_ark(params.vk.delta_g2),
        )),
        ic: ic_vec,
    };

    let mut short_inputs: Vec<Bn254Fr> = Vec::new(&env);
    for value in inputs.iter().take(1) {
        short_inputs.push_back(fr_from_ark(&env, *value));
    }

    let groth16_proof = groth16_proof_from_ark(&env, &proof);
    let result = verify_with_vk(&env, &vk, groth16_proof, short_inputs);
    assert!(matches!(result, Err(Groth16Error::MalformedPublicInputs)));
}
