# Chapter 16 — Delivery Plan

## 16.1 Team assumptions

3–4 senior engineers: 2 Go (one with deep Paladin familiarity), 1–2 Rust/Soroban (at least one
experienced Stellar engineer as anchor — see risk R14), part-time Solidity for interop
contracts, QA/infra support. "em" = engineer-month.

## 16.2 Milestones (port)

| # | Milestone | Contents | Effort | Demo | Exit criteria |
|---|---|---|---|---|---|
| **M0** | Spikes & upstream RFC | Groth16-on-Soroban benchmark matrix (ch. 13 §13.3); circomlib-vs-host Poseidon vectors; soroban-env-host embed spike; Rust-cdylib-plugin loader spike; **BLI RFC posted to Paladin maintainers** | 1.5 em | Groth16 verify tx on testnet with measured costs | benchmark report committed; go/no-go on SZeto batch shape; upstream feedback collected |
| **M1** | `ChainAddress` + type widening | ch. 11 §11.4; DB migrations; **zero behavior change** | 2.5 em | existing full EVM test suite green on refactor | byte-identical EVM RPC payloads (golden tests) |
| **M2** | BLI + proto v2 + EVM behind BLI | ch. 11 §§11.2–11.3, 11.6; ethclient/blockindexer/publictxmgr refactored | 4 em | Paladin-on-Besu with **unmodified domain binaries** on the new core | upstream Gradle CI green; domain-binary compat test |
| **M3** | Stellar backend | ch. 12 complete: stellarclient, ed25519 signing, ingestor, submitter (channel accounts, fee-bump, restore), SaladinFactory, quickstart docker in testinfra | 4 em | raw Soroban invoke submitted & indexed via `ptx_` APIs on local quickstart | ch. 12 acceptance criteria 1–6 |
| **M4** | SNoto end-to-end | SNoto contract, Noto chain-kind switch, typed-data libs (3 impls + vectors), ttlJanitor | 3 em | private notarized transfer across 3 Saladin nodes on Stellar testnet | 3-node testnet CI job; state-resync drill; ch. 13/14 SNoto criteria |
| **M5** | SZeto + SAtom + native-asset gateway | SZeto verifier + nullifiers; SAtom + factory; DvP SNoto⇄SZeto; SAC shield/unshield in SNoto & SZeto, `XDR_CLASSIC_OPS` + trustline tooling (ch. 12 §12.3, ch. 13 §13.6) | 4.5 em | anonymous ZK transfer; atomic DvP; shield→private→unshield of a classic asset on testnet | batch caps enforced from M0 numbers; failing-leg revert test; AUTH_REQUIRED asset flow green |
| **M6** | Sente | ch. 14 §14.3 phases S1–S4 | 6 em | private Soroban contract in a 3-member group + atomic external call to SNoto | determinism audit; endorsement-divergence chaos test |

**Port total ≈ 25.5 em**, ~9–12 months wall-clock (M0 ∥ M1; M3 overlaps M2 tail; M6 off the MVP
path).

## 16.3 Interop phases (ch. 15, incremental)

| Phase | Effort | Note |
|---|---|---|
| I-0 dual-ledger node | ~1.5 em | mostly wiring; interfaces already BLI-shaped |
| I-1 notary settlement (`interopmgr`) | ~2.5 em | includes compensation tooling + runbook |
| I-2 HTLC | ~2.5 em | + external security audit (budget separately) |
| I-3 M-of-N settlement payloads | ~1.5 em | tracks upstream V1.0 endorsement work |

**MVP definition** (aligned with the risk chapter's timeline sanity check): SNoto + dual-ledger
node + notary DvP ≈ 9–10 months with 5–6 engineers if port and interop tracks run in parallel;
port-only with 3–4 engineers lands in the same window without I-1/I-2.

## 16.4 Testing strategy

- **Unit:** soroban-sdk testutils for contracts; Go table tests + regenerated `mocks/` for BLI
  interfaces; shared cross-language typed-data/Poseidon vector files.
- **Component:** `stellar/quickstart` (protocol-26 image, accelerated ledgers for TTL tests)
  joins `testinfra/docker-compose-test.yml`; Stellar testbed configs beside each domain's
  `integration-test/`.
- **Integration:** 3-node docker-compose Saladin vs quickstart in CI; nightly against public
  Stellar testnet (⚠️ quarterly testnet resets — environments must rebuild from one script).
- **Chaos:** retention-gap drill (stop indexer > retention → loud failure → RPC/indexer/
  archive-based backfill, no Horizon); forced-archival drill; auth-entry-expiry → re-endorsement;
  sequencer coordinator kill/handover on Stellar timing.
- **CI:** new matrix axis `baseledger={evm,stellar}` for core tests; Rust toolchain +
  `stellar-cli` in build images; reproducible-Wasm check (pinned rustc, locked build profile);
  CI leg against the *next* protocol's preview quickstart image (risk R11).

## 16.5 Upstream engagement

The BLI RFC (M0) goes to Paladin maintainers with the M1/M2 refactor offered upstream: the
abstraction benefits Paladin regardless of Stellar (future Fabric/Solana/Corda backends), and
merged-upstream is the only durable defense against fork drift (risk R8/R9). Track upstream
monthly; measure rebase pain as a metric.

## 16.6 Decision log

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
| 13 | Interop first mode | notary-coordinated settlement | HTLC-first (griefing/free-option UX; notary adds zero trust in Noto deployments) |
| 14 | Native-asset custody | pooled SAC contract balance held by the domain contract itself; shield/unshield verbs in SNoto/SZeto | separate gateway contract (extra trust boundary, split footprint); per-user wrapped balances (leaks holder set, defeats pooling privacy) |
| 15 | Sente read/write-set discovery | embed `soroban-simulation` (recording-mode host) over a Paladin-state `SnapshotSource` (ch. 14 §14.3) | remote stellar-rpc simulateTransaction (public-ledger state only, no injection API, non-deterministic across endorsers, leaks private payloads); hand-rolled read/write tracking (Pente-style — redundant, recording mode already does it) |

---

*Next: [Chapter 17 — Risk map](17-risk-map.md)*
