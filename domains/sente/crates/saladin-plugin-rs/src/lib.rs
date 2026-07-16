//! Thin Rust re-implementation of Paladin's `plugintk` gRPC handshake: dial the plugin manager,
//! open the `ConnectDomain` bidirectional stream, send `REGISTER`, then dispatch
//! `REQUEST_TO_PLUGIN` messages to a `DomainHandler` implementation and reply with
//! `RESPONSE_FROM_PLUGIN`/`ERROR_RESPONSE`, correlated by `message_id`. Mirrors
//! `toolkit/go/pkg/plugintk`'s `instance.go`/`plugin_base.go` handshake exactly, reusable for any
//! future Rust plugin (not just Sente).

use std::collections::HashMap;
use std::sync::{Arc, Mutex};

use tokio::sync::{mpsc, oneshot};
use tokio_stream::wrappers::ReceiverStream;
use tonic::transport::{Channel, Endpoint, Uri};
use tonic::Request;
use tower::service_fn;
use uuid::Uuid;

pub mod pb {
    tonic::include_proto!("io.kaleido.paladin.toolkit");
}

use pb::domain_message::{RequestFromDomain, ResponseToDomain};
use pb::header::{ErrorType, MessageType};
use pb::plugin_controller_client::PluginControllerClient;
use pb::{DomainMessage, Header};

type PendingReplies =
    Arc<Mutex<HashMap<String, oneshot::Sender<Result<ResponseToDomain, String>>>>>;

/// Lets `DomainHandler` implementations call back into Paladin core (`request_from_domain` in
/// `service.proto` - `FindAvailableStates`, `GetStatesByID`, etc.), correlated by `message_id` the
/// same way core's own `REQUEST_TO_PLUGIN` calls are correlated, just in the opposite direction.
/// Cloneable and cheap to clone (an `mpsc::Sender` + a shared pending-replies map) - handlers can
/// hold their own copy for the lifetime of the plugin.
#[derive(Clone)]
pub struct PaladinClient {
    plugin_id: String,
    to_core_tx: mpsc::Sender<DomainMessage>,
    pending: PendingReplies,
}

impl PaladinClient {
    pub async fn find_available_states(
        &self,
        req: pb::FindAvailableStatesRequest,
    ) -> Result<pb::FindAvailableStatesResponse, String> {
        match self
            .call(RequestFromDomain::FindAvailableStates(req))
            .await?
        {
            ResponseToDomain::FindAvailableStatesRes(res) => Ok(res),
            other => Err(format!("unexpected response_to_domain variant: {other:?}")),
        }
    }

    pub async fn get_states_by_id(
        &self,
        req: pb::GetStatesByIdRequest,
    ) -> Result<pb::GetStatesByIdResponse, String> {
        match self.call(RequestFromDomain::GetStatesById(req)).await? {
            ResponseToDomain::GetStatesByIdRes(res) => Ok(res),
            other => Err(format!("unexpected response_to_domain variant: {other:?}")),
        }
    }

    async fn call(&self, request: RequestFromDomain) -> Result<ResponseToDomain, String> {
        let message_id = Uuid::new_v4().to_string();
        let (tx, rx) = oneshot::channel();
        self.pending.lock().unwrap().insert(message_id.clone(), tx);

        let msg = DomainMessage {
            header: Some(Header {
                plugin_id: self.plugin_id.clone(),
                message_id: message_id.clone(),
                correlation_id: None,
                error_message: None,
                message_type: MessageType::RequestFromPlugin as i32,
                error_type: ErrorType::Unknown as i32,
            }),
            request_from_domain: Some(request),
            ..Default::default()
        };
        if self.to_core_tx.send(msg).await.is_err() {
            self.pending.lock().unwrap().remove(&message_id);
            return Err("failed to send request_from_domain: stream closed".to_string());
        }
        rx.await
            .map_err(|_| "core dropped the response channel".to_string())?
    }

    /// Test/harness-only: builds a `PaladinClient` wired to a fresh channel pair instead of a real
    /// `ConnectDomain` stream, returning the receiving end so a test can drive its own fake "core"
    /// loop - reading each outgoing `DomainMessage`'s `request_from_domain` and answering via
    /// [`resolve_test`](Self::resolve_test). Real production code always gets its `PaladinClient`
    /// from [`run`]'s `build_handler` callback instead; this exists so a `DomainHandler`
    /// implementation's own logic (e.g. Sente's `assemble_transaction`/`endorse_transaction`) can
    /// be exercised as a genuinely separate OS process (see `domains/sente/crates/sente/tests/`)
    /// without needing a real Paladin core or gRPC connection.
    pub fn new_test(plugin_id: &str) -> (Self, mpsc::Receiver<DomainMessage>) {
        let (to_core_tx, to_core_rx) = mpsc::channel(16);
        (
            Self {
                plugin_id: plugin_id.to_string(),
                to_core_tx,
                pending: Arc::new(Mutex::new(HashMap::new())),
            },
            to_core_rx,
        )
    }

    /// Test/harness-only: resolves a pending `call()` by `message_id`, the same way `run()`'s own
    /// dispatch loop resolves a real `RESPONSE_TO_PLUGIN`/`ERROR_RESPONSE` reply - lets a fake
    /// "core" loop (driven from [`new_test`](Self::new_test)) answer calls without reimplementing
    /// the correlation bookkeeping `call()` already does.
    pub fn resolve_test(&self, message_id: &str, response: Result<ResponseToDomain, String>) {
        if let Some(tx) = self.pending.lock().unwrap().remove(message_id) {
            let _ = tx.send(response);
        }
    }
}

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

    async fn init_deploy(
        &self,
        _req: pb::InitDeployRequest,
    ) -> Result<pb::InitDeployResponse, String> {
        Err("init_deploy not implemented".to_string())
    }

    async fn prepare_deploy(
        &self,
        _req: pb::PrepareDeployRequest,
    ) -> Result<pb::PrepareDeployResponse, String> {
        Err("prepare_deploy not implemented".to_string())
    }

    async fn init_contract(
        &self,
        _req: pb::InitContractRequest,
    ) -> Result<pb::InitContractResponse, String> {
        Err("init_contract not implemented".to_string())
    }

    async fn init_transaction(
        &self,
        _req: pb::InitTransactionRequest,
    ) -> Result<pb::InitTransactionResponse, String> {
        Err("init_transaction not implemented".to_string())
    }

    async fn assemble_transaction(
        &self,
        _req: pb::AssembleTransactionRequest,
    ) -> Result<pb::AssembleTransactionResponse, String> {
        Err("assemble_transaction not implemented".to_string())
    }

    async fn endorse_transaction(
        &self,
        _req: pb::EndorseTransactionRequest,
    ) -> Result<pb::EndorseTransactionResponse, String> {
        Err("endorse_transaction not implemented".to_string())
    }

    async fn prepare_transaction(
        &self,
        _req: pb::PrepareTransactionRequest,
    ) -> Result<pb::PrepareTransactionResponse, String> {
        Err("prepare_transaction not implemented".to_string())
    }

    async fn handle_event_batch(
        &self,
        _req: pb::HandleEventBatchRequest,
    ) -> Result<pb::HandleEventBatchResponse, String> {
        Err("handle_event_batch not implemented".to_string())
    }

    async fn configure_privacy_group(
        &self,
        _req: pb::ConfigurePrivacyGroupRequest,
    ) -> Result<pb::ConfigurePrivacyGroupResponse, String> {
        Err("configure_privacy_group not implemented".to_string())
    }

    async fn init_privacy_group(
        &self,
        _req: pb::InitPrivacyGroupRequest,
    ) -> Result<pb::InitPrivacyGroupResponse, String> {
        Err("init_privacy_group not implemented".to_string())
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
///
/// `build_handler` is called once, after the REGISTER handshake frame is queued but before the
/// request loop starts, with a `PaladinClient` already wired to this connection - so a handler
/// that needs to call back into core (e.g. Sente's `AssembleTransaction` querying prior
/// `SenteEntry` states) can stash its own clone at construction time, instead of every
/// `DomainHandler` trait method needing a client parameter threaded through it.
pub async fn run<F>(grpc_target: &str, plugin_id: &str, build_handler: F) -> Result<(), String>
where
    F: FnOnce(PaladinClient) -> Arc<dyn DomainHandler>,
{
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

    let pending: PendingReplies = Arc::new(Mutex::new(HashMap::new()));
    let paladin_client = PaladinClient {
        plugin_id: plugin_id.to_string(),
        to_core_tx: to_core_tx.clone(),
        pending: pending.clone(),
    };
    let handler = build_handler(paladin_client);

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
        // RESPONSE_TO_PLUGIN and ERROR_RESPONSE are replies to our own `PaladinClient` calls
        // (REQUEST_FROM_PLUGIN) - correlate by `correlation_id` and resolve the matching pending
        // oneshot. An ERROR_RESPONSE whose correlation_id isn't in `pending` is a reply to some
        // other exchange (or a message type this loop doesn't otherwise expect) - dropped, not
        // treated as a protocol error, since the correlation map is the only ownership signal we
        // have for "is this ours to resolve".
        if header.message_type == MessageType::ResponseToPlugin as i32 {
            if let Some(correlation_id) = &header.correlation_id {
                if let Some(tx) = pending.lock().unwrap().remove(correlation_id) {
                    let _ = tx.send(msg.response_to_domain.ok_or_else(|| {
                        "RESPONSE_TO_PLUGIN with no response_to_domain set".to_string()
                    }));
                }
            }
            continue;
        }
        if header.message_type == MessageType::ErrorResponse as i32 {
            if let Some(correlation_id) = &header.correlation_id {
                if let Some(tx) = pending.lock().unwrap().remove(correlation_id) {
                    let _ = tx.send(Err(header.error_message.clone().unwrap_or_else(|| {
                        "core returned ERROR_RESPONSE with no message".to_string()
                    })));
                }
            }
            continue;
        }
        if header.message_type != MessageType::RequestToPlugin as i32 {
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
/// `response_from_domain` oneof variant. `ConfigureDomain`/`InitDomain` (Phase 0),
/// `InitContract`/`InitTransaction`/`AssembleTransaction`/`EndorseTransaction`/
/// `PrepareTransaction` (Phase 2/S2 - the chain needed for one private invoke to go from
/// submitted to endorsed and prepared), `InitDeploy`/`PrepareDeploy` (Phase 3/S3 - genesis: a
/// new privacy group's on-chain deploy, declarative verifier resolution the same
/// `required_verifiers`/`resolved_verifiers` shape `InitTransaction`/`AssembleTransaction` already
/// use), and `HandleEventBatch` (Phase 3/S3 - Go-side integration: turning a confirmed on-chain
/// `genesis`/`transition` event back into Paladin states and transaction completions) are wired -
/// every other request type errors cleanly rather than panicking, so a plugin fails loudly (and
/// correctly, via `ERROR_RESPONSE`) if core ever asks it to do more than it's built for yet.
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
        Some(RequestToDomain::InitDeploy(req)) => handler
            .init_deploy(req)
            .await
            .map(|res| Some(ResponseFromDomain::InitDeployRes(res))),
        Some(RequestToDomain::PrepareDeploy(req)) => handler
            .prepare_deploy(req)
            .await
            .map(|res| Some(ResponseFromDomain::PrepareDeployRes(res))),
        Some(RequestToDomain::InitContract(req)) => handler
            .init_contract(req)
            .await
            .map(|res| Some(ResponseFromDomain::InitContractRes(res))),
        Some(RequestToDomain::InitTransaction(req)) => handler
            .init_transaction(req)
            .await
            .map(|res| Some(ResponseFromDomain::InitTransactionRes(res))),
        Some(RequestToDomain::AssembleTransaction(req)) => handler
            .assemble_transaction(req)
            .await
            .map(|res| Some(ResponseFromDomain::AssembleTransactionRes(res))),
        Some(RequestToDomain::EndorseTransaction(req)) => handler
            .endorse_transaction(req)
            .await
            .map(|res| Some(ResponseFromDomain::EndorseTransactionRes(res))),
        Some(RequestToDomain::PrepareTransaction(req)) => handler
            .prepare_transaction(req)
            .await
            .map(|res| Some(ResponseFromDomain::PrepareTransactionRes(res))),
        Some(RequestToDomain::HandleEventBatch(req)) => handler
            .handle_event_batch(req)
            .await
            .map(|res| Some(ResponseFromDomain::HandleEventBatchRes(res))),
        Some(RequestToDomain::ConfigurePrivacyGroup(req)) => handler
            .configure_privacy_group(req)
            .await
            .map(|res| Some(ResponseFromDomain::ConfigurePrivacyGroupRes(res))),
        Some(RequestToDomain::InitPrivacyGroup(req)) => handler
            .init_privacy_group(req)
            .await
            .map(|res| Some(ResponseFromDomain::InitPrivacyGroupRes(res))),
        Some(other) => Err(format!("unhandled request_to_domain variant: {other:?}")),
        None => Err("request_to_domain not set".to_string()),
    }
}
