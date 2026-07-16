//! Shared fixture data for Sente's cross-process tests (`tests/two_node_invoke.rs`,
//! `tests/divergence.rs`) and the `sente_step` harness binary they drive - kept in one place so
//! both sides agree on the exact same genesis group without duplicating its construction. A real
//! Paladin deployment would populate a group's genesis `SenteEntry` from indexing an actual
//! on-chain deploy (Go-side work, out of scope for this phase - see `domain.rs`'s own module doc
//! comment); this fixture stands in for that here.

use base64::engine::general_purpose::STANDARD as BASE64;
use base64::Engine;
use soroban_env_host::xdr::{ContractId, Hash, Limits, ScAddress, WriteXdr};

use crate::domain;

/// The state id this harness's fake `find_available_states` assigns the genesis fixture -
/// arbitrary, just needs to be stable across the `assemble`/`endorse` process pair.
pub const GENESIS_STATE_ID: &str = "group-genesis-0";

const CONTRACT_SEED: [u8; 32] = [0x11; 32];
const WASM_HASH: [u8; 32] = [0x22; 32];
const NETWORK_PASSPHRASE: &[u8] = b"Test SDF Network ; September 2015";
/// Placeholder pubkeys - never read by `assemble_transaction`/`endorse_transaction`'s root-only
/// logic (only `decode_root` reads the instance value), so their exact values don't matter here.
const MEMBER_PUBKEYS: [[u8; 32]; 2] = [[1u8; 32], [2u8; 32]];
pub const MEMBERS: [&str; 2] = ["sender@node1", "endorser@node2"];

pub fn contract_address() -> String {
    stellar_strkey::Contract(CONTRACT_SEED).to_string()
}

/// `ConfigureDomainRequest.config_json` for this fixture's group - `senteFactoryAddress`/
/// `saladinFactoryAddress` are placeholders (never read by root-only `assemble_transaction`/
/// `endorse_transaction`, only by genesis's `init_deploy`/`prepare_deploy`, which this fixture
/// doesn't exercise); `networkPassphrase` must match [`genesis_entry`]'s own `NETWORK_PASSPHRASE`
/// exactly, since it's what the on-chain typed-data digest is computed over.
pub fn config_json() -> String {
    serde_json::json!({
        "senteFactoryAddress": contract_address(),
        "saladinFactoryAddress": contract_address(),
        "senteWasmHash": hex::encode(WASM_HASH),
        "networkPassphrase": String::from_utf8_lossy(NETWORK_PASSPHRASE),
    })
    .to_string()
}

/// The group's genesis `SenteEntry` (`root = [0; 32]`, fixed members/passphrase) - what a real
/// on-chain deploy's indexed result would look like, hand-built here via the same
/// `domain::genesis_instance_val` the plugin itself would use if it ever needed to construct one.
pub fn genesis_entry() -> sente_host::SenteEntry {
    let contract_id_base64 = BASE64.encode(
        ScAddress::Contract(ContractId(Hash(CONTRACT_SEED)))
            .to_xdr(Limits::none())
            .expect("contract address always encodes"),
    );
    let val = domain::genesis_instance_val(WASM_HASH, &MEMBER_PUBKEYS, NETWORK_PASSPHRASE)
        .expect("fixture genesis value must build");
    sente_host::SenteEntry {
        contract_id: contract_id_base64,
        key_xdr: domain::instance_key_xdr_base64(),
        val_xdr: BASE64.encode(
            val.to_xdr(Limits::none())
                .expect("instance value always encodes"),
        ),
        durability: sente_host::EntryDurability::Persistent,
        seq: 0,
    }
}
