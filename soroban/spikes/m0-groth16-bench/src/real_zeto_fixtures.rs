//! Shared fixture-loading/decimal-to-bytes conversion helpers for the M0 spike's real-proof tests
//! (`real_zeto_proof.rs`, `bench_test.rs`). Independently re-implements the same math as
//! `sdk/go/pkg/groth16bn254` (not shared code, by design - this Rust side and the Go production
//! translation layer are separate implementations of the same well-specified conversion).
extern crate alloc;
extern crate std;

use num_bigint::BigUint;
use serde::Deserialize;
use std::{fs, path::PathBuf};

#[derive(Deserialize)]
pub struct ProofData {
    pub pi_a: alloc::vec::Vec<std::string::String>,
    pub pi_b: alloc::vec::Vec<alloc::vec::Vec<std::string::String>>,
    pub pi_c: alloc::vec::Vec<std::string::String>,
}

#[derive(Deserialize)]
pub struct SnarkjsProof {
    pub proof: ProofData,
    pub pub_signals: alloc::vec::Vec<std::string::String>,
}

#[derive(Deserialize)]
pub struct SnarkjsVk {
    pub vk_alpha_1: alloc::vec::Vec<std::string::String>,
    pub vk_beta_2: alloc::vec::Vec<alloc::vec::Vec<std::string::String>>,
    pub vk_gamma_2: alloc::vec::Vec<alloc::vec::Vec<std::string::String>>,
    pub vk_delta_2: alloc::vec::Vec<alloc::vec::Vec<std::string::String>>,
    #[serde(rename = "IC")]
    pub ic: alloc::vec::Vec<alloc::vec::Vec<std::string::String>>,
}

/// Decimal field-element string -> 32-byte big-endian.
pub fn fe_bytes(decimal: &str) -> [u8; 32] {
    let n = BigUint::parse_bytes(decimal.as_bytes(), 10).expect("invalid decimal field element");
    let be = n.to_bytes_be();
    assert!(be.len() <= 32, "field element too large");
    let mut out = [0u8; 32];
    out[32 - be.len()..].copy_from_slice(&be);
    out
}

pub fn g1_bytes(x: &str, y: &str) -> [u8; 64] {
    let mut out = [0u8; 64];
    out[..32].copy_from_slice(&fe_bytes(x));
    out[32..].copy_from_slice(&fe_bytes(y));
    out
}

/// Soroban orders Fp2 as c1||c0 (imaginary||real) - the reverse of snarkjs's [c0, c1].
pub fn g2_bytes(x_c0: &str, x_c1: &str, y_c0: &str, y_c1: &str) -> [u8; 128] {
    let mut out = [0u8; 128];
    out[0..32].copy_from_slice(&fe_bytes(x_c1));
    out[32..64].copy_from_slice(&fe_bytes(x_c0));
    out[64..96].copy_from_slice(&fe_bytes(y_c1));
    out[96..128].copy_from_slice(&fe_bytes(y_c0));
    out
}

fn fixture_path(name: &str) -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("fixtures")
        .join(name)
}

/// Loads the real Zeto `anon` circuit proof + verification key fixtures (see `bench.rs`/
/// `real_zeto_proof.rs` doc comments for provenance).
pub fn load_fixtures() -> (SnarkjsProof, SnarkjsVk) {
    let proof_json =
        fs::read_to_string(fixture_path("real_zeto_proof.json")).expect("read proof fixture");
    let proof: SnarkjsProof = serde_json::from_str(&proof_json).expect("parse proof fixture");

    let vk_json = fs::read_to_string(fixture_path("anon-vkey.json")).expect("read vkey fixture");
    let vk: SnarkjsVk = serde_json::from_str(&vk_json).expect("parse vkey fixture");

    (proof, vk)
}
