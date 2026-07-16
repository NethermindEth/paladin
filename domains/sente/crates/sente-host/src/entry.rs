//! Sente's Paladin ABI state schema: one state per modified Soroban ledger entry (contract
//! storage slot), directly mirroring Soroban's own recording-mode footprint shape rather than
//! Pente's coarser per-account states - `simulate_invoke_host_function_op`'s
//! `modified_entries: Vec<LedgerEntryDiff>` *is* this list, so no separate bookkeeping (like
//! Pente's `AccountLoader`/`DynamicLoadWorldState`) is needed to derive it.

use anyhow::{Context, Result};
use base64::engine::general_purpose::STANDARD as BASE64;
use base64::Engine;
use serde::{Deserialize, Serialize};

use soroban_env_host::xdr::{
    ContractDataDurability, ContractDataEntry, LedgerEntry, LedgerEntryData, LedgerEntryExt,
    LedgerKey, Limits, ReadXdr, WriteXdr,
};

/// Mirrors `soroban_env_host::xdr::ContractDataDurability` - a local, `serde`-derived copy is
/// needed since the orphan rule blocks implementing `Serialize`/`Deserialize` for the XDR type
/// here directly. This is part of the ledger *key*, not just the value
/// (`LedgerKeyContractData{contract, key, durability}`) - `SnapshotSource::get` looks entries up by
/// the full key including durability, so losing this on a round-trip through a Paladin state would
/// make a reconstructed snapshot silently miss the entry (a `None` lookup), not error loudly.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum EntryDurability {
    Temporary,
    Persistent,
}

impl From<ContractDataDurability> for EntryDurability {
    fn from(d: ContractDataDurability) -> Self {
        match d {
            ContractDataDurability::Temporary => EntryDurability::Temporary,
            ContractDataDurability::Persistent => EntryDurability::Persistent,
        }
    }
}

impl From<EntryDurability> for ContractDataDurability {
    fn from(d: EntryDurability) -> Self {
        match d {
            EntryDurability::Temporary => ContractDataDurability::Temporary,
            EntryDurability::Persistent => ContractDataDurability::Persistent,
        }
    }
}

/// One version of one Soroban contract storage slot, tracked as a Paladin state.
///
/// `contract_id`/`key_xdr`/`val_xdr` are base64-encoded XDR (`ScAddress`/`ScVal`/`ScVal`
/// respectively) - the same convention `core/go/internal/domainmgr/domain.go`'s
/// `EncodingType::SALADIN_TYPED_DATA_V0`/`XDR_SCVAL` cases already use for carrying XDR through
/// Paladin's JSON state/RPC layer, so no new encoding convention is introduced here. `seq`
/// distinguishes successive versions of the same `(contract_id, key_xdr)` slot - each version gets
/// its own Paladin state id (states are immutable once created; a slot mutation spends the old
/// version's state and creates a new one at `seq + 1`), mirroring how Pente's per-account states
/// get spent/recreated on every mutation.
///
/// Deliberately NOT carried: `last_modified_ledger_seq` and the entry's own `LedgerEntryExt`
/// (rent-bump extension data) - reconstructed as `0`/`LedgerEntryExt::V0` by [`to_ledger_entry`],
/// an S2 scope limitation (fine for `factory.wasm`'s plain storage; would matter for a contract
/// relying on the no-eviction extension flag). Live-until (TTL) is likewise not stored here at
/// all: `sente_host::snapshot::build_snapshot_source` recomputes a deterministic protocol-floor
/// value from the pinned `LedgerInfo` instead, since the real per-entry TTL bump isn't part of
/// `InvokeHostFunctionSimulationResult`'s public output.
///
/// [`to_ledger_entry`]: SenteEntry::to_ledger_entry
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct SenteEntry {
    #[serde(rename = "contractId")]
    pub contract_id: String,
    #[serde(rename = "keyXdr")]
    pub key_xdr: String,
    #[serde(rename = "valXdr")]
    pub val_xdr: String,
    pub durability: EntryDurability,
    pub seq: u64,
}

/// The ABI parameter schema registered via
/// `ConfigureDomainResponse.domain_config.abi_state_schemas_json`. Same JSON shape
/// (`{name, type, internalType, components, indexed}`) as every other domain's own schemas (see
/// `firefly-signer`'s `abi.Parameter`, used directly by `domains/noto/pkg/types/states.go`'s
/// `NotoCoinABI`) even though Sente's states are chain-neutral XDR payloads, not Solidity values:
/// the schema/indexing system itself is unchanged by which chain a domain targets. `seq` is typed
/// `uint256` (not the more natural `uint64`) to match the only integer type this schema system has
/// been proven against so far (`NotoCoin.amount`) rather than risk being the first user of an
/// untested type string.
pub const SENTE_ENTRY_ABI_SCHEMA_JSON: &str = r#"{
  "name": "SenteEntry",
  "type": "tuple",
  "internalType": "struct SenteEntry",
  "components": [
    {"name": "contractId", "type": "string", "indexed": true},
    {"name": "keyXdr", "type": "string", "indexed": true},
    {"name": "valXdr", "type": "string"},
    {"name": "durability", "type": "string", "indexed": true},
    {"name": "seq", "type": "uint256", "indexed": true}
  ]
}"#;

impl SenteEntry {
    /// Builds the `(contract_id, key_xdr, val_xdr, durability)` portion of an entry from a Soroban
    /// `LedgerEntry` - `seq` is a Paladin-side versioning concern (this slot's prior state count),
    /// not derivable from a single ledger entry, so callers assign it separately.
    pub fn from_ledger_entry(entry: &LedgerEntry, seq: u64) -> Result<Self> {
        let LedgerEntryData::ContractData(ContractDataEntry {
            contract,
            key,
            val,
            durability,
            ..
        }) = &entry.data
        else {
            anyhow::bail!(
                "expected a CONTRACT_DATA ledger entry, got {:?}",
                entry.data
            );
        };
        Ok(Self {
            contract_id: BASE64.encode(contract.to_xdr(Limits::none())?),
            key_xdr: BASE64.encode(key.to_xdr(Limits::none())?),
            val_xdr: BASE64.encode(val.to_xdr(Limits::none())?),
            durability: (*durability).into(),
            seq,
        })
    }

    /// The inverse of [`from_ledger_entry`](Self::from_ledger_entry): reconstructs a full
    /// `LedgerEntry` suitable for feeding into a `SnapshotSource` (see
    /// `sente_host::snapshot::build_snapshot_source`). `last_modified_ledger_seq` and `ext` are not
    /// carried by `SenteEntry` (see the struct's own doc comment) and are reset to `0`/`V0` here.
    pub fn to_ledger_entry(&self) -> Result<LedgerEntry> {
        Ok(LedgerEntry {
            last_modified_ledger_seq: 0,
            data: LedgerEntryData::ContractData(ContractDataEntry {
                ext: soroban_env_host::xdr::ExtensionPoint::V0,
                contract: self.contract()?,
                key: self.key()?,
                durability: self.durability.into(),
                val: self.val()?,
            }),
            ext: LedgerEntryExt::V0,
        })
    }

    /// The `LedgerKey` this entry is stored/looked-up under - `SnapshotSource::get` is keyed on
    /// this (contract+key+durability), not on the value, so this is what callers building a
    /// snapshot need to construct the map they feed to `MockSnapshotSource::from_entries`.
    pub fn ledger_key(&self) -> Result<LedgerKey> {
        Ok(LedgerKey::ContractData(
            soroban_env_host::xdr::LedgerKeyContractData {
                contract: self.contract()?,
                key: self.key()?,
                durability: self.durability.into(),
            },
        ))
    }

    pub fn contract(&self) -> Result<soroban_env_host::xdr::ScAddress> {
        let bytes = BASE64
            .decode(&self.contract_id)
            .context("contract_id is not valid base64")?;
        soroban_env_host::xdr::ScAddress::from_xdr(bytes, Limits::none())
            .context("contract_id is not valid XDR ScAddress")
    }

    pub fn key(&self) -> Result<soroban_env_host::xdr::ScVal> {
        let bytes = BASE64
            .decode(&self.key_xdr)
            .context("key_xdr is not valid base64")?;
        soroban_env_host::xdr::ScVal::from_xdr(bytes, Limits::none())
            .context("key_xdr is not valid XDR ScVal")
    }

    pub fn val(&self) -> Result<soroban_env_host::xdr::ScVal> {
        let bytes = BASE64
            .decode(&self.val_xdr)
            .context("val_xdr is not valid base64")?;
        soroban_env_host::xdr::ScVal::from_xdr(bytes, Limits::none())
            .context("val_xdr is not valid XDR ScVal")
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use soroban_env_host::e2e_testutils::CreateContractData;

    #[test]
    fn schema_json_is_valid_json() {
        let parsed: serde_json::Value = serde_json::from_str(SENTE_ENTRY_ABI_SCHEMA_JSON)
            .expect("SENTE_ENTRY_ABI_SCHEMA_JSON must be valid JSON");
        assert_eq!(parsed["name"], "SenteEntry");
        assert_eq!(parsed["components"].as_array().unwrap().len(), 5);
    }

    fn factory_wasm() -> Vec<u8> {
        std::fs::read(
            std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("../../../../soroban/artifacts/factory.wasm"),
        )
        .expect("failed to read factory.wasm - run `./gradlew :soroban:compile` first")
    }

    #[test]
    fn round_trips_through_ledger_entry_and_back() {
        let contract = CreateContractData::new([9; 32], &factory_wasm());
        let entry = SenteEntry::from_ledger_entry(&contract.contract_entry, 0)
            .expect("contract_entry is a CONTRACT_DATA entry");
        assert_eq!(entry.seq, 0);

        // Round-trip: the decoded XDR must match what went in.
        let LedgerEntryData::ContractData(ContractDataEntry {
            contract: expected_contract,
            ..
        }) = &contract.contract_entry.data
        else {
            unreachable!()
        };
        assert_eq!(&entry.contract().unwrap(), expected_contract);

        let json = serde_json::to_string(&entry).unwrap();
        let round_tripped: SenteEntry = serde_json::from_str(&json).unwrap();
        assert_eq!(entry, round_tripped);
    }

    #[test]
    fn rejects_non_contract_data_entries() {
        let account = soroban_env_host::e2e_testutils::account_entry(
            &soroban_env_host::e2e_testutils::get_account_id([1; 32]),
        );
        assert!(SenteEntry::from_ledger_entry(&account, 0).is_err());
    }

    #[test]
    fn to_ledger_entry_round_trips_contract_data() {
        let contract = CreateContractData::new([10; 32], &factory_wasm());
        let entry = SenteEntry::from_ledger_entry(&contract.contract_entry, 3).unwrap();

        let rebuilt = entry.to_ledger_entry().unwrap();
        let LedgerEntryData::ContractData(original) = &contract.contract_entry.data else {
            unreachable!()
        };
        let LedgerEntryData::ContractData(rebuilt_data) = &rebuilt.data else {
            unreachable!()
        };
        assert_eq!(rebuilt_data.contract, original.contract);
        assert_eq!(rebuilt_data.key, original.key);
        assert_eq!(rebuilt_data.val, original.val);
        assert_eq!(rebuilt_data.durability, original.durability);

        assert_eq!(
            entry.ledger_key().unwrap(),
            LedgerKey::ContractData(soroban_env_host::xdr::LedgerKeyContractData {
                contract: original.contract.clone(),
                key: original.key.clone(),
                durability: original.durability,
            })
        );
    }
}
