//! Shared helpers for `two_node_invoke.rs`/`divergence.rs`: build fixed `TransactionSpecification`/
//! `AssembleTransactionRequest`/`EndorseTransactionRequest` values, and drive the `sente_step`
//! harness binary (each call a genuinely separate OS process - see that binary's own doc comment)
//! for the `assemble`/`endorse` steps of S3's real (root-only) group transition.

use assert_cmd::Command;
use base64::engine::general_purpose::STANDARD as BASE64;
use base64::Engine;
use prost::Message;
use saladin_plugin_rs::pb;

fn run_step(mode: &str, request_bytes: &[u8]) -> Vec<u8> {
    let output = Command::cargo_bin("sente_step")
        .expect("sente_step binary must be built")
        .arg(mode)
        .write_stdin(BASE64.encode(request_bytes))
        .output()
        .unwrap_or_else(|e| panic!("failed to spawn sente_step {mode}: {e}"));
    assert!(
        output.status.success(),
        "sente_step {mode} exited non-zero: stderr={}",
        String::from_utf8_lossy(&output.stderr)
    );
    BASE64
        .decode(
            String::from_utf8(output.stdout)
                .expect("stdout must be valid UTF-8")
                .trim(),
        )
        .expect("stdout must be valid base64")
}

pub fn fixed_transaction(transaction_id: &str) -> pb::TransactionSpecification {
    pb::TransactionSpecification {
        transaction_id: transaction_id.to_string(),
        from: "sender@node1".to_string(),
        contract_info: Some(pb::ContractInfo {
            contract_address: sente::fixture::contract_address(),
            contract_config_json: "{}".to_string(),
        }),
        base_block: 100,
        base_block_timestamp: 1_700_000_000,
        ..Default::default()
    }
}

/// Runs `assemble_transaction` in its own process ("node A").
pub fn assemble(transaction_id: &str) -> pb::AssembleTransactionResponse {
    let req = pb::AssembleTransactionRequest {
        state_query_context: "ctx-1".to_string(),
        transaction: Some(fixed_transaction(transaction_id)),
        resolved_verifiers: vec![],
    };
    let response_bytes = run_step("assemble", &req.encode_to_vec());
    pb::AssembleTransactionResponse::decode(response_bytes.as_slice())
        .expect("invalid AssembleTransactionResponse")
}

/// Builds the `EndorseTransactionRequest` "node B" would receive for the transaction node A just
/// assembled - callers may mutate the returned request's `info`/`inputs`/`reads` before calling
/// [`endorse`] to exercise a divergent endorsement.
pub fn endorse_request(
    transaction_id: &str,
    assembled: &pb::AssembledTransaction,
) -> pb::EndorseTransactionRequest {
    pb::EndorseTransactionRequest {
        state_query_context: "ctx-1".to_string(),
        endorsement_request: Some(pb::AttestationRequest {
            name: "endorsement".to_string(),
            attestation_type: pb::AttestationType::Endorse as i32,
            ..Default::default()
        }),
        endorsement_verifier: Some(pb::ResolvedVerifier {
            lookup: "endorser@node2".to_string(),
            algorithm: "eddsa:ed25519".to_string(),
            verifier_type: "stellar_address".to_string(),
            verifier: "GENDORSERPLACEHOLDER".to_string(),
        }),
        transaction: Some(fixed_transaction(transaction_id)),
        resolved_verifiers: vec![],
        // Mirrors what a real Paladin engine passes along: the exact prior state data
        // corresponding to `assembled.input_states`' ids (here, the same genesis fixture
        // `sente_step`'s own fake `find_available_states` served node A).
        inputs: vec![pb::EndorsableState {
            id: sente::fixture::GENESIS_STATE_ID.to_string(),
            schema_id: "SenteEntry".to_string(),
            state_data_json: serde_json::to_string(&sente::fixture::genesis_entry())
                .expect("fixture genesis entry must serialize"),
        }],
        reads: vec![],
        outputs: assembled
            .output_states
            .iter()
            .enumerate()
            .map(|(i, ns)| pb::EndorsableState {
                id: format!("output-{i}"),
                schema_id: ns.schema_id.clone(),
                state_data_json: ns.state_data_json.clone(),
            })
            .collect(),
        info: assembled
            .info_states
            .iter()
            .enumerate()
            .map(|(i, ns)| pb::EndorsableState {
                id: format!("info-{i}"),
                schema_id: ns.schema_id.clone(),
                state_data_json: ns.state_data_json.clone(),
            })
            .collect(),
        signatures: vec![],
    }
}

/// Runs `endorse_transaction` in its own process ("node B").
pub fn endorse(req: &pb::EndorseTransactionRequest) -> pb::EndorseTransactionResponse {
    let response_bytes = run_step("endorse", &req.encode_to_vec());
    pb::EndorseTransactionResponse::decode(response_bytes.as_slice())
        .expect("invalid EndorseTransactionResponse")
}
