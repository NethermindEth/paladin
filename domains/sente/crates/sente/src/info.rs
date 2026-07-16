//! Sente's info state: the data the assembling node hands to endorsers so they can re-execute the
//! same recording-mode invocation deterministically (chapter 14 §14.3, S2's "Info state" design
//! decision) - Sente's equivalent of Pente's info state carrying a signed raw EVM transaction.
//!
//! Signing mechanism: the standard `AttestationType.SIGN` round-trip (`to_domain.proto`), the same
//! mechanism Noto/Zeto already use for their own private-data signatures - not Pente's
//! `EncodeData`+`EncodingType.ETH_TRANSACTION_SIGNED` shortcut, which is EVM-specific and has no
//! chain-neutral equivalent in `core/go/internal/domainmgr/domain.go`'s `EncodeData` today.
//! `signing_payload()` below is exactly the `AttestationRequest.payload` the assembling node's
//! `AssembleTransaction` response asks its own key to sign over (`Algorithm: EDDSA_ED25519`,
//! `VerifierType: STELLAR_ADDRESS`, `PayloadType: OPAQUE_TO_EDDSA` - all three already proven by
//! this session's earlier Noto-Stellar-port work), and the same payload any endorser recomputes
//! from the info state to verify the attached signature.

use anyhow::{Context, Result};
use base64::engine::general_purpose::STANDARD as BASE64;
use base64::Engine;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use soroban_env_host::e2e_invoke::{RecordingInvocationAuthMode, RecordingInvocationAuthParams};
use soroban_env_host::xdr::{
    HostFunction, InvokeContractArgs, Limits, ReadXdr, ScAddress, ScSymbol, ScVal, VecM, WriteXdr,
};
use soroban_env_host::LedgerInfo;

/// `toolkit/go/pkg/signpayloads.OPAQUE_TO_EDDSA` - the payload_type for both the sender's SIGN
/// attestation and every endorser's ENDORSE attestation, both signing over `signing_payload()`.
pub const SIGN_PAYLOAD_TYPE: &str = "opaque:eddsa";

/// Mirrors `soroban_env_host::LedgerInfo` field-for-field (a local type is needed since
/// `LedgerInfo` isn't `serde`-derived and the orphan rule blocks implementing `Serialize` for it
/// here) - every field is determinism-sensitive to a recording-mode invocation, so all of them are
/// pinned and carried, not just `sequence_number`/`timestamp`/`protocol_version`.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct PinnedLedgerInfo {
    pub protocol_version: u32,
    pub sequence_number: u32,
    pub timestamp: u64,
    #[serde(with = "hex::serde")]
    pub network_id: [u8; 32],
    pub base_reserve: u32,
    pub min_temp_entry_ttl: u32,
    pub min_persistent_entry_ttl: u32,
    pub max_entry_ttl: u32,
}

impl From<&LedgerInfo> for PinnedLedgerInfo {
    fn from(li: &LedgerInfo) -> Self {
        Self {
            protocol_version: li.protocol_version,
            sequence_number: li.sequence_number,
            timestamp: li.timestamp,
            network_id: li.network_id,
            base_reserve: li.base_reserve,
            min_temp_entry_ttl: li.min_temp_entry_ttl,
            min_persistent_entry_ttl: li.min_persistent_entry_ttl,
            max_entry_ttl: li.max_entry_ttl,
        }
    }
}

impl From<&PinnedLedgerInfo> for LedgerInfo {
    fn from(p: &PinnedLedgerInfo) -> Self {
        Self {
            protocol_version: p.protocol_version,
            sequence_number: p.sequence_number,
            timestamp: p.timestamp,
            network_id: p.network_id,
            base_reserve: p.base_reserve,
            min_temp_entry_ttl: p.min_temp_entry_ttl,
            min_persistent_entry_ttl: p.min_persistent_entry_ttl,
            max_entry_ttl: p.max_entry_ttl,
        }
    }
}

/// The invocation being assembled - target contract, function, and arguments, all XDR-encoded
/// (base64), the same convention `sente_host::SenteEntry` uses for its own XDR fields. `args_xdr`
/// is the whole argument vector encoded as ONE XDR blob (a `VecM<ScVal>`/`InvokeContractArgs.args`
/// shape), matching `to_domain.proto`'s `SorobanInvoke.args_xdr: bytes` (a single blob, not one
/// entry per arg) so `prepare_transaction` can copy it through directly instead of re-encoding.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct InvocationSpec {
    pub contract_id: String,
    pub function_name: String,
    pub args_xdr: String,
}

impl InvocationSpec {
    pub fn new(contract: &ScAddress, function_name: &str, args: VecM<ScVal>) -> Result<Self> {
        Ok(Self {
            contract_id: BASE64.encode(contract.to_xdr(Limits::none())?),
            function_name: function_name.to_string(),
            args_xdr: BASE64.encode(args.to_xdr(Limits::none())?),
        })
    }

    pub fn contract(&self) -> Result<ScAddress> {
        let bytes = BASE64
            .decode(&self.contract_id)
            .context("contract_id is not valid base64")?;
        ScAddress::from_xdr(bytes, Limits::none()).context("contract_id is not valid XDR ScAddress")
    }

    pub fn args(&self) -> Result<VecM<ScVal>> {
        let bytes = BASE64
            .decode(&self.args_xdr)
            .context("args_xdr is not valid base64")?;
        VecM::<ScVal>::from_xdr(bytes, Limits::none()).context("args_xdr is not valid XDR VecM<ScVal>")
    }

    /// The `HostFunction` `sente_host::recording_invoke` expects - built fresh from this spec by
    /// both the assembler (once) and every endorser (independently, from the same `InfoState`),
    /// so a mismatch here is exactly the kind of divergence endorsement is meant to catch.
    pub fn to_host_function(&self) -> Result<HostFunction> {
        Ok(HostFunction::InvokeContract(InvokeContractArgs {
            contract_address: self.contract()?,
            function_name: ScSymbol(
                self.function_name
                    .as_str()
                    .try_into()
                    .map_err(|_| anyhow::anyhow!("function_name is not a valid ScSymbol"))?,
            ),
            args: self.args()?,
        }))
    }
}

/// Mirrors `soroban_env_host::e2e_invoke::RecordingInvocationAuthParams` (again `serde`-derived
/// locally for the same orphan-rule reason as `PinnedLedgerInfo`) - determinism-sensitive (it
/// affects recorded auth entries, which `sente_host::digest` hashes) and so must be pinned by the
/// assembler and reused verbatim by every endorser, not left as a shared hardcoded constant.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub struct AuthParams {
    pub disable_non_root_auth: bool,
    pub use_address_v2: bool,
}

impl AuthParams {
    pub fn to_auth_mode(self) -> RecordingInvocationAuthMode {
        RecordingInvocationAuthMode::Recording(RecordingInvocationAuthParams::new(
            self.disable_non_root_auth,
            self.use_address_v2,
        ))
    }
}

/// The assembling party's signature over `signing_payload()`, attached once the `AttestationType.SIGN`
/// round-trip completes (populated after assembly, before the transaction is considered fully
/// assembled - matching `AttestationType.SIGN`'s own doc comment in `to_domain.proto`).
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct SenderSignature {
    /// The resolved verifier (a Stellar address) - `AttestationResult.verifier.verifier`.
    pub verifier: String,
    /// Raw 64-byte ed25519 signature (R||S), base64-encoded - the `OPAQUE_TO_EDDSA` payload_type's
    /// output shape, `AttestationResult.payload`.
    pub signature: String,
}

/// Everything an endorser needs to independently re-execute the same recording-mode invocation
/// and verify the assembling node actually committed to it - `transaction_id` ties this back to
/// the Paladin transaction it belongs to, `base_prng_seed`/`ledger_info`/`auth_params` pin every
/// determinism-sensitive host input (chapter 14's determinism checklist), `result_digest` is the
/// `sente_host::digest()` output the assembler got when it first ran the invocation (what every
/// endorser recomputes and compares against - see `sente::lib`'s `endorse_transaction`), and
/// `signature` is `None` until the `AttestationType.SIGN` round-trip resolves.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct InfoState {
    pub transaction_id: String,
    pub ledger_info: PinnedLedgerInfo,
    #[serde(with = "hex::serde")]
    pub base_prng_seed: [u8; 32],
    pub invocation: InvocationSpec,
    pub auth_params: AuthParams,
    #[serde(with = "hex::serde")]
    pub result_digest: [u8; 32],
    pub signature: Option<SenderSignature>,
}

impl InfoState {
    #[allow(clippy::too_many_arguments)]
    pub fn new(
        transaction_id: String,
        ledger_info: &LedgerInfo,
        base_prng_seed: [u8; 32],
        invocation: InvocationSpec,
        auth_params: AuthParams,
        result_digest: [u8; 32],
    ) -> Self {
        Self {
            transaction_id,
            ledger_info: ledger_info.into(),
            base_prng_seed,
            invocation,
            auth_params,
            result_digest,
            signature: None,
        }
    }

    /// The digest an endorser recomputes and checks against `signature` - covers every field the
    /// assembling node is committing to (transaction id, pinned ledger info, PRNG seed, auth
    /// params, the invocation spec, and the claimed result digest), XDR/hex-canonical, not
    /// `Debug`-formatted, matching `sente_host::digest`'s own reasoning for why canonical wire
    /// encodings (not Rust `Debug` output) are the only sound basis for a cross-process equality
    /// check. `result_digest` MUST be covered here: otherwise a tampered digest could be
    /// substituted after the sender signs, and endorsers would compare their own re-execution
    /// against a digest the sender never actually committed to - defeating the point of the
    /// SIGN-before-ENDORSE ordering `to_domain.proto`'s `AttestationType` doc comment specifies.
    pub fn signing_payload(&self) -> Result<[u8; 32]> {
        let canonical = serde_json::to_vec(&(
            &self.transaction_id,
            &self.ledger_info,
            hex::encode(self.base_prng_seed),
            &self.invocation,
            &self.auth_params,
            hex::encode(self.result_digest),
        ))
        .context("failed to canonicalize info state for signing")?;
        Ok(Sha256::digest(canonical).into())
    }
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
    fn ledger_info_round_trips_through_pinned_form() {
        let li = default_ledger_info();
        let pinned: PinnedLedgerInfo = (&li).into();
        let back: LedgerInfo = (&pinned).into();
        assert_eq!(li.protocol_version, back.protocol_version);
        assert_eq!(li.sequence_number, back.sequence_number);
        assert_eq!(li.timestamp, back.timestamp);
        assert_eq!(li.network_id, back.network_id);
        assert_eq!(li.base_reserve, back.base_reserve);
        assert_eq!(li.min_temp_entry_ttl, back.min_temp_entry_ttl);
        assert_eq!(li.min_persistent_entry_ttl, back.min_persistent_entry_ttl);
        assert_eq!(li.max_entry_ttl, back.max_entry_ttl);
    }

    const TEST_AUTH_PARAMS: AuthParams = AuthParams {
        disable_non_root_auth: true,
        use_address_v2: false,
    };

    fn invocation_spec(contract: &ScAddress, arg: bool) -> InvocationSpec {
        InvocationSpec::new(
            contract,
            "register",
            vec![ScVal::Bool(arg)].try_into().unwrap(),
        )
        .unwrap()
    }

    #[test]
    fn invocation_spec_args_round_trip_through_xdr() {
        let contract = CreateContractData::new([5; 32], &factory_wasm());
        let spec = invocation_spec(&contract.contract_address, true);
        let args = spec.args().unwrap();
        assert_eq!(args.as_slice(), &[ScVal::Bool(true)]);
        assert_eq!(spec.contract().unwrap(), contract.contract_address);
    }

    #[test]
    fn signing_payload_is_deterministic_and_json_round_trips() {
        let contract = CreateContractData::new([3; 32], &factory_wasm());
        let invocation = invocation_spec(&contract.contract_address, true);
        let info = InfoState::new(
            "tx-1".to_string(),
            &default_ledger_info(),
            [0x42; 32],
            invocation,
            TEST_AUTH_PARAMS,
            [0x7; 32],
        );

        let first = info.signing_payload().unwrap();
        let second = info.signing_payload().unwrap();
        assert_eq!(first, second, "signing_payload must be deterministic");

        let json = serde_json::to_string(&info).unwrap();
        let round_tripped: InfoState = serde_json::from_str(&json).unwrap();
        assert_eq!(info, round_tripped);
        assert_eq!(first, round_tripped.signing_payload().unwrap());
    }

    #[test]
    fn signing_payload_changes_if_invocation_changes() {
        let contract = CreateContractData::new([4; 32], &factory_wasm());
        let ledger_info = default_ledger_info();
        let base = InfoState::new(
            "tx-1".to_string(),
            &ledger_info,
            [0x42; 32],
            invocation_spec(&contract.contract_address, true),
            TEST_AUTH_PARAMS,
            [0x7; 32],
        );
        let changed = InfoState::new(
            "tx-1".to_string(),
            &ledger_info,
            [0x42; 32],
            invocation_spec(&contract.contract_address, false),
            TEST_AUTH_PARAMS,
            [0x7; 32],
        );
        assert_ne!(
            base.signing_payload().unwrap(),
            changed.signing_payload().unwrap()
        );
    }

    #[test]
    fn signing_payload_changes_if_result_digest_changes() {
        let contract = CreateContractData::new([6; 32], &factory_wasm());
        let ledger_info = default_ledger_info();
        let invocation = invocation_spec(&contract.contract_address, true);
        let base = InfoState::new(
            "tx-1".to_string(),
            &ledger_info,
            [0x42; 32],
            invocation.clone(),
            TEST_AUTH_PARAMS,
            [0x7; 32],
        );
        let tampered_digest = InfoState::new(
            "tx-1".to_string(),
            &ledger_info,
            [0x42; 32],
            invocation,
            TEST_AUTH_PARAMS,
            [0x8; 32],
        );
        assert_ne!(
            base.signing_payload().unwrap(),
            tampered_digest.signing_payload().unwrap(),
            "a tampered result_digest must invalidate the signing payload"
        );
    }
}
