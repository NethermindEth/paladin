//! Sente's `soroban-env-host`/`soroban-simulation` embedding library. Phase 1's spike (chapter 14
//! §14.3, S1) proved deterministic recording-mode re-execution across genuinely separate OS
//! processes as a standalone binary (`main.rs`, now a thin CLI wrapper over this library, still
//! exercised by `tests/determinism.rs`). Phase 2 needs the same recording-mode
//! invoke and digest logic reusable from the real domain plugin (`crates/sente`) for both
//! `AssembleTransaction` (produce a footprint + result) and `EndorseTransaction` (re-execute
//! against a closed snapshot and compare digests) - hence this library crate.

pub mod entry;
pub mod invoke;
pub mod snapshot;

pub use entry::{EntryDurability, SenteEntry, SENTE_ENTRY_ABI_SCHEMA_JSON};
pub use invoke::{
    adjustment_config, digest, network_config, recording_invoke, seed_from_transaction_id,
};
pub use snapshot::{
    build_snapshot_source, build_snapshot_source_from_parts, protocol_floor_live_until,
};
