//! Sente Phase 1's actual exit criterion: run the same recording-mode invocation, with every
//! determinism-sensitive input pinned (`LedgerInfo`, PRNG seed), across two genuinely separate OS
//! processes - not two threads or two identities within one process, which is exactly the gap
//! that left Pente's own test suite unable to exercise real cross-process endorsement divergence.
//! `assert_cmd` spawns the compiled `sente-host` binary as a real child process each time, so
//! `run_once()` below is never the same OS process twice.

use assert_cmd::Command;

fn run_once() -> String {
    let output = Command::cargo_bin("sente-host")
        .expect("sente-host binary must be built")
        .output()
        .expect("failed to spawn sente-host as a child process");
    assert!(
        output.status.success(),
        "sente-host exited non-zero: stderr={}",
        String::from_utf8_lossy(&output.stderr)
    );
    String::from_utf8(output.stdout)
        .expect("stdout must be valid UTF-8")
        .trim()
        .to_string()
}

#[test]
fn recording_mode_invocation_is_deterministic_across_processes() {
    let first = run_once();
    let second = run_once();
    let third = run_once();

    assert!(!first.is_empty(), "digest must not be empty");
    assert_eq!(
        first, second,
        "two independent process invocations of the same pinned inputs produced different digests"
    );
    assert_eq!(
        second, third,
        "a third independent process invocation diverged too"
    );
}
