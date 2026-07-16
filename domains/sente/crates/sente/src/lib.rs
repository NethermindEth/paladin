//! Sente's Rust domain plugin entrypoint. Phase 0 proved the `cdylib`-via-JNA loading path (see
//! `saladin-book/part-2-saladin/14-domain-ports.md` §14.3's phasing) with a trivial hello-world
//! `DomainHandler`. Phase 2 (S2) replaces it with `domain::SenteDomain`, a deliberately
//! scoped-down but real assemble/endorse/prepare implementation - see that module's own doc
//! comment for what's in/out of scope.

use std::collections::HashMap;
use std::ffi::{c_char, c_int, CStr};
use std::sync::{Arc, Mutex};

use saladin_plugin_rs::DomainHandler;
use tokio::sync::oneshot;

pub mod domain;
pub mod fixture;
pub mod info;
pub mod scval_json;

/// Keyed by plugin_id, mirroring toolkit/go/pkg/plugintk's `PluginLibraryEntrypoint.plugins` map -
/// lets `Stop` signal the matching `Run` call's loop to shut down cleanly.
static STOP_SIGNALS: Mutex<Option<HashMap<String, oneshot::Sender<()>>>> = Mutex::new(None);

fn register_stop_signal(plugin_id: String, tx: oneshot::Sender<()>) {
    let mut guard = STOP_SIGNALS.lock().unwrap();
    guard.get_or_insert_with(HashMap::new).insert(plugin_id, tx);
}

fn take_stop_signal(plugin_id: &str) -> Option<oneshot::Sender<()>> {
    let mut guard = STOP_SIGNALS.lock().unwrap();
    guard.as_mut().and_then(|m| m.remove(plugin_id))
}

/// # Safety
/// `grpc_target_ptr`/`plugin_id_ptr` must be non-null, null-terminated C strings valid for the
/// duration of this call - guaranteed by JNA's own `String` marshalling on the Java side (the same
/// contract `toolkit/go/domainstarter/domain_starter.go`'s `//export Run` already relies on).
#[no_mangle]
pub unsafe extern "C" fn Run(
    grpc_target_ptr: *const c_char,
    plugin_id_ptr: *const c_char,
) -> c_int {
    // A panic unwinding across this FFI boundary is undefined behavior (unlike Go, which recovers
    // panics into an rc=1 itself in plugintk's lib_entrypoint.go) - catch_unwind is mandatory here,
    // not an optional nicety.
    let result = std::panic::catch_unwind(|| {
        let grpc_target = unsafe { CStr::from_ptr(grpc_target_ptr) }
            .to_str()
            .map_err(|e| format!("invalid grpc_target: {e}"))?
            .to_string();
        let plugin_id = unsafe { CStr::from_ptr(plugin_id_ptr) }
            .to_str()
            .map_err(|e| format!("invalid plugin_id: {e}"))?
            .to_string();

        let _ = tracing_subscriber::fmt::try_init();
        tracing::info!(plugin_id = %plugin_id, grpc_target = %grpc_target, "Starting Sente plugin (Phase 2/S2)");

        let rt = tokio::runtime::Runtime::new()
            .map_err(|e| format!("failed to build tokio runtime: {e}"))?;
        rt.block_on(async {
            let (stop_tx, stop_rx) = oneshot::channel();
            register_stop_signal(plugin_id.clone(), stop_tx);

            tokio::select! {
                result = saladin_plugin_rs::run(&grpc_target, &plugin_id, |client| {
                    Arc::new(domain::SenteDomain::new(client)) as Arc<dyn DomainHandler>
                }) => result,
                _ = stop_rx => Ok(()),
            }
        })
    });

    match result {
        Ok(Ok(())) => 0,
        Ok(Err(err)) => {
            eprintln!("sente plugin error: {err}");
            1
        }
        Err(_) => {
            eprintln!("sente plugin panicked");
            1
        }
    }
}

/// # Safety
/// `plugin_id_ptr` must be a non-null, null-terminated C string valid for the duration of this
/// call.
#[no_mangle]
pub unsafe extern "C" fn Stop(plugin_id_ptr: *const c_char) {
    let Ok(plugin_id) = unsafe { CStr::from_ptr(plugin_id_ptr) }.to_str() else {
        return;
    };
    if let Some(tx) = take_stop_signal(plugin_id) {
        let _ = tx.send(());
    }
}
