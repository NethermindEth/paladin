//! Reusable recording-mode invocation logic, extracted from Phase 1's spike (originally
//! `main.rs`) so the actual domain plugin (`crates/sente`, Phase 2) calls the exact same code
//! Phase 1 already proved deterministic across processes - for both `AssembleTransaction`
//! (produce a footprint + result) and `EndorseTransaction` (re-execute against a closed snapshot
//! and compare digests), rather than each reimplementing it.

use std::rc::Rc;

use anyhow::{Context, Result};
use sha2::{Digest, Sha256};

use soroban_env_host::e2e_invoke::RecordingInvocationAuthMode;
use soroban_env_host::fees::{FeeConfiguration, RentFeeConfiguration};
use soroban_env_host::storage::SnapshotSource;
use soroban_env_host::xdr::{
    AccountId, ContractCostParamEntry, ContractCostParams, ContractCostType, ExtensionPoint,
    HostFunction, Limits, WriteXdr,
};
use soroban_env_host::LedgerInfo;
use soroban_simulation::simulation::{
    simulate_invoke_host_function_op, InvokeHostFunctionSimulationResult,
    SimulationAdjustmentConfig, SimulationAdjustmentFactor,
};
use soroban_simulation::NetworkConfig;

/// The determinism checklist's "seed the PRNG from the private transaction ID" item
/// (`saladin-book/part-2-saladin/14-domain-ports.md` §14.3) - a plain SHA-256 of the transaction
/// id string, so every endorser derives the identical `base_prng_seed` from the same
/// `TransactionSpecification.transaction_id` without needing it carried as a separate value.
pub fn seed_from_transaction_id(transaction_id: &str) -> [u8; 32] {
    Sha256::digest(transaction_id.as_bytes()).into()
}

pub fn adjustment_config() -> SimulationAdjustmentConfig {
    SimulationAdjustmentConfig {
        instructions: SimulationAdjustmentFactor::new(1.0, 0),
        read_bytes: SimulationAdjustmentFactor::new(1.0, 0),
        write_bytes: SimulationAdjustmentFactor::new(1.0, 0),
        tx_size: SimulationAdjustmentFactor::new(1.0, 0),
        refundable_fee: SimulationAdjustmentFactor::new(1.0, 0),
    }
}

/// Mirrors `soroban-simulation`'s own `test::simulation::default_network_config` fixture - the
/// crate doesn't expose one publicly (it's test-only), so this is hand-built from the same
/// pattern rather than depended on directly.
pub fn network_config(ledger_info: &LedgerInfo) -> NetworkConfig {
    let mut cpu_cost_params = vec![
        ContractCostParamEntry {
            ext: ExtensionPoint::V0,
            const_term: 0,
            linear_term: 0,
        };
        ContractCostType::variants().len()
    ];
    let mut mem_cost_params = cpu_cost_params.clone();
    for i in 0..ContractCostType::variants().len() {
        let v = i as i64;
        cpu_cost_params[i].const_term = (v + 1) * 1000;
        cpu_cost_params[i].linear_term = v << 7;
        mem_cost_params[i].const_term = (v + 1) * 500;
        mem_cost_params[i].linear_term = v << 6;
    }

    NetworkConfig {
        fee_configuration: FeeConfiguration {
            fee_per_instruction_increment: 10,
            fee_per_disk_read_entry: 20,
            fee_per_write_entry: 30,
            fee_per_disk_read_1kb: 40,
            fee_per_write_1kb: 50,
            fee_per_historical_1kb: 60,
            fee_per_contract_event_1kb: 70,
            fee_per_transaction_size_1kb: 80,
        },
        rent_fee_configuration: RentFeeConfiguration {
            fee_per_rent_1kb: 100,
            fee_per_write_1kb: 50,
            fee_per_write_entry: 30,
            persistent_rent_rate_denominator: 100,
            temporary_rent_rate_denominator: 1000,
        },
        tx_max_instructions: 100_000_000,
        tx_memory_limit: 40_000_000,
        cpu_cost_params: ContractCostParams(cpu_cost_params.try_into().unwrap()),
        memory_cost_params: ContractCostParams(mem_cost_params.try_into().unwrap()),
        min_temp_entry_ttl: ledger_info.min_temp_entry_ttl,
        min_persistent_entry_ttl: ledger_info.min_persistent_entry_ttl,
        max_entry_ttl: ledger_info.max_entry_ttl,
    }
}

/// Runs one recording-mode invocation with every determinism-sensitive input pinned by the
/// caller (`ledger_info`, `base_prng_seed`) - the exact mechanism Phase 1 proved reproduces
/// byte-identical XDR output (via [`digest`]) across genuinely separate OS processes. `enable_diagnostics`
/// is always `true`: diagnostic events are useful for debugging and don't affect [`digest`] (which
/// only hashes determinism-sensitive fields, not `diagnostic_events`).
pub fn recording_invoke(
    snapshot_source: Rc<dyn SnapshotSource>,
    ledger_info: &LedgerInfo,
    host_fn: HostFunction,
    auth_mode: RecordingInvocationAuthMode,
    source_account: &AccountId,
    base_prng_seed: [u8; 32],
) -> Result<InvokeHostFunctionSimulationResult> {
    let network_config = network_config(ledger_info);
    simulate_invoke_host_function_op(
        snapshot_source,
        &network_config,
        &adjustment_config(),
        ledger_info,
        host_fn,
        auth_mode,
        source_account,
        base_prng_seed,
        true,
    )
    .map_err(|e| anyhow::anyhow!("{e}"))
}

/// XDR-encodes (not `Debug`-formats - the canonical, deterministic wire format every part of this
/// repo already uses, not Rust's own unstable-across-versions `Debug` output) every
/// determinism-sensitive output field and returns a SHA-256 digest. This is the exact equality
/// check both Phase 1's cross-process proof and Phase 2's `EndorseTransaction` re-execution check
/// depend on: two invocations with matching digests are, by construction, the same result on the
/// same footprint.
pub fn digest(result: &InvokeHostFunctionSimulationResult) -> Result<[u8; 32]> {
    let invoke_result = result
        .invoke_result
        .as_ref()
        .map_err(|e| anyhow::anyhow!("invocation failed: {e:?}"))
        .context("cannot digest a failed invocation")?;

    let mut hasher = Sha256::new();
    hasher.update(invoke_result.to_xdr(Limits::none())?);
    for diff in &result.modified_entries {
        if let Some(e) = &diff.state_before {
            hasher.update(e.to_xdr(Limits::none())?);
        }
        if let Some(e) = &diff.state_after {
            hasher.update(e.to_xdr(Limits::none())?);
        }
    }
    for auth in &result.auth {
        hasher.update(auth.to_xdr(Limits::none())?);
    }
    for event in &result.contract_events {
        hasher.update(event.to_xdr(Limits::none())?);
    }
    hasher.update(result.simulated_instructions.to_le_bytes());
    hasher.update(result.simulated_memory.to_le_bytes());

    Ok(hasher.finalize().into())
}
