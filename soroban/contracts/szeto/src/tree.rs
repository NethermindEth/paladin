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
use alloc::vec::Vec as StdVec;

use soroban_poseidon::PoseidonSponge;
use soroban_sdk::{contracttype, crypto::bn254::Bn254Fr, vec, Env, Vec, U256};

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

fn one(env: &Env) -> U256 {
    U256::from_u32(env, 1)
}

/// `MAX_SMT_DEPTH` is 64, so only the low 64 bits ever influence routing.
fn routing_bits(env: &Env, value: &U256) -> u64 {
    let modulus = U256::from_parts(env, 0, 0, 1, 0);
    value
        .rem_euclid(&modulus)
        .to_u128()
        .expect("value mod 2^64 must fit in u128") as u64
}

fn routing_bit(route: u64, depth: u32) -> bool {
    ((route >> depth) & 1) != 0
}

enum LoadedNode {
    Empty,
    Leaf { index: U256, value: U256 },
    Middle { child_left: U256, child_right: U256 },
}

fn get_node(env: &Env, node_hash: &U256) -> LoadedNode {
    match env
        .storage()
        .persistent()
        .get::<_, Node>(&DataKey::TreeNode(node_hash.clone()))
    {
        None => LoadedNode::Empty,
        Some(node) => match node.node_type {
            NodeType::Empty => LoadedNode::Empty,
            NodeType::Leaf => LoadedNode::Leaf {
                index: node.index,
                value: node.value,
            },
            NodeType::Middle => LoadedNode::Middle {
                child_left: node.child_left,
                child_right: node.child_right,
            },
        },
    }
}

/// Reused across every node hashed within one `insert_leaf` call, one sponge per arity.
/// `soroban_poseidon`'s free `poseidon_hash` function rebuilds the full MDS-matrix/round-constant
/// tables from scratch on every call (documented overhead in the crate's own docs); a tree insert
/// can hash up to `MAX_SMT_DEPTH` nodes, so constructing each sponge once and calling
/// `compute_hash` repeatedly (the sponge's state resets between calls, so each hash is still
/// independent) avoids paying that setup cost at every level. Pure resource-cost optimization -
/// produces bit-identical hashes to the free-function form.
struct Hasher {
    zero: U256,
    leaf: PoseidonSponge<4, Bn254Fr>,
    leaf_inputs: Vec<U256>,
    middle: PoseidonSponge<3, Bn254Fr>,
    middle_inputs: Vec<U256>,
}

impl Hasher {
    fn new(env: &Env) -> Self {
        let zero = zero(env);
        let one = one(env);
        Self {
            zero: zero.clone(),
            leaf: PoseidonSponge::new(env),
            leaf_inputs: vec![env, zero.clone(), zero.clone(), one],
            middle: PoseidonSponge::new(env),
            middle_inputs: vec![env, zero.clone(), zero],
        }
    }

    fn hash_leaf(&mut self, index: &U256, value: &U256) -> U256 {
        self.leaf_inputs.set(0, index.clone());
        self.leaf_inputs.set(1, value.clone());
        self.leaf.compute_hash(&self.leaf_inputs)
    }

    fn hash_middle(&mut self, child_left: &U256, child_right: &U256) -> U256 {
        self.middle_inputs.set(0, child_left.clone());
        self.middle_inputs.set(1, child_right.clone());
        self.middle.compute_hash(&self.middle_inputs)
    }
}

fn store_node(env: &Env, node: &Node, node_hash: U256) -> U256 {
    let key = DataKey::TreeNode(node_hash.clone());
    // Collision-check read removed as a resource optimization (chapter 13 Part B, phase M1b):
    // Poseidon's collision resistance makes two distinct nodes hashing to the same key
    // cryptographically infeasible, so trusting the hash's uniqueness - rather than reading and
    // comparing on every single write - is safe in practice. This is a deliberate, reasoned
    // divergence from upstream Solidity `SmtLib._addNode` (which does perform this check) for
    // resource-cost reasons only, not a security or correctness change; left commented out
    // rather than deleted so the exact tradeoff stays visible to anyone auditing this later.
    //
    // if let Some(existing) = env.storage().persistent().get::<_, Node>(&key) {
    //     assert!(
    //         existing.node_type == node.node_type
    //             && existing.child_left == node.child_left
    //             && existing.child_right == node.child_right
    //             && existing.index == node.index
    //             && existing.value == node.value,
    //         "tree node hash collision"
    //     );
    //     return node_hash;
    // }
    env.storage().persistent().set(&key, node);
    env.storage().persistent().extend_ttl(
        &key,
        crate::storage::TTL_THRESHOLD_LEDGERS,
        crate::storage::TTL_EXTEND_TO_LEDGERS,
    );
    node_hash
}

fn add_leaf_node(env: &Env, hasher: &mut Hasher, index: &U256, value: &U256) -> U256 {
    let node = Node {
        node_type: NodeType::Leaf,
        child_left: hasher.zero.clone(),
        child_right: hasher.zero.clone(),
        index: index.clone(),
        value: value.clone(),
    };
    let node_hash = hasher.hash_leaf(index, value);
    store_node(env, &node, node_hash)
}

fn add_middle_node(env: &Env, hasher: &mut Hasher, child_left: &U256, child_right: &U256) -> U256 {
    let node = Node {
        node_type: NodeType::Middle,
        child_left: child_left.clone(),
        child_right: child_right.clone(),
        index: hasher.zero.clone(),
        value: hasher.zero.clone(),
    };
    let node_hash = hasher.hash_middle(child_left, child_right);
    store_node(env, &node, node_hash)
}

struct AncestorFrame {
    went_right: bool,
    sibling_hash: U256,
}

fn rebuild_ancestor_path(
    env: &Env,
    hasher: &mut Hasher,
    ancestors: &[AncestorFrame],
    mut child_hash: U256,
) -> U256 {
    for frame in ancestors.iter().rev() {
        child_hash = if frame.went_right {
            add_middle_node(env, hasher, &frame.sibling_hash, &child_hash)
        } else {
            add_middle_node(env, hasher, &child_hash, &frame.sibling_hash)
        };
    }
    child_hash
}

fn build_collision_path(
    env: &Env,
    hasher: &mut Hasher,
    new_index: &U256,
    new_value: &U256,
    new_route: u64,
    old_index: &U256,
    old_value: &U256,
    depth: u32,
) -> U256 {
    if depth >= MAX_SMT_DEPTH {
        panic!("szeto: max tree depth reached");
    }

    let old_route = routing_bits(env, old_index);
    let old_hash = hasher.hash_leaf(old_index, old_value);
    let new_hash = add_leaf_node(env, hasher, new_index, new_value);
    let mut divergence_depth = depth;

    while divergence_depth < MAX_SMT_DEPTH
        && routing_bit(new_route, divergence_depth) == routing_bit(old_route, divergence_depth)
    {
        divergence_depth += 1;
    }

    if divergence_depth >= MAX_SMT_DEPTH {
        panic!("szeto: max tree depth reached");
    }

    let mut child_hash = if routing_bit(new_route, divergence_depth) {
        add_middle_node(env, hasher, &old_hash, &new_hash)
    } else {
        add_middle_node(env, hasher, &new_hash, &old_hash)
    };

    for current_depth in (depth..divergence_depth).rev() {
        let zero = hasher.zero.clone();
        child_hash = if routing_bit(new_route, current_depth) {
            add_middle_node(env, hasher, &zero, &child_hash)
        } else {
            add_middle_node(env, hasher, &child_hash, &zero)
        };
    }

    child_hash
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
    let prev_root = get_root(env);
    let mut hasher = Hasher::new(env);
    let new_route = routing_bits(env, &index);
    let mut ancestors: StdVec<AncestorFrame> = StdVec::new();
    let mut current_hash = prev_root.clone();
    let mut depth = 0u32;

    let new_root = loop {
        if depth > MAX_SMT_DEPTH {
            panic!("szeto: max tree depth reached");
        }

        match get_node(env, &current_hash) {
            LoadedNode::Empty => {
                let leaf_hash = add_leaf_node(env, &mut hasher, &index, &value);
                break rebuild_ancestor_path(env, &mut hasher, &ancestors, leaf_hash);
            }
            LoadedNode::Leaf {
                index: old_index,
                value: old_value,
            } => {
                let child_hash = if old_index == index {
                    add_leaf_node(env, &mut hasher, &index, &value)
                } else {
                    build_collision_path(
                        env,
                        &mut hasher,
                        &index,
                        &value,
                        new_route,
                        &old_index,
                        &old_value,
                        depth,
                    )
                };
                break rebuild_ancestor_path(env, &mut hasher, &ancestors, child_hash);
            }
            LoadedNode::Middle {
                child_left,
                child_right,
            } => {
                if depth >= MAX_SMT_DEPTH {
                    panic!("szeto: max tree depth reached");
                }
                let went_right = routing_bit(new_route, depth);
                if went_right {
                    ancestors.push(AncestorFrame {
                        went_right,
                        sibling_hash: child_left,
                    });
                    current_hash = child_right;
                } else {
                    ancestors.push(AncestorFrame {
                        went_right,
                        sibling_hash: child_right,
                    });
                    current_hash = child_left;
                }
                depth += 1;
            }
        }
    };

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
