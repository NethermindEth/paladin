//! S3's real-transition cross-process proof (`saladin-book/part-2-saladin/14-domain-ports.md`
//! §14.3): node A assembles a root-only group transition against the fixture group's genesis
//! state, node B independently re-derives and verifies it before endorsing. Each "node" is a
//! genuinely separate OS process (`support::assemble`/`support::endorse`, both spawning the
//! `sente_step` binary) - the same cross-process proof mechanism Phase 1's `tests/determinism.rs`
//! established, not two threads or identities simulated within one process.

mod support;

use saladin_plugin_rs::pb;

#[test]
fn node_b_endorses_node_as_assembled_invocation() {
    let assembled_response = support::assemble("two-node-invoke-tx");
    assert_eq!(
        assembled_response.assembly_result,
        pb::assemble_transaction_response::Result::Ok as i32,
        "node A failed to assemble: {:?}",
        assembled_response.revert_reason
    );
    let assembled = assembled_response
        .assembled_transaction
        .expect("OK assembly must include an assembled_transaction");
    assert_eq!(
        assembled.info_states.len(),
        1,
        "exactly one info state (the InfoState) is expected"
    );
    // A root-only transition always produces exactly one output SenteEntry - the group's own
    // instance value with `Root` spliced to the newly derived `new_root` (external_calls remain
    // unwired at the plugin level - see `domain.rs`'s own module doc comment).
    assert_eq!(
        assembled.output_states.len(),
        1,
        "a transition always advances the group's own root"
    );

    let endorse_req = support::endorse_request("two-node-invoke-tx", &assembled);
    let endorsed = support::endorse(&endorse_req);

    assert_eq!(
        endorsed.endorsement_result,
        pb::endorse_transaction_response::Result::Sign as i32,
        "node B failed to endorse: {:?}",
        endorsed.revert_reason
    );
    assert!(
        endorsed.payload.is_some(),
        "a SIGN result must carry a payload to sign"
    );
}
