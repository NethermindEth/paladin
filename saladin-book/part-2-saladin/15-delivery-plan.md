# Chapter 15 — Delivery Plan

## 15.1 Team assumptions

3–4 senior engineers: 2 Go (one with deep Paladin familiarity), 1–2 Rust/Soroban (at least one
experienced Stellar engineer as anchor — see risk R14), part-time Solidity for interop
contracts, QA/infra support. "em" = engineer-month.

## 15.2 Milestones (port)

| # | Milestone | Contents | Effort | Demo | Exit criteria |
|---|---|---|---|---|---|
| **M0** | Spikes & upstream RFC | Groth16-on-Soroban benchmark matrix (ch. 13 §13.3); circomlib-vs-host Poseidon vectors; soroban-env-host embed spike; Rust-cdylib-plugin loader spike; **BLI RFC posted to Paladin maintainers** | 1.5 em | Groth16 verify tx on testnet with measured costs | benchmark report committed; go/no-go on SZeto batch shape; upstream feedback collected |
| **M1** | `ChainAddress` + type widening | ch. 11 §11.4; DB migrations; **zero behavior change** | 2.5 em | existing full EVM test suite green on refactor | byte-identical EVM RPC payloads (golden tests) |
| **M2** | BLI + proto v2 + EVM behind BLI | ch. 11 §§11.2–11.3, 11.6; ethclient/blockindexer/publictxmgr refactored | 4 em | Paladin-on-Besu with **unmodified domain binaries** on the new core | upstream Gradle CI green; domain-binary compat test |
| **M3** | Stellar backend | ch. 12 complete: stellarclient, ed25519 signing, ingestor, submitter (channel accounts, fee-bump, restore), SaladinFactory, quickstart docker in testinfra | 4 em | raw Soroban invoke submitted & indexed via `ptx_` APIs on local quickstart | ch. 12 acceptance criteria 1–6 |
| **M4** | SNoto end-to-end | SNoto contract, Noto chain-kind switch, typed-data libs (3 impls + vectors), ttlJanitor | 3 em | private notarized transfer across 3 Saladin nodes on Stellar testnet | 3-node testnet CI job; state-resync drill; ch. 13/14 SNoto criteria — ⚠️ **local quickstart path proven, public-Testnet demo still manual/incomplete.** deploy/mint/transfer/lock/prepareUnlock/delegateLock + the state-resync drill are proven live against local quickstart (ch. 14 §14.1). Testnet config/friendbot/fixture-script overrides exist, and public Testnet was checked on 2026-07-21 (`getNetwork`: passphrase `Test SDF Network ; September 2015`, protocol `27`, friendbot `https://friendbot.stellar.org/`), and the fixture script/configs now cover `getNetwork` validation, persistent SQLite files, fixed ports, and reduced pools. A reset-aware `soroban/scripts/testnet-demo.sh` (reset detection, conditional redeploy, deployer funding, running both suites) and a consolidated resolve-and-fund identity helper now exist; both are unrun against real public Testnet from this environment, and no CI/nightly job exists yet. |
| **M5** | SZeto + SAtom + native-asset gateway | SZeto verifier + nullifiers; SAtom + factory; DvP SNoto⇄SZeto; SAC shield/unshield in SNoto & SZeto, `XDR_CLASSIC_OPS` + trustline tooling (ch. 12 §12.3, ch. 13 §13.6) | 4.5 em | anonymous ZK transfer; atomic DvP; shield→private→unshield of a classic asset on testnet | batch caps enforced from M0 numbers; failing-leg revert test; AUTH_REQUIRED asset flow green |
| **M6** | Sente | ch. 14 §14.3 phases S1–S4 | 6 em | private Soroban contract in a 3-member group + atomic external call to SNoto | ⚠️ S1/S2 and most S3 implementation are in place, including stateful UTXO lifecycle and restart-safe event confirmation. The external SNoto call now targets a real state ID (not `keepalive([])`), proven at the contract-test level and live via `Testbed` (with an open ~30s first-private-transaction dispatch-latency caveat, ch. 14 §14.3). Remaining demo work: run real Sente on three separate Paladin node processes, one member per node — `Testbed`'s own single-JVM harness cannot do this today (Go core has no non-Go plugin loader), so this needs new JVM-process-orchestration test infrastructure, not yet started. Sente `BuildReceipt` is wired; S4 hardening, determinism audit, protocol-upgrade drill, and chaos testing remain open. |
| **M7** | Operator UI (`ui/client`) | §15.6 phases U1–U5: chain-neutral ledger-query RPC surface, mechanical address/domain-lookup fixes, new domain dialogs (SNoto/SZeto/Sente), Soroban call/event decoder, ledger-browsing rework | ~3.5 em | operator deploys and interacts with a Stellar-backed domain instance in the same UI used for EVM today, including browsing its ledger transactions | new-domain dialogs + Soroban decoder pass component review; Transactions/Events views work against a `type: stellar` node |

**Port total ≈ 25.5 em** (M0–M6), ~9–12 months wall-clock (M0 ∥ M1; M3 overlaps M2 tail; M6 off
the MVP path). **M7 (≈3.5 em) is additive, not included in the port total** — it was out of scope
for every milestone above until this chapter, since none of M0–M6 touch `ui/client` at all (see
§15.6).

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
  loop have nothing to call against a Stellar node. This is the same gap noted in §11's status box.

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
- No test or Storybook coverage exists anywhere in `ui/client` today — no regression safety net for
  any of this, mechanical or not.

**Internal phasing (~3.5 em total, U2/U3 can proceed in parallel with U1; U4/U5 depend on U1):**

| Phase | Content | Depends on |
|---|---|---|
| U1 (~1 em) | Backend prerequisite: register a chain-neutral ledger-query RPC surface for `type: stellar` nodes (either extend `BlockIndexer().RPCModule()`'s registration to cover the Stellar ledger indexer, or give it an equivalent `RPCModule()` of its own) — this is `core/go`/M3 follow-up work, not `ui/client` work, but everything in U5 is blocked on it | — |
| U2 (~0.5 em) | Mechanical frontend fixes: relax/generalize the address/hash/privacy-group-ID validators to accept Soroban/Stellar formats; stop hardcoding `'pente'` in `getPrivacyGroupById`; make the Keys address column detect the active chain's verifier type instead of `eth_address` | none |
| U3 (~1 em) | Domain-aware UI: generalize the `noto`/`zeto`/`pente` string-switches in `DomainDeploy`/`DomainButtons`/`SmartContractsTable` to add `snoto`/`szeto`/`sente`; add parallel dialog folders under `dialogs/domains/` adapted from the existing Noto/Zeto ones (largely reuse — those dialogs are already identity-string-based, not address-based) | none |
| U4 (~0.5 em) | Soroban call/event decoder: a chain-neutral decode RPC (mirroring `ptx_decodeCall`/`ptx_decodeEvent` for Soroban args) plus a `SorobanPrivateDetails`-style component alongside `EVMPrivateDetails` | new backend decode endpoint |
| U5 (~0.5 em) | Ledger-browsing rework: once U1 lands, adapt `interfaces.ts`'s EVM-flavored `ITransaction`/`IEvent` types (`blockNumber`/`nonce`/`transactionIndex`) and the components that render them (`EnrichedTransaction.tsx`, `TransactionOverview.tsx`) to consume the already-existing chain-neutral fields and render chain-appropriate labels | U1 |

**Exit criteria:** an operator can deploy and interact with a Stellar-backed domain instance
(SNoto/SZeto/Sente) through the same UI used for EVM domains today — including browsing that
instance's transactions/events — without any `0x`-hex address assumption breaking a flow.
Recommend adding baseline test/Storybook coverage as part of this milestone given none exists
today, particularly for U3/U5's mechanical-but-broad changes.

---

*Next: [Chapter 16 — Risk map](16-risk-map.md)*
