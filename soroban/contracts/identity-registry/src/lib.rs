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

//! identity-registry (chapter 13 §13.5) - a faithful port of `solidity/contracts/registry/
//! IdentityRegistry.sol`'s semantics onto Soroban, feeding the `registries/stellar` plugin.
//!
//! The book's own spec for this contract is a single data-model bullet
//! (`identity_hash -> {owner: Address, properties: Map<Symbol, Bytes>}`) with no method
//! signatures, `DataKey` enum, or events given - unlike SNoto/SaladinFactory, which the book
//! specifies in much more depth. The design below is cross-checked against the real EVM contract
//! rather than invented from scratch:
//! - **Identity hash derivation**: `sha256(parent || name)`, mirroring EVM's
//!   `sha256(abi.encodePacked(parentIdentityHash, name))` exactly - an identity is a
//!   `(parent, name)` pair, not an arbitrary caller-chosen value.
//! - **Root identity**: a fixed `[0u8; 32]` sentinel key, mirroring EVM's `bytes32(0)` root,
//!   created once via `initialize`.
//! - **Authorization**: registering a new child requires the *parent* identity's owner to
//!   `require_auth()` (EVM: `identities[parentIdentityHash].owner == msg.sender`), except when
//!   the contract is in "rootless" mode and the parent is root itself, in which case anyone may
//!   register (EVM achieves this via a sentinel owner address `type(uint160).max`; Soroban has no
//!   safe analogous "any address" sentinel to construct, so this implementation uses a plain
//!   instance-storage bool flag checked specifically against the root id instead - see
//!   `storage::DataKey::Rootless`). Setting a property on an *existing* identity requires that
//!   identity's *own* owner to `require_auth()` (EVM: `identities[identityHash].owner ==
//!   msg.sender`) - a different, narrower authorization than registration.
//! - **No on-chain children enumeration**: EVM's `Identity.children: bytes32[]` (and its
//!   `propertyNames` enumeration array) are omitted here. Appending to a shared per-parent
//!   `children` vec on every registration would make each parent a contended shared write target
//!   across every one of its children's registrations - exactly the kind of unnecessary
//!   serialization `SaladinFactory`'s own "no persistent storage" design (chapter 13 Phase 2)
//!   argues against. Off-chain indexers (the `registries/stellar` plugin itself, via the
//!   `identity_registered` event stream) are the intended way to enumerate children/properties,
//!   not on-chain storage.
//! - **Properties stored as an embedded `Map`** on the identity's own storage entry, matching the
//!   book's literal `properties: Map<Symbol, Bytes>` data model - a different storage-locality
//!   tradeoff than EVM's per-property separate mapping slots (each `set_property` call here
//!   rewrites the whole identity entry), acceptable since identities aren't expected to carry
//!   large property counts.
#![no_std]

mod storage;

use soroban_sdk::{
    contract, contractevent, contractimpl, contracttype, Address, Bytes, BytesN, Env, Map, Symbol,
};

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Identity {
    pub parent: BytesN<32>,
    pub name: Bytes,
    pub owner: Address,
    pub properties: Map<Symbol, Bytes>,
}

#[contractevent(topics = ["identity_registered"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct IdentityRegistered {
    #[topic]
    pub identity: BytesN<32>,
    pub parent: BytesN<32>,
    pub name: Bytes,
    pub owner: Address,
}

#[contractevent(topics = ["property_set"], data_format = "vec")]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct PropertySet {
    #[topic]
    pub identity: BytesN<32>,
    pub name: Symbol,
    pub value: Bytes,
}

#[contract]
pub struct Contract;

#[contractimpl]
impl Contract {
    /// Creates the root identity. `rootless` mirrors EVM's `constructor(bool rootless)`: if
    /// true, anyone may register a direct child of root (no `require_auth` check against
    /// `root_owner`); registering under any *other* identity always requires that identity's
    /// owner regardless of this flag. `root_owner` is still recorded as root's owner either way
    /// (matching EVM, which always sets *some* owner on root, sentinel or not).
    pub fn initialize(env: Env, rootless: bool, root_owner: Address) {
        let root = root_id(&env);
        if storage::has_identity(&env, &root) {
            panic!("already initialized");
        }
        storage::set_rootless(&env, rootless);
        storage::set_identity(
            &env,
            &root,
            &Identity {
                parent: root.clone(),
                name: Bytes::new(&env),
                owner: root_owner.clone(),
                properties: Map::new(&env),
            },
        );

        IdentityRegistered {
            identity: root.clone(),
            parent: root,
            name: Bytes::new(&env),
            owner: root_owner,
        }
        .publish(&env);
    }

    /// Returns the newly-registered identity's hash (`sha256(parent || name)`) - deterministic
    /// and independently computable by any caller ahead of time, exactly as in EVM; returning it
    /// here is a convenience, not a departure from that determinism.
    pub fn register_identity(
        env: Env,
        parent: BytesN<32>,
        name: Bytes,
        owner: Address,
    ) -> BytesN<32> {
        let parent_identity =
            storage::get_identity(&env, &parent).unwrap_or_else(|| panic!("parent not found"));

        let root = root_id(&env);
        if !(storage::is_rootless(&env) && parent == root) {
            parent_identity.owner.require_auth();
        }

        let identity_hash = compute_identity_hash(&env, &parent, &name);
        if storage::has_identity(&env, &identity_hash) {
            panic!("identity already registered");
        }

        storage::set_identity(
            &env,
            &identity_hash,
            &Identity {
                parent: parent.clone(),
                name: name.clone(),
                owner: owner.clone(),
                properties: Map::new(&env),
            },
        );

        IdentityRegistered {
            identity: identity_hash.clone(),
            parent,
            name,
            owner,
        }
        .publish(&env);

        identity_hash
    }

    pub fn set_property(env: Env, identity_hash: BytesN<32>, name: Symbol, value: Bytes) {
        let mut identity = storage::get_identity(&env, &identity_hash)
            .unwrap_or_else(|| panic!("identity not found"));
        identity.owner.require_auth();

        identity.properties.set(name.clone(), value.clone());
        storage::set_identity(&env, &identity_hash, &identity);

        PropertySet {
            identity: identity_hash,
            name,
            value,
        }
        .publish(&env);
    }

    pub fn get_identity(env: Env, identity_hash: BytesN<32>) -> Identity {
        storage::get_identity(&env, &identity_hash).unwrap_or_else(|| panic!("identity not found"))
    }

    pub fn get_root(env: Env) -> Identity {
        let root = root_id(&env);
        storage::get_identity(&env, &root).unwrap_or_else(|| panic!("not initialized"))
    }

    pub fn compute_hash(env: Env, parent: BytesN<32>, name: Bytes) -> BytesN<32> {
        compute_identity_hash(&env, &parent, &name)
    }
}

fn root_id(env: &Env) -> BytesN<32> {
    BytesN::from_array(env, &[0u8; 32])
}

fn compute_identity_hash(env: &Env, parent: &BytesN<32>, name: &Bytes) -> BytesN<32> {
    let mut buf = Bytes::from_array(env, &parent.to_array());
    buf.append(name);
    env.crypto().sha256(&buf).to_bytes()
}

#[cfg(test)]
mod test;
