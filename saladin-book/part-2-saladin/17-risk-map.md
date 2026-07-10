# Chapter 17 — Risk Map

The development-risk register for the whole Part 2 program (port + interop). Scales: likelihood
(L) and impact (I) each Low/Med/High. **EWI** = early-warning indicator. Ordered roughly by
severity within each group.

## 17.1 Correctness & protocol risks

| # | Risk | L | I | Mitigation | EWI |
|---|---|---|---|---|---|
| R1 | **State archival expires UTXO/nullifier entries.** Soroban persistent entries have TTLs; expired = archived. Nuance that saves us: archived persistent keys are *not absent* — transactions touching them **fail until restored**, so the failure is **liveness (stuck transfers, blocked refunds), never silent double-spend** — *provided* contracts never treat "not found" as "never existed" and never keep consensus-critical facts in `temporary` storage. | H | H | `extend_ttl` on every write; node `ttlJanitor`; automatic restore preamble in the submitter; code-review rule "no temporary storage for state/nullifier/lock data"; forced-archival chaos tests | any `entryArchived` failure on testnet; TTL-remaining metric below threshold |
| R2 | **Per-tx resource limits break the UTXO transaction shape.** Footprint entries, read/write bytes, CPU instructions are far tighter than EVM gas; a 50-input transfer trivial on Besu may not fit, and Groth16 verification consumes a large fixed budget first. | H | H | **Week-1 benchmark spike (M0)** for N∈{2,10,20,50} states and verify+nullifiers; entry-per-state storage (fine-grained footprints); documented per-domain batch caps; assembler-level transaction splitting | simulation >50 % of network limits at realistic sizes; `exceeded-limits` errors in tests |
| R3 | **Groth16/Poseidon parameter mismatch.** circomlib's Poseidon constants/arity vs Soroban's host Poseidon; snarkjs verifying-key serialization; G2 endianness. Late discovery forces in-Wasm Poseidon (budget blowout → collides with R2) or circuit regeneration + new trusted setup. | M | H | Week-1 vector test (same inputs through circomlib and host fn); verify one real Zeto proof from repo fixtures in the official verifier example **before any SZeto code** | any vector divergence; VK round-trip failure |
| R4 | **Event-retention window vs indexer downtime.** stellar-rpc keeps 24h–7d; an outage or fresh sync beyond retention gaps the domain state view. | M | H | history-archive/Horizon backfill designed in from day one; self-hosted RPC at max retention; aggressive checkpointing; alert at 25 % of retention lag | indexer-lag trending up; gap error in the 48h-downtime soak |
| R5 | **Simulation/footprint race under contention.** Contested entries changing between simulate and inclusion fail the tx. | M | M | re-simulate-on-retry loop; contract design avoids hot shared entries (per-state keys); the sequencer already serializes spends per domain | footprint-mismatch failures >1 % in load tests |
| R6 | **Sequence-number head-of-line blocking.** One stuck tx blocks everything behind it on a source account; EVM nonce-gap logic doesn't map. | H | M | channel-account pool (ch. 12); fee-bump escalation; pool size monitored/configurable | `txBAD_SEQ` in load tests; latency correlating with queue depth |
| R7 | **Auth-entry expiry mid-flight.** `signature_expiration_ledger` passes while a tx is queued → endorsement must be redone; no EVM analogue. | M | M | generous expiration budgets; submitter detects and bounces to sequencer re-endorsement (tested path); expirations sized vs worst-case fee-retry cycles | re-endorsement counter non-zero in steady state |

## 17.2 Architecture & refactor risks

| # | Risk | L | I | Mitigation | EWI |
|---|---|---|---|---|---|
| R8 | **`EthAddress`/`Bytes32`/ABI type refactor destroys upstream merge-ability** (~200+ files). | H | H | seam-discipline: new types at boundaries, EVM-only paths untouched; monthly upstream rebase with pain tracked as a metric; upstream the enabling refactors (M0 RFC) | rebase >2 engineer-days; conflict count trending up |
| R9 | **Proto v2 scope creep** ("let's redesign everything chain-agnostic"). | H | H | freeze a minimal additive delta (ch. 11 §11.6); written non-goals; version-gated messages; existing domains compile unchanged as a CI gate | proto PRs touching messages Saladin doesn't strictly need |
| R10 | **Upstream Paladin churn** (V1.0 M-of-N endorsement; Noto V0→V1 lock API — the very API interop leans on). | H | M–H | pin SNoto to a config-variant ID (Noto's 4-byte selector discipline); "upstream watcher" role; engage maintainers on interop payloads early | upstream PRs touching `ILockableCapability`/`INoto_V1`; variant drift |
| R11 | **Stellar protocol upgrade cadence** (~2–3×/yr, validator-voted activation; fees/limits/host behavior can shift under a deployed system). | H | M | subscribe to CAP calendar; CI leg on next-protocol preview images; re-run R2 benchmarks each protocol bump; no reliance on undocumented limit headroom | CI-vs-preview failures; CAPs touching Soroban limits |
| R12 | **Rust-plugin loader friction (Sente).** The loader's happy paths are Go .so + JVM JARs; a Rust cdylib is new territory. | H | M | decide the sidecar-process fallback upfront (the plugin contract is already gRPC); M0 spike; hello-world Rust domain before Sente proper | >2 weeks on loader plumbing without a passing hello-world |

## 17.3 Security & environment risks

| # | Risk | L | I | Mitigation | EWI |
|---|---|---|---|---|---|
| R13 | **Interop protocol security defects** (commitment malleability, deadline arithmetic, SettlementPayload replay across swaps, preimage front-running interactions). | M | H | external audit of HTLCDelegate (both chains) + `interopmgr` before production DvP; property-based/model-checked swap state machine incl. crash/timeout interleavings; adversarial testnet exercises | audit not booked by end of I-1; property tests finding late interleaving bugs |
| R14 | **Team skill spread** (Go + Rust + Solidity + XDR/Soroban auth/fee model — none of the EVM muscle memory transfers). | H | M–H | hire/contract one Soroban anchor; Go↔Rust pairing on every contract task; week-1 internal bootcamp on the three chronic traps (auth entries, footprints, TTLs); "Stellar-isms" doc | Rust/Soroban PR review latency ≫ Go; single name on all Soroban commits |
| R15 | **Sente scope gravity.** "Private Soroban" is research-grade and can eat the program. | M | H | contractually descoped from MVP; M6 only; internal S1–S4 gates | Sente tasks appearing on MVP boards |
| R16 | **Soroban auth-signing semantics done wrong** (signing opaque bytes instead of explicit auth-entry payload types → broken or over-broad authorizations). | M | M–H | explicit `SOROBAN_AUTH_ENTRY` payload type in the signing path — never "sign these bytes"; invocation-tree display in signing modules; expiry edge-case tests | any code path signing raw hashes for Soroban auth |
| R17 | **Cross-language hashing determinism** (typed-data digests must match across Rust contract, Go engine, and any future implementation, or interop payloads fork). | M | M | one canonical spec + shared vector files consumed by all implementations' CI | hand-rolled hashing without a vector file in review |
| R18 | **Fee surge vs HTLC deadlines** (underbid inclusion during surge pricing stalls a leg inside its window). | M | M | fee-bump escalation policy; headroom multipliers on simulation; Δ sized for worst-case fee-retry cycles | fee-related failures in load tests; time-to-inclusion p99 nearing Δ/4 |
| R19 | **Testnet quarterly resets** (certainty, not risk: all data wiped ~quarterly). | H | L–M | everything rebuildable from one script (deploy+fund+seed); primary testing on local quickstart; reset dates on the team calendar | env rebuild time >1 h |
| R20 | **Recipient trustline missing/unauthorized blocks unshield** (native assets, ch. 13 §13.6): a withdrawal to a `G…` account without an existing, authorized, non-full trustline fails — a liveness/UX failure class with no EVM analogue. | M | M | assemble-time trustline pre-flight (`CheckTrustline`, ch. 12 §12.3) with actionable errors; `ChangeTrust` tooling for local identities; docs for asset issuers on the approval flow | unshield failures reaching the chain instead of pre-flight; support tickets about "stuck withdrawals" |
| R21 | **Issuer clawback/freeze of the pooled SAC balance** (native assets): with `AUTH_CLAWBACK_ENABLED`, the pool's contract balance is *permanently* clawback-capable — one issuer action hits **all** shielded holders; `set_authorized(false)` freezes shield/unshield entirely. | L–M | H | asset-policy allowlist per domain instance (default: clawback-free assets only); notary–issuer organizational alignment for regulated assets; record issuer flags at shield time into receipts; plain-language legal disclosure | a shieldable asset with clawback flag appearing without an alignment agreement; issuer-flag changes observed on a backing asset |
| R22 | **Classic-op scope creep in the BLI**: `XDR_CLASSIC_OPS` (added for trustlines/accounts) becomes a backdoor for DEX offers, claimable balances, payment paths — dragging classic-Stellar semantics into the chain-agnostic core. | M | M | written non-goal list (ch. 12 §12.3); code review gate: classic ops limited to ChangeTrust/SetTrustLineFlags/Payment/CreateAccount; anything else needs a design doc | PRs adding new classic op types without a design doc |

## 17.4 Timeline sanity check

Critical path: **BLI (M1–M2) → Stellar submitter/indexer (M3) → SNoto locks (M4) → notary
interop (I-1) → HTLC (I-2)** — Go-core work plus exactly one Soroban contract. SZeto is
deliberately *off* the critical path so R3 surprises cannot block interop; Sente (M6) is off the
MVP entirely.

- MVP (SNoto + dual-ledger node + notary DvP): **~9–10 months** with 5–6 engineers running port
  and interop tracks in parallel.
- With HTLC + external audit: **~12–14 months**.
- Any plan promising materially less has not priced R1, R2, and R8.

Week-1 gates that de-risk the whole map: the R2 resource benchmark and the R3 Poseidon/proof
vectors — both cheap, both go/no-go for the SZeto shape, both scheduled in M0.

---

*Next: [Chapter 18 — Glossary](18-glossary.md)*
