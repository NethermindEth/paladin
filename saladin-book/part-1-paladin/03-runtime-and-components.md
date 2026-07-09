# Chapter 3 — Runtime & Components

This chapter walks the Go engine (`core/go`) component by component. Every interface named here is
declared in `core/go/internal/components/` — one file per manager — which makes that directory the
best "map" of the engine.

## 3.1 Process bootstrap: JVM → Go via JNA

The runnable artifact is a Java process:

- `core/java/src/main/java/io/kaleido/paladin/Main.java` starts the JVM.
- `CoreJNA.java` uses **JNA** (Java Native Access) to `Native.load("core", …)` — loading the Go
  engine compiled as a C-shared library — and calls its exported
  `Run(socketAddress, loaderUUID, configFile, engineName)`.
- On the Go side, `core/go/core.go` exports `Run`/`Stop` via **cgo** (`//export Run`), delegating
  to `core/go/pkg/bootstrap/entrypoint.go`.
- The JVM also hosts the **plugin loader** (`core/java/.../loader/PluginLoader.java`), which the
  engine instructs over gRPC to load each configured plugin — Go plugins as C-shared libraries
  (`PluginJNA.java`), Java plugins as JARs (`PluginJAR.java`).

Why this shape? One process can then host Go *and* Java plugin code with a uniform lifecycle,
while the engine stays a pure-Go library. (Part 3 revisits whether this triple-runtime — JVM + Go
+ C ABI — is worth keeping.)

## 3.2 Component manager and lifecycle

`core/go/internal/componentmgr/manager.go` is the composition root. Every component implements the
lifecycle in `core/go/internal/components/components.go`:

```go
type ManagerLifecycle interface {   // components.go:61
    PreInit(PreInitComponents) (*ManagerInitResult, error)
    PostInit(AllComponents) error
    Start() error
    Stop()
}
```

- `PreInitComponents` (components.go:29) exposes the base infrastructure available before
  managers wire up: **KeyManager, EthClientFactory, Persistence, BlockIndexer, RPCServer,
  MetricsManager**.
- `ManagerInitResult` lets each manager contribute JSON-RPC modules and a block-indexer
  **pre-commit handler** (a hook invoked inside the DB transaction that commits a batch of
  indexed blocks — how receipts become atomic with indexing).
- Init order (manager.go:185-249): key → state → domain → transport → registry → rpcauth →
  plugin → publicTx → sequencer → tx → group → identityResolver.

## 3.3 The managers

### Transaction Manager (TXManager) — `components/txmgr.go:113`, impl `internal/txmgr/`

The user-facing API layer. Owns:

- Submission and validation of transactions (`ptx_sendTransaction`, `ptx_prepareTransaction`) —
  both *public* (plain EVM calls through Paladin) and *private* (routed to the sequencer).
- The **ABI store**: applications register contract ABIs once (`ptx_storeABI`); everything else
  references them by hash. **ABI** (Application Binary Interface) is Ethereum's JSON schema
  describing a contract's functions/events and how to encode their parameters.
- **Receipts** (`ptx_getTransactionReceipt`, `ptx_getDomainReceipt`, `ptx_getStateReceipt`) and
  **receipt listeners** / **blockchain event listeners** — durable server-side cursors that
  deliver receipts/events to applications over WebSocket with at-least-once semantics.
- Revert-reason decoding, idempotency keys, and **chained/dependent transactions**.

### Distributed Sequencer (SequencerManager) — `components/sequencermgr.go:68`, impl `internal/sequencer/`

The heart of cross-node coordination; chapter 4 is devoted to it. It orchestrates assembly,
endorsement, and dispatch of private transactions, running a **coordinator** role and an
**originator** role per smart contract (`internal/sequencer/coordinator/`,
`internal/sequencer/originator/` — both event-driven state machines).

### State Manager (StateManager) — `components/statemgr.go:31`, impl `internal/statemgr/`

Paladin's **UTXO state store**:

- States are immutable JSON documents typed by an **ABI-defined schema**
  (`Schema`, statemgr.go:241; registered by domains via `EnsureABISchemas`). The schema defines
  which fields are indexed for query and how the state is hashed to its on-chain ID.
- `DomainContext` (statemgr.go:108) is the crucial concurrency device: a per-contract, in-memory,
  locked view where a domain assembles transactions — `FindAvailableStates`,
  `UpsertStates`, `AddStateLocks` — accumulating **state locks** (reservations of states as
  spent/created by an in-flight transaction) that only hit the database on `Flush`. This is what
  lets many transactions be assembled optimistically before any of them confirms.
- **Nullifier** support for ZK domains (`FindAvailableNullifiers`, `UpsertNullifiers`): a
  nullifier is a one-way token published on-chain when a state is spent, unlinkable to the state
  itself — it prevents double-spending without revealing *which* state was spent.
- Distinguishes `WritePreVerifiedStates` (locally created, trusted) from `WriteReceivedStates`
  (arrived over the network — the domain re-validates the hash before storage).
- `WriteStateFinalizations` records the on-chain outcome: spent / confirmed / read / info.

### Domain Manager (DomainManager) — `components/domainmgr.go:39`, impl `internal/domainmgr/`

The engine-side boundary to domain plugins. Two interfaces:

- `Domain` (domainmgr.go:51) — domain-level operations (configure, deploy new contract instances,
  handle event batches).
- `DomainSmartContract` (domainmgr.go:84) — per-contract-instance transaction lifecycle:
  `InitTransaction`, `AssembleTransaction`, `WritePotentialStates`, `LockStates`,
  `EndorseTransaction`, `PrepareTransaction`, `InitCall`/`ExecCall`, plus privacy-group hooks
  (`WrapPrivacyGroupEVMTX`).

It also watches the chain for **contract discovery**: on-chain factories emit
`PaladinRegisterSmartContract_V0(txId, instance, config)` (declared in
`solidity/contracts/domains/interfaces/IPaladinContractRegistry.sol`); the domain manager indexes
these events to learn that "address X is an instance of domain Y with config Z".

### Key Manager (KeyManager) — `components/keymanager.go:40`, impl `internal/keymanager/`

- Resolves human-friendly hierarchical **key identifiers** (`bond.issuer.signer1`) to concrete
  keys in a **wallet** (an ordered ruleset mapping identifier patterns to signing backends), with
  **BIP-32 hierarchical-deterministic (HD) derivation** — one seed derives a tree of keys.
- Algorithm-agnostic by design: algorithms, verifier types, and payload types are open string
  sets (`toolkit/go/pkg/algorithms`: `ecdsa:secp256k1`; `toolkit/go/pkg/verifiers`:
  `eth_address`). Zeto registers `domain:zeto:snark:babyjubjub` — proof that non-EVM crypto slots
  in cleanly. A **verifier** is the public identity derived from a key (an Ethereum address, a
  BabyJubJub public key, a Stellar address in Part 2).
- Signing backends: in-memory (filesystem/static keystores, `toolkit/go/pkg/signer`) or remote
  **signing-module plugins** (HSM-friendly; protos `to_signing_module.proto`).
- Domains can register **in-memory signers** (`AddInMemorySigner`) — how Zeto's SNARK prover
  masquerades as a "signer" (signing = producing a proof).

### Identity Resolver — `components/identityresolver.go:23`, impl `internal/identityresolver/`

Resolves an **identity locator** (`alice@node2`) to a verifier of a requested type — locally via
the key manager, or by asking the remote node over the transport. This is how a transaction's
"to: bob@node3" becomes a concrete address/key for the domain to build states with.

### Group Manager (GroupManager) — `components/groupmgr.go:53`, impl `internal/groupmgr/`

**Privacy groups**: durable named groups of identities with a genesis state, used by Pente
(chapter 6) and by applications for private messaging (`pgroup_sendMessage`, message listeners).

### Registry Manager — `components/registrymgr.go:46` / Transport Manager — `components/transportmgr.go:89`

Covered in chapter 8. Registry: maps node names → transport endpoints (from registry plugins).
Transport: routes messages to components, with `Send` (best-effort) and `SendReliable`
(persisted, retried, acknowledged — used for state distribution and receipts).

### Public Transaction Manager — `components/publictxmgr.go:58`, impl `internal/publictxmgr/`

The base-ledger submission engine: assigns **nonces** (Ethereum's strictly-increasing per-account
transaction counters), estimates and prices **gas** (EVM's execution-metering fee unit; EIP-1559
dynamic fees), signs via the key manager, submits, tracks in-flight transactions per signing
address with an orchestrator state machine, resubmits with escalated fees when stuck, and matches
confirmed transactions reported by the block indexer (`MatchUpdateConfirmedTransactions`).

### Block Indexer — `core/go/pkg/blockindexer/block_indexer.go:50`

Ingests the chain: subscribes to new blocks (WebSocket `newHeads`), fetches receipts
(`eth_getBlockReceipts`), applies a configurable confirmation depth (re-org protection), persists
indexed blocks/transactions/events, and fans out **event streams** — ABI-filtered, checkpointed
event subscriptions consumed by the domain manager, txmgr, registry, and applications (`bidx_*`
RPCs). Delivery into managers can be transactional with the indexing commit (pre-commit handlers).

## 3.4 Persistence

- **GORM** (Go ORM) over **PostgreSQL** (production) or **SQLite** (dev/test);
  `core/go/pkg/persistence` abstracts the DB transaction (`DBTX`).
- Migrations in `core/go/db/migrations/{postgres,sqlite}` (35 numbered pairs at time of writing):
  `public_txns`, `schemas`, `states`, state-record tables, block-index tables, reliable messages,
  prepared transactions, privacy groups, etc.
- Everything critical is written transactionally with its trigger (e.g. receipts commit in the
  same DB transaction as the block batch that produced them).

## 3.5 The JSON-RPC surface

| Prefix | Module | Highlights |
|---|---|---|
| `ptx_*` | txmgr (`internal/txmgr/rpcmodule.go`) | sendTransaction(s), prepareTransaction(s), call, getTransactionReceipt, getDomainReceipt, getPreparedTransaction, storeABI, decodeCall/Event/Error, resolveVerifier, receipt & blockchain-event listeners |
| `pgroup_*` | groupmgr | createGroup, sendTransaction, call, sendMessage, message listeners |
| `keymgr_*` | keymanager | wallets, resolveKey, resolveEthAddress, reverseKeyLookup, sign |
| `reg_*` | registrymgr | registries, queryEntries, getEntryProperties |
| `domain_*` | domainmgr | listDomains, querySmartContracts, invokeRPC (domain-specific RPC) |
| `transport_*` | transportmgr | nodeName, peers, queryReliableMessages |
| `bidx_*` | blockindexer | block/tx/event queries, getConfirmedBlockHeight, decodeTransactionEvents |
| `debug_*` | sequencer | getTransactionStatus (private-tx state machine introspection) |

The **TypeScript SDK** (`sdk/typescript`) and **Go SDK** (`sdk/go`) wrap this surface; the web UI
(`ui/client`) consumes it too. This API is the de-facto compatibility contract: anything that
preserves it keeps every existing app, SDK, and the UI working — a fact both Part 2 and Part 3
lean on.

---

*Next: [Chapter 4 — The private transaction lifecycle](04-private-transaction-lifecycle.md)*
