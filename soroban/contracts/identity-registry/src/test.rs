// Copyright © 2026 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0

extern crate std;

use super::*;
use soroban_sdk::testutils::Address as _;

struct Setup {
    env: Env,
    contract_id: Address,
    root_owner: Address,
}

fn setup(rootless: bool) -> Setup {
    let env = Env::default();
    env.mock_all_auths();
    let contract_id = env.register(Contract, ());
    let root_owner = Address::generate(&env);
    let client = ContractClient::new(&env, &contract_id);
    client.initialize(&rootless, &root_owner);
    Setup {
        env,
        contract_id,
        root_owner,
    }
}

#[test]
fn initialize_creates_root() {
    let s = setup(false);
    let client = ContractClient::new(&s.env, &s.contract_id);
    let root = client.get_root();
    assert_eq!(root.owner, s.root_owner);
    assert_eq!(root.parent, BytesN::from_array(&s.env, &[0u8; 32]));
}

#[test]
#[should_panic(expected = "already initialized")]
fn initialize_rejects_double_init() {
    let s = setup(false);
    let client = ContractClient::new(&s.env, &s.contract_id);
    client.initialize(&false, &s.root_owner);
}

#[test]
fn register_identity_under_owned_parent_requires_parent_owner_auth() {
    let s = setup(false);
    let client = ContractClient::new(&s.env, &s.contract_id);
    let root = BytesN::from_array(&s.env, &[0u8; 32]);
    let name = Bytes::from_slice(&s.env, b"alice");
    let owner = Address::generate(&s.env);

    let hash = client.register_identity(&root, &name, &owner);
    assert_eq!(hash, client.compute_hash(&root, &name));

    let identity = client.get_identity(&hash);
    assert_eq!(identity.parent, root);
    assert_eq!(identity.name, name);
    assert_eq!(identity.owner, owner);
}

#[test]
#[should_panic(expected = "identity already registered")]
fn register_identity_rejects_duplicate() {
    let s = setup(false);
    let client = ContractClient::new(&s.env, &s.contract_id);
    let root = BytesN::from_array(&s.env, &[0u8; 32]);
    let name = Bytes::from_slice(&s.env, b"alice");
    let owner = Address::generate(&s.env);

    client.register_identity(&root, &name, &owner);
    client.register_identity(&root, &name, &owner);
}

#[test]
#[should_panic(expected = "parent not found")]
fn register_identity_rejects_unknown_parent() {
    let s = setup(false);
    let client = ContractClient::new(&s.env, &s.contract_id);
    let bogus_parent = BytesN::from_array(&s.env, &[9u8; 32]);
    client.register_identity(
        &bogus_parent,
        &Bytes::from_slice(&s.env, b"orphan"),
        &Address::generate(&s.env),
    );
}

#[test]
fn rootless_mode_allows_anyone_to_register_under_root() {
    let env = Env::default();
    let contract_id = env.register(Contract, ());
    let root_owner = Address::generate(&env);
    let client = ContractClient::new(&env, &contract_id);

    env.mock_all_auths();
    client.initialize(&true, &root_owner);

    // No auths mocked for this call at all - rootless mode must not require root_owner's auth.
    env.set_auths(&[]);
    let root = BytesN::from_array(&env, &[0u8; 32]);
    let owner = Address::generate(&env);
    let hash = client.register_identity(&root, &Bytes::from_slice(&env, b"anyone"), &owner);
    assert_eq!(client.get_identity(&hash).owner, owner);
}

#[test]
#[should_panic]
fn non_rootless_mode_rejects_unauthorized_registration() {
    let s = setup(false);
    let client = ContractClient::new(&s.env, &s.contract_id);
    let root = BytesN::from_array(&s.env, &[0u8; 32]);

    // Clear mocked auths so root_owner's require_auth() has nothing to satisfy it.
    s.env.set_auths(&[]);
    client.register_identity(
        &root,
        &Bytes::from_slice(&s.env, b"alice"),
        &Address::generate(&s.env),
    );
}

#[test]
fn set_property_requires_identitys_own_owner() {
    let s = setup(false);
    let client = ContractClient::new(&s.env, &s.contract_id);
    let root = BytesN::from_array(&s.env, &[0u8; 32]);
    let owner = Address::generate(&s.env);
    let hash = client.register_identity(&root, &Bytes::from_slice(&s.env, b"alice"), &owner);

    let prop_name = Symbol::new(&s.env, "email");
    let prop_value = Bytes::from_slice(&s.env, b"alice@example.com");
    client.set_property(&hash, &prop_name, &prop_value);

    let identity = client.get_identity(&hash);
    assert_eq!(identity.properties.get(prop_name), Some(prop_value));
}

#[test]
#[should_panic(expected = "identity not found")]
fn set_property_rejects_unknown_identity() {
    let s = setup(false);
    let client = ContractClient::new(&s.env, &s.contract_id);
    client.set_property(
        &BytesN::from_array(&s.env, &[7u8; 32]),
        &Symbol::new(&s.env, "email"),
        &Bytes::from_slice(&s.env, b"x"),
    );
}

/// Registering a *grandchild* still requires the immediate parent's owner, not root's - rootless
/// mode only bypasses the check for root's direct children.
#[test]
#[should_panic]
fn rootless_mode_does_not_cascade_below_root() {
    let env = Env::default();
    let contract_id = env.register(Contract, ());
    let root_owner = Address::generate(&env);
    let client = ContractClient::new(&env, &contract_id);

    env.mock_all_auths();
    client.initialize(&true, &root_owner);
    let root = BytesN::from_array(&env, &[0u8; 32]);
    let child_owner = Address::generate(&env);
    let child = client.register_identity(&root, &Bytes::from_slice(&env, b"child"), &child_owner);

    env.set_auths(&[]);
    client.register_identity(
        &child,
        &Bytes::from_slice(&env, b"grandchild"),
        &Address::generate(&env),
    );
}
