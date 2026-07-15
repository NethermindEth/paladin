//! Thin Rust re-implementation of Paladin's `plugintk` gRPC handshake: dial the plugin manager,
//! open the `ConnectDomain` bidirectional stream, send `REGISTER`, then dispatch
//! `REQUEST_TO_PLUGIN` messages to a `DomainHandler` implementation and reply with
//! `RESPONSE_FROM_PLUGIN`/`ERROR_RESPONSE`, correlated by `message_id`. Mirrors
//! `toolkit/go/pkg/plugintk`'s `instance.go`/`plugin_base.go` handshake exactly, reusable for any
//! future Rust plugin (not just Sente).

use std::sync::Arc;

use tokio::sync::mpsc;
use tokio_stream::wrappers::ReceiverStream;
use tonic::transport::{Channel, Endpoint, Uri};
use tonic::Request;
use tower::service_fn;
use uuid::Uuid;

pub mod pb {
    tonic::include_proto!("io.kaleido.paladin.toolkit");
}

use pb::header::{ErrorType, MessageType};
use pb::plugin_controller_client::PluginControllerClient;
use pb::{DomainMessage, Header};

/// Implemented by the actual domain plugin logic; `saladin-plugin-rs` owns the gRPC/handshake
/// plumbing, this trait owns the business logic. Every method defaults to "not implemented" so a
/// hello-world plugin (or one still being built out) only needs to override what it actually
/// handles - mirroring how Go's `plugintk.DomainAPIBase` leaves unset `DomainAPIFunctions` fields
/// nil and errors cleanly if core ever calls one that isn't wired up.
#[async_trait::async_trait]
pub trait DomainHandler: Send + Sync + 'static {
    async fn configure_domain(
        &self,
        _req: pb::ConfigureDomainRequest,
    ) -> Result<pb::ConfigureDomainResponse, String> {
        Err("configure_domain not implemented".to_string())
    }

    async fn init_domain(
        &self,
        _req: pb::InitDomainRequest,
    ) -> Result<pb::InitDomainResponse, String> {
        Err("init_domain not implemented".to_string())
    }
}

/// Parses the `grpc_target` string Paladin's loader passes to `Run` - `"unix:<path>"` for a Unix
/// domain socket (the only form actually produced today, see `configlight/YamlConfig.java`'s
/// `getRuntimeInfo()`), or `"tcp:"`/`"tcp4:"`/`"tcp6:"` + `host:port` mirroring
/// `core/go/internal/plugins/plugin_manager.go`'s own `parseGRPCAddress` scheme prefixes - kept
/// for robustness even though only the `unix:` form is exercised by today's loader.
async fn dial(grpc_target: &str) -> Result<Channel, tonic::transport::Error> {
    if let Some(path) = grpc_target.strip_prefix("unix:") {
        let path = path.to_string();
        // The URI given to Endpoint is never actually used to open a connection - our connector
        // ignores it and always dials the fixed Unix socket path instead - but tonic still
        // requires a well-formed placeholder to build the Endpoint.
        Endpoint::try_from("http://sente.local")
            .expect("static placeholder URI is always valid")
            .connect_with_connector(service_fn(move |_: Uri| {
                let path = path.clone();
                async move {
                    let stream = tokio::net::UnixStream::connect(path).await?;
                    Ok::<_, std::io::Error>(hyper_util::rt::TokioIo::new(stream))
                }
            }))
            .await
    } else {
        let tcp_target = grpc_target
            .strip_prefix("tcp:")
            .or_else(|| grpc_target.strip_prefix("tcp4:"))
            .or_else(|| grpc_target.strip_prefix("tcp6:"))
            .unwrap_or(grpc_target);
        Endpoint::try_from(format!("http://{tcp_target}"))?
            .connect()
            .await
    }
}

/// Runs the plugin handshake and request/response loop to completion - blocks until the stream
/// ends (matching `plugintk`'s own `Run`, which the Java loader calls on a dedicated background
/// thread specifically because it blocks: see `PluginJNA.loadAndStart`'s
/// `CompletableFuture.runAsync(..., Executors.newSingleThreadExecutor())`). Returns `Ok(())` on a
/// clean stream close, `Err` otherwise - the caller (a `cdylib`'s exported `Run`) is responsible
/// for converting that into the C-ABI return code and for panic-catching around this call.
pub async fn run(
    grpc_target: &str,
    plugin_id: &str,
    handler: Arc<dyn DomainHandler>,
) -> Result<(), String> {
    let channel = dial(grpc_target)
        .await
        .map_err(|e| format!("failed to dial {grpc_target}: {e}"))?;
    let mut client = PluginControllerClient::new(channel);

    let (to_core_tx, to_core_rx) = mpsc::channel::<DomainMessage>(16);

    // REGISTER must be the first frame on the stream (plugin_base.go's serve() drops anything
    // else sent before it).
    to_core_tx
        .send(DomainMessage {
            header: Some(Header {
                plugin_id: plugin_id.to_string(),
                message_id: Uuid::new_v4().to_string(),
                correlation_id: None,
                error_message: None,
                message_type: MessageType::Register as i32,
                error_type: ErrorType::Unknown as i32,
            }),
            ..Default::default()
        })
        .await
        .map_err(|e| format!("failed to queue REGISTER: {e}"))?;

    let outbound = ReceiverStream::new(to_core_rx);
    let response = client
        .connect_domain(Request::new(outbound))
        .await
        .map_err(|e| format!("ConnectDomain failed: {e}"))?;
    let mut inbound = response.into_inner();

    loop {
        let msg = match inbound.message().await {
            Ok(Some(msg)) => msg,
            Ok(None) => return Ok(()), // clean EOF
            // A stream error here is expected on ordinary manager-initiated shutdown (the
            // underlying transport closes while this loop is mid-`Recv`) - Go's own plugintk
            // client treats a plain "EOF" the same way and otherwise retries indefinitely via
            // `retry.NewRetryIndefinite`. Phase 0 doesn't need a real reconnect/retry loop (that's
            // genuine Sente work for a later phase, not this M0 spike) - treating every stream
            // termination as a clean exit is a deliberate Phase-0-only simplification, not an
            // attempt to distinguish "shutdown" from "genuine connection loss".
            Err(status) => {
                tracing::warn!(%status, "stream ended (treated as normal shutdown for Phase 0)");
                return Ok(());
            }
        };
        let Some(header) = msg.header.clone() else {
            tracing::warn!("received DomainMessage with no header, dropping");
            continue;
        };
        if header.message_type != MessageType::RequestToPlugin as i32 {
            // Only REQUEST_TO_PLUGIN is handled by this minimal loop today - RESPONSE_TO_PLUGIN/
            // ERROR_RESPONSE correlation for plugin-initiated callbacks (REQUEST_FROM_PLUGIN,
            // e.g. FindAvailableStates) is not needed by Phase 0's hello-world and is left for
            // whichever later phase first needs to call back into Paladin.
            continue;
        }

        let handler = handler.clone();
        let to_core_tx = to_core_tx.clone();
        tokio::spawn(async move {
            let result = dispatch(handler, msg.request_to_domain).await;
            let reply = match result {
                Ok(response_from_domain) => DomainMessage {
                    header: Some(Header {
                        plugin_id: header.plugin_id,
                        message_id: Uuid::new_v4().to_string(),
                        correlation_id: Some(header.message_id),
                        error_message: None,
                        message_type: MessageType::ResponseFromPlugin as i32,
                        error_type: ErrorType::Unknown as i32,
                    }),
                    response_from_domain,
                    ..Default::default()
                },
                Err(err) => DomainMessage {
                    header: Some(Header {
                        plugin_id: header.plugin_id,
                        message_id: Uuid::new_v4().to_string(),
                        correlation_id: Some(header.message_id),
                        error_message: Some(err),
                        message_type: MessageType::ErrorResponse as i32,
                        error_type: ErrorType::Unknown as i32,
                    }),
                    ..Default::default()
                },
            };
            let _ = to_core_tx.send(reply).await;
        });
    }
}

/// Dispatches one `request_to_domain` oneof variant to the handler, returning the matching
/// `response_from_domain` oneof variant. Only `ConfigureDomain`/`InitDomain` are wired for
/// Phase 0 - every other request type errors cleanly rather than panicking, so a hello-world
/// plugin fails loudly (and correctly, via `ERROR_RESPONSE`) if core ever asks it to do more than
/// it's built for yet.
async fn dispatch(
    handler: Arc<dyn DomainHandler>,
    request: Option<pb::domain_message::RequestToDomain>,
) -> Result<Option<pb::domain_message::ResponseFromDomain>, String> {
    use pb::domain_message::{RequestToDomain, ResponseFromDomain};
    match request {
        Some(RequestToDomain::ConfigureDomain(req)) => handler
            .configure_domain(req)
            .await
            .map(|res| Some(ResponseFromDomain::ConfigureDomainRes(res))),
        Some(RequestToDomain::InitDomain(req)) => handler
            .init_domain(req)
            .await
            .map(|res| Some(ResponseFromDomain::InitDomainRes(res))),
        Some(other) => Err(format!("unhandled request_to_domain variant: {other:?}")),
        None => Err("request_to_domain not set".to_string()),
    }
}
