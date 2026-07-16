//! S2's exit criterion (`saladin-book/part-2-saladin/14-domain-ports.md` §14.3 phasing table):
//! "two-node private invoke" - node A assembles a private Soroban invocation, node B
//! independently re-executes it and endorses. Each "node" is a genuinely separate OS process
//! (`support::assemble`/`support::endorse`, both spawning the `sente_step` binary) - the same
//! cross-process proof mechanism Phase 1's `tests/determinism.rs` already established, not two
//! threads or identities simulated within one process.

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
    // factory.wasm's register() is deliberately storage-free (see its own module doc comment and
    // register_has_no_persistent_storage_side_effects test) - chosen for Phase 1's spike, and
    // reused here as-is per S2's own scoping (saladin-book §14.3 S2 item 5), specifically because
    // it needs no auth setup. It produces zero SenteEntry outputs - this test's job is to prove
    // the cross-process assemble/endorse/digest-comparison mechanism itself, not SenteEntry
    // output-state creation. A follow-up scenario against a storage-mutating contract (proving a
    // second transaction consuming the first's output) is future work, not S2's exit criterion.
    assert_eq!(
        assembled.output_states.len(),
        0,
        "register() has no persistent storage side effects"
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
