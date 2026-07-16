//! Sente's info state: the data the assembling node hands to endorsers so they can independently
//! re-derive and verify a proposed group transition (chapter 14 §14.3, S3's real-transition
//! mechanism, replacing S2's fixed-scenario `InvocationSpec`/`PinnedLedgerInfo` design entirely -
//! see `domain.rs`'s own module doc comment for why a real transition needs no pinned
//! `soroban-env-host` invocation at all for this phase's root-only scope).
//!
//! Signing mechanism: the standard `AttestationType.SIGN` round-trip (`to_domain.proto`), the same
//! mechanism Noto/Zeto already use for their own private-data signatures. `signing_payload()`
//! below is the sender's own off-chain integrity commitment (`AttestationRequest.payload` for the
//! "sender-signature" request); `on_chain_digest` is the *separate*, on-chain-verifiable
//! `SALADIN_TYPED_DATA_V0("sente.Transition", ...)` digest every group member's own ENDORSE
//! attestation actually signs (see `domain.rs::endorse_transaction`) - the two payloads are
//! deliberately different because only the latter is checked by `SentePrivacyGroup::transition`
//! on-chain; `signing_payload()` still covers `on_chain_digest` so a tampered digest invalidates
//! the sender's own signature too.

use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

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

/// Everything an endorser needs to independently re-derive and verify a proposed Sente group
/// transition. `contract_id` is base64 XDR `ScAddress` (same convention `SenteEntry::contract_id`
/// uses); `old_root`/`new_root` are the hash-chain head being advanced (chapter 14 §14.3's
/// on-chain contract has no UTXO set to diff, just this one head); `on_chain_digest` is the
/// `SALADIN_TYPED_DATA_V0("sente.Transition", {old_root, new_root, external_calls=[]})` digest -
/// what the assembler claims, and what every endorser recomputes and compares (see
/// `domain.rs::endorse_transaction`) before agreeing to sign it.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct InfoState {
    pub transaction_id: String,
    #[serde(rename = "contractId")]
    pub contract_id: String,
    #[serde(rename = "oldRoot", with = "hex::serde")]
    pub old_root: [u8; 32],
    #[serde(rename = "newRoot", with = "hex::serde")]
    pub new_root: [u8; 32],
    #[serde(rename = "onChainDigest", with = "hex::serde")]
    pub on_chain_digest: [u8; 32],
    /// JSON array of `domain::ExternalCallJson` (`"[]"` for a root-only transition with none) -
    /// carried here, not just implied by `on_chain_digest`, so `endorse_transaction`/
    /// `prepare_transaction` can independently re-derive the exact same `AtomOperation`s the
    /// assembler used, rather than trusting the digest alone.
    #[serde(rename = "externalCallsJson", default = "empty_external_calls_json")]
    pub external_calls_json: String,
    pub signature: Option<SenderSignature>,
}

fn empty_external_calls_json() -> String {
    "[]".to_string()
}

/// The ABI schema registered alongside `SENTE_ENTRY_ABI_SCHEMA_JSON` for `info_states` -
/// `InfoState` is a genuinely different shape (no `keyXdr`/`valXdr`/`durability`/`seq`), so reusing
/// SenteEntry's schema_id for it fails core's schema-driven state processing with "Input map
/// missing key 'keyXdr'" the first time an info state is actually round-tripped through core (as
/// opposed to just unit-tested against a mock). `signature` is deliberately omitted here: it's
/// always `None`/JSON `null` at the point `assemble_transaction` writes this state (see `new`'s own
/// doc comment - nothing in this crate ever populates it), and extra JSON keys not declared in the
/// schema are simply ignored by core's ABI-tuple parsing, so there's no need to model it.
pub const INFO_STATE_ABI_SCHEMA_JSON: &str = r#"{
  "name": "SenteInfo",
  "type": "tuple",
  "internalType": "struct SenteInfo",
  "components": [
    {"name": "transaction_id", "type": "string"},
    {"name": "contractId", "type": "string"},
    {"name": "oldRoot", "type": "string"},
    {"name": "newRoot", "type": "string"},
    {"name": "onChainDigest", "type": "string"},
    {"name": "externalCallsJson", "type": "string"}
  ]
}"#;

impl InfoState {
    pub fn new(
        transaction_id: String,
        contract_id: String,
        old_root: [u8; 32],
        new_root: [u8; 32],
        on_chain_digest: [u8; 32],
        external_calls_json: String,
    ) -> Self {
        Self {
            transaction_id,
            contract_id,
            old_root,
            new_root,
            on_chain_digest,
            external_calls_json,
            signature: None,
        }
    }

    /// The digest the sender's own `AttestationType.SIGN` request asks it to sign - Sente's
    /// off-chain integrity commitment (distinct from `on_chain_digest`, which endorsers sign
    /// instead - see the module doc comment for why they must differ). Covers `on_chain_digest`
    /// and `external_calls_json` too, so tampering with either after the sender signs invalidates
    /// that signature, the same reasoning `sente_host::digest`-based endorsement used in S2.
    pub fn signing_payload(&self) -> Result<[u8; 32]> {
        let canonical = serde_json::to_vec(&(
            &self.transaction_id,
            &self.contract_id,
            hex::encode(self.old_root),
            hex::encode(self.new_root),
            hex::encode(self.on_chain_digest),
            &self.external_calls_json,
        ))
        .context("failed to canonicalize info state for signing")?;
        Ok(Sha256::digest(canonical).into())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn info(old_root: [u8; 32], new_root: [u8; 32], digest: [u8; 32]) -> InfoState {
        InfoState::new(
            "tx-1".to_string(),
            "contract-1".to_string(),
            old_root,
            new_root,
            digest,
            "[]".to_string(),
        )
    }

    #[test]
    fn signing_payload_is_deterministic_and_json_round_trips() {
        let state = info([0u8; 32], [1u8; 32], [2u8; 32]);

        let first = state.signing_payload().unwrap();
        let second = state.signing_payload().unwrap();
        assert_eq!(first, second, "signing_payload must be deterministic");

        let json = serde_json::to_string(&state).unwrap();
        let round_tripped: InfoState = serde_json::from_str(&json).unwrap();
        assert_eq!(state, round_tripped);
        assert_eq!(first, round_tripped.signing_payload().unwrap());
    }

    #[test]
    fn signing_payload_changes_if_new_root_changes() {
        let base = info([0u8; 32], [1u8; 32], [2u8; 32]);
        let changed = info([0u8; 32], [9u8; 32], [2u8; 32]);
        assert_ne!(
            base.signing_payload().unwrap(),
            changed.signing_payload().unwrap()
        );
    }

    #[test]
    fn signing_payload_changes_if_on_chain_digest_changes() {
        let base = info([0u8; 32], [1u8; 32], [2u8; 32]);
        let tampered_digest = info([0u8; 32], [1u8; 32], [9u8; 32]);
        assert_ne!(
            base.signing_payload().unwrap(),
            tampered_digest.signing_payload().unwrap(),
            "a tampered on_chain_digest must invalidate the signing payload"
        );
    }

    #[test]
    fn signing_payload_changes_if_external_calls_json_changes() {
        let mut base = info([0u8; 32], [1u8; 32], [2u8; 32]);
        base.external_calls_json = "[]".to_string();
        let mut tampered = base.clone();
        tampered.external_calls_json =
            r#"[{"contract":"C123","function":"keepalive","args":[]}]"#.to_string();
        assert_ne!(
            base.signing_payload().unwrap(),
            tampered.signing_payload().unwrap(),
            "a tampered external_calls_json must invalidate the signing payload"
        );
    }
}
