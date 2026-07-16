//! Builds the `Rc<dyn SnapshotSource>` `recording_invoke` needs, from Paladin's own view of prior
//! state (a set of `SenteEntry`s queried via `PaladinClient::find_available_states`/
//! `get_states_by_id`) - shared by both `AssembleTransaction` (building the initial snapshot to
//! discover the footprint) and `EndorseTransaction` (rebuilding the exact same snapshot from the
//! inputs/reads the assembler claims it used, to independently re-execute).

use std::rc::Rc;

use anyhow::Result;
use soroban_env_host::storage::SnapshotSource;
use soroban_env_host::xdr::LedgerEntry;
use soroban_env_host::LedgerInfo;
use soroban_simulation::testutils::MockSnapshotSource;

use crate::entry::{EntryDurability, SenteEntry};

/// Live-until is not stored on `SenteEntry` (see its own doc comment) - every endorser already
/// pins identical `LedgerInfo` (via `InfoState.ledger_info`), so the protocol-minimum floor for
/// this entry's durability is recomputable deterministically instead of needing to be carried.
/// This is a floor-only model: an entry actually extended further than the floor by some earlier,
/// unrelated transaction is treated as if only the floor applies. Real rent/TTL-extension
/// tracking is future work, not modeled here. Exposed (not private) so callers building a snapshot
/// out of both `SenteEntry`s and other raw `LedgerEntry` fixtures (e.g. `sente`'s own S2 bootstrap
/// contract, see `build_snapshot_source_from_parts`) can compute one consistent live-until.
pub fn protocol_floor_live_until(durability: EntryDurability, ledger_info: &LedgerInfo) -> u32 {
    let min_ttl = match durability {
        EntryDurability::Temporary => ledger_info.min_temp_entry_ttl,
        EntryDurability::Persistent => ledger_info.min_persistent_entry_ttl,
    };
    ledger_info.sequence_number + min_ttl - 1
}

/// Builds a `SnapshotSource` from a flat list of `SenteEntry`s (the privacy group's prior state,
/// as queried from Paladin) and the pinned `LedgerInfo` every endorser re-executes against.
pub fn build_snapshot_source(
    entries: &[SenteEntry],
    ledger_info: &LedgerInfo,
) -> Result<Rc<dyn SnapshotSource>> {
    let with_ttl = entries
        .iter()
        .map(|e| {
            Ok((
                e.to_ledger_entry()?,
                Some(protocol_floor_live_until(e.durability, ledger_info)),
            ))
        })
        .collect::<Result<Vec<_>>>()?;
    build_snapshot_source_from_parts(with_ttl)
}

/// Lower-level builder taking already-assembled `(LedgerEntry, Option<u32>)` pairs directly -
/// lets callers mix `SenteEntry`-derived entries with other raw `LedgerEntry` fixtures that aren't
/// tracked as `SenteEntry` Paladin states at all (S2's fixed bootstrap contract instance/code,
/// which predates any real on-chain deploy - see `sente`'s own `SenteDomain`). Keeps
/// `soroban-simulation`'s `MockSnapshotSource`/`testutils` feature confined to this crate.
pub fn build_snapshot_source_from_parts(
    entries: Vec<(LedgerEntry, Option<u32>)>,
) -> Result<Rc<dyn SnapshotSource>> {
    Ok(Rc::new(
        MockSnapshotSource::from_entries(entries).map_err(|e| anyhow::anyhow!("{e}"))?,
    ))
}

#[cfg(test)]
mod tests {
    use super::*;
    use soroban_env_host::e2e_testutils::{default_ledger_info, CreateContractData};

    fn factory_wasm() -> Vec<u8> {
        std::fs::read(
            std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("../../../../soroban/artifacts/factory.wasm"),
        )
        .expect("failed to read factory.wasm - run `./gradlew :soroban:compile` first")
    }

    #[test]
    fn built_snapshot_finds_the_entry_it_was_given() {
        let ledger_info = default_ledger_info();
        let contract = CreateContractData::new([11; 32], &factory_wasm());
        let entry = SenteEntry::from_ledger_entry(&contract.contract_entry, 0).unwrap();

        let snapshot = build_snapshot_source(std::slice::from_ref(&entry), &ledger_info).unwrap();
        let key = Rc::new(entry.ledger_key().unwrap());
        let found = snapshot.get(&key).unwrap();
        assert!(found.is_some(), "entry must be found by its ledger key");
    }

    #[test]
    fn empty_snapshot_finds_nothing() {
        let ledger_info = default_ledger_info();
        let contract = CreateContractData::new([12; 32], &factory_wasm());
        let entry = SenteEntry::from_ledger_entry(&contract.contract_entry, 0).unwrap();

        let snapshot = build_snapshot_source(&[], &ledger_info).unwrap();
        let key = Rc::new(entry.ledger_key().unwrap());
        assert!(snapshot.get(&key).unwrap().is_none());
    }
}
