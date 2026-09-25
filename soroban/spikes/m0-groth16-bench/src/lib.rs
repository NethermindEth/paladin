// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//! Chapter 13 Phase 5 M0 feasibility spike - NOT a workspace member, NOT shipped.
//!
//! Ports the BN254 Groth16 verifier design from
//! `NethermindEth/stellar-private-payments` (`contracts/circom-groth16-verifier`,
//! Apache-2.0-licensed, commit as of 2026-07 `main`), adapted to this repo's soroban-sdk 27.0.0
//! pin. The struct shapes, byte layout (G2 in `c1||c0` order), and `verify_with_vk` pairing-check
//! logic are a direct port of that reference; this file exists to independently confirm the
//! design compiles and verifies correctly against our own pinned toolchain (soroban-sdk 27.0.0,
//! rustc 1.92.0 - the reference pins soroban-sdk "26") before any of it lands in a real contract.
//! See the chapter 13 plan (M0 spike section) for the full risk analysis (R2/R3) this crate exists
//! to resolve.
#![no_std]

extern crate alloc;

use soroban_sdk::{
    contracterror, contracttype,
    crypto::bn254::{Bn254Fr, Bn254G1Affine as G1Affine, Bn254G2Affine as G2Affine},
    vec, Env, Vec,
};

/// Errors that can occur during Groth16 proof verification.
#[contracterror]
#[derive(Copy, Clone, Debug, Eq, PartialEq, PartialOrd, Ord)]
#[repr(u32)]
pub enum Groth16Error {
    /// The pairing product did not equal identity.
    InvalidProof = 0,
    /// The public inputs length does not match the verification key.
    MalformedPublicInputs = 1,
}

/// Groth16 verification key for BN254 curve.
#[derive(Clone)]
pub struct VerificationKey {
    pub alpha: G1Affine,
    pub beta: G2Affine,
    pub gamma: G2Affine,
    pub delta: G2Affine,
    pub ic: Vec<G1Affine>,
}

/// Groth16 proof composed of points A, B, and C.
///
/// `b` (G2) uses Soroban's `c1||c0` (imaginary||real) ordering - matches
/// `Bn254G2Affine`'s own byte convention, confirmed against soroban-sdk 27.0.0's doc comment and
/// the reference implementation's `g2_to_soroban_bytes` helper.
#[derive(Clone)]
#[contracttype]
pub struct Groth16Proof {
    pub a: G1Affine,
    pub b: G2Affine,
    pub c: G1Affine,
}

/// Verify a Groth16 proof against an explicit verification key.
///
/// Ported from the reference's `CircomGroth16Verifier::verify_with_vk` essentially unchanged:
/// accumulate `vk_x = ic[0] + sum(public_input[i] * ic[i+1])`, then check
/// `e(-A,B) * e(alpha,beta) * e(vk_x,gamma) * e(C,delta) == 1` via the host's `pairing_check`.
pub fn verify_with_vk(
    env: &Env,
    vk: &VerificationKey,
    proof: Groth16Proof,
    pub_inputs: Vec<Bn254Fr>,
) -> Result<bool, Groth16Error> {
    let bn = env.crypto().bn254();

    if pub_inputs.len().checked_add(1) != Some(vk.ic.len()) {
        return Err(Groth16Error::MalformedPublicInputs);
    }

    let mut vk_x = vk.ic.get(0).ok_or(Groth16Error::MalformedPublicInputs)?;

    for i in 0..pub_inputs.len() {
        let s = pub_inputs
            .get(i)
            .ok_or(Groth16Error::MalformedPublicInputs)?;
        let ic_idx = i
            .checked_add(1)
            .ok_or(Groth16Error::MalformedPublicInputs)?;
        let v = vk
            .ic
            .get(ic_idx)
            .ok_or(Groth16Error::MalformedPublicInputs)?;
        let prod = bn.g1_mul(&v, &s);
        vk_x = bn.g1_add(&vk_x, &prod);
    }

    #[allow(clippy::arithmetic_side_effects)]
    let neg_a = -proof.a;

    let g1_points = vec![env, neg_a, vk.alpha.clone(), vk_x, proof.c];
    let g2_points = vec![
        env,
        proof.b,
        vk.beta.clone(),
        vk.gamma.clone(),
        vk.delta.clone(),
    ];

    if bn.pairing_check(g1_points, g2_points) {
        Ok(true)
    } else {
        Err(Groth16Error::InvalidProof)
    }
}

mod bench;

#[cfg(test)]
mod test;

#[cfg(test)]
mod poseidon_check;

#[cfg(test)]
mod real_zeto_fixtures;

#[cfg(test)]
mod real_zeto_proof;

#[cfg(test)]
mod bench_test;
