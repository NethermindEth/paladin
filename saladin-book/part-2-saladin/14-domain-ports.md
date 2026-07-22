# Chapter 14 — Porting the Domains

## 14.1 Noto → SNoto (Go changes)

## How it works today

`domainmgr` accepts a `"stellar"` chain kind end-to-end (chain-neutral `ChainInfo` derivation, not
just a gate check); `DomainConfig` carries `max_input_states`/`max_output_states` (informational —
no Go-side enforcement yet); and `domains/noto` isolates its EVM-specific logic behind an internal
`chainIO` interface (`chainio_evm.go`/`chainio_stellar.go`). The Stellar implementer covers
**mint, transfer, lock, unlock, and cancelLock**, each producing a real
`PreparedChainTransaction.soroban`/`SorobanInvoke` with genuine XDR-encoded args for SNoto's actual
on-chain calls (chapter 13 §13.2). `domainmgr` trusts and registers `SaladinFactory.register`
events the same way it already does EVM's `PaladinRegisterSmartContract_V0`, and `PrepareDeploy`
has a real Stellar branch (`SNotoFactory.deploy`). The full submission pipeline downstream of the
domain plugin — `publictxmgr`'s orchestrator, chain-neutral `PublicTxInput`, sequencer/coordinator
dispatch — genuinely consumes a `SorobanInvoke` and gets it on-chain (ch. 11/12).

**Proven live, not just unit-tested**: a real 3-node Stellar harness
(`stellar_component_test.go`) runs the full
deploy→mint→transfer→lock→prepareUnlock→delegateLock sequence, plus a restart/resync drill,
against a genuinely cold-started chain — and against **real public Stellar Testnet** as well as
local quickstart (174s, fully confirmed on-chain, 2026-07-22).

**`cancelLock`/`cancel_unlock`** is implemented and unit-tested on both chains, closing what was
otherwise the last outstanding EVM-parity gap in the lock family.

## What's left for production use / full EVM parity

- **Real non-invoker Soroban authorization — the one underlying capability gap that blocks several
  operations at once.** SNoto's `cancel_unlock` and `unlock` (via `spendLock`) both call
  `lock.delegate.require_auth()` on-chain, and in Paladin's submission model the notary (not the
  party) always submits every transaction — so any lock consumption by its non-notary
  owner/delegate needs a genuine non-invoker `SorobanAuthorizationEntry`, which nothing in this
  repo constructs today. The same gap blocks `deposit`'s second-signer requirement. Until it
  exists, `cancelLock`/`unlock`'s Go+Rust wiring is provably correct (unit-tested against the exact
  on-chain args/digest shape) but their live end-to-end execution — and `deposit`/`withdraw`
  entirely — stay unexercised. `prepareUnlock`/`delegateLock` (which only ever *commit* a future
  spend/cancel path) are proven live.
- **`delegate_lock`, `prepare_unlock`/`prepare_mint_unlock`/`prepare_burn_unlock`, and the three
  create-lock variants** (`createTransferLock`/`createMintLock`/`createBurnLock`) are not
  implemented for Stellar — they need real `UnlockHashFromIDsV0`/`V1`/`EncodeDelegateLock`
  implementations on `stellarChainIO` (currently stubs), since these operations genuinely use the
  commit-reveal `SALADIN_TYPED_DATA_V0` scheme on-chain, unlike the base lock/unlock handlers.
- **No CI/nightly job** exercises any of this against real public Testnet — every Testnet
  confirmation to date (SNoto or Sente) has been a manual, one-off run.
- **`buildEndorsePlan` hardcodes `ECDSA_SECP256K1`/`ETH_ADDRESS`** for both attestation requests it
  builds, rather than asking `chainIO` for the right algorithm/verifier type — a latent
  inconsistency that hasn't bitten a test yet but would need fixing whenever that function's
  signature is next revisited.

**Transfer** spends real input coins (querying `owner.chainAddress.String()`, the chain-neutral
successor to the EVM-only `identityPair.address`). Not yet supported: nullifier-variant transfers
(`encodeRootAndSignature`'s proof wrapping stays EVM-ABI-shaped) and masked transfers
(`EncodeTransferMasked` is dead code, unrelated to the `useNullifiers` config switch); locked-coin/
hooks paths stay EVM/Pente-only, gated behind `NotaryModeHooks`.

**Lock/unlock** required one small on-chain contract change: SNoto's `lock` gained a sixth
`outputs: Vec<BytesN<32>>` parameter so a partial lock can spend `inputs` into both
`locked_outputs` *and* an ordinary remainder in one call, matching EVM Noto's own
`lock`/`createLock` shape. `NotoLockedCoin.Owner` and `NotoLockInfo`'s `Owner`/`Spender`/`Delegate`
are now `pldtypes.ChainAddress` (same pattern as `NotoCoin.Owner`), and `EncodeLock`/`EncodeUnlock`
on `stellarChainIO` are real `SALADIN_TYPED_DATA_V0` digests. `UnlockHashFromIDsV0`/`V1`/
`EncodeDelegateLock` stay stubs — base `unlock` never calls them, only `prepare_unlock`/the
create-lock variants do (see "what's left," above). `unlock`'s own on-chain args are a genuinely
different shape from EVM's `spendLock` (no separate lock-info state ref, no signature/proof slot at
all — the lock lives in native contract storage keyed by `lock_id`), handled by
`stellarBaseLedgerInvokeUnlock` filtering `req.InputStates` down to just the locked-coin state
before encoding.

**The `SaladinFactory.register` trust-consumer** mirrors EVM's own
`PaladinRegisterSmartContract_V0` flow, with two Stellar-specific design points: (1) **one
dedicated `factory` instance per domain, not one shared instance** — `domainmgr`'s trust model is a
strict 1:1 map keyed by each domain's `RegistryAddress`, and a cross-contract call into
`SaladinFactory.register` attributes the emitted event to `SaladinFactory`'s own address regardless
of which domain-specific factory called it, so sharing one `SaladinFactory` across domains would
let the second domain's registrations silently clobber the first's map entry; (2) **Stellar event
delivery is raw XDR, not ABI-decoded JSON** — `domainmgr` hand-decodes the known, fixed
`(Address, Bytes)` vec shape itself (`event_indexer_stellar.go`'s
`decodeSaladinFactoryRegistration`), since the Stellar ledger indexer deliberately leaves Soroban
event bodies undecoded at that layer (ch. 12 §12.5).

The Noto domain plugin (`domains/noto`) is mostly chain-independent business logic; the port is
a **chain-kind switch**, not a rewrite:

| Concern | Today (EVM) | Saladin |
|---|---|---|
| State hashing | EIP-712 over `NotoCoin` | `SALADIN_TYPED_DATA_V0` (via `EncodeData` v2) |
| Prepared tx | `PreparedTransaction{function_abi_json, contract_address}` | `PreparedChainTransaction.soroban{contract_id, function_name, args_xdr, args_json, auth_entries_xdr, read_footprint_hints}` |
| Notary approval | EIP-712 signature in calldata | pre-signed Soroban auth entry (preferred) or typed-data signature in `data` |
| Events | `Transfer` EVM log | `("transfer", tx_id)` contract event via neutral `ChainEvent` |
| Deploy | `NotoFactory` bytecode call | `SorobanDeploy{wasm, wasm_hash, salt, constructor_fn, constructor_args_xdr}` via SNotoFactory |
| Verifier type | `eth_address` | `stellar_address` |

(`Saladin` column fields per ch. 11's authoritative proto definitions, not abbreviated here.)

`chainKind` (from `ChainInfo`) selects the chain-specific implementation of a small internal
interface (`chainio_evm.go`/`chainio_stellar.go`) that covers both the transaction-preparation
methods and `states.go`'s state/message-hashing family. The signature-check method is
`VerifySignature` (payload, signature, expected verifier string), not EVM's recoverable
`RecoverSignature` — ed25519 signatures aren't recoverable, so verification needs the claimed
public key up front. `TransactionWrapper` and `buildEndorsePlan` need no chain-kind branching at
all — each handler's Stellar path is a sibling method `Prepare` dispatches to directly.
`NotoCoin.Owner` and `identityPair.chainAddress` use `pldtypes.ChainAddress` throughout, with zero
regression for EVM (the `owner` ABI parameter was already string-typed). Notary `hooks` mode stays
EVM-only until Sente ships — `hooks.go`'s Pente-private-invoke path has no Soroban equivalent yet.

Mint is transfer with empty inputs (confirmed by SNoto's own test suite) — `args_xdr` is
hand-built XDR-encoded `Vec<SCVal>`, with no need for the full spec-driven `scspec` package for
this known, fixed shape. SNoto's `transfer` needs no on-chain signature verification (the notary
authorizes via a native Soroban auth entry), but Paladin's own off-chain endorsement runs
unconditionally regardless of chain kind, so `EncodeTransferUnmasked` computes a real
`SALADIN_TYPED_DATA_V0` digest. `contract_id` (in `SorobanInvoke`) is currently derived from the
20-byte EVM-shaped `ParsedTransaction.ContractAddress` (zero-padded to 32 bytes) rather than a real
Stellar contract identity — generalizing that type is shared cross-domain work, out of scope here.
`auth_entries_xdr`/`read_footprint_hints` are left empty by design (Paladin's core signing/
submission pipeline's job, not the domain plugin's).

**Native-asset (SNoto-SAC) verbs:** for token instances configured with a backing classic asset
(ch. 13 §13.6), the domain adds `shield`/`unshield` handlers alongside `mint`/`burn`: `shield`
assembles the depositor's SAC-transfer auth entry plus notary-authorized outputs; `unshield` runs
the **trustline pre-flight** (ch. 12 §12.3 `CheckTrustline`) during `AssembleTransaction` and
rejects with an actionable error if the recipient lacks an authorized trustline. For regulated
assets the notary is the natural gatekeeper of `unshield` (approve only KYC'd recipients) —
mirroring the issuer's own `AUTH_REQUIRED` control, and strongest when notary and issuer are the
same organization. Issuer flags observed at shield time are recorded into the domain receipt.

**Acceptance: met.** One Noto binary passes both the EVM testbed and the Stellar testbed
(quickstart); the full 3-node deploy→mint→transfer→lock→prepareUnlock→delegateLock flow plus a
state-resync drill runs and confirms on real public Testnet.
The remaining gaps (non-invoker Soroban authorization, `delegate_lock`/`prepare_unlock`/create-lock
variants, no CI/nightly job) are listed above under "what's left."

## 14.2 Zeto → SZeto (Go changes)

## How it works today

Not started. `domains/zeto` mirrors `domains/noto`'s pre-port state exactly: the same
`PreparedTransaction`/EVM-typed pattern throughout, zero chain-kind or Soroban awareness. No
separate blocker beyond §14.1's: the chain-kind gate and `chainio_evm`/`chainio_stellar` seam
already exist (proven by Noto's port), so Zeto's port follows the same structure.

## What's left for production use / full EVM parity

The whole port. Smaller than SNoto's was, because the cryptography is untouched:

- Prover, witness generation (wasmer), BabyJubJub signer registration
  (`domain:zeto:snark:babyjubjub`), nullifier computation, SMT bookkeeping: **unchanged**.
- **`deposit`/`withdraw` become the native-asset gateway**: the circuits already exist (they
  wrap ERC-20 on EVM); on Saladin the same handlers target the SZeto contract's pooled SAC
  balance (ch. 13 §13.6). `withdraw` gets the same assemble-time trustline pre-flight as
  SNoto's `unshield`.
- Chain-kind switch mirrors Noto's: prepared transactions become `SorobanInvoke` with the proof
  in `args_xdr`; events consumed via `ChainEvent`; deploys via SZetoFactory.
- Batch-size limits from the M0 resource benchmarks constrain `AssembleTransaction` coin
  selection (the assembler already selects coins; it learns per-chain caps).
- The KYC variant's registry SMT lives on-chain per Zeto's design — root-history pattern per
  ch. 13.

**Acceptance:** anonymous transfer + deposit/withdraw against SZeto on testnet with proofs from
the unchanged proving stack; nullifier double-spend rejection observed end-to-end.

## 14.3 Sente — private Soroban (the Pente analogue)

## How it works today

**Sente is Paladin's first Rust plugin.** `domains/sente/crates/sente` is a real `cdylib` domain
loading through the *exact same* JNA path Go's `c-shared` domains already use
(`PluginJNA`/`PluginCShared`), with zero changes to `core/go/internal/plugins` or the loader
itself, via a thin, reusable handshake crate (`domains/sente/crates/saladin-plugin-rs`) that
re-implements `plugintk`'s gRPC dial/register/dispatch loop directly against
`toolkit/proto/protos/service.proto` — confirming the plugin wire format really is pure proto3
with no Go/Java-specific coupling.

**`soroban-env-host`/`soroban-simulation` is embedded directly in the plugin**
(`domains/sente/crates/sente-host`), giving Sente the same recording-mode execution engine that
powers stellar-rpc's own `simulateTransaction` — pointed at the privacy group's own state instead
of the public ledger (see the design note below for why a remote RPC call can't substitute for
this). Every determinism-sensitive input (`LedgerInfo{protocol_version, sequence_number,
timestamp}`, the PRNG seed) is pinned per-transaction, and deterministic re-execution is proven
across three genuinely separate OS processes via SHA-256 digest equality over the invocation's
XDR-encoded outputs — not `Debug` output, so the proof is over the same wire format every other
part of this repo treats as canonical.

**`SenteEntry` is the state model**: one state per modified Soroban ledger entry
(`contract_id`/`key_xdr`/`val_xdr`/`durability`/`seq`), which maps directly onto
`soroban-simulation`'s own `modified_entries` output — no separate account-loader bookkeeping is
needed the way Pente needed for the EVM. `SenteDomain` implements a full
`ConfigureDomain`/`InitDomain`/`InitContract`/`InitTransaction`/`AssembleTransaction`/
`EndorseTransaction`/`PrepareTransaction` chain; `InfoState.result_digest` is the actual
endorsement mechanism (each endorser independently rebuilds the snapshot from its own copy of the
input states, re-executes, and compares digests — not a Pente-style structural diff, since the
per-ledger-entry state model already *is* the footprint a diff would otherwise reconstruct).
Endorsement is proven as a genuine cross-process proof at the Rust level
(`crates/sente/tests/{two_node_invoke,divergence}.rs`), including divergence rejection.

**The on-chain `SentePrivacyGroup` contract** (`soroban/contracts/sente`) anchors a single
hash-chain `root` rather than Pente's per-account UTXO mapping — the off-chain `SenteEntry` model
already carries the per-entry state, so the on-chain contract only needs to anchor a head.
`transition(new_root, external_calls, signatures)` verifies a 100% member ed25519-signature
threshold over `SALADIN_TYPED_DATA_V0("sente.Transition", {old_root, new_root, external_calls})`;
replay protection comes from reading `old_root` out of the contract's own storage rather than
taking it as a parameter. External calls execute atomically via `env.invoke_contract` — no
event-log indirection the way Pente's private-EVM-to-public-Solidity path needs.
`SenteFactory` deploys/initializes/registers a group in one atomic invocation, salted by
`sha256(members)` so independently-assembling members agree on a deployed address with no prior
coordination; genesis goes through the same `InitDeploy`/`PrepareDeploy` split Pente's own
`initDeploy`/`prepareDeploy` uses.

**Ordinary transitions can carry a real business invocation, not just a placeholder root
advance.** A transition's `function_params_json` may declare an `invoke` (`InvokeJson`) naming a
target contract/function/args; when present, `assemble_transaction` builds a `SnapshotSource` from
every `SenteEntry` currently tracked for the group, genuinely re-executes the call, and the real
write footprint becomes new/updated `SenteEntry` states with `new_root` folding in the invocation's
own result digest — a divergent re-execution is therefore genuinely detectable, not assumed.
Absent `invoke`, behavior is unchanged: only the group's Root entry advances. Transitions may also
carry `external_calls` (`ExternalCallJson`/`scval_json.rs`'s tagged-value `ScVal` encoder), proven
end to end against a real, separately-deployed SNoto instance — a Sente transition invoking a real
SNoto coin state's `keepalive` atomically alongside the root update.

**Go-side integration is real**: confirmed `genesis`/`transition` contract events become tracked
`SenteEntry` states and transaction completions via `handle_event_batch`; `groupmgr`/`domainmgr`
support chain-neutral group deployment (`pgroup_createGroup`) and transitions for Sente the same
way they do for Pente; multi-member groups (independent per-member endorsement, each with its own
group-scoped signing key) are proven, not just single-member ones.

**Proven live, not just unit-tested.** A real 3-node harness
(`TestSenteThreeNodeHarness.java` — three genuinely separate OS processes, real cross-process gRPC
transport, real peer discovery via the `static` registry plugin, one Sente group member per node)
runs a group genesis and transition end to end, confirmed against both local quickstart and real
public Stellar Testnet (73s, a real confirmed on-chain receipt with a real transaction hash and
block number). The external-SNoto-call variant, and multi-member endorsement, are proven via the
single-JVM `TestSenteRealTransition.java` harness, including a full stateful UTXO lifecycle
(spends, read-only dependencies, sequenced successors, manifest commitment in `SenteInfo`, and
restart-safe output recovery via the domain's own `FindStates` callback).

## What's left for production use / full EVM parity

- **S4 hardening — determinism audit, protocol-upgrade drill, endorsement-divergence chaos
  suite — has not started.** The protocol-upgrade drill's direction is scoped (mirroring how
  Stellar Core itself vendors multiple pinned `soroban-env-host` builds concurrently, dispatched by
  matching each ledger's `protocol_version`), but no code exists yet.
- **The 3-node harness proves group genesis and transitions (including business invocations); it
  does not yet combine that with the external-SNoto-call variant across three real node
  processes** — that variant is proven only in the single-JVM `TestSenteRealTransition` simulation
  today. Reusing the same mint/external-call code already written, just against the real
  `pgroup_sendTransaction` path instead of `testbed_invoke`, is the natural next step.
- **No CI/nightly job** exercises Sente's testnet demo (or the 3-node quickstart harness)
  automatically.
- **A Soroban invocation cannot execute without its target's own `ContractCode` entry present, and
  `SenteEntry` has no shape for one** (a code entry is identified by a bare wasm hash, not an
  owning `ScAddress` the way `SenteEntry.contract_id` assumes) — so `InvokeJson.code` carries the
  target's wasm bytes directly on every invocation, rather than code being tracked/distributed as
  its own Paladin state the way its data already is. Closing this — code-as-a-trackable-`SenteEntry`,
  or a real deploy-time code-distribution mechanism — is unsolved future work.
- **The general JSON→`ScVal` argument encoder covers a limited type set**
  (void/bool/u32/i32/u64/i64/symbol/string/bytes/contract-address/vec), not every `ScVal` variant
  (no `u128`/`i128`/`map`) — sufficient for today's exit criteria, not a general-purpose encoder.
- **Fix C from the coordinator split-brain analysis** (avoiding self-delegation in the first place,
  rather than recovering from it) is deliberately deferred — the existing recovery path handles the
  split-brain within budget when it occurs, so avoiding it entirely is lower priority.
- **Sente's deterministic `salt = sha256(members)` addressing** means repeated demo runs against the
  same (unreset) Testnet fixtures correctly fail with `Storage::ExistingValue` on the second and
  later attempts — this is the address-collision-avoidance scheme working as intended, not
  flakiness; a genuinely fresh run needs either fresh fixtures or a Testnet reset.


### What Pente does, translated

Pente embeds the base-ledger VM (Besu's EVM, in-JVM) in a domain plugin; the privacy group's
world state is a UTXO chain of account-state snapshots; the on-chain contract verifies unanimous
signatures over transition hashes. The Soroban translation:

- **Embed `soroban-env-host` via the `soroban-simulation` crate** — the actual Soroban
  execution environment, a Rust library *designed* for embedding: pluggable
  `Storage`/`SnapshotSource`, deterministic metered execution (a budget), controllable
  `LedgerInfo`. `soroban-simulation` (part of `stellar/rs-soroban-env`) is the exact library
  stellar-rpc's own `simulateTransaction` preflight is built on: it runs the host in
  **recording mode**, capturing the read/write footprint, resource consumption, and required
  auth payloads of an invocation over whatever `SnapshotSource` you give it. Sente gives it one
  backed by the privacy group's Paladin states. Architecturally friendlier to embed than
  Besu's EVM.
- **Sente is Paladin's first Rust plugin.** Rust produces C-shared libraries (`cdylib`)
  naturally, and the plugin contract is language-neutral gRPC (ch. 5). Work items: a small
  `saladin-plugin-rs` crate re-implementing the thin `plugintk` handshake (~2–4 weeks, reusable
  for any future Rust plugin), plus the domain itself in `domains/sente/`. **Fallback** (risk R12,
  ch. 16) if loading a Rust cdylib through the JVM loader fights: run Sente as a sidecar process
  speaking the same gRPC — verify against `core/go/internal/plugins` in an M0 spike.
- **State model:** the group's state is the set of Soroban ledger entries owned by the group's
  contracts, chunked into UTXO states `SenteEntry{contract_id, key_xdr, val_xdr, seq}` —
  per-*ledger-entry* granularity (finer than Pente's per-account states). Elegant consequence:
  **the recording-mode footprint is exactly the input/read state list** for
  `AssembleTransaction` — read/write-set discovery is a first-class native mechanism on
  Soroban, where Pente had to hand-build the equivalent bookkeeping around the Besu EVM
  (`AccountLoader`/`DynamicLoadWorldState` tracking which accounts an execution touched).
- **Determinism checklist** (all endorsers must re-execute identically):
  - Pin `LedgerInfo{sequence, timestamp}` from assembly time into the endorsed transition.
  - Seed the PRNG from the private transaction ID.
  - Pin `protocol_version` (and thus the metering cost model) into the transition hash;
    endorsers on a mismatched `soroban-env-host` version must refuse to endorse — making
    coordinated plugin upgrades an explicit ops requirement around Stellar protocol upgrades.
- **On-chain `SentePrivacyGroup` contract:** stores the hash-chain head of transitions;
  `transition(new_root, transition_hash, signatures: Vec<(BytesN<32>, BytesN<64>)>)` verifies
  **100 % of members' ed25519 signatures** (host function) over
  `SALADIN_TYPED_DATA_V0("sente.Transition", {old_root, new_root, external_calls_hash,
  protocol_version, …})`. Membership fixed at genesis (as Pente v1).
- **SenteExternalCall:** transitions may carry `external_calls: Vec<AtomOperation>`; the
  contract invokes them within the transition — atomic private-logic-triggers-public-effect,
  *simpler* than Pente's event-indirection (contracts call contracts directly on Soroban).
  Combined with SAtom delegation, this restores the Noto-hooks pattern (SNoto hooks in Sente)
  as a fast-follow.
- **Scope restrictions (v1):** only contracts deployed *into* the group run privately; no
  mid-execution calls to public contracts (use the external-call pattern); host features that
  cannot be virtualized identically (TTL interactions, live `prng` beyond the seeded one) are
  disallowed. Pente carries the same philosophy.

### Design note: why not remote `simulateTransaction`?

A natural question: Stellar already has native transaction simulation — could Sente just call
stellar-rpc's `simulateTransaction` to learn which states an invocation reads and writes,
instead of embedding an execution environment? No, for four reasons that are architectural, not
incidental:

1. **Wrong state.** The RPC endpoint executes against the *public ledger*. Sente's contracts
   and their storage exist only as privacy-group Paladin states — they are not deployed on the
   public network, and the RPC API has no mechanism to inject a substitute state snapshot (no
   analogue of `eth_call`'s state overrides). The simulation would simply fail to find the
   contract.
2. **Endorsement is local re-execution, not delegation.** Every group member must independently
   re-execute the transition with pinned `LedgerInfo`, PRNG seed, and protocol version, and get
   byte-identical results before signing. A remote node's simulation offers none of that
   control, and its output is not attributable — an endorsement must attest "I verified this",
   never "an RPC told me".
3. **Privacy.** Sending private function arguments and private contract code to RPC
   infrastructure — even self-hosted, and certainly shared — defeats the purpose of the domain.
4. **Determinism drift.** RPC simulation reflects whatever host version and network settings
   that node runs at that moment; endorsers hitting different nodes (or the same node at
   different times) could legitimately diverge.

The resolution is the reframe above: Sente **does** use native Stellar simulation — the very
same `soroban-simulation`/recording-mode engine that powers `simulateTransaction` — just
embedded in-process and pointed at the group's private `SnapshotSource` instead of the public
ledger. Same engine, different state source. Remote `simulateTransaction` remains exactly where
it belongs: in the public-transaction pipeline (ch. 12 §12.1), footprinting the *anchor*
transactions Sente eventually submits on-chain.

### Internal phasing (within M6, ~6 em total)

| Phase | Content | Exit |
|---|---|---|
| S1 (~1.5 em) | ✅ **Done.** Embed soroban-env-host; execute contracts against snapshots in tests | ✅ deterministic re-execution proven across two processes (`domains/sente/crates/sente-host`) |
| S2 (~2 em) | ✅ **Done, deliberately scoped down** (fixed `factory.wasm` `register` scenario, no general ABI arg encoding, no genesis/deploy — see above). Domain plugin: assemble/endorse with re-execution equality check (`SenteEntry`, `InfoState.result_digest`, `PaladinClient`, `SenteDomain`) | ✅ two-node private invoke, endorsement divergence detected (`crates/sente/tests/{two_node_invoke,divergence}.rs`, Rust-level cross-process, not a Java `Testbed`) |
| S3 (~1.5 em) | ✅ **Done.** Contracts, genesis deploy, transaction assemble/endorse/prepare (including real business invocations and external calls), Go event-indexing, and the stateful `SenteEntry` UTXO lifecycle are all implemented and proven live. | ✅ exit met: a real 3-node harness (`TestSenteThreeNodeHarness.java`) runs group genesis and transition end to end, confirmed on both local quickstart and real public Stellar Testnet with an on-chain receipt; the external-SNoto-call variant and multi-member endorsement are proven via the single-JVM harness. |
| S4 (~1+ em) | Hardening: determinism audit, protocol-upgrade drill, chaos. Protocol-upgrade drill direction (verified against `stellar/stellar-core` source, not yet built): Stellar Core itself vendors multiple exact-pinned `soroban-env-host` builds concurrently (currently seven, `p21`–`p27`, via Cargo package-renaming to distinct dependency names all mapping to `package = "soroban-env-host"`), dispatched at runtime by matching each ledger's `protocol_version` against a static table (`get_host_module_for_protocol`), dropped only once replay against them is confirmed bit-identical under the newer host — a rolling policy, not a fixed N/N−1 window. Sente's own S2 deliberately does *not* need this (see above); S4 would mirror the same shape — a small dispatch module in `sente-host` vendoring N pinned versions, selected by `InfoState.ledger_info.protocol_version` | endorsement-divergence chaos suite green |

## 14.4 Pente on Saladin?

Pente itself (private *EVM*) remains EVM-network-only: its base contracts and trust anchoring
have no meaning on Stellar. On dual-ledger nodes (ch. 15), Pente continues to run against the
EVM ledger unchanged. Migration of Pente-based apps to Sente means recompiling private Solidity
logic to Soroban contracts — a porting guide belongs to Sente's documentation, not this plan.

## 14.5 Acceptance criteria (chapter-level)

1. ✅ **Met, with known gaps.** One Noto binary passes both the EVM testbed and the Stellar testbed;
   the full 3-node deploy→mint→transfer→lock→prepareUnlock→delegateLock sequence, plus a
   restart/resync drill, confirms cleanly on-chain against both local quickstart and real public
   Stellar Testnet. `cancelLock`/`cancel_unlock` is Go+Rust-complete and unit-tested on both chains.
   What remains is a single underlying gap, real non-invoker Soroban authorization
   (`lock.delegate.require_auth()`, needed by `cancelLock`/`unlock`'s live execution and `deposit`'s
   second-signer requirement — see §14.1's "what's left" section) — until it exists,
   `cancelLock`/`unlock`'s live end-to-end execution and `deposit`/`withdraw` stay unexercised, and
   the create-lock variants remain unexercised through this same live flow.
2. ❌ **Not started.** One Zeto binary, both testbeds green; proofs byte-identical across chains
   for identical inputs (the proving stack must not fork).
3. ✅ **Met.** `saladin-plugin-rs` handshake conformance: a hello-world Rust domain loads via the
   standard loader path (the primary path, not the sidecar fallback — see §14.3's Phase 0) and
   completes `ConfigureDomain`/`InitDomain`, proven by
   `TestStartTestbedWithSenteHelloWorld.java` passing against a real plugin manager.
4. ✅ **Met for S1/S2/S3; S4 not started.** S1 (deterministic re-execution across separate
   processes, embedding `soroban-env-host`/`soroban-simulation`), S2 (domain plugin assemble/endorse
   with re-execution equality check), and S3 (`SentePrivacyGroup`/`SenteFactory` on-chain, real
   genesis deploy and `transition` assemble/endorse/prepare, `external_calls`, event-driven state
   confirmation, and the full stateful UTXO lifecycle) are all done and proven live: a real 3-node
   Paladin harness with one Sente member per node runs group genesis and a transition end to end,
   confirmed on both local quickstart and real public Stellar Testnet. S4 (hardening — determinism
   audit, protocol-upgrade drill, endorsement-divergence chaos suite) has not been started.

---

*Next: [Chapter 15 — Delivery Plan](15-delivery-plan.md)*
