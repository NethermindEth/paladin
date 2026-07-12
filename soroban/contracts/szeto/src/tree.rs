// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

//! A faithful port of `@iden3/contracts`'s `SmtLib.sol` - a lazy, general sparse Merkle tree
//! with real node storage (`Empty`/`Leaf`/`Middle`), not a fixed-depth "always walk N levels"
//! incremental tree. Insert cost is proportional to actual occupancy/collision depth, not a
//! constant - cheap early in a domain instance's life, growing toward the worst case
//! (`MAX_SMT_DEPTH` Poseidon calls) only as the tree fills. Confirmed against the real Solidity
//! source (`solidity/node_modules/@iden3/contracts/contracts/lib/SmtLib.sol`) rather than
//! assumed - this repo does not vendor or modify that source, only replicates its algorithm.
//!
//! Only the subset EVM Zeto's own contracts actually call from their `transfer` path is ported:
//! `addLeaf`/`getRoot`/`rootExists`. Off-chain proof generation (`getProof`/`getProofByRoot`) is
//! a client-side concern (Zeto's Go SDK already maintains its own copy of this tree) and is not
//! needed on-chain.
use soroban_poseidon::poseidon_hash;
use soroban_sdk::{contracttype, crypto::bn254::Bn254Fr, vec, Env, U256};

use crate::storage::DataKey;

/// Matches EVM Zeto's `MAX_SMT_DEPTH` (`solidity/node_modules/@lfdecentralizedtrust/
/// zeto-contracts/contracts/lib/interfaces/izeto.sol:19`) exactly - the tree's actual behavior
/// (which roots are valid) depends on this constant, so it must match for cross-chain proof
/// compatibility.
pub const MAX_SMT_DEPTH: u32 = 64;

#[contracttype]
#[derive(Clone, PartialEq, Eq)]
enum NodeType {
    Empty,
    Leaf,
    Middle,
}

#[contracttype]
#[derive(Clone)]
struct Node {
    node_type: NodeType,
    child_left: U256,
    child_right: U256,
    index: U256,
    value: U256,
}

fn zero(env: &Env) -> U256 {
    U256::from_u32(env, 0)
}

fn empty_node(env: &Env) -> Node {
    Node {
        node_type: NodeType::Empty,
        child_left: zero(env),
        child_right: zero(env),
        index: zero(env),
        value: zero(env),
    }
}

fn get_bit(env: &Env, value: &U256, bit: u32) -> bool {
    let shifted = value.shr(bit);
    shifted.rem_euclid(&U256::from_u32(env, 2)) == U256::from_u32(env, 1)
}

fn get_node(env: &Env, node_hash: &U256) -> Node {
    env.storage()
        .persistent()
        .get(&DataKey::TreeNode(node_hash.clone()))
        .unwrap_or_else(|| empty_node(env))
}

/// Hash of a node - `Empty` hashes to `0` (matches Solidity's zero-initialized-mapping default,
/// not a special case in the algorithm itself), `Leaf` is `poseidon(index, value, 1)` (t=4,
/// matches `getLeafNodeHash`/`PoseidonUnit3L` exactly), `Middle` is `poseidon(left, right)` (t=3,
/// matches `PoseidonUnit2L`).
fn node_hash(env: &Env, node: &Node) -> U256 {
    match node.node_type {
        NodeType::Empty => zero(env),
        NodeType::Leaf => {
            let inputs = vec![
                env,
                node.index.clone(),
                node.value.clone(),
                U256::from_u32(env, 1),
            ];
            poseidon_hash::<4, Bn254Fr>(env, &inputs)
        }
        NodeType::Middle => {
            let inputs = vec![env, node.child_left.clone(), node.child_right.clone()];
            poseidon_hash::<3, Bn254Fr>(env, &inputs)
        }
    }
}

/// Stores `node` keyed by its own hash, extending TTL. Mirrors Solidity's `_addNode`'s collision
/// safety check: if a node already exists at this hash, its content must match exactly (a
/// defensive assertion against a hash collision, not an expected code path).
fn add_node(env: &Env, node: &Node) -> U256 {
    let h = node_hash(env, node);
    let key = DataKey::TreeNode(h.clone());
    if let Some(existing) = env.storage().persistent().get::<_, Node>(&key) {
        assert!(
            existing.node_type == node.node_type
                && existing.child_left == node.child_left
                && existing.child_right == node.child_right
                && existing.index == node.index
                && existing.value == node.value,
            "tree node hash collision"
        );
        return h;
    }
    env.storage().persistent().set(&key, node);
    env.storage().persistent().extend_ttl(
        &key,
        crate::storage::TTL_THRESHOLD_LEDGERS,
        crate::storage::TTL_EXTEND_TO_LEDGERS,
    );
    h
}

/// Recursively descends an existing leaf's position by one bit at a time until the new and old
/// leaves' index bits diverge, building the middle-node chain needed to distinguish them.
/// Mirrors Solidity's `_pushLeaf` exactly.
fn push_leaf(env: &Env, new_leaf: &Node, old_leaf: &Node, depth: u32) -> U256 {
    if depth >= MAX_SMT_DEPTH {
        panic!("szeto: max tree depth reached");
    }
    let new_bit = get_bit(env, &new_leaf.index, depth);
    let old_bit = get_bit(env, &old_leaf.index, depth);

    if new_bit == old_bit {
        let next_hash = push_leaf(env, new_leaf, old_leaf, depth + 1);
        let middle = if new_bit {
            Node {
                node_type: NodeType::Middle,
                child_left: zero(env),
                child_right: next_hash,
                index: zero(env),
                value: zero(env),
            }
        } else {
            Node {
                node_type: NodeType::Middle,
                child_left: next_hash,
                child_right: zero(env),
                index: zero(env),
                value: zero(env),
            }
        };
        return add_node(env, &middle);
    }

    let old_hash = node_hash(env, old_leaf);
    let new_hash = node_hash(env, new_leaf);
    let middle = if new_bit {
        Node {
            node_type: NodeType::Middle,
            child_left: old_hash,
            child_right: new_hash,
            index: zero(env),
            value: zero(env),
        }
    } else {
        Node {
            node_type: NodeType::Middle,
            child_left: new_hash,
            child_right: old_hash,
            index: zero(env),
            value: zero(env),
        }
    };
    add_node(env, new_leaf);
    add_node(env, &middle)
}

/// Mirrors Solidity's `_addLeaf` exactly: descends from `node_hash` (the current subtree root)
/// inserting `new_leaf`, returning the new subtree root hash.
fn add_leaf(env: &Env, new_leaf: &Node, node_hash_at: &U256, depth: u32) -> U256 {
    if depth > MAX_SMT_DEPTH {
        panic!("szeto: max tree depth reached");
    }
    let node = get_node(env, node_hash_at);
    match node.node_type {
        NodeType::Empty => add_node(env, new_leaf),
        NodeType::Leaf => {
            if node.index == new_leaf.index {
                add_node(env, new_leaf)
            } else {
                push_leaf(env, new_leaf, &node, depth)
            }
        }
        NodeType::Middle => {
            let bit = get_bit(env, &new_leaf.index, depth);
            let middle = if bit {
                let next = add_leaf(env, new_leaf, &node.child_right, depth + 1);
                Node {
                    node_type: NodeType::Middle,
                    child_left: node.child_left,
                    child_right: next,
                    index: zero(env),
                    value: zero(env),
                }
            } else {
                let next = add_leaf(env, new_leaf, &node.child_left, depth + 1);
                Node {
                    node_type: NodeType::Middle,
                    child_left: next,
                    child_right: node.child_right,
                    index: zero(env),
                    value: zero(env),
                }
            };
            add_node(env, &middle)
        }
    }
}

/// The current tree root - `0` before any leaf has ever been inserted, matching EVM's
/// `initialize()` seeding root history with `0`.
pub fn get_root(env: &Env) -> U256 {
    env.storage()
        .instance()
        .get(&DataKey::TreeRoot)
        .unwrap_or_else(|| zero(env))
}

/// Whether `root` has ever been a valid tree root - an **append-only** check (matches
/// `SmtLib.rootExists`): any historical root remains valid for membership proofs forever, not
/// just the latest one, since insertion never removes or rewrites existing leaves.
pub fn root_exists(env: &Env, root: &U256) -> bool {
    if *root == zero(env) {
        return true;
    }
    env.storage()
        .persistent()
        .has(&DataKey::TreeRootExists(root.clone()))
}

/// Inserts one leaf (`index`, `value` - Zeto always calls this with `index == value == commitment
/// hash`, matching `processOutputs`'s `_commitmentsTree.addLeaf(outputs[i], outputs[i])`),
/// updates the current root, and records the new root as forever-valid. Returns the new root.
pub fn insert_leaf(env: &Env, index: U256, value: U256) -> U256 {
    let new_leaf = Node {
        node_type: NodeType::Leaf,
        child_left: zero(env),
        child_right: zero(env),
        index,
        value,
    };
    let prev_root = get_root(env);
    let new_root = add_leaf(env, &new_leaf, &prev_root, 0);

    env.storage().instance().set(&DataKey::TreeRoot, &new_root);
    let exists_key = DataKey::TreeRootExists(new_root.clone());
    env.storage().persistent().set(&exists_key, &());
    env.storage().persistent().extend_ttl(
        &exists_key,
        crate::storage::TTL_THRESHOLD_LEDGERS,
        crate::storage::TTL_EXTEND_TO_LEDGERS,
    );

    new_root
}
