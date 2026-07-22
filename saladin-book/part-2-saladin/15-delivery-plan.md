# Chapter 15 — Delivery Plan

## 15.1 Team assumptions

3–4 senior engineers: 2 Go (one with deep Paladin familiarity), 1–2 Rust/Soroban (at least one
experienced Stellar engineer as anchor — see risk R13, ch. 16), QA/infra support.
"em" = engineer-month.

## 15.2 Milestones (port) — remaining work and effort to deliver it

Every milestone below already works on the `saladin` branch — M0–M6 are demonstrated live, most
against real public Stellar Testnet (chapters 11–14 have the full technical detail behind each
gap). This table covers what's left: **Demo scope delivered?** is whether the milestone's own
original exit criteria were met (most were). **Gaps for production / EVM parity** is a separate,
larger bar — hardening and edge cases the milestone never promised — so "✅ delivered" and a
non-zero effort figure aren't a contradiction.

**Delivery track** answers a third question per gap, tagged inline (**[Agent]** / **[Human]**):
does closing it need a human to *originate* the design, or is it pattern-following work an agent
can execute against a real correctness oracle (a compiler, golden-payload tests, an already-proven
sibling port, an existing CR/dialog to mirror)? **[Human]** doesn't mean no AI assistance — it
means the design and sign-off must be a person's, because the gap is a novel security/consensus
mechanism, not a known pattern on new surface. **[Agent]** work still gets ordinary code review.
Effort figures are scope estimates, not agent-calendar predictions — **[Agent]** figures sit below
a pure-human estimate of the same gap because a strong oracle (e.g. the compiler enumerating every
`ChainAddress` call site) does the discovery/verification work for free; **[Human]** figures are
unchanged, since nothing can tell you a novel mechanism is *correct*, only that it compiles.

| # | Milestone | Demo scope delivered? | Gaps for production / EVM parity | Effort to close gaps | Delivery track |
|---|---|---|---|---|---|
| **M0** | Spikes & upstream RFC | ✅ Yes | None — benchmarks committed, upstream RFC posted, go/no-go decisions made. | 0 em | — |
| **M1** | `ChainAddress` + type widening | ◐ Partial — type/proto layer yes, internal-manager migration no | **[Agent]** Complete the `EthAddress`→`ChainAddress` sweep (ch. 11): compiler-guided for most of the surface (change the type, let `go build` enumerate every call site, keep golden-payload tests byte-identical); the nonce-allocation/balance-check cluster needs real reasoning too, since Stellar's sequence number isn't semantically a nonce despite compiling fine either way. | ~1.5 em | **Agent** ~1.5 em |
| **M2** | BLI + proto v2 + EVM behind BLI | ✅ Yes | **[Agent]** Finish the `ledgerindexer` split's consumer-facing side (event-stream/query/discovery, not just ingestion — ch. 12) by mirroring EVM's already-working implementation against the chain-neutral schema already in place. (The related `bidx_*` RPC gap is costed once, under M7/U1.) | ~1 em | **Agent** ~1 em |
| **M3** | Stellar backend | ✅ Yes | **[Human]** Fee-bump/auth-entry-expiry re-endorsement and retention-gap fail-loud/backfill (§12.2/§12.4, ~1.5 em) — replay-safety and data-loss semantics, no oracle for "is this safe" beyond tests written after the fact. **[Agent]** `SnapshotContractState` escape hatch, operator CR additions mirroring the EVM node CR, CI/nightly wiring of `testnet-demo.sh`, and throughput/chaos drill harnesses for scenarios ch. 12/16 already describe (~1.5 em). | ~3 em | **Mixed** — Agent ~1.5 em / Human ~1.5 em |
| **M4** | SNoto end-to-end | ✅ Yes — proven live on quickstart *and* real public Testnet (`TestStellarComponentTest`, 174s, 2026-07-22) | **[Human]** Real non-invoker Soroban authorization (`lock.delegate.require_auth()`, ~2 em) — a new mechanism gating live money-movement paths (`cancelLock`/`unlock`/`deposit`/`withdraw`); no compiler or sibling to check it against, so this doesn't compress like M1. **[Agent]**, once it lands: the lock-family variants that depend on it, `buildEndorsePlan`'s Stellar-aware signer branch, and a CI job (~1 em). | ~3 em | **Mixed** — Agent ~1 em / Human ~2 em |
| **M5** | SZeto + SAtom + native-asset gateway | ◐ Partial — SAtom and the native-asset (SAC) gateway yes, SZeto's own chain-kind port no | **[Agent]** The full SZeto port (~1.25 em) — SNoto's completed port is a line-by-line template, and the proving stack is untouched, so this is translation, not new design; SAtom's testnet demo (~0.25 em, have a human present for the real-Testnet-funds run as an operational safeguard); reproducible-Wasm tooling and `$specName` generalization (~0.5 em). | ~2 em | **Agent** ~2 em |
| **M6** | Sente | ✅ Yes — proven live via a real 3-node harness on quickstart *and* real public Testnet (`TestSenteThreeNodeHarness`, on-chain receipt confirmed, 2026-07-22) | **[Human]** S4 hardening (determinism audit, protocol-upgrade drill, chaos suite — designing adversarial scenarios needs a person, ~1 em) and the code-distribution mechanism for target-contract code not yet a trackable `SenteEntry` (unscoped, ~0.5 em). **[Agent]** wiring the already-proven external-SNoto-call code into the 3-node harness, plus a CI job (~1 em). Fix C (avoiding self-delegation rather than recovering from it) is deliberately deferred and not counted. | ~2.5 em | **Mixed** — Agent ~1 em / Human ~1.5 em |
| **M7** | Operator UI (`ui/client`) | ❌ Not started | **[Agent]** §15.6 phases U0–U5 in full — see that section for the per-phase breakdown. A dev/operator tool, not a consensus path; every phase mirrors an already-working EVM equivalent once U0 establishes a real test oracle. | ~2.5 em | **Agent** ~2.5 em |

**Remaining effort across M1–M7 ≈ 15.5 em**: ~10.5 em agent-suitable (M1, M2, M5, M7 in full;
mechanical slices of M3/M4/M6) and ~5 em needing human-originated design (M3's replay-safety
semantics; M4's non-invoker-authorization mechanism, the single highest-stakes item here and the
one figure that doesn't compress under agent execution; M6's S4 audit design and code-distribution
architecture). The human-led ~5 em gates calendar time — design-and-review-bound, roughly 1.5–2
months even fully parallelized across M3/M4/M6. The agent-suitable ~10.5 em is bottlenecked on
compiler/test/CI cycle time and review bandwidth, not agent throughput — realistically weeks when
run alongside the human-led work, not the months a purely human-calibrated total would suggest.
M0–M6 are all already demonstrable end to end; this is hardening and porting work on top of a
working system, not work required to reach a first working one.

## 15.3 Testing strategy

- **Unit:** soroban-sdk testutils for contracts; Go table tests + regenerated `mocks/` for BLI
  interfaces; shared cross-language typed-data/Poseidon vector files.
- **Component:** `stellar/quickstart` runs at the protocol version required by the checked-in
  Soroban contracts (currently protocol 27 in this branch), with accelerated ledgers for TTL tests;
  Stellar testbed configs sit beside the existing node component-test configs.
- **Integration:** 3-node docker-compose Saladin vs quickstart in CI; manual and then nightly runs
  against public Stellar Testnet must begin with `getNetwork` validation and a reset-aware fixture
  rebuild (Testnet resets wipe contracts, accounts, and history).
- **Chaos:** retention-gap drill (stop indexer > retention → loud failure → RPC/indexer/
  archive-based backfill, no Horizon); forced-archival drill; auth-entry-expiry → re-endorsement;
  sequencer coordinator kill/handover on Stellar timing.
- **CI:** new matrix axis `baseledger={evm,stellar}` for core tests; Rust toolchain +
  `stellar-cli` in build images; reproducible-Wasm check (pinned rustc, locked build profile);
  CI leg against the *next* protocol's preview quickstart image (risk R11).

## 15.4 Upstream engagement

The BLI RFC (M0) goes to Paladin maintainers with the M1/M2 refactor offered upstream: the
abstraction benefits Paladin regardless of Stellar (future Fabric/Solana/Corda backends), and
merged-upstream is the only durable defense against fork drift (risk R8/R9). Track upstream
monthly; measure rebase pain as a metric.

## 15.5 Decision log

| # | Decision | Chosen | Rejected (why) |
|---|---|---|---|
| 1 | Port strategy | abstraction layer, track upstream | hard fork (drift); translator sidecar node (two state views) |
| 2 | Address type | opaque var-length `ChainAddress`, native text per kind | 32-byte universal (breaks EVM API); generics (interface infection) |
| 3 | Tx ID | keep `Bytes32` hash | opaque string (needless churn; both chains 32B) |
| 4 | Proto evolution | additive v2 messages, oneof payloads | breaking v2 package (orphans every domain build) |
| 5 | State schema language | keep ABI-typed schemas | XDR/SEP-48 schemas (breaks store+hashing+domains for zero on-chain gain) |
| 6 | Submission parallelism | channel-account pool | single source account (serial); per-tx ephemeral only (funding overhead) |
| 7 | SNoto storage | persistent entry per state ID + TTL janitor | Merkle accumulator (global write contention) |
| 8 | EIP-712 analogue | SALADIN_TYPED_DATA_V0 (SHA-256 + canonical XDR); prefer native auth entries | keccak/EIP-712 on Soroban (alien, costly); raw signing (replay-unsafe) |
| 9 | Finality | trust SCP: final-only ingestion, confirmations=0 | configurable depth (cargo-cult latency) |
| 10 | Event history | continuous getLedgers ingestion + archive backfill, fail-loud | rely on getEvents retention (data loss); mandatory Galexie (too heavy for dev) |
| 11 | Sente engine | embed soroban-env-host in a Rust gRPC plugin (cdylib; sidecar fallback) | JNI-wrap in Java (pointless); re-implement Wasm metering (madness) |
| 12 | Zeto crypto | keep circom/Groth16/BN254/BabyJubJub/Poseidon; rewrite verifier only | new proof system (discards audited circuits; host fns exist) |
| 13 | Native-asset custody | pooled SAC contract balance held by the domain contract itself; shield/unshield verbs in SNoto/SZeto | separate gateway contract (extra trust boundary, split footprint); per-user wrapped balances (leaks holder set, defeats pooling privacy) |
| 14 | Sente read/write-set discovery | embed `soroban-simulation` (recording-mode host) over a Paladin-state `SnapshotSource` (ch. 14 §14.3) | remote stellar-rpc simulateTransaction (public-ledger state only, no injection API, non-deterministic across endorsers, leaks private payloads); hand-rolled read/write tracking (Pente-style — redundant, recording mode already does it) |

## 15.6 Operator UI (M7)

`ui/client` (React/MUI, single-page, talks to one node over JSON-RPC) is Paladin's existing
operator/dev UI — domain browsing and deploy, transaction/receipt inspection, privacy groups,
key manager, registry, private-state browser. **No milestone above touches it**: it was entirely
out of scope for M0–M6, and none of chapters 11–14 mention it. This section scopes what adapting
it for Stellar-backed domains (SNoto/SZeto/Sente) actually requires — surveyed read-only against
the current `saladin` branch, not yet built.

**The headline finding**: this isn't a uniform "a few formatting tweaks" job. The app splits
cleanly into two halves with very different readiness:

- **Private-transaction side (privacy groups, states, keys, registry, domain dialogs)** is already
  close to chain-neutral at the RPC level. The `ptx_*`/`pgroup_*`/`pstate_*`/`keymgr_*`/`reg_*`/
  `domain_*` JSON-RPC namespaces take/return string identifiers and opaque JSON, not EVM-typed
  params. The Noto/Zeto mint/transfer/burn dialogs (`dialogs/domains/{noto,zeto}/*.tsx`) already
  use free-text `from`/`to` fields resolved via the key manager, not raw address validation — most
  of that code is reusable near-verbatim for SNoto/SZeto/Sente dialogs.
- **Public-ledger side (Transactions/Events views, the app-wide 5-second auto-refresh)** is blocked
  on a **backend gap, not a UI gap**: `componentmgr/manager.go` only registers
  `cm.BlockIndexer().RPCModule()` for `type: evm` nodes today. A `type: stellar` node's own ledger
  indexer (`internal/ledgerindexer/stellar/indexer.go`) already writes into the same chain-neutral
  `indexed_blocks/indexed_transactions/indexed_events` tables (`pldapi.IndexedTransaction` etc.
  already carry parallel `FromChain`/`ToChain`/`ContractAddressChain` fields alongside the
  deprecated EVM-only ones — this groundwork already landed), but nothing exposes an equivalent
  `bidx_*`-shaped RPC surface for it, so the UI's Transactions/Events pages and its global refresh
  loop have nothing to call against a Stellar node. This is the same gap ch. 11's own "what's left"
  section flags.

**Concrete EVM-specific coupling found** (all in `ui/client/src`):

- `utils.ts`'s `isValidAddress`/`isValidTransactionHash`/`isValidPrivacyGroupId` are `0x[a-fA-F0-9]`
  regexes — a Soroban contract ID (`C...`) or Stellar account (`G...`) fails every one, silently
  disabling the domain-contract and privacy-group "Lookup" dialogs and breaking direct URL nav.
- `DomainDeploy.tsx`/`DomainButtons.tsx`/`SmartContractsTable.tsx` all `switch`/gate on the literal
  domain name (`'noto'`, `'zeto'`, `'pente'`) rather than a domain *type* — both files already carry
  a `// TODO: should key off of the domain "type" instead` comment. A `snoto`/`szeto`/`sente`
  domain renders with zero action buttons and zero domain-specific columns until new branches are
  added.
- `queries/privacyGroups.ts`'s `getPrivacyGroupById` hardcodes `'pente'` as the domain to query —
  a Sente group looked up by ID would query the wrong domain entirely.
- `views/Keys.tsx`'s address column hardcodes `eth_address` (`components/config.ts`'s
  `KEY_ETHEREUM_TYPE`/`KEY_ETHEREUM_ALGORITHM`) — a Stellar ed25519 verifier falls into the generic
  "Other Verifiers" column instead of getting its own.
- `EVMPrivateDetails.tsx` decodes private EVM calls/receipts via `ptx_decodeCall`/`ptx_decodeEvent`
  (EVM-ABI-only endpoints); there is no equivalent for Soroban invocations, so Sente receipts
  render as raw JSON until a decoder (backend RPC + component) exists.
- No test or Storybook coverage exists anywhere in `ui/client` today. That's the one caveat to
  treating M7 as this plan's strongest agent-driven candidate (§15.2): "mistakes surface as
  visibly broken UI" only substitutes for a real oracle if a human clicks through every changed
  flow. U0, below, closes that gap first.

**Internal phasing (~2.5 em total).** U0 lands first so U2/U3/U5 have a regression oracle before
touching broad mechanical surface area; U2/U3 can then run parallel to U1; U4/U5 depend on U1.
Every phase past U0 mirrors an already-working EVM equivalent (an RPC module, a dialog, a decoder,
fields the backend already emits) — strong oracles for agent-driven execution (§15.2):

| Phase | Content | Depends on |
|---|---|---|
| U0 (~0.25 em) | Baseline test/Storybook scaffolding for the components U2/U3/U5 touch — the regression oracle the rest of this milestone's agent-driven execution relies on, rather than a human eyeballing broken UI. | none |
| U1 (~0.5 em) | Backend prerequisite: register a chain-neutral ledger-query RPC surface for `type: stellar` nodes (extend `BlockIndexer().RPCModule()`'s registration, or give it an equivalent of its own) — the indexer already writes these chain-neutral rows (ch. 11), so this is wiring, not new design. | — |
| U2 (~0.25 em) | Mechanical frontend fixes: relax the address/hash/privacy-group-ID validators to accept Soroban/Stellar formats; stop hardcoding `'pente'` in `getPrivacyGroupById`; make the Keys address column detect the active chain's verifier type. | U0 (recommended) |
| U3 (~0.75 em) | Domain-aware UI: generalize the `noto`/`zeto`/`pente` string-switches in `DomainDeploy`/`DomainButtons`/`SmartContractsTable`; add SNoto/SZeto/Sente dialogs copied and adapted from the existing Noto/Zeto ones (already identity-string-based, largely mechanical) — the largest phase simply because there are three domains to cover. | U0 (recommended) |
| U4 (~0.5 em) | Soroban call/event decoder mirroring the existing `ptx_decodeCall`/`ptx_decodeEvent` EVM decoders, plus a `SorobanPrivateDetails` component alongside `EVMPrivateDetails` — the SCVal decode logic itself is the one genuinely new piece. | new backend decode endpoint |
| U5 (~0.25 em) | Ledger-browsing rework: adapt `interfaces.ts`'s EVM-flavored types and the components rendering them to consume chain-neutral fields the backend already emits (ch. 11). | U1, U0 (recommended) |

**Exit criteria:** an operator can deploy and interact with a Stellar-backed domain instance
(SNoto/SZeto/Sente) through the same UI used for EVM domains today — including browsing that
instance's transactions/events — without any `0x`-hex address assumption breaking a flow, with
U0's test/Storybook coverage actually exercising U2/U3/U5's mechanical-but-broad changes rather
than relying on manual click-through.

---

*Next: [Chapter 16 — Risk map](16-risk-map.md)*
