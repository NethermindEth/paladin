//! `SenteDomain`: the real `DomainHandler` implementation for Sente (chapter 14 §14.3).
//!
//! - **Phase 2 (S2)'s mechanism** - `assemble`/`endorse`/`prepare` for one arbitrary Soroban host
//!   invocation, re-executed and digest-compared across processes - proved the cross-process
//!   endorsement mechanism against a fixed, deterministically-reconstructible bootstrap contract
//!   (`factory.wasm`'s `register`). That mechanism is now superseded entirely by S3's real
//!   transition flow below, not kept alongside it.
//! - **Phase 3 (S3)'s real transition flow, deliberately root-only for now**: a group transition
//!   advances `SentePrivacyGroup`'s on-chain hash-chain head (`root`) with unanimous member
//!   signatures - see `soroban/contracts/sente/src/lib.rs`. Its signature check
//!   (`saladin_typed_data::verify`, plain application-level ed25519 verification, not Soroban's
//!   `require_auth` framework) genuinely requires valid signatures to succeed - meaning
//!   `assemble_transaction`/`endorse_transaction` cannot simulate a call to `transition` itself
//!   (no member's real private key is available to the plugin at assemble time). Instead:
//!   - `new_root` is derived deterministically from `(old_root, transaction_id)` - an opaque,
//!     content-free commitment every party can recompute identically, not tied to any simulated
//!     execution result (root-only transitions have no external effects to encode into it yet).
//!   - The actual thing every member's `ENDORSE` attestation signs is
//!     `SALADIN_TYPED_DATA_V0("sente.Transition", {old_root, new_root, external_calls=[]})` -
//!     precisely what `transition`'s on-chain check verifies - not a separate off-chain-only
//!     payload the way S2's `result_digest` was. `endorse_transaction` independently re-derives
//!     and checks this digest (see its own doc comment) instead of re-executing a host invocation.
//!   - The group's own genesis instance state (`members`/`network_passphrase`/`root=0`) must
//!     already exist as a tracked `SenteEntry` before its first transition can be assembled -
//!     populating that from a real on-chain deploy is Go-side indexing work, out of scope here
//!     (see `saladin-book/part-2-saladin/14-domain-ports.md` §14.3 S3 for the precise boundary).
//!   - `external_calls` (the SNoto-atomicity half of S3's exit criterion) remain unwired at the
//!     plugin level - proven only at the contract-test level
//!     (`soroban/contracts/sente/src/test.rs`'s `transition_executes_external_call_atomically`).
//!     Wiring them in needs both a general JSON->`ScVal` argument encoder (already flagged as
//!     separable future work) and a way to bootstrap an external contract's own genesis state,
//!     which - unlike the wrapper's own genesis - this domain has no way to know how to construct.

use std::collections::HashMap;
use std::sync::Mutex;

use async_trait::async_trait;
use base64::engine::general_purpose::STANDARD as BASE64;
use base64::Engine;
use saladin_plugin_rs::{pb, DomainHandler, PaladinClient};
use sha2::{Digest, Sha256};

use soroban_env_host::xdr::{
    ContractExecutable, ContractId, Hash, Limits, ReadXdr, ScAddress, ScBytes, ScContractInstance,
    ScMap, ScMapEntry, ScSymbol, ScVal, ScVec, VecM, WriteXdr,
};

use crate::info::InfoState;

/// `toolkit/go/pkg/algorithms.EDDSA_ED25519` ("eddsa" + ":" + "ed25519").
const SIGN_ALGORITHM: &str = "eddsa:ed25519";
/// `toolkit/go/pkg/verifiers.STELLAR_ADDRESS`.
const VERIFIER_TYPE: &str = "stellar_address";
/// `toolkit/go/pkg/signpayloads.OPAQUE_TO_EDDSA` - the sender's own SIGN attestation's payload
/// type (a raw digest signed with ed25519, same convention this session's Noto-Stellar work
/// already established).
const SIGN_PAYLOAD_TYPE: &str = "opaque:eddsa";
/// `SALADIN_TYPED_DATA_V0`'s type name for a Sente transition (chapter 14 §14.3), matching
/// `soroban/contracts/sente`'s own `transition`/`sign_transition` test helper exactly.
const TRANSITION_TYPE_NAME: &str = "sente.Transition";

/// S3 genesis config, supplied by the Paladin administrator as this domain's `config_json`
/// (`ConfigureDomainRequest.config_json`) - the same "runtime config comes from the domain's own
/// config block, not a compiled-in constant" convention Noto's Stellar `chainIO` config already
/// uses (`domains/noto/pkg/types/config.go`'s `StellarSnotoFactoryAddress`/`StellarSnotoWasmHash`),
/// since a factory's deployed address is only known once `deploy-stellar-fixtures.sh` has actually
/// run against a real network - it can never be a compile-time constant.
#[derive(Debug, Clone, serde::Deserialize)]
#[serde(rename_all = "camelCase")]
struct SenteConfig {
    /// The pre-deployed `SenteFactory` contract's address (`soroban/contracts/sente-factory`).
    sente_factory_address: String,
    /// The pre-deployed `SaladinFactory` registry contract's address (`soroban/contracts/factory`).
    saladin_factory_address: String,
    /// Hex-encoded Wasm hash of the `SentePrivacyGroup` contract (`soroban/contracts/sente`),
    /// uploaded (not deployed) once, the same way `snotoWasmHash` is - see
    /// `deploy-stellar-fixtures.sh`.
    sente_wasm_hash: String,
    /// Raw network passphrase bytes, doubling as `SentePrivacyGroup::initialize`'s
    /// `network_passphrase` argument - the same "config is the raw passphrase" convention
    /// `snoto::initialize`/`snoto-factory::deploy` already established, and what
    /// `SALADIN_TYPED_DATA_V0` digests (both genesis and ordinary transitions) are computed over.
    network_passphrase: String,
}

/// The constructor params a genesis deploy transaction supplies (`DeployTransactionSpecification.
/// constructor_params_json`) - `group.salt`/`group.members` mirrors Pente's own
/// `PrivacyGroupConstructorParamsJSON` shape (`PenteTransaction.java`), reused here rather than
/// invented fresh since it's the same "group genesis" concept being translated.
#[derive(Debug, Clone, serde::Deserialize)]
struct DeployConstructorParams {
    group: DeployGroupParams,
}

#[derive(Debug, Clone, serde::Deserialize)]
struct DeployGroupParams {
    /// Hex string (with or without a `0x` prefix) - salts each member's per-group identity lookup
    /// below. Chosen by whoever submits the genesis transaction; every node must derive the same
    /// lookups from it, so it travels as plain constructor data, not something this plugin invents.
    salt: String,
    /// Raw member locators (`"identity"` or `"identity@node"`), before per-group salting.
    members: Vec<String>,
}

/// Pente's own per-group identity scoping (`PenteTransaction.buildGroupScopeIdentityLookups`),
/// translated verbatim: splice the group's salt hex in before any `@node` suffix, so the same
/// person's identity resolves to a distinct verifier in every privacy group they belong to,
/// instead of reusing one signing key across every group. `salt_hex` has already had any `0x`
/// prefix stripped by the caller.
fn group_scope_lookup(member: &str, salt_hex: &str) -> String {
    match member.split_once('@') {
        Some((identity, node)) => format!("{identity}.{salt_hex}@{node}"),
        None => format!("{member}.{salt_hex}"),
    }
}

/// Decodes a Stellar contract strkey (`"C..."`) into the XDR `ContractId` `ScAddress::Contract`
/// wraps - the inverse of this module's own `contract_strkey`.
pub fn decode_contract_address(strkey: &str) -> Result<ContractId, String> {
    let contract = stellar_strkey::Contract::from_string(strkey)
        .map_err(|e| format!("{strkey} is not a valid contract strkey: {e}"))?;
    Ok(ContractId(Hash(contract.0)))
}

fn contract_strkey(address: &ScAddress) -> Result<String, String> {
    match address {
        ScAddress::Contract(contract_id) => {
            Ok(stellar_strkey::Contract(contract_id.0 .0).to_string())
        }
        other => Err(format!("expected a contract ScAddress, got {other:?}")),
    }
}

/// The `ScVal` key `soroban/contracts/sente::storage::DataKey`'s unit variants encode to - a
/// fieldless `#[contracttype]` enum variant encodes as `ScVal::Vec(Some(ScVec([ScVal::Symbol(
/// variant_name)])))`, empirically confirmed against the real contract crate (not assumed) via a
/// throwaway diagnostic test against `soroban_sdk::IntoVal` during this phase's development.
fn data_key_scval(name: &str) -> ScVal {
    ScVal::Vec(Some(ScVec(
        vec![ScVal::Symbol(ScSymbol(name.try_into().expect("valid symbol")))]
            .try_into()
            .expect("single-element vec"),
    )))
}

/// The `ScVal::LedgerKeyContractInstance` key every Soroban contract's own "instance" storage
/// entry is stored under - a constant, unit-variant `ScVal`, same convention `sente_host::SenteEntry`
/// already uses (`ledger_key()`) for the `key_xdr` field of any tracked storage-slot state.
pub fn instance_key_xdr_base64() -> String {
    BASE64.encode(
        ScVal::LedgerKeyContractInstance
            .to_xdr(Limits::none())
            .expect("LedgerKeyContractInstance always encodes"),
    )
}

/// Builds `SentePrivacyGroup`'s genesis "instance" storage value directly - `members`/
/// `network_passphrase`/`root=[0;32]`, matching exactly what a real on-chain
/// `initialize(members, network_passphrase)` call produces (`soroban/contracts/sente/src/
/// storage.rs`'s `init`). Hand-built rather than derived by actually running the constructor via
/// `soroban-env-host`: this phase never feeds the result into a real host invocation (root-only
/// transitions need no simulated execution at all - see the module doc comment), so the map's
/// internal ordering has no host-side validity requirement to satisfy, only round-trip fidelity
/// with this module's own `decode_root`/`with_updated_root` - and the three keys are already in
/// the enum's declared (and, coincidentally, alphabetical) order.
pub fn genesis_instance_val(
    wasm_hash: [u8; 32],
    member_pubkeys: &[[u8; 32]],
    network_passphrase: &[u8],
) -> Result<ScVal, String> {
    let members_val = ScVal::Vec(Some(ScVec(
        member_pubkeys
            .iter()
            .map(|pk| ScVal::Bytes(ScBytes(pk.to_vec().try_into().unwrap())))
            .collect::<Vec<_>>()
            .try_into()
            .map_err(|_| "failed to build members ScVec".to_string())?,
    )));
    let passphrase_val = ScVal::Bytes(ScBytes(
        network_passphrase
            .to_vec()
            .try_into()
            .map_err(|_| "network passphrase too long for ScBytes".to_string())?,
    ));
    let root_val = ScVal::Bytes(ScBytes([0u8; 32].to_vec().try_into().unwrap()));
    let map = ScMap(
        vec![
            ScMapEntry {
                key: data_key_scval("Members"),
                val: members_val,
            },
            ScMapEntry {
                key: data_key_scval("NetworkPassphrase"),
                val: passphrase_val,
            },
            ScMapEntry {
                key: data_key_scval("Root"),
                val: root_val,
            },
        ]
        .try_into()
        .map_err(|_| "failed to build instance storage map".to_string())?,
    );
    Ok(ScVal::ContractInstance(ScContractInstance {
        executable: ContractExecutable::Wasm(Hash(wasm_hash)),
        storage: Some(map),
    }))
}

/// Reads the `Root` entry out of a `SentePrivacyGroup` instance value (see `genesis_instance_val`)
/// - the inverse half of `with_updated_root`.
fn decode_root(instance_val: &ScVal) -> Result<[u8; 32], String> {
    let ScVal::ContractInstance(inst) = instance_val else {
        return Err("expected a ContractInstance value".to_string());
    };
    let map = inst
        .storage
        .as_ref()
        .ok_or("group instance has no storage map")?;
    let root_key = data_key_scval("Root");
    let entry = map
        .0
        .iter()
        .find(|e| e.key == root_key)
        .ok_or("group instance storage has no Root entry")?;
    let ScVal::Bytes(bytes) = &entry.val else {
        return Err("Root entry is not Bytes".to_string());
    };
    bytes
        .0
        .as_slice()
        .try_into()
        .map_err(|_| "Root entry is not 32 bytes".to_string())
}

/// Splices a new `Root` value into an existing, already-valid `SentePrivacyGroup` instance value -
/// `Members`/`NetworkPassphrase` and the map's own ordering are left untouched, only the one
/// mutable field changes, exactly what a real `transition` call would do on-chain.
fn with_updated_root(instance_val: &ScVal, new_root: [u8; 32]) -> Result<ScVal, String> {
    let ScVal::ContractInstance(inst) = instance_val else {
        return Err("expected a ContractInstance value".to_string());
    };
    let map = inst
        .storage
        .as_ref()
        .ok_or("group instance has no storage map")?;
    let root_key = data_key_scval("Root");
    let mut entries = map.0.to_vec();
    let idx = entries
        .iter()
        .position(|e| e.key == root_key)
        .ok_or("group instance storage has no Root entry")?;
    entries[idx].val = ScVal::Bytes(ScBytes(new_root.to_vec().try_into().unwrap()));
    Ok(ScVal::ContractInstance(ScContractInstance {
        executable: inst.executable.clone(),
        storage: Some(ScMap(
            entries
                .try_into()
                .map_err(|_| "failed to rebuild instance storage map".to_string())?,
        )),
    }))
}

/// Mirrors `soroban/crates/saladin-typed-data::digest` (`SALADIN_TYPED_DATA_V0`, chapter 13 §13.1)
/// exactly - duplicated rather than depended on: that crate is `#![no_std]` and unconditionally
/// pulls in `soroban-sdk` even for this one pure function, and this plugin already depends on
/// `soroban-env-host` directly - adding both risks feature/version-unification surprises for a
/// function this small and stable. Verified byte-identical against the same shared test vectors
/// that crate's own test uses - see `tests::saladin_typed_data_digest_matches_shared_vectors`.
fn saladin_typed_data_digest(
    network_passphrase: &[u8],
    contract_id: &[u8; 32],
    type_name: &str,
    payload_xdr: &[u8],
) -> [u8; 32] {
    fn sha256(data: &[u8]) -> [u8; 32] {
        Sha256::digest(data).into()
    }
    let mut hasher = Sha256::new();
    hasher.update(b"SALADIN_TYPED_DATA_V0");
    hasher.update(sha256(network_passphrase));
    hasher.update(contract_id);
    hasher.update(sha256(type_name.as_bytes()));
    hasher.update(sha256(payload_xdr));
    hasher.finalize().into()
}

/// XDR shape of `soroban/contracts/sente::TransitionPayload(BytesN<32>, BytesN<32>,
/// Vec<AtomOperation>)` - a tuple struct, so its `#[contracttype]` derive encodes it as a plain
/// positional `ScVal::Vec`, matching the contract's own doc comment for choosing a tuple struct
/// here. `external_calls` is always empty for this phase's root-only transitions (module doc
/// comment) - an empty `Vec<AtomOperation>` encodes as `ScVal::Vec(Some(ScVec([])))` regardless of
/// `AtomOperation`'s own shape (empirically confirmed), so no `AtomOperation` encoding is needed
/// yet.
fn transition_payload_xdr(old_root: [u8; 32], new_root: [u8; 32]) -> Result<Vec<u8>, String> {
    let payload = ScVal::Vec(Some(ScVec(
        vec![
            ScVal::Bytes(ScBytes(old_root.to_vec().try_into().unwrap())),
            ScVal::Bytes(ScBytes(new_root.to_vec().try_into().unwrap())),
            ScVal::Vec(Some(ScVec(VecM::default()))),
        ]
        .try_into()
        .map_err(|_| "failed to build TransitionPayload vector".to_string())?,
    )));
    payload
        .to_xdr(Limits::none())
        .map_err(|e| format!("failed to XDR-encode TransitionPayload: {e}"))
}

/// `new_root` is an opaque, content-free commitment for this phase's root-only transitions (module
/// doc comment) - deterministically derived from `(old_root, transaction_id)` so every party
/// (assembler and every endorser) recomputes the identical value without coordination.
fn derive_new_root(old_root: &[u8; 32], transaction_id: &str) -> [u8; 32] {
    let mut hasher = Sha256::new();
    hasher.update(old_root);
    hasher.update(transaction_id.as_bytes());
    hasher.finalize().into()
}

/// A `SenteEntry` Paladin state as queried from core, paired with the state id core assigned it -
/// `id` is needed to build `StateRef`s for `AssembledTransaction.input_states`.
struct PriorEntry {
    id: String,
    entry: sente_host::SenteEntry,
}

/// Per-group state, populated once at `InitContract` (paged in whenever a sequencer loads this
/// privacy group into memory) and read by every later `AssembleTransaction`/`EndorseTransaction`
/// call for the same contract - a real per-contract map, not S2's single global `members` field,
/// since a Paladin node can host more than one Sente group at once.
struct GroupState {
    /// Member identity locators (`"identity"` or `"identity@node"`), from
    /// `InitContractRequest.privacy_group.members` - used as `AttestationRequest.parties` for the
    /// "endorsement" round, and as `NewState.distribution_list` for this group's states.
    members: Vec<String>,
}

pub struct SenteDomain {
    client: PaladinClient,
    schema_id: Mutex<Option<String>>,
    config: Mutex<Option<SenteConfig>>,
    contracts: Mutex<HashMap<String, GroupState>>,
}

impl SenteDomain {
    pub fn new(client: PaladinClient) -> Self {
        Self {
            client,
            schema_id: Mutex::new(None),
            config: Mutex::new(None),
            contracts: Mutex::new(HashMap::new()),
        }
    }

    fn schema_id(&self) -> Result<String, String> {
        self.schema_id
            .lock()
            .unwrap()
            .clone()
            .ok_or_else(|| "schema_id not set - init_domain not yet called".to_string())
    }

    /// S3 genesis needs this domain's `SenteConfig` (factory addresses, wasm hash, network
    /// passphrase) - set once from `ConfigureDomainRequest.config_json` and required from then on.
    fn config(&self) -> Result<SenteConfig, String> {
        self.config
            .lock()
            .unwrap()
            .clone()
            .ok_or_else(|| "sente config not set - configure_domain's config_json is missing or empty".to_string())
    }

    fn group_members(&self, contract_address: &str) -> Result<Vec<String>, String> {
        self.contracts
            .lock()
            .unwrap()
            .get(contract_address)
            .map(|g| g.members.clone())
            .ok_or_else(|| {
                format!(
                    "unknown contract {contract_address} - init_contract not yet called for this group"
                )
            })
    }

    async fn prior_entries(&self, state_query_context: &str) -> Result<Vec<PriorEntry>, String> {
        let schema_id = self.schema_id()?;
        let res = self
            .client
            .find_available_states(pb::FindAvailableStatesRequest {
                state_query_context: state_query_context.to_string(),
                schema_id,
                query_json: "{}".to_string(),
                use_nullifiers: None,
            })
            .await?;
        res.states
            .into_iter()
            .map(|s| {
                let entry: sente_host::SenteEntry = serde_json::from_str(&s.data_json)
                    .map_err(|e| format!("invalid SenteEntry state data: {e}"))?;
                Ok(PriorEntry { id: s.id, entry })
            })
            .collect()
    }
}

#[async_trait]
impl DomainHandler for SenteDomain {
    async fn configure_domain(
        &self,
        req: pb::ConfigureDomainRequest,
    ) -> Result<pb::ConfigureDomainResponse, String> {
        // Empty is tolerated (harness/tests that never call init_deploy/prepare_deploy don't need
        // it) - genesis just fails clearly via `config()` if a deploy is actually attempted
        // without it.
        if !req.config_json.trim().is_empty() {
            let config: SenteConfig = serde_json::from_str(&req.config_json)
                .map_err(|e| format!("invalid Sente domain config_json: {e}"))?;
            *self.config.lock().unwrap() = Some(config);
        }
        Ok(pb::ConfigureDomainResponse {
            domain_config: Some(pb::DomainConfig {
                custom_hash_function: false,
                abi_state_schemas_json: vec![sente_host::SENTE_ENTRY_ABI_SCHEMA_JSON.to_string()],
                abi_events_json: String::new(),
                signing_algorithms: Default::default(),
                full_state_availablity_required: false,
                max_input_states: 0,
                max_output_states: 0,
            }),
            supported_chain_kinds: vec!["stellar".to_string()],
        })
    }

    async fn init_domain(
        &self,
        req: pb::InitDomainRequest,
    ) -> Result<pb::InitDomainResponse, String> {
        let schema = req
            .abi_state_schemas
            .first()
            .ok_or("init_domain: expected the SenteEntry schema to be registered")?;
        *self.schema_id.lock().unwrap() = Some(schema.id.clone());
        Ok(pb::InitDomainResponse {})
    }

    /// S3 genesis, step 1: declares which verifiers need resolving before the deploy can be
    /// prepared - purely declarative (`InitDeployResponse.required_verifiers`), the same
    /// `required_verifiers`/`resolved_verifiers` round trip `init_transaction`/
    /// `assemble_transaction` already use, no new synchronous call needed. Mirrors Pente's
    /// `initDeploy`/`buildGroupScopeIdentityLookups` exactly, translated to ed25519/Stellar.
    async fn init_deploy(
        &self,
        req: pb::InitDeployRequest,
    ) -> Result<pb::InitDeployResponse, String> {
        let transaction = req.transaction.ok_or("init_deploy: transaction not set")?;
        let params: DeployConstructorParams =
            serde_json::from_str(&transaction.constructor_params_json)
                .map_err(|e| format!("invalid deploy constructor_params_json: {e}"))?;
        let salt_hex = params.group.salt.trim_start_matches("0x");

        Ok(pb::InitDeployResponse {
            required_verifiers: params
                .group
                .members
                .iter()
                .map(|member| pb::ResolveVerifierRequest {
                    lookup: group_scope_lookup(member, salt_hex),
                    algorithm: SIGN_ALGORITHM.to_string(),
                    verifier_type: VERIFIER_TYPE.to_string(),
                })
                .collect(),
        })
    }

    /// S3 genesis, step 2: builds the real on-chain deploy - a `SorobanInvoke` against the
    /// pre-deployed `SenteFactory`'s `deploy_group`, the same "the plugin only ever invokes an
    /// already-deployed factory, never builds contract-creation XDR itself" pattern Noto's
    /// `stellarPrepareDeploy` already established (`domains/noto/internal/noto/deploy_stellar.go`).
    /// No assemble/endorse round trip here - a genesis deploy isn't privately coordinated the way
    /// a regular transaction is, matching Pente's `prepareDeploy` returning a prepared transaction
    /// directly.
    async fn prepare_deploy(
        &self,
        req: pb::PrepareDeployRequest,
    ) -> Result<pb::PrepareDeployResponse, String> {
        let transaction = req.transaction.ok_or("prepare_deploy: transaction not set")?;
        let config = self.config()?;
        let params: DeployConstructorParams =
            serde_json::from_str(&transaction.constructor_params_json)
                .map_err(|e| format!("invalid deploy constructor_params_json: {e}"))?;
        let salt_hex = params.group.salt.trim_start_matches("0x");

        // Re-derives the same lookups `init_deploy` requested, and matches each one back to its
        // resolved verifier by lookup+algorithm+verifier_type equality - the same matching
        // `PenteDomain.getResolvedEndorsers` does against `PrepareDeployRequest.resolved_verifiers`.
        let mut members_scval = Vec::with_capacity(params.group.members.len());
        for member in &params.group.members {
            let lookup = group_scope_lookup(member, salt_hex);
            let resolved = req
                .resolved_verifiers
                .iter()
                .find(|v| {
                    v.lookup == lookup
                        && v.algorithm == SIGN_ALGORITHM
                        && v.verifier_type == VERIFIER_TYPE
                })
                .ok_or_else(|| format!("prepare_deploy: no resolved verifier for {lookup}"))?;
            let public_key = stellar_strkey::ed25519::PublicKey::from_string(&resolved.verifier)
                .map_err(|e| {
                    format!(
                        "resolved verifier {} is not a valid Stellar ed25519 address: {e}",
                        resolved.verifier
                    )
                })?;
            members_scval.push(ScVal::Bytes(ScBytes(
                public_key
                    .0
                    .to_vec()
                    .try_into()
                    .map_err(|_| "failed to build member ScBytes".to_string())?,
            )));
        }

        let wasm_hash_bytes = hex::decode(&config.sente_wasm_hash)
            .map_err(|e| format!("invalid senteWasmHash config: {e}"))?;
        let saladin_factory = decode_contract_address(&config.saladin_factory_address)?;
        let tx_id_hash: [u8; 32] = Sha256::digest(transaction.transaction_id.as_bytes()).into();

        // Argument order must match `sente-factory`'s real Rust signature exactly:
        // `deploy_group(wasm_hash, members, config, saladin_factory, tx_id)`.
        let args: VecM<ScVal> = vec![
            ScVal::Bytes(ScBytes(wasm_hash_bytes.try_into().map_err(|_| {
                "senteWasmHash config must decode to exactly 32 bytes".to_string()
            })?)),
            ScVal::Vec(Some(ScVec(members_scval.try_into().map_err(|_| {
                "failed to build members ScVec".to_string()
            })?))),
            ScVal::Bytes(ScBytes(
                config
                    .network_passphrase
                    .as_bytes()
                    .to_vec()
                    .try_into()
                    .map_err(|_| "network passphrase too long for ScBytes".to_string())?,
            )),
            ScVal::Address(ScAddress::Contract(saladin_factory)),
            ScVal::Bytes(ScBytes(
                tx_id_hash
                    .to_vec()
                    .try_into()
                    .map_err(|_| "failed to build tx_id ScBytes".to_string())?,
            )),
        ]
        .try_into()
        .map_err(|_| "failed to build deploy args vector".to_string())?;
        let args_bytes = args
            .to_xdr(Limits::none())
            .map_err(|e| format!("failed to XDR-encode deploy args: {e}"))?;

        Ok(pb::PrepareDeployResponse {
            signer: None,
            transaction: None,
            deploy: None,
            chain_transaction: Some(pb::PreparedChainTransaction {
                r#type: pb::prepared_chain_transaction::TransactionType::Public as i32,
                required_signer: None,
                payload: Some(pb::prepared_chain_transaction::Payload::Soroban(
                    pb::SorobanInvoke {
                        contract_id: config.sente_factory_address.clone(),
                        function_name: "deploy_group".to_string(),
                        args_xdr: args_bytes,
                        args_json: String::new(),
                        auth_entries_xdr: vec![],
                        read_footprint_hints: vec![],
                    },
                )),
            }),
            soroban_deploy: None,
        })
    }

    async fn init_contract(
        &self,
        req: pb::InitContractRequest,
    ) -> Result<pb::InitContractResponse, String> {
        let members = req
            .privacy_group
            .as_ref()
            .map(|pg| pg.members.clone())
            .unwrap_or_default();
        self.contracts.lock().unwrap().insert(
            req.contract_address.clone(),
            GroupState {
                members: members.clone(),
            },
        );
        Ok(pb::InitContractResponse {
            valid: true,
            contract_config: Some(pb::ContractConfig {
                contract_config_json: "{}".to_string(),
                coordinator_selection: pb::contract_config::CoordinatorSelection::CoordinatorEndorser
                    as i32,
                static_coordinator: None,
                coordinator_endorser_candidates: members,
                submitter_selection: pb::contract_config::SubmitterSelection::SubmitterCoordinator
                    as i32,
            }),
        })
    }

    async fn init_transaction(
        &self,
        req: pb::InitTransactionRequest,
    ) -> Result<pb::InitTransactionResponse, String> {
        let transaction = req
            .transaction
            .ok_or("init_transaction: transaction not set")?;
        Ok(pb::InitTransactionResponse {
            required_verifiers: vec![pb::ResolveVerifierRequest {
                lookup: transaction.from,
                algorithm: SIGN_ALGORITHM.to_string(),
                verifier_type: VERIFIER_TYPE.to_string(),
            }],
        })
    }

    /// Builds a root-only group transition (module doc comment): finds the group's currently
    /// tracked genesis/prior instance `SenteEntry`, derives `new_root`, computes the on-chain
    /// typed-data digest every member's endorsement will actually sign, and declares the
    /// resulting spliced-root `SenteEntry` as this transaction's one output state.
    async fn assemble_transaction(
        &self,
        req: pb::AssembleTransactionRequest,
    ) -> Result<pb::AssembleTransactionResponse, String> {
        let transaction = req
            .transaction
            .ok_or("assemble_transaction: transaction not set")?;
        let schema_id = self.schema_id()?;
        let contract_info = transaction
            .contract_info
            .clone()
            .ok_or("assemble_transaction: contract_info not set")?;
        let members = self.group_members(&contract_info.contract_address)?;

        let contract_id = decode_contract_address(&contract_info.contract_address)?;
        let contract_address = ScAddress::Contract(contract_id.clone());
        let contract_id_base64 = BASE64.encode(
            contract_address
                .to_xdr(Limits::none())
                .map_err(|e| e.to_string())?,
        );
        let instance_key_base64 = instance_key_xdr_base64();

        let prior = self.prior_entries(&req.state_query_context).await?;
        let Some(prior_group_entry) = prior.iter().find(|p| {
            p.entry.contract_id == contract_id_base64 && p.entry.key_xdr == instance_key_base64
        }) else {
            return Ok(pb::AssembleTransactionResponse {
                assembly_result: pb::assemble_transaction_response::Result::Revert as i32,
                assembled_transaction: None,
                attestation_plan: vec![],
                revert_reason: Some(
                    "group genesis state not found - the group's genesis SenteEntry must be \
                     recorded before its first transition can be assembled"
                        .to_string(),
                ),
            });
        };

        let old_root = decode_root(&prior_group_entry.entry.val().map_err(|e| e.to_string())?)?;
        let new_root = derive_new_root(&old_root, &transaction.transaction_id);
        let payload_xdr = transition_payload_xdr(old_root, new_root)?;
        let config = self.config()?;
        let on_chain_digest = saladin_typed_data_digest(
            config.network_passphrase.as_bytes(),
            &contract_id.0 .0,
            TRANSITION_TYPE_NAME,
            &payload_xdr,
        );

        let new_instance_val = with_updated_root(
            &prior_group_entry.entry.val().map_err(|e| e.to_string())?,
            new_root,
        )?;
        let new_entry = sente_host::SenteEntry {
            contract_id: contract_id_base64.clone(),
            key_xdr: instance_key_base64,
            val_xdr: BASE64.encode(
                new_instance_val
                    .to_xdr(Limits::none())
                    .map_err(|e| e.to_string())?,
            ),
            durability: sente_host::EntryDurability::Persistent,
            seq: prior_group_entry.entry.seq + 1,
        };

        let info = InfoState::new(
            transaction.transaction_id.clone(),
            contract_id_base64,
            old_root,
            new_root,
            on_chain_digest,
        );
        let info_json = serde_json::to_string(&info).map_err(|e| e.to_string())?;
        let signing_payload = info.signing_payload().map_err(|e| e.to_string())?;

        Ok(pb::AssembleTransactionResponse {
            assembly_result: pb::assemble_transaction_response::Result::Ok as i32,
            assembled_transaction: Some(pb::AssembledTransaction {
                input_states: vec![pb::StateRef {
                    id: prior_group_entry.id.clone(),
                    schema_id: schema_id.clone(),
                }],
                read_states: vec![],
                output_states: vec![pb::NewState {
                    schema_id: schema_id.clone(),
                    state_data_json: serde_json::to_string(&new_entry)
                        .map_err(|e| e.to_string())?,
                    distribution_list: members.clone(),
                    id: None,
                    nullifier_specs: vec![],
                }],
                info_states: vec![pb::NewState {
                    schema_id: schema_id.clone(),
                    state_data_json: info_json,
                    distribution_list: members.clone(),
                    id: None,
                    nullifier_specs: vec![],
                }],
                domain_data: None,
            }),
            attestation_plan: vec![
                pb::AttestationRequest {
                    name: "sender-signature".to_string(),
                    attestation_type: pb::AttestationType::Sign as i32,
                    algorithm: SIGN_ALGORITHM.to_string(),
                    verifier_type: VERIFIER_TYPE.to_string(),
                    payload: signing_payload.to_vec(),
                    payload_type: SIGN_PAYLOAD_TYPE.to_string(),
                    parties: vec![transaction.from.clone()],
                    threshold: None,
                },
                pb::AttestationRequest {
                    name: "endorsement".to_string(),
                    attestation_type: pb::AttestationType::Endorse as i32,
                    algorithm: SIGN_ALGORITHM.to_string(),
                    verifier_type: VERIFIER_TYPE.to_string(),
                    payload: vec![],
                    payload_type: String::new(),
                    parties: members,
                    threshold: None,
                },
            ],
            revert_reason: None,
        })
    }

    /// Independently re-derives and verifies the proposed transition, rather than trusting the
    /// assembler's `InfoState` blindly: `old_root` comes from `req.inputs` (the claimed prior
    /// state, the same "trust state via inputs, not via info" pattern S2 already used), `new_root`
    /// is recomputed from it, and the on-chain typed-data digest is recomputed and compared against
    /// `info.on_chain_digest`. Returns `Sign` with that digest as the payload - the exact bytes
    /// `SentePrivacyGroup::transition` verifies on-chain, not a separate off-chain-only payload.
    async fn endorse_transaction(
        &self,
        req: pb::EndorseTransactionRequest,
    ) -> Result<pb::EndorseTransactionResponse, String> {
        let info_state = req
            .info
            .first()
            .ok_or("endorse_transaction: no info state provided")?;
        let info: InfoState = serde_json::from_str(&info_state.state_data_json)
            .map_err(|e| format!("invalid InfoState JSON: {e}"))?;

        let input = req
            .inputs
            .first()
            .ok_or("endorse_transaction: no input state provided")?;
        let prior_entry: sente_host::SenteEntry = serde_json::from_str(&input.state_data_json)
            .map_err(|e| format!("invalid SenteEntry JSON: {e}"))?;
        let old_root_actual =
            decode_root(&prior_entry.val().map_err(|e| e.to_string())?)?;

        if old_root_actual != info.old_root {
            return Ok(pb::EndorseTransactionResponse {
                endorsement_result: pb::endorse_transaction_response::Result::Revert as i32,
                payload: None,
                revert_reason: Some(format!(
                    "old_root mismatch: claimed={} actual={}",
                    hex::encode(info.old_root),
                    hex::encode(old_root_actual)
                )),
            });
        }

        let expected_new_root = derive_new_root(&old_root_actual, &info.transaction_id);
        if expected_new_root != info.new_root {
            return Ok(pb::EndorseTransactionResponse {
                endorsement_result: pb::endorse_transaction_response::Result::Revert as i32,
                payload: None,
                revert_reason: Some(format!(
                    "new_root mismatch: expected={} claimed={}",
                    hex::encode(expected_new_root),
                    hex::encode(info.new_root)
                )),
            });
        }

        let payload_xdr = transition_payload_xdr(info.old_root, info.new_root)?;
        let config = self.config()?;
        let contract_id_bytes = BASE64
            .decode(&info.contract_id)
            .map_err(|e| format!("invalid contract_id: {e}"))
            .and_then(|bytes| {
                ScAddress::from_xdr(bytes, Limits::none()).map_err(|e| e.to_string())
            })
            .and_then(|addr| match addr {
                ScAddress::Contract(cid) => Ok(cid.0 .0),
                other => Err(format!("expected a contract ScAddress, got {other:?}")),
            })?;
        let expected_digest = saladin_typed_data_digest(
            config.network_passphrase.as_bytes(),
            &contract_id_bytes,
            TRANSITION_TYPE_NAME,
            &payload_xdr,
        );

        if expected_digest != info.on_chain_digest {
            return Ok(pb::EndorseTransactionResponse {
                endorsement_result: pb::endorse_transaction_response::Result::Revert as i32,
                payload: None,
                revert_reason: Some(format!(
                    "on_chain_digest mismatch: assembler={} local={}",
                    hex::encode(info.on_chain_digest),
                    hex::encode(expected_digest)
                )),
            });
        }

        Ok(pb::EndorseTransactionResponse {
            endorsement_result: pb::endorse_transaction_response::Result::Sign as i32,
            payload: Some(info.on_chain_digest.to_vec()),
            revert_reason: None,
        })
    }

    /// Bundles every collected member endorsement into the real, final on-chain
    /// `transition(new_root, external_calls=[], signatures)` call - the collected
    /// `AttestationResult`s already carry each member's resolved verifier (public key) and the raw
    /// ed25519 signature it produced over `endorse_transaction`'s returned payload (the on-chain
    /// digest itself), so no separate signature-collection mechanism is needed here.
    async fn prepare_transaction(
        &self,
        req: pb::PrepareTransactionRequest,
    ) -> Result<pb::PrepareTransactionResponse, String> {
        let info_state = req
            .info_states
            .first()
            .ok_or("prepare_transaction: no info state provided")?;
        let info: InfoState = serde_json::from_str(&info_state.state_data_json)
            .map_err(|e| format!("invalid InfoState JSON: {e}"))?;

        let mut signature_pairs = Vec::new();
        for result in &req.attestation_result {
            if result.name != "endorsement"
                || result.attestation_type != pb::AttestationType::Endorse as i32
            {
                continue;
            }
            let verifier = result
                .verifier
                .as_ref()
                .ok_or("endorsement attestation result missing verifier")?;
            let public_key = stellar_strkey::ed25519::PublicKey::from_string(&verifier.verifier)
                .map_err(|e| {
                    format!(
                        "endorsing verifier {} is not a valid Stellar ed25519 address: {e}",
                        verifier.verifier
                    )
                })?;
            let signature_bytes = result
                .payload
                .clone()
                .ok_or("endorsement attestation result missing payload (signature)")?;
            let signature: [u8; 64] = signature_bytes
                .try_into()
                .map_err(|_| "endorsement signature is not 64 bytes".to_string())?;
            signature_pairs.push((public_key.0, signature));
        }
        if signature_pairs.is_empty() {
            return Err("prepare_transaction: no endorsement signatures collected".to_string());
        }

        let signatures_val = ScVal::Vec(Some(ScVec(
            signature_pairs
                .iter()
                .map(|(pk, sig)| {
                    ScVal::Vec(Some(ScVec(
                        vec![
                            ScVal::Bytes(ScBytes(pk.to_vec().try_into().unwrap())),
                            ScVal::Bytes(ScBytes(sig.to_vec().try_into().unwrap())),
                        ]
                        .try_into()
                        .unwrap(),
                    )))
                })
                .collect::<Vec<_>>()
                .try_into()
                .map_err(|_| "failed to build signatures ScVec".to_string())?,
        )));

        let args: VecM<ScVal> = vec![
            ScVal::Bytes(ScBytes(info.new_root.to_vec().try_into().unwrap())),
            ScVal::Vec(Some(ScVec(VecM::default()))),
            signatures_val,
        ]
        .try_into()
        .map_err(|_| "failed to build transition args vector".to_string())?;
        let args_bytes = args
            .to_xdr(Limits::none())
            .map_err(|e| format!("failed to XDR-encode transition args: {e}"))?;

        let contract_address = BASE64
            .decode(&info.contract_id)
            .map_err(|e| format!("invalid contract_id: {e}"))
            .and_then(|bytes| {
                ScAddress::from_xdr(bytes, Limits::none()).map_err(|e| e.to_string())
            })?;
        let contract_id = contract_strkey(&contract_address)?;

        Ok(pb::PrepareTransactionResponse {
            transaction: Some(pb::PreparedTransaction {
                function_abi_json: String::new(),
                params_json: String::new(),
                contract_address: None,
                r#type: pb::prepared_transaction::TransactionType::Private as i32,
                required_signer: None,
            }),
            metadata: None,
            chain_transaction: Some(pb::PreparedChainTransaction {
                r#type: pb::prepared_chain_transaction::TransactionType::Private as i32,
                required_signer: None,
                payload: Some(pb::prepared_chain_transaction::Payload::Soroban(
                    pb::SorobanInvoke {
                        contract_id,
                        function_name: "transition".to_string(),
                        args_xdr: args_bytes,
                        args_json: String::new(),
                        auth_entries_xdr: vec![],
                        read_footprint_hints: vec![],
                    },
                )),
            }),
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn saladin_typed_data_digest_matches_shared_vectors() {
        #[derive(serde::Deserialize)]
        struct Vector {
            name: String,
            network_passphrase: String,
            contract_id: String,
            type_name: String,
            payload_scval_xdr_base64: String,
            digest_hex: String,
        }
        let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../../../testdata/saladin/saladin_typed_data_v0_vectors.json");
        let vectors: Vec<Vector> =
            serde_json::from_slice(&std::fs::read(path).expect("vectors file must exist"))
                .expect("vectors file must be valid JSON");
        assert!(!vectors.is_empty(), "expected at least one shared vector");
        for vector in vectors {
            let contract = stellar_strkey::Contract::from_string(&vector.contract_id).unwrap();
            let payload = BASE64.decode(vector.payload_scval_xdr_base64).unwrap();
            assert_eq!(
                hex::encode(saladin_typed_data_digest(
                    vector.network_passphrase.as_bytes(),
                    &contract.0,
                    &vector.type_name,
                    &payload,
                )),
                vector.digest_hex,
                "{}",
                vector.name
            );
        }
    }

    #[test]
    fn genesis_instance_round_trips_root() {
        let val = genesis_instance_val([9u8; 32], &[[1u8; 32], [2u8; 32]], b"passphrase").unwrap();
        assert_eq!(decode_root(&val).unwrap(), [0u8; 32]);

        let updated = with_updated_root(&val, [7u8; 32]).unwrap();
        assert_eq!(decode_root(&updated).unwrap(), [7u8; 32]);

        // Members/NetworkPassphrase survive untouched.
        let ScVal::ContractInstance(inst) = &updated else {
            unreachable!()
        };
        let map = inst.storage.as_ref().unwrap();
        assert_eq!(map.0.len(), 3);
    }

    #[test]
    fn derive_new_root_is_deterministic_and_content_sensitive() {
        let old_root = [0u8; 32];
        let a = derive_new_root(&old_root, "tx-1");
        let b = derive_new_root(&old_root, "tx-1");
        let c = derive_new_root(&old_root, "tx-2");
        assert_eq!(a, b);
        assert_ne!(a, c);
    }

    /// `prepare_transaction` never cryptographically verifies a signature itself (that's the
    /// on-chain contract's job) - it just decodes each endorsement's resolved verifier strkey and
    /// repackages whatever bytes it was given, so a synthetic (not genuinely-signed) payload is
    /// enough to prove the XDR bundling itself is correct: real `new_root`/empty `external_calls`/
    /// the exact `(pubkey, signature)` pairs, in `transition`'s real positional argument order.
    #[tokio::test]
    async fn prepare_transaction_bundles_signatures_into_transition_args() {
        let (client, _to_core_rx) = PaladinClient::new_test("prepare-test");
        let domain = SenteDomain::new(client);

        let new_root = [7u8; 32];
        let contract_id_base64 = BASE64.encode(
            ScAddress::Contract(ContractId(Hash([0x11; 32])))
                .to_xdr(Limits::none())
                .unwrap(),
        );
        let info = InfoState::new(
            "tx-1".to_string(),
            contract_id_base64,
            [0u8; 32],
            new_root,
            [3u8; 32],
        );

        let public_key = [5u8; 32];
        let signature = [9u8; 64];
        let req = pb::PrepareTransactionRequest {
            info_states: vec![pb::EndorsableState {
                id: "info-0".to_string(),
                schema_id: "SenteEntry".to_string(),
                state_data_json: serde_json::to_string(&info).unwrap(),
            }],
            attestation_result: vec![pb::AttestationResult {
                name: "endorsement".to_string(),
                attestation_type: pb::AttestationType::Endorse as i32,
                verifier: Some(pb::ResolvedVerifier {
                    lookup: "endorser@node2".to_string(),
                    algorithm: SIGN_ALGORITHM.to_string(),
                    verifier_type: VERIFIER_TYPE.to_string(),
                    verifier: stellar_strkey::ed25519::PublicKey(public_key).to_string(),
                }),
                payload_type: None,
                payload: Some(signature.to_vec()),
                constraints: vec![],
            }],
            ..Default::default()
        };

        let response = domain.prepare_transaction(req).await.unwrap();
        let chain_tx = response.chain_transaction.unwrap();
        let Some(pb::prepared_chain_transaction::Payload::Soroban(invoke)) = chain_tx.payload
        else {
            panic!("expected a Soroban invoke payload");
        };
        assert_eq!(invoke.function_name, "transition");

        let args = VecM::<ScVal>::from_xdr(invoke.args_xdr, Limits::none()).unwrap();
        assert_eq!(args.len(), 3);
        assert_eq!(
            args[0],
            ScVal::Bytes(ScBytes(new_root.to_vec().try_into().unwrap()))
        );
        assert_eq!(args[1], ScVal::Vec(Some(ScVec(VecM::default()))));
        let ScVal::Vec(Some(sigs)) = &args[2] else {
            panic!("expected a Vec of signature pairs");
        };
        assert_eq!(sigs.len(), 1);
        let ScVal::Vec(Some(pair)) = &sigs[0] else {
            panic!("expected a (pubkey, signature) pair");
        };
        assert_eq!(
            pair[0],
            ScVal::Bytes(ScBytes(public_key.to_vec().try_into().unwrap()))
        );
        assert_eq!(
            pair[1],
            ScVal::Bytes(ScBytes(signature.to_vec().try_into().unwrap()))
        );
    }
}
