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
- ✅ **CLOSED (was stale).** ~~`delegate_lock`, `prepare_unlock`... are not implemented for
  Stellar~~ — both are fully implemented and proven live (see "how it works today" above:
  the 3-node harness runs the full `deploy→mint→transfer→lock→prepareUnlock→delegateLock`
  sequence against real public Testnet). `UnlockHashFromIDsV1`/`EncodeDelegateLock` on
  `stellarChainIO` are real, not stubs; only `UnlockHashFromIDsV0` remains a stub, and
  deliberately so — Stellar always uses the V1 variant, so V0 is dead code by design, not a gap.
  **The narrower claim that's still accurate**: `prepare_mint_unlock`/`prepare_burn_unlock` and the
  three create-lock variants (`createTransferLock`/`createMintLock`/`createBurnLock`) genuinely have
  no Stellar branch yet (no `ChainKind()`/Stellar handling anywhere in their own `handler_create_*_
  lock.go`/`handler_prepare_{mint,burn}_unlock.go` files) — that part of this gap remains open.
- **No CI/nightly job** exercises any of this against real public Testnet — every Testnet
  confirmation to date (SNoto or Sente) has been a manual, one-off run.
- ✅ **CLOSED (was stale).** ~~`buildEndorsePlan` hardcodes `ECDSA_SECP256K1`/`ETH_ADDRESS`~~ —
  fixed; it now calls `chainIO.SigningAlgorithm()`/`.VerifierType()` for both attestation requests
  it builds (`handlers.go`), matching this chapter's own later claim (below) that `buildEndorsePlan`
  "needs no chain-kind branching at all." This bullet had drifted out of sync with that claim in the
  same chapter — removed rather than left contradicting it.

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
on `stellarChainIO` are real `SALADIN_TYPED_DATA_V0` digests. `UnlockHashFromIDsV1`/
`EncodeDelegateLock` are real, used by `prepare_unlock`/`delegate_lock` (proven live, see "how it
works today" above) — only `UnlockHashFromIDsV0` stays a stub, and deliberately so: Stellar always
uses the V1 variant, so V0 is dead code by design, not an open gap. The create-lock variants
(`createTransferLock`/`createMintLock`/`createBurnLock`) genuinely have no Stellar implementation
yet (see "what's left," above). `unlock`'s own on-chain args are a genuinely
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
- ✅ **CLOSED. `SentePrivacyGroup` now has a real, content-addressed structural check alongside
  `root`, closing risk R21 (ch. 16 §16.1).** `root` still gives total ordering (a stale/replayed
  `old_root` — read from the contract's own storage, never taken as a parameter — recomputes its
  signed payload over the wrong root and fails signature verification), but it used to be the
  *only* on-chain check: `new_root` was just `sha256(old_root, tx_id[, invocation_digest])`, a hash
  chain with no way to independently verify a transition's claimed business-state effects were the
  *correct current version* of anything. That's now closed the same way Pente's own `_unspent`
  mapping works: `transition` takes two new parameters, `inputs`/`outputs: Vec<BytesN<32>>` —
  content-addressed commitment hashes (`domain::entry_commitment`, a self-contained SHA-256 over
  each touched `SenteEntry`'s own content — deliberately *not* the same identifier Paladin core
  assigns internally, since the Rust plugin toolkit has no synchronous state-ID-allocation callback
  the way Java/Go plugins do) — checked against a real, persistent `storage::Unspent` set: an input
  must currently be unspent (else `sente: input not available`) and gets deleted; an output must
  not already be unspent (else `sente: output already unspent`) and gets inserted. Empty for a
  root-only transition, so that path costs exactly what it costs today — only transactions
  carrying a real business invocation pay the new, `_unspent`-style cost. `assemble_transaction`
  computes these commitments from `TransitionStateChanges`; `endorse_transaction` independently
  recomputes and cross-checks them from its own re-execution before folding them into the signed
  payload (`crates/sente/src/domain.rs`, `crates/sente/src/info.rs`); the contract-level fix and its
  new tests (`transition_rejects_input_never_created`, `transition_rejects_double_spend_of_same_input`,
  `transition_rejects_output_already_unspent`) live in `soroban/contracts/sente/src/{lib,storage,test}.rs`.
  Full workspace test suites (Soroban contracts and the Rust plugin, including the cross-process
  `two_node_invoke`/`divergence` tests) pass unmodified in behavior for every existing root-only
  caller — this is a strictly additive on-chain check, not a breaking redesign of the happy path.
- **R21's cost spike — run, not just proposed** (`soroban/contracts/sente/src/bench_test.rs`,
  mirroring `szeto`'s own `batch_bench_test.rs`/R2's methodology: measure the real metered cost via
  `env.cost_estimate()`, don't estimate it). A steady-state `transition()` — spending N previously-
  created business entries and creating N new ones, 2-member group — measured via real Soroban
  invocation-resource metering:

  | N | CPU instructions | write_entries | headroom vs 600M instr | headroom vs 200 write_entries |
  |---|---|---|---|---|
  | 0 (root-only, today's existing behavior) | 938,079 | 1 | 99.8% | 99.5% |
  | 5 | 1,184,291 | 11 | 99.8% | 94.5% |
  | 10 | 1,572,460 | 21 | 99.7% | 89.5% |
  | 20 | 2,656,076 | 41 | 99.6% | 79.5% |
  | 50 | 8,209,452 | 101 | 98.6% | 49.5% |
  | 75 | 15,420,793 | 151 | 97.4% | 24.5% |
  | 99 | 24,394,013 | 199 | 95.9% | 0.5% |
  | 100 | 24,794,568 | 201 | 95.9% | **−0.5% (exceeds the limit)** |
  | 120 | 33,926,563 | 241 | 94.3% | −20.5% |

  Confirms the predicted shape exactly: `write_entries = 2N + 1` (one shared instance-storage write
  for the root advance, plus one persistent-entry delete per input and one insert per output — each
  its own real Soroban ledger key, no batching). **CPU instructions are never the binding
  constraint** (>94% headroom even at N=120) — matching R2's own finding for SZeto that
  `write_entries`, not instructions, is what actually bites first.

  **Correction (caught by a live mainnet check, not by this spike itself):** the 200-entry limit
  above is Stellar mainnet's real, current `tx_max_write_ledger_entries` network setting — **not**
  `soroban-sdk`'s own `testutils::cost_estimate::NetworkInvocationResourceLimits::mainnet()` helper,
  which hardcodes `write_entries: 50` and explicitly warns in its own doc comment that "this is not
  pulling the values dynamically." An earlier version of this spike (and R2's own write-up, ch. 16
  §16.1, unrelated to R21) used that stale SDK constant and understated the real safe capacity by
  roughly 4×. **N=99 touched business entries per transition is the measured-safe ceiling** against
  the real mainnet limit; N=100 already exceeds it. This is a live, validator-voted network
  parameter, not a fixed constant — re-check it against the real network before relying on these
  numbers, the same caveat the SDK's own docs give. A future production
  `MAX_SAFE_TOUCHED_ENTRIES`-style enforced cap (mirroring SZeto's own `MAX_SAFE_BATCH_OUTPUTS`,
  ch. 13 §13.3) is worth adding once Sente's business-invocation path sees real use — not urgent
  today, since every current caller (the repo
  demo included) is root-only and touches zero business entries.
- **Sente's private simulation runs against a frozen, fake `LedgerInfo`, not the real chain's
  current one — a distinct gap from S4's protocol-version dispatch work, tracked as risk R22
  (ch. 16 §16.1).** `assemble_transaction`/`endorse_transaction` (`domains/sente/crates/sente/src/
  domain.rs:1434`/`:1691` — the real production path every business invocation goes through, not
  test-only code) both call `soroban_env_host::e2e_testutils::default_ledger_info()`, a hardcoded
  SDK test fixture (`sequence_number: DEFAULT_LEDGER_SEQ`, `timestamp: 12345678` — literally that
  fixed number) returned identically on *every* invocation, regardless of when it's actually
  assembled in real wall-clock time. A privately-hosted contract's own `env.ledger().sequence()`/
  `.timestamp()` calls therefore never see real, live chain state — only the same frozen snapshot,
  always. This is independent of S4's "pin the correct protocol version" work (ch. 16 §16.1) — even
  with correct multi-version host dispatch, these specific fields would still be fake; the gap is
  in the *source* of the pinned value, not which host version interprets it. Confirmed concretely
  via the institutional repo demo (ch. 18): `agreeRepoTerms`'s own `maturityLedger` value
  (`getLatestLedger() + maturityDays*24*3600/5`) is computed once, stored in the repo-terms private
  state, and never referenced again anywhere — the "far leg matures the repo" narration is a
  purely human-paced UI pause (`pauseForDemo`, gated on a presenter pressing Enter, or a no-op in
  non-interactive mode), not an enforced condition. Even if repo-terms or a future Sente-hosted
  contract wanted to enforce "only execute once the real ledger reaches maturity," it structurally
  couldn't today — the ledger context any private Sente contract observes isn't real. Closing this
  needs `assemble_transaction`/`endorse_transaction` to derive `LedgerInfo` from the real, live
  chain at assembly time (the same live query Stellar's own `simulateTransaction` preflight already
  performs for ordinary transactions, ch. 12 §12.1) instead of the hardcoded fixture — the pinning
  mechanism itself (pin at assembly, verify identically at endorsement) is already correct; only the
  *source* of the value needs to change.


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

### Pente vs. Sente — common ground, key differences

Both are the same architectural pattern — embed the base ledger's real execution engine, run
privacy-group state as a UTXO chain of state snapshots, require unanimous member endorsement over
a transition hash — realized against two different execution models (account-based EVM vs.
ledger-entry-based Soroban):

| Aspect | Pente (EVM) | Sente (Soroban) |
|---|---|---|
| Embedded engine | Besu's EVM library, in-JVM (`evmrunner/EVMRunner.java`) | `soroban-env-host` via the `soroban-simulation` crate, in-process (`crates/sente-host`) — the same engine `simulateTransaction` itself runs on |
| Plugin language | Java | Rust — Paladin's first Rust plugin, via a thin reusable `saladin-plugin-rs` handshake crate reimplementing `plugintk`'s gRPC dial/register/dispatch loop |
| Loader path | Standard JVM plugin loader | The *exact same* JNA/`c-shared` loader path Go plugins use — zero core loader changes |
| State unit | One state per **account** (`PersistedAccount`: nonce, balance, code, full storage trie) | One state per **ledger entry** (`SenteEntry{contract_id, key_xdr, val_xdr, seq}`) — finer-grained than Pente's per-account snapshots |
| Read/write-set discovery | Hand-built: `AccountLoader`/`DynamicLoadWorldState` track which addresses were queried vs. committed, then `PenteTransaction.buildAssembledTransaction` classifies each into input/read/output | Native: `soroban-simulation`'s recording-mode footprint (`modified_entries`) *is* the input/read state list directly — no separate bookkeeping layer needed |
| Endorsement mechanism | Independent re-execution + **structural diff** of resulting account states (input/read/output hash equality) | Independent re-execution + **digest comparison** (`InfoState.result_digest`) — no diff needed since the per-entry state model already is the footprint a diff would reconstruct |
| Endorsement threshold | 100% of group members (M-of-N on the roadmap) | 100% of group members (same v1 restriction) |
| On-chain anchor | `PentePrivacyGroup.sol`: full per-account UTXO mapping — `transition` takes four hash arrays (inputs/reads/outputs/info) and an unspent-set check | `SentePrivacyGroup`: a single hash-chain `root` — `transition(new_root, external_calls, signatures)` reads `old_root` from its own storage (replay protection) rather than taking it as a parameter |
| Signature scheme verified on-chain | EIP-712 `Transition` hash, ECDSA/secp256k1 | `SALADIN_TYPED_DATA_V0("sente.Transition", …)`, ed25519 (native Soroban host function) |
| External calls | `PenteExternalCall(address, encodedCall)` **event**; the on-chain contract executes the event log's entries within the same base-ledger tx | `env.invoke_contract` called **directly** from within `transition` — no event-log indirection, since Soroban contracts can call each other natively |
| Genesis/factory | `PenteFactory.sol`, deployed per group | `SenteFactory`, deployed per group, salted by `sha256(members)` so independently-assembling members converge on the same address with no prior coordination |
| Delegated submission | `approveTransition`/`transitionWithApproval` | Same `InitDeploy`/`PrepareDeploy` genesis split as Pente; delegated transition submission not yet a separate proven path |
| Determinism inputs pinned into the transition | EVM version, base block, base block timestamp | `LedgerInfo{sequence, timestamp}`, PRNG seed (from the private tx ID), `protocol_version` (metering cost model) |
| On-chain structural verification | **Content-addressed**: `_unspent` mapping independently checks the *actual* referenced account-state hash is currently valid — catches a stale/wrong input regardless of endorser belief | **Content-addressed too, since R21's fix (ch. 16 §16.1)**: `root` still gives total ordering (replay/race rejected), and a real `Unspent` set now independently checks `inputs`/`outputs` commitment hashes the same way — empty (and free) for a root-only transition, populated only when a business invocation actually touches `SenteEntry` states |
| Membership | Fixed at genesis | Fixed at genesis (same as Pente v1) |
| Code distribution | Contract code is itself part of the account state (`PersistedAccount.code`), tracked as a Paladin state like any other field | **Not yet solved** — a Soroban invocation needs its target's `ContractCode` entry, but `SenteEntry` has no shape for one; `InvokeJson.code` carries the wasm bytes on every invocation today rather than as a tracked/distributed state |
| Plugin-contract shape | `ConfigureDomain`/`InitDomain`/`InitContract`/`InitTransaction`/`AssembleTransaction`/`EndorseTransaction`/`PrepareTransaction` | Identical method chain — same language-neutral gRPC plugin contract, proven by a hello-world Rust domain passing the standard conformance test |
| Chain-neutral group ops (`groupmgr`/`domainmgr`) | `pgroup_createGroup` + transitions | Same `pgroup_createGroup` + transitions — no domain-specific changes needed on the Go side |
| Maturity | Production reference implementation | S1–S3 done and proven live (3-node harness, real Testnet receipt); S4 hardening (determinism audit, protocol-upgrade drill, endorsement-divergence chaos) not started |

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
