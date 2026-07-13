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

//! Build script embedding SZeto's Groth16 verification keys at compile time - mirrors
//! `NethermindEth/stellar-private-payments`'s `circom-groth16-verifier/build.rs` pattern
//! (Apache-2.0), except reading directly from this repo's own `domains/zeto/zkp/*-vkey.json`
//! files rather than an env-var path.
//!
//! Chapter 13 Part B phase B.3 (batch support): embeds BOTH the non-batch `anon_nullifier_
//! transfer-vkey.json` (nPublic:7, 2 inputs/2 outputs) and the batch `anon_nullifier_transfer_
//! batch-vkey.json` (nPublic:31, 10 inputs/10 outputs) verification keys - these are two
//! completely separate trusted setups (different circuits, different alpha/beta/gamma/delta/IC),
//! not a parameterized single circuit, so both need their own full constant set. `lib.rs` selects
//! between them at runtime based on how many real (non-padding) nullifiers/outputs a `transfer`
//! call actually uses - mirrors EVM's own `verifyProof`, which does the same regular-vs-batch
//! verifier selection (`solidity/node_modules/@lfdecentralizedtrust/zeto-contracts/contracts/
//! zeto_anon_nullifier.sol`).
//!
//! No persistent storage or admin mutation for either VK at all - both are part of the contract
//! Wasm itself, same rationale as the reference.

use num_bigint::BigUint;
use serde_json::Value;
use std::{env, fmt::Write as _, fs, path::PathBuf};

fn parse_fq_decimal(value: &str) -> [u8; 32] {
    let n = BigUint::parse_bytes(value.as_bytes(), 10)
        .unwrap_or_else(|| panic!("invalid decimal field element: {value}"));
    let be = n.to_bytes_be();
    assert!(be.len() <= 32, "field element too large: {value}");
    let mut out = [0u8; 32];
    out[32 - be.len()..].copy_from_slice(&be);
    out
}

/// snarkjs G1 point `[x, y, "1"]` -> Soroban's 64-byte (x || y) big-endian layout.
fn g1_bytes(pt: &Value) -> [u8; 64] {
    let arr = pt.as_array().expect("G1 point must be a JSON array");
    let x = parse_fq_decimal(arr[0].as_str().expect("G1.x must be a string"));
    let y = parse_fq_decimal(arr[1].as_str().expect("G1.y must be a string"));
    let mut out = [0u8; 64];
    out[..32].copy_from_slice(&x);
    out[32..].copy_from_slice(&y);
    out
}

/// snarkjs G2 point `[[x_c0,x_c1],[y_c0,y_c1],["1","0"]]` -> Soroban's 128-byte layout, in
/// `c1||c0` (imaginary||real) order - the reverse of snarkjs's own `[c0, c1]` convention,
/// confirmed against soroban-sdk's `Bn254G2Affine` doc comment and empirically (chapter 13
/// Phase 5 M0 spike, `soroban/spikes/m0-groth16-bench`).
fn g2_bytes(pt: &Value) -> [u8; 128] {
    let arr = pt.as_array().expect("G2 point must be a JSON array");
    let x = arr[0].as_array().expect("G2.x must be a JSON array");
    let y = arr[1].as_array().expect("G2.y must be a JSON array");
    let x_c0 = parse_fq_decimal(x[0].as_str().expect("G2.x.c0 must be a string"));
    let x_c1 = parse_fq_decimal(x[1].as_str().expect("G2.x.c1 must be a string"));
    let y_c0 = parse_fq_decimal(y[0].as_str().expect("G2.y.c0 must be a string"));
    let y_c1 = parse_fq_decimal(y[1].as_str().expect("G2.y.c1 must be a string"));
    let mut out = [0u8; 128];
    out[0..32].copy_from_slice(&x_c1);
    out[32..64].copy_from_slice(&x_c0);
    out[64..96].copy_from_slice(&y_c1);
    out[96..128].copy_from_slice(&y_c0);
    out
}

fn fmt_bytes(bytes: &[u8]) -> String {
    let mut s = String::from("[");
    for (i, b) in bytes.iter().enumerate() {
        if i > 0 {
            s.push(',');
        }
        write!(s, "0x{b:02x}").expect("infallible write to String");
    }
    s.push(']');
    s
}

/// Reads one snarkjs verification-key JSON file and emits its VK constants, named with `suffix`
/// (e.g. `_N2`, `_N10`) so both circuits' constant sets coexist without colliding.
fn emit_vk_constants(out: &mut String, vk_path: &PathBuf, suffix: &str) {
    println!("cargo:rerun-if-changed={}", vk_path.display());

    let json = fs::read_to_string(vk_path)
        .unwrap_or_else(|e| panic!("failed to read VK file `{}`: {e}", vk_path.display()));
    let v: Value = serde_json::from_str(&json).expect("VK file is not valid JSON");

    let alpha = g1_bytes(&v["vk_alpha_1"]);
    let beta = g2_bytes(&v["vk_beta_2"]);
    let gamma = g2_bytes(&v["vk_gamma_2"]);
    let delta = g2_bytes(&v["vk_delta_2"]);

    let ic_arr = v["IC"].as_array().expect("IC must be a JSON array");
    let ic_len = ic_arr.len();
    let ic_items: Vec<String> = ic_arr.iter().map(|pt| fmt_bytes(&g1_bytes(pt))).collect();

    writeln!(
        out,
        "const VK_ALPHA_G1{suffix}: [u8; 64] = {};",
        fmt_bytes(&alpha)
    )
    .unwrap();
    writeln!(
        out,
        "const VK_BETA_G2{suffix}: [u8; 128] = {};",
        fmt_bytes(&beta)
    )
    .unwrap();
    writeln!(
        out,
        "const VK_GAMMA_G2{suffix}: [u8; 128] = {};",
        fmt_bytes(&gamma)
    )
    .unwrap();
    writeln!(
        out,
        "const VK_DELTA_G2{suffix}: [u8; 128] = {};",
        fmt_bytes(&delta)
    )
    .unwrap();
    writeln!(
        out,
        "const VK_IC{suffix}: [[u8; 64]; {ic_len}] = [{}];",
        ic_items.join(",")
    )
    .unwrap();
}

fn main() {
    let manifest_dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    let zkp_dir = manifest_dir
        .join("..")
        .join("..")
        .join("..")
        .join("domains")
        .join("zeto")
        .join("zkp");

    let mut out = String::new();
    writeln!(
        out,
        "// Auto-generated by build.rs from anon_nullifier_transfer{{,_batch}}/deposit/withdraw_nullifier -vkey.json - do not edit manually."
    )
    .unwrap();
    emit_vk_constants(
        &mut out,
        &zkp_dir.join("anon_nullifier_transfer-vkey.json"),
        "_N2",
    );
    emit_vk_constants(
        &mut out,
        &zkp_dir.join("anon_nullifier_transfer_batch-vkey.json"),
        "_N10",
    );
    emit_vk_constants(&mut out, &zkp_dir.join("deposit-vkey.json"), "_DEPOSIT");
    emit_vk_constants(
        &mut out,
        &zkp_dir.join("withdraw_nullifier-vkey.json"),
        "_WITHDRAW",
    );

    let out_dir = PathBuf::from(env::var("OUT_DIR").expect("OUT_DIR not set"));
    fs::write(out_dir.join("vk.rs"), out).expect("failed to write vk.rs");
}
