# Chapter 11 — The Base Ledger Abstraction (BLI)

This chapter specifies the core refactor: making Paladin's engine chain-agnostic so a Stellar
backend (ch. 12) can sit beside the EVM one. It is written to be executable by a coding agent:
concrete packages, interfaces, proto messages, and migration steps.

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
    BackfillSource() BackfillCapability   // NONE | ARCHIVE (Horizon/Galexie) | FULL (EVM node)
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
    Selector pldtypes.Bytes32     // EVM: topic0. Stellar: derived symbol hash (ch. 12 §12.3)
    Topics   [][]byte             // raw topics (EVM topics[1:], Stellar SCVal XDR topics)
    Data     []byte
}
```

Event *decoding* (raw → JSON the domains consume) moves behind a per-chain `EventDecoder`
(EVM: `abi.ABI`; Stellar: SCVal-vs-contract-spec). Domains keep receiving the same
"decoded JSON + friendly signature" shape they get today.

### `publictxmgr.ChainSubmitter` — the seam inside the public tx manager

The publictxmgr's orchestration — per-signer in-flight orchestrators, persisted submission
records, stage state machine, confirmation matching — is ~80 % chain-neutral and stays. The
EVM-specific 20 % (nonce assignment, gas pricing, RLP signing, resubmit-with-higher-gas) is
extracted:

```go
type ChainSubmitter interface {
    AssignOrderingKey(ctx context.Context, from pldtypes.ChainAddress) (uint64, error)
        // EVM: next nonce. Stellar: next sequence number (see ch. 12 — channel accounts change the model)
    PrepareSubmission(ctx context.Context, ptx *PersistedPubTx) (*PreparedSubmission, error)
        // estimate/simulate + build + sign via KeyManager
    Submit(ctx context.Context, ps *PreparedSubmission) (pldtypes.Bytes32, SubmissionOutcome, error)
    ActionOnStale(ctx context.Context, ptx *PersistedPubTx) (StaleAction, error)
        // EVM: re-price gas, resubmit same nonce
        // Stellar: fee-bump wrap OR re-simulate+rebuild; archived entries → restore preamble (ch. 12)
}
```

The orchestrator's `nonce` column persists as the opaque *ordering key* (uint64 on both chains).
One new nullable column: `restore_tx_hash` on `public_txns` (Stellar restore preamble, ch. 12).

## 11.4 The address problem

`pldtypes.EthAddress` ([20]byte, `sdk/go/pkg/pldtypes/eth_address.go`) appears in **~200+ Go
files**, GORM columns, JSON-RPC shapes, and domain-proto strings. Stellar needs 32-byte
addresses of two kinds (`G…` accounts, `C…` contracts).

| Option | Verdict |
|---|---|
| A. Widen everything to a universal 32-byte address (left-pad EVM) | ❌ Breaks every EVM JSON-RPC consumer; padding ambiguity; upstream would never accept |
| B. Parameterize managers with Go generics | ❌ Infects every interface in `components/`; kills upstream diffs; generics+GORM misery |
| **C. Opaque variable-length `pldtypes.ChainAddress`** | ✅ chosen |

**`ChainAddress` specification** (`sdk/go/pkg/pldtypes/chain_address.go`, new):

- **Binary form:** 1 discriminator byte + payload. `0x01` = EVM (20 bytes), `0x02` =
  Stellar account (32B ed25519), `0x03` = Stellar contract (32B), `0x04` reserved (muxed).
  DB address columns are `BYTEA` (already variable-length in Postgres); existing EVM rows are
  migrated by prefixing: `UPDATE t SET col = '\x01' || col` — one migration file per affected
  table (enumerate with `grep -rl EthAddress core/go/internal | xargs grep -l gorm`).
- **Text form (JSON-RPC, protos, domain strings): the native rendering per kind** — `0x…` 40-hex
  for EVM, StrKey `G…`/`C…` for Stellar. Parsing is unambiguous by prefix. **Therefore existing
  EVM API payloads are byte-identical** — the property that makes this both backward-compatible
  and upstreamable.
- `EthAddress` remains as a type with `ChainAddress()`/`FromChainAddress()` converters and a
  deprecation path; internal managers migrate to `ChainAddress`; the EVM backend converts at its
  boundary.
- `pldtypes.Bytes32` (state IDs, tx hashes, schema hashes) is untouched.

This is the single largest mechanical refactor of the port (~2.5 em) and gets its own milestone
(M1) with an iron rule: **zero behavior change**, enforced by golden tests capturing EVM
JSON-RPC payloads byte-for-byte before/after.

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

1. `git grep -l "ethclient.EthClient"` inside `core/go/internal` returns only
   `baseledger/evm/**` after the refactor (managers depend on `baseledger.Client`).
2. Golden-payload tests: recorded EVM JSON-RPC request/response fixtures replay byte-identically
   before/after M1+M2 (addresses, receipts, queries).
3. All 35 existing migrations + new `ChainAddress` migrations apply cleanly on Postgres and
   SQLite; migrated EVM rows round-trip to identical `0x…` text forms.
4. Existing domain plugin binaries (unrebuilt) pass the full EVM testbed
   (`domains/integration-test`) against the refactored engine.
5. `pldconf` accepts legacy configs (no `baseLedger.type`) as EVM without warnings.
6. Upstream CI (Gradle build, Go tests, Solidity tests) is green on the refactor branch.

---

*Next: [Chapter 12 — The Stellar backend](12-stellar-backend.md)*
