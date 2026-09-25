//! R3 Poseidon cross-check (M0 spike, chapter 13 Phase 5).
//!
//! Zeto's real, unmodified Solidity contracts (`solidity/node_modules/@lfdecentralizedtrust/
//! zeto-contracts/contracts/lib/zeto_nullifier.sol:293-296` and `@iden3/contracts/contracts/lib/
//! hash/PoseidonHasher.sol`) maintain an on-chain sparse Merkle tree via `@iden3/contracts`'
//! `SmtLib`, calling Poseidon on-chain at two arities: `hash2([left, right])` for branch nodes
//! (t=3) and `hash3([index, value, 1])` for leaf nodes (t=4). A faithful, EVM-parity port of
//! this on-chain tree maintenance needs Soroban-side Poseidon at both arities, bit-identical to
//! `go-iden3-crypto`'s output (the same library Zeto's own Go prover/witness code uses -
//! `domains/zeto/internal/zeto/signer/common/nullifier.go`, vendored `smt` module).
//!
//! This test cross-checks the official `soroban-poseidon` crate (`github.com/stellar/
//! rs-soroban-poseidon`, which explicitly claims circomlib compatibility) against real
//! `go-iden3-crypto` output computed independently in Go (see each test's comment) - not trusting
//! the crate's own README claim or its own test suite.
extern crate std;

use soroban_poseidon::poseidon_hash;
use soroban_sdk::{crypto::bn254::Bn254Fr, vec, Env, U256};

fn u256_hex(_env: &Env, v: &U256) -> std::string::String {
    let bytes = v.to_be_bytes();
    let mut buf = [0u8; 32];
    for (i, b) in bytes.iter().enumerate() {
        buf[i] = b;
    }
    std::format!("0x{}", hex::encode(buf))
}

#[test]
fn poseidon_t3_matches_go_iden3_crypto() {
    // Independently computed via: poseidon.Hash([]*big.Int{big.NewInt(1), big.NewInt(2)})
    // from github.com/iden3/go-iden3-crypto@v0.0.17 (run directly, not assumed) ->
    // 0x115cc0f5e7d690413df64c6b9662e9cf2a3617f2743245519e19607a4417189a
    let env = Env::default();
    let inputs = vec![&env, U256::from_u32(&env, 1), U256::from_u32(&env, 2)];
    let hash = poseidon_hash::<3, Bn254Fr>(&env, &inputs);
    let hex = u256_hex(&env, &hash);
    assert_eq!(
        hex,
        "0x115cc0f5e7d690413df64c6b9662e9cf2a3617f2743245519e19607a4417189a"
    );
}

#[test]
fn poseidon_t4_leaf_hash_matches_go_iden3_crypto() {
    // Independently computed via:
    // poseidon.Hash([]*big.Int{big.NewInt(7), big.NewInt(9), big.NewInt(1)})
    // -> 0x1807289cc1a8e6745bf8b85402c6e44f26484581da2a38b9b0db0a0b98996f91
    // matching zeto_nullifier.sol's getLeafNodeHash(index, value) = poseidon([index, value, 1])
    let env = Env::default();
    let inputs = vec![
        &env,
        U256::from_u32(&env, 7),
        U256::from_u32(&env, 9),
        U256::from_u32(&env, 1),
    ];
    let hash = poseidon_hash::<4, Bn254Fr>(&env, &inputs);
    let hex = u256_hex(&env, &hash);
    assert_eq!(
        hex,
        "0x1807289cc1a8e6745bf8b85402c6e44f26484581da2a38b9b0db0a0b98996f91"
    );
}

#[test]
fn poseidon_single_call_cost() {
    let env = Env::default();
    let inputs = vec![&env, U256::from_u32(&env, 1), U256::from_u32(&env, 2)];
    let _ = poseidon_hash::<3, Bn254Fr>(&env, &inputs);
    let resources = env.cost_estimate().resources();
    std::println!("single poseidon_hash::<3> call: {} CPU instructions", resources.instructions);
}
