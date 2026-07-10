# Chapter 11 — The Base Ledger Abstraction (BLI)

This chapter specifies the core refactor: making Paladin's engine chain-agnostic so a Stellar
backend (ch. 12) can sit beside the EVM one. It is written to be executable by a coding agent:
concrete packages, interfaces, proto messages, and migration steps.

> **Implementation status.** The `baseledger` package, the EVM `Client` implementation, the
> `ChainAddress` type, the proto v2 additions (§11.6), and the `ChainSubmitter` seam (§11.3) are
> implemented on the `saladin` branch. Chapter 12's foundational slice is also implemented, which
> pulled forward part of §11.4's manager migration: `DBPublicTxn.From`/`.To` (and the
> `InMemoryTxStateReadOnly` interface) are now `pldtypes.ChainAddress`, discovered as a genuine
> blocking requirement for a Stellar `ChainSubmitter` to resolve a real signing address from a
> persisted transaction record, not merely a nice-to-have. The boundary conversions back to the
> EVM-shaped `pldapi.PublicTx` API type (which remains `EthAddress`-typed, out of scope) are
> fallible and documented at each call site (`mapPersistedTransaction`, the submission-writer
> sequencer handoff, etc.). `orchestrator.signingAddress` and the nonce-allocation/balance-check
> cluster were deliberately left `EthAddress`-typed — not forced to change, consistent with
> channel-account pooling still being deferred (ch. 12 §12.2). Three design details evolved from
> this chapter's original text during implementation — noted inline where they occur (§11.3's
> `ChainSubmitter` signatures; §11.4's `ChainAddress` storage format; the manager migration just
> described). Not yet started: the `ledgerindexer` split (§11.3's `Ingestor`) and full
> `componentmgr` node boot for `type: stellar` (blocked on that same ledger indexer) — still
> scoped to their originally planned milestones (ch. 16).

## 11.1 Design principles

1. **One node, one chain profile (initially).** A node is configured for `evm` *or* `stellar`.
   This keeps the DB single-interpretation and the refactor bounded. The interop work (ch. 15)
   later pluralizes the config to a `baseLedgers` map — the BLI interfaces defined here are
   identical in both shapes, so nothing in this chapter is throwaway. (This is the deliberate
   reconciliation between the port plan and the interop plan; both chapters flag it.)
2. **Additive, upstreamable changes.** Existing EVM behavior — including JSON-RPC payload bytes —
   must not change. Every proto change is additive. Existing compiled EVM domains must run
   unmodified. This is what keeps the fork rebase-able on upstream Paladin.
3. **Attack order: protos → types → subsystems.** The plugin protos are the contract every
   domain compiles against (highest leverage); the address/type change is pervasive but
   mechanical; the three subsystems (client, indexer, submitter) are then refactored behind
   interfaces.

## 11.2 Package layout

```
core/go/pkg/baseledger/            # NEW: the BLI — interfaces + chain-neutral types
core/go/pkg/baseledger/evm/        # EVM implementation (wraps existing ethclient/blockindexer logic)
core/go/pkg/baseledger/stellar/    # Stellar implementation (ch. 12)
core/go/pkg/stellarclient/         # NEW: low-level stellar-rpc client (mirror of ethclient)
core/go/internal/ledgerindexer/    # refactor of blockindexer: chain-neutral core + per-chain ingestors
core/go/internal/publictxmgr/      # split: orchestration core + ChainSubmitter backends
```

Config (`config/pkg/pldconf`):

```yaml
baseLedger:
  type: stellar            # or "evm" (default — absent key means evm, preserving old configs)
  stellar:
    rpcURL: https://stellar-rpc.example
    networkPassphrase: "Public Global Stellar Network ; September 2015"
    horizonURL: https://horizon.example    # optional: deep-history backfill
  # evm: { ws: ..., http: ... }            # the existing EthClientConfig shape, nested
```

## 11.3 The interfaces

### `baseledger.Client` — replaces direct engine use of `ethclient.EthClient` (`core/go/pkg/ethclient/client.go:44`)

```go
package baseledger

type Client interface {
    Close()
    ChainInfo() ChainInfo
    // Read path
    Call(ctx context.Context, req *CallRequest) (*CallResult, error)              // eth_call / read-only simulateTransaction
    GetAccountInfo(ctx context.Context, addr pldtypes.ChainAddress) (*AccountInfo, error) // balance + nonce/sequence
    // Write path
    EstimateResources(ctx context.Context, tx *UnsignedChainTx) (*ResourceEstimate, error)
    BuildTransaction(ctx context.Context, tx *UnsignedChainTx, est *ResourceEstimate) (SignablePayload, error)
    Submit(ctx context.Context, raw SignedChainTx) (TxID, error)
    GetTransactionResult(ctx context.Context, id TxID) (*TxResult, error)
}

type ChainInfo struct {
    Kind              ChainKind // "evm" | "stellar"
    NetworkID         string    // evm: decimal chain ID; stellar: network passphrase
    EVMChainID        int64     // evm only (convenience for existing callers)
}

type UnsignedChainTx struct {
    From        pldtypes.ChainAddress
    To          *pldtypes.ChainAddress   // nil → deploy
    PayloadKind PayloadEncoding          // FUNCTION_CALL_DATA (ABI calldata) | XDR_INVOKE_CONTRACT_ARGS
    Payload     []byte
    Intent      json.RawMessage          // pre-encoding JSON, for receipts/observability
}

type ResourceEstimate struct {
    Gas        *uint64          // evm
    GasPricing *EVMGasPricing   // evm
    Soroban    *SorobanResources // stellar: SorobanTransactionData XDR (footprint+resources),
                                 // resource fee, auth entries XDR, restore-preamble requirement
}
```

Notes:

- **`TxID` stays `pldtypes.Bytes32`.** Transaction hashes are 32 bytes on both chains — a lucky
  break that preserves the `public_txns` schema, hash-based receipt correlation, and
  `GetPublicTransactionForHash` unchanged.
- `ResourceEstimate` is opaque to everything except the chain's own `ChainSubmitter`; the
  sequencer and txmgr never inspect it.

### `baseledger.Ingestor` — the chain-specific half of the split block indexer

The current `blockindexer` (`core/go/pkg/blockindexer/block_indexer.go:50`) splits:

- The **consumer side** — event streams, queries, `WaitForTransactionSuccess`, checkpointing,
  DB persistence — is already ~chain-neutral and moves to `core/go/internal/ledgerindexer`
  (tables renamed `indexed_blocks` → `indexed_ledgers` by migration; semantics unchanged).
- The **ingestion side** becomes:

```go
type Ingestor interface {
    // Ordered, FINAL ledger units from the checkpoint. EVM resolves re-orgs internally and
    // emits only at configured confirmation depth; Stellar emits every closed ledger (SCP finality).
    StreamLedgers(ctx context.Context, from LedgerCheckpoint) (<-chan *LedgerUnit, error)
    BackfillSource() BackfillCapability   // NONE | ARCHIVE (history-archives/Galexie or a future RPC/indexer-based backfill) | FULL (EVM node)
    TipHeight(ctx context.Context) (uint64, error)
}

type LedgerUnit struct {         // one EVM block / one Stellar ledger
    Sequence  uint64             // block number / ledger sequence — the universal ordering key
    Hash      pldtypes.Bytes32
    Timestamp pldtypes.Timestamp
    Txs       []*IndexedChainTx  // {TxID, From ChainAddress, Result, RevertData, TxIndex}
    Events    []*IndexedChainEvent
}

type IndexedChainEvent struct {
    Sequence, TxIndex, EventIndex int64
    Emitter  pldtypes.ChainAddress
    Selector pldtypes.Bytes32     // EVM: topic0. Stellar: derived symbol hash (ch. 12 §12.4)
    Topics   [][]byte             // raw topics (EVM topics[1:], Stellar SCVal XDR topics)
    Data     []byte
}
```

Event *decoding* (raw → JSON the domains consume) moves behind a per-chain `EventDecoder`
(EVM: `abi.ABI`; Stellar: SCVal-vs-contract-spec). Domains keep receiving the same
"decoded JSON + friendly signature" shape they get today.

### `publictxmgr.ChainSubmitter` — the seam inside the public tx manager

**Implemented.** The publictxmgr's orchestration — per-signer in-flight orchestrators, persisted
submission records, stage state machine, confirmation matching, balance/retry/metrics handling —
is ~80 % chain-neutral and stays untouched. The EVM-specific 20 % (nonce assignment, gas pricing
and signing, submission response classification, resubmit-with-higher-gas policy) is extracted
behind `ChainSubmitter` (`core/go/internal/publictxmgr/chain_submitter.go`), with an EVM
implementation in `core/go/internal/publictxmgr/evm_chain_submitter.go`:

```go
type PreparedSubmission struct {
    PublicTxnID     uint64
    RawTransaction  pldtypes.HexBytes
    TransactionHash *pldtypes.Bytes32
    GasPricing      *pldapi.PublicTxGasPricing
}

type SubmitResult struct {
    TxHash      *pldtypes.Bytes32
    Outcome     SubmissionOutcome
    ErrorReason string // EVM-specific today; chain-neutral string, not ethclient.ErrorReason
    Retry       bool   // true only when the caller's bounded submission retry should attempt again immediately
}

type ChainSubmitter interface {
    AssignOrderingKey(ctx context.Context, from pldtypes.ChainAddress) (uint64, error)
        // EVM: next nonce (baseLedger.GetAccountInfo). Stellar: next sequence number
        // (ch. 12 — channel accounts change the model)
    PrepareSubmission(ctx context.Context, ptx *DBPublicTxn, gasPricing *pldapi.PublicTxGasPricing) (*PreparedSubmission, error)
        // build + sign via KeyManager. Note: gasPricing is passed explicitly rather than read off
        // ptx, because the resolved-for-this-attempt gas price lives in in-memory orchestrator
        // state (mtx.CurrentGasPrice), not the persisted row — a signature detail settled during
        // implementation, per this book's own "final signatures at code review" caveat.
    Submit(ctx context.Context, ps *PreparedSubmission) (*SubmitResult, error)
    ActionOnStale(ctx context.Context, ptx *DBPublicTxn) (StaleAction, error)
        // EVM: always StaleActionRebuild (re-price, re-sign, re-submit) — matches the engine's
        // existing unconditional behavior on resubmit-interval expiry.
        // Stellar: fee-bump wrap OR re-simulate+rebuild; archived entries → restore preamble (ch. 12)
}
```

Design note on why `Submit` returns `*SubmitResult` rather than the originally-sketched
`(pldtypes.Bytes32, SubmissionOutcome, error)`: the caller's bounded retry loop (`submitTX` in
`transaction_submission.go`) needs to distinguish "stop retrying, an error occurred" from "retry
immediately" — a distinction that, for EVM, depends on classifying the underlying RPC error
(nonce-too-low, already-known, underpriced, reverted, or unrecognized). Folding that
chain-specific classification into a small result struct (rather than requiring the chain-neutral
`submitTX` wrapper to inspect `ethclient.ErrorReason` values itself) keeps the orchestration layer
genuinely chain-agnostic.

The orchestrator's `nonce` column persists as the opaque *ordering key* (uint64 on both chains).
One new nullable column: `restore_tx_hash` on `public_txns` (Stellar restore preamble, ch. 12;
migration `000036_public_txn_restore_hash` already applied).

## 11.4 The address problem

`pldtypes.EthAddress` ([20]byte, `sdk/go/pkg/pldtypes/eth_address.go`) appears in **~200+ Go
files**, GORM columns, JSON-RPC shapes, and domain-proto strings. Stellar needs 32-byte
addresses of two kinds (`G…` accounts, `C…` contracts).

| Option | Verdict |
|---|---|
| A. Widen everything to a universal 32-byte address (left-pad EVM) | ❌ Breaks every EVM JSON-RPC consumer; padding ambiguity; upstream would never accept |
| B. Parameterize managers with Go generics | ❌ Infects every interface in `components/`; kills upstream diffs; generics+GORM misery |
| **C. Opaque `pldtypes.ChainAddress`** | ✅ chosen — implemented as a **string**-tagged union, not the binary form originally sketched (below) |

**`ChainAddress` specification, as implemented** (`sdk/go/pkg/pldtypes/chain_address.go`):

```go
type ChainAddress struct {
    kind ChainAddressKind // e.g. "eth", "stellar_account", "stellar_contract"
    text string           // the native text rendering — see below
}
```

- **No binary/discriminator-byte encoding.** The originally-sketched design (1 discriminator
  byte + raw address bytes, `0x01`/`0x02`/`0x03` prefixes) was **not** built. Instead, the type
  is a kind tag plus the address's native text form.
- **Text form is the native rendering per kind** — `0x…` 40-hex for EVM (via `EthAddress.String()`),
  StrKey `G…`/`C…` passed through verbatim for Stellar. Parsing (`ParseChainAddressCtx`) is
  unambiguous by prefix (`0x`/40-hex-no-prefix → EVM; `G…` → Stellar account; `C…` → Stellar
  contract). **Existing EVM API payloads are byte-identical** — the property that makes this both
  backward-compatible and upstreamable — achieved here directly, without a binary layer.
- **Storage is TEXT, not BYTEA.** `Value()`/`Scan()` (implementing `driver.Valuer`/`sql.Scanner`)
  persist the hex string (without `0x` prefix, for EVM) or the raw StrKey (for Stellar) as plain
  text. **Consequently, no migration of existing EVM address columns is needed at all** —
  `public_txns.from`/`.to` and other address columns remain the `TEXT` type they always were;
  `ChainAddress` values round-trip through the same column type unchanged. (Verify this holds for
  every address column before relying on it in a new context: some tables may still declare a
  fixed-width `VARCHAR` — confirm `TEXT`/unbounded `VARCHAR` before assuming zero migration.)
- `EthAddress.ChainAddress()` and `EthAddressFromChainAddress()` converters exist; internal
  managers migrate to `ChainAddress` gradually (as of this writing, `ChainAddress` is used only at
  the `baseledger` boundary itself — the internal-manager migration below is still ahead of us).
- `pldtypes.Bytes32` (state IDs, tx hashes, schema hashes) is untouched.

**Why the simpler design won.** The binary/BYTEA scheme was written before implementation began;
during implementation, the TEXT-based design was chosen instead because it achieves the same
goal (byte-identical EVM API payloads, opaque cross-chain addressing) with less mechanical churn
— no address-column migrations, no binary/text conversion boundary to get wrong — at the cost of
a few extra bytes per stored address and losing the discriminator byte as a single-byte
fast-dispatch (kind is checked via the Go type's `kind` field instead, which is equivalent at
negligible cost). This book's plan and the shipped code diverged here; the plan is now corrected
to match reality rather than the reverse.

The internal-manager `EthAddress` → `ChainAddress` migration itself (the single largest mechanical
refactor of the port, ~2.5 em) has **not** started — it remains milestone M1's scope (ch. 16), with
an iron rule once it begins: **zero behavior change**, enforced by golden tests capturing EVM
JSON-RPC payloads byte-for-byte before/after (an initial golden-payload test already exists —
`core/go/internal/publictxmgr/golden_payload_test.go` — fixing `mapPersistedTransaction`'s JSON
shape as the first such regression guard; §11.8 criterion 2).

## 11.5 State schemas: deliberately unchanged

Paladin's state store types states with **ABI schemas** and hashes some of them with EIP-712.
This looks EVM-flavored but is actually *Paladin's own state-description language* — the chain
never sees a schema. Changing it would break every domain, the DB, and all hashing, for zero
on-chain benefit. **Decision: keep ABI-typed state schemas in Saladin v1.** Where a Soroban
contract must verify a state hash on-chain, the domain defines the hashing explicitly via
`SALADIN_TYPED_DATA_V0` (ch. 13), which is schema-driven but chain-neutral.

## 11.6 Domain plugin proto evolution (v2 — additive)

Current EVM-baked messages (`toolkit/proto/protos/`): `ConfigureDomainRequest.chain_id`;
`PreparedTransaction{function_abi_json, params_json, contract_address}` (to_domain.proto);
`BaseLedgerDeployTransaction{constructor_abi_json, bytecode}`; `OnChainEvent{signature(keccak),
block_number, log_index}` (on_chain_events.proto); `EncodingType{TUPLE, FUNCTION_CALL_DATA,
ETH_TRANSACTION, TYPED_DATA_V4, EVENT_DATA, ETH_TRANSACTION_SIGNED}` (from_domain.proto).

Additions (all fields optional/new; nothing removed or renumbered):

```protobuf
message ChainInfo {                       // added to ConfigureDomainRequest (chain_id still set for evm)
  string chain_kind = 1;                  // "evm" | "stellar"
  string network_id = 2;                  // evm: decimal chain id; stellar: network passphrase
  int64  evm_chain_id = 3;                // evm convenience
}

message PreparedChainTransaction {        // new sibling of PreparedTransaction (which is retained)
  enum TransactionType { PUBLIC = 0; PRIVATE = 1; }
  TransactionType type = 1;
  optional string required_signer = 2;
  oneof payload {
    EVMInvoke     evm = 10;               // exactly today's {function_abi_json, params_json, contract_address}
    SorobanInvoke soroban = 11;
  }
}
message SorobanInvoke {
  string contract_id = 1;                 // StrKey C...
  string function_name = 2;
  bytes  args_xdr = 3;                    // XDR-encoded Vec<SCVal>
  string args_json = 4;                   // JSON mirror for receipts/observability
  repeated bytes auth_entries_xdr = 5;    // pre-signed SorobanAuthorizedInvocation entries
  repeated bytes read_footprint_hints = 6;// ledger keys the domain knows will be read (optional)
}
message SorobanDeploy {                   // sibling of BaseLedgerDeployTransaction
  bytes  wasm = 1;                        // or:
  bytes  wasm_hash = 2;                   // pre-uploaded code
  bytes  salt = 3;                        // deterministic contract-ID derivation
  string constructor_fn = 4;
  bytes  constructor_args_xdr = 5;
}

message ChainEventLocation {              // neutral sibling of OnChainEventLocation
  string transaction_id = 1;              // 32-byte tx hash hex — both chains
  int64  ledger_sequence = 2;             // block number / ledger sequence
  int64  transaction_index = 3;
  int64  event_index = 4;                 // log_index / event index within tx
}
message ChainEvent {
  ChainEventLocation location = 1;
  string selector = 2;                    // topic0 hex (evm) / symbol-hash (stellar, ch. 12)
  string friendly_signature = 3;          // "Transfer(address,...)" / "snoto.transfer#v0"
  string data_json = 4;                   // decoded — same consumption model as today
}

// from_domain.proto EncodingType — new entries
//   XDR_SCVAL = 10;                 SCVal <-> JSON given a SEP-48 type definition
//   XDR_INVOKE_CONTRACT_ARGS = 11;  full InvokeContractArgs encode
//   SOROBAN_AUTH_ENTRY = 12;        HashIdPreimage(SOROBAN_AUTHORIZATION) encode-for-signing
//   SALADIN_TYPED_DATA_V0 = 13;     domain-separated structured hash (ch. 13)
```

**Compatibility contract:**

- For EVM nodes, both old and new fields are populated; old fields are empty on Stellar nodes.
- `ConfigureDomainResponse` gains `supported_chain_kinds`; the engine refuses (with a clear
  error) to load an EVM-only domain on a Stellar node.
- **Acceptance test:** the existing compiled Noto/Zeto/Pente plugin binaries run unmodified
  against a v2-proto engine on an EVM node — this is milestone M2's exit gate.

## 11.7 What is not touched

For emphasis — chain-agnostic and inherited as-is, apart from mechanical `ChainAddress`
adoption: **SequencerManager** (all state machines and the inter-node protocol),
**StateManager** (incl. nullifiers and DomainContext), **TransportManager** and the gRPC
transport plugin, **RegistryManager** and the plugin API, **GroupManager**, **IdentityResolver**,
**KeyManager core** (only new algorithm constants), and the bulk of **TXManager**.

## 11.8 Acceptance criteria (for a coding agent)

1. `git grep -l "ethclient.EthClient"` inside `core/go/internal` returns **zero hits** (note:
   `baseledger/evm/**` lives under `core/go/pkg`, not `core/go/internal`, so it is outside this
   grep's scope by construction — the criterion is about `core/go/internal` managers no longer
   holding a direct `ethclient.EthClient`-typed dependency, not a literal substring match against
   the package path). ✅ Met for `publictxmgr` (the `ChainSubmitter` extraction removed its direct
   `ethClient`/`ethClientFactory` fields). ⚠️ Known, documented exception: `txmgr` still holds
   `ethClientFactory` for one call site needing `EthClientWithKeyManager`'s ABI-encoding helpers
   (`transaction_submission.go:335`) — ABI encoding is intentionally kept EVM-specific and out of
   the BLI (§11.5), so this is deferred to milestone M1's broader migration, not silently ignored.
2. Golden-payload tests: recorded EVM JSON-RPC request/response fixtures replay byte-identically
   before/after M1+M2 (addresses, receipts, queries). An initial instance exists —
   `core/go/internal/publictxmgr/golden_payload_test.go` fixes `mapPersistedTransaction`'s JSON
   shape against `testdata/golden/public_tx.json`; this needs extending as the M1 migration
   proceeds (more shapes: receipts, queries, other managers' JSON payloads).
3. All 35 existing migrations apply cleanly on Postgres and SQLite (confirmed); **no
   `ChainAddress` migration is needed** — see §11.4's "storage is TEXT, not BYTEA" correction —
   existing EVM address columns are unmigrated and unaffected.
4. Existing domain plugin binaries (unrebuilt) pass the full EVM testbed
   (`domains/integration-test`) against the refactored engine.
5. `pldconf` accepts legacy configs (no `baseLedger.type`) as EVM without warnings. ✅ Met —
   `BaseLedgerConfig.ResolvedType()` defaults absent/empty `Type` to `evm`.
6. Upstream CI (Gradle build, Go tests, Solidity tests) is green on the refactor branch.

---

*Next: [Chapter 12 — The Stellar backend](12-stellar-backend.md)*
