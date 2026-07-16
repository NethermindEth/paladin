//! Sente Phase 1 spike (chapter 14 §14.3, S1): embeds `soroban-env-host`/`soroban-simulation`
//! directly and proves deterministic recording-mode re-execution across two genuinely separate OS
//! processes - the exit criterion for this phase, learning directly from the gap in Pente's own
//! test suite (single-JVM multi-identity tests can't exercise real cross-process divergence).
//!
//! Invokes `soroban/contracts/factory`'s real, already-built `factory.wasm` (`register`, chosen
//! because it needs no proof/auth setup - the simplest real invocation available in this repo),
//! against an in-memory snapshot (no live network needed), with every determinism-sensitive input
//! pinned explicitly: `LedgerInfo{sequence_number, timestamp, protocol_version}` and the base PRNG
//! seed. Prints a SHA-256 digest of the invocation's XDR-encoded outputs (return value, modified
//! ledger entries, auth entries, contract events, and the raw CPU/memory instruction counts) to
//! stdout - run this binary twice as two separate processes (see `tests/determinism.rs`) and a
//! matching digest is the proof this phase needs.

use std::rc::Rc;

use anyhow::{bail, Context, Result};
use sha2::{Digest, Sha256};

use soroban_env_host::e2e_invoke::RecordingInvocationAuthMode;
use soroban_env_host::e2e_testutils::{
    account_entry, default_ledger_info, get_account_id, CreateContractData,
};
use soroban_env_host::xdr::{HostFunction, InvokeContractArgs, ScBytes, ScSymbol, ScVal, WriteXdr};
use soroban_simulation::simulation::{
    simulate_invoke_host_function_op, SimulationAdjustmentConfig, SimulationAdjustmentFactor,
};
use soroban_simulation::testutils::MockSnapshotSource;
use soroban_simulation::NetworkConfig;

/// A fixed, deterministic seed - in real Sente use this would be derived from the private
/// transaction ID (per the book's determinism checklist); here it's just pinned to a constant so
/// two independent process runs use the identical value.
const BASE_PRNG_SEED: [u8; 32] = [0x42; 32];

fn adjustment_config() -> SimulationAdjustmentConfig {
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
fn network_config(ledger_info: &soroban_env_host::LedgerInfo) -> NetworkConfig {
    use soroban_env_host::fees::{FeeConfiguration, RentFeeConfiguration};
    use soroban_env_host::xdr::{
        ContractCostParamEntry, ContractCostParams, ContractCostType, ExtensionPoint,
    };

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

fn run() -> Result<String> {
    let wasm = std::fs::read(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../../../soroban/artifacts/factory.wasm"),
    )
    .context("failed to read factory.wasm - run `./gradlew :soroban:compile` first")?;

    // Every determinism-sensitive input pinned explicitly, not left to a default: sequence
    // number, timestamp, and protocol_version all come from `default_ledger_info()` (which itself
    // pins protocol_version to this build's own compiled-in soroban-env-host version via
    // `Host::current_test_protocol()`) - real Sente work will pin these from the actual privacy
    // group transition instead, but the mechanism being provable is the point of this phase.
    let ledger_info = default_ledger_info();

    let source_account = get_account_id([7; 32]);
    let contract = CreateContractData::new([1; 32], &wasm);

    let snapshot_source: Rc<dyn soroban_env_host::storage::SnapshotSource> = Rc::new(
        MockSnapshotSource::from_entries(vec![
            (
                contract.wasm_entry.clone(),
                Some(ledger_info.sequence_number + 1_000_000),
            ),
            (
                contract.contract_entry.clone(),
                Some(ledger_info.sequence_number + 1_000_000),
            ),
            (account_entry(&source_account), None),
        ])
        .map_err(|e| anyhow::anyhow!("{e}"))?,
    );

    // `register(tx_id, instance, config)` - chosen because it needs no proof and isn't
    // `require_auth`-gated (soroban/contracts/factory/src/lib.rs), so this harness can prove host
    // determinism without also needing to construct a signed auth entry.
    let tx_id = [0x11u8; 32];
    let instance_address = contract.contract_address.clone();
    let config_bytes = ScBytes(b"phase-1-determinism-spike".to_vec().try_into().unwrap());

    let host_fn = HostFunction::InvokeContract(InvokeContractArgs {
        contract_address: contract.contract_address.clone(),
        function_name: ScSymbol("register".try_into().unwrap()),
        args: vec![
            ScVal::Bytes(ScBytes(tx_id.to_vec().try_into().unwrap())),
            ScVal::Address(instance_address),
            ScVal::Bytes(config_bytes),
        ]
        .try_into()
        .unwrap(),
    });

    let network_config = network_config(&ledger_info);

    let result = simulate_invoke_host_function_op(
        snapshot_source,
        &network_config,
        &adjustment_config(),
        &ledger_info,
        host_fn,
        RecordingInvocationAuthMode::Recording(
            soroban_env_host::e2e_invoke::RecordingInvocationAuthParams::new(true, false),
        ),
        &source_account,
        BASE_PRNG_SEED,
        true,
    )?;

    let invoke_result = result
        .invoke_result
        .map_err(|e| anyhow::anyhow!("invocation failed: {e:?}"))?;

    // Digest every determinism-sensitive output field, XDR-encoded (the canonical, deterministic
    // wire format every part of this repo already uses) - not a `Debug`-formatted string, which
    // would tie this spike's result to Rust's own (unstable-across-versions) Debug output instead
    // of the real, load-bearing wire format.
    let mut hasher = Sha256::new();
    hasher.update(invoke_result.to_xdr(soroban_env_host::xdr::Limits::none())?);
    for diff in &result.modified_entries {
        if let Some(e) = &diff.state_before {
            hasher.update(e.to_xdr(soroban_env_host::xdr::Limits::none())?);
        }
        if let Some(e) = &diff.state_after {
            hasher.update(e.to_xdr(soroban_env_host::xdr::Limits::none())?);
        }
    }
    for auth in &result.auth {
        hasher.update(auth.to_xdr(soroban_env_host::xdr::Limits::none())?);
    }
    for event in &result.contract_events {
        hasher.update(event.to_xdr(soroban_env_host::xdr::Limits::none())?);
    }
    hasher.update(result.simulated_instructions.to_le_bytes());
    hasher.update(result.simulated_memory.to_le_bytes());

    eprintln!(
        "invoke_result={invoke_result:?} modified_entries={} auth={} events={} instructions={} memory={}",
        result.modified_entries.len(),
        result.auth.len(),
        result.contract_events.len(),
        result.simulated_instructions,
        result.simulated_memory,
    );

    Ok(hex::encode(hasher.finalize()))
}

fn main() -> Result<()> {
    match run() {
        Ok(digest) => {
            println!("{digest}");
            Ok(())
        }
        Err(e) => bail!("sente-host phase 1 spike failed: {e:?}"),
    }
}
