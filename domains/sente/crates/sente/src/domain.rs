//! `SenteDomain`: Phase 2 (S2)'s `DomainHandler` implementation - wires `sente_host::SenteEntry`/
//! `recording_invoke`/`digest` and `crate::info::InfoState` into a real (if deliberately
//! scoped-down) assemble/endorse/prepare round trip. See
//! `saladin-book/part-2-saladin/14-domain-ports.md` §14.3 S2 for the full scoping rationale; the
//! short version:
//!
//! - **Fixed test scenario, not general ABI encoding**: every `assemble_transaction`/
//!   `endorse_transaction` call invokes the same hardcoded `factory.wasm` `register(tx_id,
//!   instance, config)` call Phase 1 already proved deterministic, ignoring
//!   `TransactionSpecification.function_params_json` entirely. A general JSON->`ScVal` encoder
//!   driven by a real Soroban contract spec is separable future work.
//! - **Fixed bootstrap contract, not real on-chain deploy/genesis**: `genesis_contract()` rebuilds
//!   the exact same contract instance (wasm + instance ledger entries) from a fixed seed on every
//!   call - deterministically identical on every node, so it needs no Paladin state at all and no
//!   real `SentePrivacyGroup` deploy (that's S3). It is deliberately NOT tracked as a `SenteEntry`
//!   (its `ContractCode`/instance ledger entries aren't `ContractData`, which is all `SenteEntry`
//!   models) - only genuine storage mutations the `register()` call itself produces become
//!   `SenteEntry` Paladin states.
//! - **Digest-based endorsement, not a structural diff**: `endorse_transaction` re-executes and
//!   compares `sente_host::digest()` output against the `result_digest` the assembler committed to
//!   in its `InfoState` - see that struct's own doc comment for why this is sufficient.

use std::sync::Mutex;

use async_trait::async_trait;
use base64::engine::general_purpose::STANDARD as BASE64;
use base64::Engine;
use saladin_plugin_rs::{pb, DomainHandler, PaladinClient};
use sha2::{Digest, Sha256};

use soroban_env_host::e2e_testutils::{get_account_id, CreateContractData};
use soroban_env_host::xdr::{
    AccountId, ContractId, Hash, LedgerEntry, Limits, ScAddress, ScBytes, ScVal, ScVec, VecM,
    WriteXdr,
};
use soroban_env_host::LedgerInfo;

use crate::info::{AuthParams, InfoState, InvocationSpec, SIGN_PAYLOAD_TYPE};

/// `toolkit/go/pkg/algorithms.EDDSA_ED25519` ("eddsa" + ":" + "ed25519").
const SIGN_ALGORITHM: &str = "eddsa:ed25519";
/// `toolkit/go/pkg/verifiers.STELLAR_ADDRESS`.
const VERIFIER_TYPE: &str = "stellar_address";

/// S2's fixed scenario: `factory.wasm`'s `register` function, matching Phase 1's spike exactly.
const REGISTER_FUNCTION: &str = "register";
/// Fixed seed for S2's bootstrap contract instance (see the module doc comment) - arbitrary but
/// constant, so every node derives byte-identical `wasm_entry`/`contract_entry` ledger entries.
const GENESIS_CONTRACT_SEED: [u8; 32] = [0x99; 32];
/// Fixed seed for the invocation's source account - mirrors Phase 1's own hardcoded `[7; 32]`.
const SOURCE_ACCOUNT_SEED: [u8; 32] = [7; 32];
/// S2 ignores `function_params_json` (see module doc comment) - this is `register`'s third,
/// otherwise-arbitrary `config` argument.
const FIXED_CONFIG_BYTES: &[u8] = b"sente-s2-fixed-scenario";

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
    /// `snoto::initialize`/`snoto-factory::deploy` already established.
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
fn decode_contract_address(strkey: &str) -> Result<ContractId, String> {
    let contract = stellar_strkey::Contract::from_string(strkey)
        .map_err(|e| format!("{strkey} is not a valid contract strkey: {e}"))?;
    Ok(ContractId(Hash(contract.0)))
}

fn factory_wasm() -> Vec<u8> {
    std::fs::read(
        std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../../soroban/artifacts/factory.wasm"),
    )
    .expect("failed to read factory.wasm - run `./gradlew :soroban:compile` first")
}

fn genesis_contract() -> CreateContractData {
    CreateContractData::new(GENESIS_CONTRACT_SEED, &factory_wasm())
}

fn source_account() -> AccountId {
    get_account_id(SOURCE_ACCOUNT_SEED)
}

/// Pins `sequence_number`/`timestamp` from the real, chain-neutral `TransactionSpecification`
/// fields every node receives identically; every other `LedgerInfo` field (protocol version, TTL
/// floors, etc.) comes from this build's own compiled-in defaults - deriving those from live chain
/// config is out of scope for S2 (see the determinism checklist's "coordinated plugin upgrades"
/// note for why a compiled-in protocol version is already the right behavior, not a gap).
fn pinned_ledger_info(transaction: &pb::TransactionSpecification) -> LedgerInfo {
    let mut ledger_info = soroban_env_host::e2e_testutils::default_ledger_info();
    ledger_info.sequence_number = transaction.base_block as u32;
    ledger_info.timestamp = transaction.base_block_timestamp as u64;
    ledger_info
}

/// The two bootstrap ledger entries (contract code + contract instance) every node reconstructs
/// independently - see the module doc comment for why these are never `SenteEntry` Paladin states.
fn bootstrap_snapshot_entries(
    contract: &CreateContractData,
    ledger_info: &LedgerInfo,
) -> Vec<(LedgerEntry, Option<u32>)> {
    let live_until = ledger_info.sequence_number + ledger_info.min_persistent_entry_ttl - 1;
    vec![
        (contract.wasm_entry.clone(), Some(live_until)),
        (contract.contract_entry.clone(), Some(live_until)),
    ]
}

/// A `SenteEntry` Paladin state as queried from core, paired with the state id core assigned it -
/// `id` is needed to build `StateRef`s for `AssembledTransaction.input_states`.
struct PriorEntry {
    id: String,
    entry: sente_host::SenteEntry,
}

fn contract_strkey(address: &ScAddress) -> Result<String, String> {
    match address {
        ScAddress::Contract(contract_id) => {
            Ok(stellar_strkey::Contract(contract_id.0 .0).to_string())
        }
        other => Err(format!("expected a contract ScAddress, got {other:?}")),
    }
}

pub struct SenteDomain {
    client: PaladinClient,
    schema_id: Mutex<Option<String>>,
    members: Mutex<Vec<String>>,
    config: Mutex<Option<SenteConfig>>,
}

impl SenteDomain {
    pub fn new(client: PaladinClient) -> Self {
        Self {
            client,
            schema_id: Mutex::new(None),
            members: Mutex::new(Vec::new()),
            config: Mutex::new(None),
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

    /// Builds this scenario's fixed `register(tx_id, instance, config)` invocation - `tx_id` is
    /// derived from the real Paladin transaction id (so distinct private transactions register
    /// distinct records), everything else is fixed (see module doc comment).
    fn fixed_invocation(
        contract: &CreateContractData,
        transaction_id: &str,
    ) -> Result<InvocationSpec, String> {
        let tx_id_hash: [u8; 32] = Sha256::digest(transaction_id.as_bytes()).into();
        let args: VecM<ScVal> = vec![
            ScVal::Bytes(ScBytes(
                tx_id_hash
                    .to_vec()
                    .try_into()
                    .map_err(|_| "failed to build tx_id ScBytes".to_string())?,
            )),
            ScVal::Address(contract.contract_address.clone()),
            ScVal::Bytes(ScBytes(
                FIXED_CONFIG_BYTES
                    .to_vec()
                    .try_into()
                    .map_err(|_| "failed to build config ScBytes".to_string())?,
            )),
        ]
        .try_into()
        .map_err(|_| "failed to build args vector".to_string())?;

        InvocationSpec::new(&contract.contract_address, REGISTER_FUNCTION, args)
            .map_err(|e| e.to_string())
    }
}

#[async_trait]
impl DomainHandler for SenteDomain {
    async fn configure_domain(
        &self,
        req: pb::ConfigureDomainRequest,
    ) -> Result<pb::ConfigureDomainResponse, String> {
        // Empty is tolerated (S2's own tests/harness never set config_json, and don't need to -
        // they never call init_deploy/prepare_deploy) - genesis just fails clearly via `config()`
        // if a deploy is actually attempted without it.
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
        // S2 stand-in (see saladin-book §14.3 S2 item 1): the fixed member list Paladin's own
        // generic `PrivacyGroup` mechanism already provides, not a Sente-specific config scheme.
        // Real genesis/membership enforcement against an on-chain `SentePrivacyGroup` is S3.
        let members = req
            .privacy_group
            .as_ref()
            .map(|pg| pg.members.clone())
            .unwrap_or_default();
        *self.members.lock().unwrap() = members.clone();
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

    async fn assemble_transaction(
        &self,
        req: pb::AssembleTransactionRequest,
    ) -> Result<pb::AssembleTransactionResponse, String> {
        let transaction = req
            .transaction
            .ok_or("assemble_transaction: transaction not set")?;
        let schema_id = self.schema_id()?;
        let prior = self.prior_entries(&req.state_query_context).await?;

        let contract = genesis_contract();
        let ledger_info = pinned_ledger_info(&transaction);
        let source_account = source_account();

        let mut raw_entries = bootstrap_snapshot_entries(&contract, &ledger_info);
        for p in &prior {
            let live_until = sente_host::protocol_floor_live_until(p.entry.durability, &ledger_info);
            raw_entries.push((
                p.entry.to_ledger_entry().map_err(|e| e.to_string())?,
                Some(live_until),
            ));
        }
        let snapshot =
            sente_host::build_snapshot_source_from_parts(raw_entries).map_err(|e| e.to_string())?;

        let invocation = Self::fixed_invocation(&contract, &transaction.transaction_id)?;
        let host_fn = invocation.to_host_function().map_err(|e| e.to_string())?;
        let auth_params = AuthParams {
            disable_non_root_auth: true,
            use_address_v2: false,
        };
        let base_prng_seed = sente_host::seed_from_transaction_id(&transaction.transaction_id);

        let invoke_result = sente_host::recording_invoke(
            snapshot,
            &ledger_info,
            host_fn,
            auth_params.to_auth_mode(),
            &source_account,
            base_prng_seed,
        );
        let result = match invoke_result {
            Ok(r) => r,
            Err(e) => {
                return Ok(pb::AssembleTransactionResponse {
                    assembly_result: pb::assemble_transaction_response::Result::Revert as i32,
                    assembled_transaction: None,
                    attestation_plan: vec![],
                    revert_reason: Some(e.to_string()),
                })
            }
        };

        let result_digest = sente_host::digest(&result).map_err(|e| e.to_string())?;
        let info = InfoState::new(
            transaction.transaction_id.clone(),
            &ledger_info,
            base_prng_seed,
            invocation,
            auth_params,
            result_digest,
        );

        let members = self.members.lock().unwrap().clone();
        let mut input_states = Vec::new();
        let mut output_states = Vec::new();
        for diff in &result.modified_entries {
            if diff.state_before == diff.state_after {
                // No real change (e.g. a read-only touch of the bootstrap wasm/instance entries) -
                // not a mutation this private ledger needs to track as a Paladin state.
                continue;
            }
            let representative = diff
                .state_after
                .as_ref()
                .or(diff.state_before.as_ref())
                .ok_or("assemble_transaction: empty ledger entry diff")?;
            let candidate = sente_host::SenteEntry::from_ledger_entry(representative, 0)
                .map_err(|e| e.to_string())?;
            let prior_match = prior.iter().find(|p| {
                p.entry.contract_id == candidate.contract_id && p.entry.key_xdr == candidate.key_xdr
            });
            if let Some(p) = prior_match {
                input_states.push(pb::StateRef {
                    id: p.id.clone(),
                    schema_id: schema_id.clone(),
                });
            }
            if let Some(after_entry) = &diff.state_after {
                let seq = prior_match.map(|p| p.entry.seq + 1).unwrap_or(0);
                let new_entry = sente_host::SenteEntry::from_ledger_entry(after_entry, seq)
                    .map_err(|e| e.to_string())?;
                let state_data_json =
                    serde_json::to_string(&new_entry).map_err(|e| e.to_string())?;
                output_states.push(pb::NewState {
                    schema_id: schema_id.clone(),
                    state_data_json,
                    distribution_list: members.clone(),
                    id: None,
                    nullifier_specs: vec![],
                });
            }
        }

        let info_json = serde_json::to_string(&info).map_err(|e| e.to_string())?;
        let signing_payload = info.signing_payload().map_err(|e| e.to_string())?;

        Ok(pb::AssembleTransactionResponse {
            assembly_result: pb::assemble_transaction_response::Result::Ok as i32,
            assembled_transaction: Some(pb::AssembledTransaction {
                input_states,
                read_states: vec![],
                output_states,
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

        let ledger_info: LedgerInfo = (&info.ledger_info).into();
        let contract = genesis_contract();
        let source_account = source_account();

        let mut raw_entries = bootstrap_snapshot_entries(&contract, &ledger_info);
        for input in req.inputs.iter().chain(req.reads.iter()) {
            let entry: sente_host::SenteEntry = serde_json::from_str(&input.state_data_json)
                .map_err(|e| format!("invalid SenteEntry JSON: {e}"))?;
            let live_until = sente_host::protocol_floor_live_until(entry.durability, &ledger_info);
            raw_entries.push((
                entry.to_ledger_entry().map_err(|e| e.to_string())?,
                Some(live_until),
            ));
        }
        let snapshot =
            sente_host::build_snapshot_source_from_parts(raw_entries).map_err(|e| e.to_string())?;

        let host_fn = info.invocation.to_host_function().map_err(|e| e.to_string())?;
        let invoke_result = sente_host::recording_invoke(
            snapshot,
            &ledger_info,
            host_fn,
            info.auth_params.to_auth_mode(),
            &source_account,
            info.base_prng_seed,
        );
        let result = match invoke_result {
            Ok(r) => r,
            Err(e) => {
                return Ok(pb::EndorseTransactionResponse {
                    endorsement_result: pb::endorse_transaction_response::Result::Revert as i32,
                    payload: None,
                    revert_reason: Some(e.to_string()),
                })
            }
        };

        let local_digest = sente_host::digest(&result).map_err(|e| e.to_string())?;
        if local_digest != info.result_digest {
            return Ok(pb::EndorseTransactionResponse {
                endorsement_result: pb::endorse_transaction_response::Result::Revert as i32,
                payload: None,
                revert_reason: Some(format!(
                    "result digest mismatch: assembler={} local={}",
                    hex::encode(info.result_digest),
                    hex::encode(local_digest)
                )),
            });
        }

        let payload = info.signing_payload().map_err(|e| e.to_string())?;
        Ok(pb::EndorseTransactionResponse {
            endorsement_result: pb::endorse_transaction_response::Result::Sign as i32,
            payload: Some(payload.to_vec()),
            revert_reason: None,
        })
    }

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

        let contract_address = info.invocation.contract().map_err(|e| e.to_string())?;
        let contract_id = contract_strkey(&contract_address)?;
        let args_bytes = BASE64
            .decode(&info.invocation.args_xdr)
            .map_err(|e| format!("invalid args_xdr: {e}"))?;

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
                        function_name: info.invocation.function_name.clone(),
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
