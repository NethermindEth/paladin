use std::path::PathBuf;

fn main() {
    // toolkit/proto/protos is the single source of truth for the plugin<->core wire contract -
    // shared unmodified with the Go (plugintk) and Java (toolkit/java) plugin implementations.
    // Resolved relative to this crate's manifest dir, not the workspace root, so this build.rs
    // keeps working if domains/sente is ever relocated independently of the proto directory.
    let manifest_dir = PathBuf::from(env!("CARGO_MANIFEST_DIR"));
    let proto_dir = manifest_dir.join("../../../../toolkit/proto/protos");
    let proto_dir = proto_dir
        .canonicalize()
        .unwrap_or_else(|e| panic!("failed to resolve {}: {}", proto_dir.display(), e));

    tonic_prost_build::configure()
        .build_server(false) // this crate is a gRPC client only (dials the plugin manager)
        .build_client(true)
        .compile_protos(&[proto_dir.join("service.proto")], &[proto_dir])
        .unwrap_or_else(|e| panic!("failed to compile service.proto: {}", e));
}
