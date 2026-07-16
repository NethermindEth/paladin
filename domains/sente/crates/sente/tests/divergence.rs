//! S2's exit criterion's other half: "endorsement divergence detected." Tampers the info state
//! node B receives (a corrupted `result_digest`, the value `endorse_transaction` compares its own
//! independent re-execution against - see `crate::info::InfoState`'s own doc comment for why this
//! digest, not a structural diff, is what endorsement compares) before node B endorses, in a
//! separate process from node A exactly as in `two_node_invoke.rs`, and asserts node B refuses to
//! endorse with an actionable reason instead of silently accepting.

mod support;

use sente::info::InfoState;

#[test]
fn tampered_result_digest_is_rejected_by_the_endorser() {
    let assembled_response = support::assemble("divergence-tx");
    let assembled = assembled_response
        .assembled_transaction
        .expect("assemble must succeed for this test to be meaningful");

    let mut endorse_req = support::endorse_request("divergence-tx", &assembled);

    let info_state = &mut endorse_req.info[0];
    let mut info: InfoState = serde_json::from_str(&info_state.state_data_json)
        .expect("assembler's info state must be valid JSON");
    info.result_digest[0] ^= 0xFF;
    info_state.state_data_json = serde_json::to_string(&info).unwrap();

    let endorsed = support::endorse(&endorse_req);

    assert_eq!(
        endorsed.endorsement_result,
        saladin_plugin_rs::pb::endorse_transaction_response::Result::Revert as i32,
        "a tampered result_digest must be rejected, not silently endorsed"
    );
    let reason = endorsed
        .revert_reason
        .expect("REVERT must carry an actionable revert_reason");
    assert!(
        reason.contains("digest mismatch"),
        "revert_reason should explain the divergence, got: {reason}"
    );
}
