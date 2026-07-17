//! Sente Phase 1 spike (chapter 14 §14.3, S1) CLI wrapper: exercises `sente_host::recording_invoke`
//! against `soroban/contracts/factory`'s real, already-built `factory.wasm` (`register`, chosen
//! because it needs no proof/auth setup - the simplest real invocation available in this repo)
//! and prints a `sente_host::digest` of the result to stdout. Run this binary twice as two
//! separate processes (see `tests/determinism.rs`) and a matching digest is Phase 1's proof.
//!
//! The recording-mode invoke/digest logic itself now lives in `lib.rs` (refactored for Phase 2,
//! which needs the same code from the real domain plugin) - this binary is just Phase 1's own
//! fixed scenario wired up to it.

use std::rc::Rc;

use anyhow::{bail, Context, Result};

use soroban_env_host::e2e_invoke::RecordingInvocationAuthMode;
use soroban_env_host::e2e_testutils::{
    account_entry, default_ledger_info, get_account_id, CreateContractData,
};
use soroban_env_host::xdr::{HostFunction, InvokeContractArgs, ScBytes, ScString, ScSymbol, ScVal};
use soroban_simulation::testutils::MockSnapshotSource;

/// A fixed, deterministic seed - in real Sente use this would be derived from the private
/// transaction ID (per the book's determinism checklist); here it's just pinned to a constant so
/// two independent process runs use the identical value.
const BASE_PRNG_SEED: [u8; 32] = [0x42; 32];

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

    // `register(tx_id, instance, config, identity_lookup)` - chosen because it needs no proof and
    // isn't `require_auth`-gated (soroban/contracts/factory/src/lib.rs), so this harness can prove
    // host determinism without also needing to construct a signed auth entry. `identity_lookup` is
    // left empty - this spike only cares about host determinism, not the identity-registration path.
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
            ScVal::String(ScString(Default::default())),
        ]
        .try_into()
        .unwrap(),
    });

    let result = sente_host::recording_invoke(
        snapshot_source,
        &ledger_info,
        host_fn,
        RecordingInvocationAuthMode::Recording(
            soroban_env_host::e2e_invoke::RecordingInvocationAuthParams::new(true, false),
        ),
        &source_account,
        BASE_PRNG_SEED,
    )?;

    let invoke_result = result
        .invoke_result
        .as_ref()
        .map_err(|e| anyhow::anyhow!("invocation failed: {e:?}"))?;
    eprintln!(
        "invoke_result={invoke_result:?} modified_entries={} auth={} events={} instructions={} memory={}",
        result.modified_entries.len(),
        result.auth.len(),
        result.contract_events.len(),
        result.simulated_instructions,
        result.simulated_memory,
    );

    Ok(hex::encode(sente_host::digest(&result)?))
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
